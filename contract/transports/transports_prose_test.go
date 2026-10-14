package transports

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// normProse reduces a string to lowercase alphanumerics so "needs-adapter",
// "needs adapter" and "Needs Adapter" all compare equal, and "unix_socket"
// matches the prose's "Unix socket".
func normProse(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// a numbered row in the TRANSPORTS.md transport table: "| 7 | **gitbroker** | ..."
var mdNumberedRow = regexp.MustCompile(`^\s*\|\s*\d+\s*\|`)

// TestProseMirrorReflectsJSON guards TRANSPORTS.md (the prose mirror) against
// drift from transports.json (the source of truth): the numbered transport
// table must list exactly the advertised transports, each row carrying that
// transport's advertised status. This is the JSON<->prose check that was
// missing when gitbroker was dropped from the prose and mqtt/webrtc statuses
// went stale while the conformance test (JSON<->`transports` tool) stayed green.
func TestProseMirrorReflectsJSON(t *testing.T) {
	m, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// TRANSPORTS.md is at the repo root, two dirs up from contract/transports.
	md, err := os.ReadFile("../../TRANSPORTS.md")
	if err != nil {
		t.Fatalf("read TRANSPORTS.md: %v", err)
	}
	var rows []string
	for _, ln := range strings.Split(string(md), "\n") {
		if mdNumberedRow.MatchString(ln) {
			rows = append(rows, ln)
		}
	}
	if len(rows) != len(m.Transports) {
		t.Errorf("TRANSPORTS.md numbered table lists %d transports; transports.json advertises %d", len(rows), len(m.Transports))
	}
	for _, e := range m.Transports {
		name, status := normProse(e.Name), normProse(e.Status)
		var row string
		for _, r := range rows {
			if strings.Contains(normProse(r), name) {
				row = r
				break
			}
		}
		if row == "" {
			t.Errorf("transport %q (status %q) advertised in transports.json but absent from the TRANSPORTS.md table", e.Name, e.Status)
			continue
		}
		if !strings.Contains(normProse(row), status) {
			t.Errorf("transport %q: prose row does not reflect advertised status %q\n  row: %s", e.Name, e.Status, strings.TrimSpace(row))
		}
	}
}
