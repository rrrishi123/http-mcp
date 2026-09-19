// Package transports is the wire's EXTENT as data: the 8 transports over the
// 2 atoms, in transports.json — the single machine-readable source of truth
// (TRANSPORTS.md is its prose mirror; the http-mcp `transports` tool returns
// it verbatim). Rule: raw bytes = wire; framing/routing/negotiation = adapter.
//
// The table is a CLAIM. Coherent checks it is internally honest; the probers
// in cmd/mcp/transports_conformance_test.go check every live wire transport
// actually works. Advertisement and proof move together.
package transports

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed transports.json
var raw []byte

// JSON is the advertised artifact, verbatim — what the `transports` tool serves.
func JSON() []byte { return raw }

// Entry is one advertised transport.
type Entry struct {
	Name       string `json:"name"`
	Mode       string `json:"mode"`   // CALL | CHANNEL | CHANNEL (afferent) | CALL|CHANNEL
	Where      string `json:"where"`  // wire | adapter
	Atom       string `json:"atom"`   // http_request | bidi_command | http_request|bidi_command (wire only)
	Status     string `json:"status"` // live (wire) | needs-adapter (adapter)
	StdlibOnly bool   `json:"stdlib_only"`
	ProvidedBy string `json:"provided_by,omitempty"` // adapters/<name> (adapter only)
	Note       string `json:"note,omitempty"`
}

// Manifest is the parsed table.
type Manifest struct {
	Principle  string            `json:"principle"`
	Modes      map[string]string `json:"modes"`
	Transports []Entry           `json:"transports"`
}

// Load parses the embedded table.
func Load() (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, fmt.Errorf("transports.json (the advertised artifact) does not parse: %w", err)
	}
	if len(m.Transports) == 0 {
		return m, fmt.Errorf("manifest advertises zero transports")
	}
	return m, nil
}

// Coherent reports the first way the advertisement over-promises: a wire
// transport must name its atom, be stdlib-only and live; an adapter transport
// must be needs-adapter, name its provider and not claim stdlib-only; an atom,
// when named, must be one of the two.
func Coherent(m Manifest) error {
	for _, e := range m.Transports {
		if e.Name == "" || e.Mode == "" || e.Where == "" || e.Status == "" {
			return fmt.Errorf("incomplete manifest entry: %+v", e)
		}
		switch e.Where {
		case "wire":
			if e.Atom == "" {
				return fmt.Errorf("%s: a wire transport must name the atom that serves it", e.Name)
			}
			if !e.StdlibOnly {
				return fmt.Errorf("%s: a wire transport must be stdlib_only (no deps is the whole claim)", e.Name)
			}
			if e.Status != "live" {
				return fmt.Errorf("%s: a wire transport advertised %q — the wire only claims what it serves now", e.Name, e.Status)
			}
		case "adapter":
			if e.Status != "needs-adapter" {
				return fmt.Errorf("%s: an adapter transport must be status=needs-adapter, got %q", e.Name, e.Status)
			}
			if e.ProvidedBy == "" {
				return fmt.Errorf("%s: an adapter transport must name its provided_by so the claim is traceable", e.Name)
			}
			if e.StdlibOnly {
				return fmt.Errorf("%s: an adapter transport cannot be stdlib_only", e.Name)
			}
		default:
			return fmt.Errorf("%s: unknown where %q (want wire|adapter)", e.Name, e.Where)
		}
		switch e.Atom {
		case "", "http_request", "bidi_command", "http_request|bidi_command":
		default:
			return fmt.Errorf("%s: atom %q is not one of the two atoms", e.Name, e.Atom)
		}
	}
	return nil
}
