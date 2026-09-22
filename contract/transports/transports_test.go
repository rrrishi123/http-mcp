package transports

import "testing"

// TestEmbedded_Coherent: the shipped table passes its own honesty rules.
func TestEmbedded_Coherent(t *testing.T) {
	m, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := Coherent(m); err != nil {
		t.Fatal(err)
	}
	if len(m.Transports) != 9 {
		t.Errorf("the wire advertises 9 transports over 2 atoms; table has %d", len(m.Transports))
	}
}

func TestCoherent_AdapterStatusAndDependencies(t *testing.T) {
	for _, status := range []string{"needs-adapter", "implemented", "experimental-primitive"} {
		for _, stdlib := range []bool{false, true} {
			entry := Entry{Name: "relay", Mode: "CALL", Where: "adapter", Atom: "http_request", Status: status, StdlibOnly: stdlib, ProvidedBy: "adapters/relay"}
			if err := Coherent(Manifest{Transports: []Entry{entry}}); err != nil {
				t.Errorf("status=%s stdlib=%v: %v", status, stdlib, err)
			}
			entry.ProvidedBy = ""
			if err := Coherent(Manifest{Transports: []Entry{entry}}); err == nil {
				t.Errorf("status=%s stdlib=%v: missing provider accepted", status, stdlib)
			}
		}
	}
	for _, status := range []string{"live", "typo"} {
		entry := Entry{Name: "relay", Mode: "CALL", Where: "adapter", Status: status, ProvidedBy: "adapters/relay"}
		if err := Coherent(Manifest{Transports: []Entry{entry}}); err == nil {
			t.Errorf("invalid adapter status %q accepted", status)
		}
	}
}

// TestCoherent_CatchesOverPromise: a wire transport that is not live is refused.
func TestCoherent_CatchesOverPromise(t *testing.T) {
	m := Manifest{Transports: []Entry{{Name: "x", Mode: "CALL", Where: "wire", Atom: "http_request", Status: "planned", StdlibOnly: true}}}
	if err := Coherent(m); err == nil {
		t.Fatal("a non-live wire transport must be incoherent")
	}
	m = Manifest{Transports: []Entry{{Name: "y", Mode: "CHANNEL", Where: "adapter", Status: "needs-adapter", ProvidedBy: "adapters/y"}}}
	if err := Coherent(m); err != nil {
		t.Fatalf("honest adapter entry refused: %v", err)
	}
}
