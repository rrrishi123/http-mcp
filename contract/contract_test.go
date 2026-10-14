package contract

import "testing"

func TestCompatible(t *testing.T) {
	cases := map[string]bool{"": true, Version: true, "v0.0.9": true, "0.0.2": true, "v0.1.0": false, "v1.0.0": false, "garbage": false}
	for v, want := range cases {
		if got := Compatible(v); got != want {
			t.Errorf("Compatible(%q) = %v, want %v", v, got, want)
		}
	}
}
