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
	if len(m.Transports) != 8 {
		t.Errorf("the wire advertises 8 transports over 2 atoms; table has %d", len(m.Transports))
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
