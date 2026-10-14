package trace

import (
	"bytes"
	"strings"
	"testing"
)

// TestEmit_StampsSeqAndContract: Seq and Contract are the Writer's, not the caller's.
func TestEmit_StampsSeqAndContract(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	for i := 0; i < 3; i++ {
		if err := w.Emit(Frame{Contract: "caller-lies", Seq: 99, Session: "s", Mode: ModeCall, Dir: DirEfferent, Method: "GET", URL: "http://x/{sid}", AuthSlot: "header:Authorization"}); err != nil {
			t.Fatal(err)
		}
	}
	frames, err := Read(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 3 {
		t.Fatalf("want 3 frames, got %d", len(frames))
	}
	for i, f := range frames {
		if f.Seq != i {
			t.Errorf("frame %d: seq %d", i, f.Seq)
		}
		if f.Contract != Version {
			t.Errorf("frame %d: contract %q, want %q", i, f.Contract, Version)
		}
		if f.AuthSlot != "header:Authorization" || strings.Contains(f.URL, "secret") {
			t.Errorf("frame %d: auth slot/url not preserved: %+v", i, f)
		}
	}
}

// TestRead_SkipsBlankLines: a partially flushed or concatenated trace still reads.
func TestRead_SkipsBlankLines(t *testing.T) {
	in := "\n" + `{"contract":"v0.0.2","seq":0,"session":"s","mode":"channel","dir":"afferent","event":"log.entryAdded"}` + "\n\n"
	frames, err := Read(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 1 || frames[0].Event != "log.entryAdded" || frames[0].Mode != ModeChannel {
		t.Fatalf("got %+v", frames)
	}
}

// TestCompatible: same major.minor replays; a foreign minor does not; unstamped does.
func TestCompatible(t *testing.T) {
	cases := map[string]bool{
		"":        true,
		Version:   true,
		"v0.0.9":  true,
		"0.0.2":   true,
		"v0.1.0":  false,
		"v1.0.0":  false,
		"garbage": false,
	}
	for v, want := range cases {
		if got := Compatible(v); got != want {
			t.Errorf("Compatible(%q) = %v, want %v", v, got, want)
		}
	}
}
