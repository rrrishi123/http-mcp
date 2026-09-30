package run

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/rrrishi123/http-mcp/contract"
)

func keys(t *testing.T, v any) []string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// TestResult_ByodShape: a byod Result serializes to exactly the names
// adapters/byod emits today (plus contract/kind), so existing readers keep parsing.
func TestResult_ByodShape(t *testing.T) {
	r := (&Result{Kind: KindByod, SessionID: "s", Platform: "ios", Device: "udid", HubURL: "http://127.0.0.1:4723", Stream: "http://127.0.0.1:9101", Transport: contract.ModeCall, IproxyPID: 42}).Stamp()
	want := []string{"contract", "device", "hub_url", "iproxy_pid", "kind", "platform", "session_id", "stream", "transport"}
	if got := keys(t, r); len(got) != len(want) || func() bool {
		for i := range got {
			if got[i] != want[i] {
				return true
			}
		}
		return false
	}() {
		t.Fatalf("byod keys %v, want %v", got, want)
	}
	if !r.Compatible() || r.Contract != contract.Version {
		t.Fatalf("stamp: %+v", r)
	}
}

// TestResult_BrowserShape: same for adapters/browser's names.
func TestResult_BrowserShape(t *testing.T) {
	r := Result{Kind: KindBrowser, SessionID: "page-1", Engine: "chrome", HubURL: "http://127.0.0.1:9333", Stream: "ws://127.0.0.1:9333/devtools/page/1", Transport: contract.ModeChannel, PID: 7, BrokerHint: "channel --port 4446"}
	want := []string{"broker_hint", "engine", "hub_url", "kind", "pid", "session_id", "stream", "transport"}
	got := keys(t, r)
	if len(got) != len(want) {
		t.Fatalf("browser keys %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("browser keys %v, want %v", got, want)
		}
	}
}

// TestRequest_RoundTrip: a Request survives JSON without loss and stamps itself.
func TestRequest_RoundTrip(t *testing.T) {
	in := (&Request{Kind: KindBrowser, Target: "firefox", Port: 4444, Profile: "/tmp/p", Broker: 4445}).Stamp()
	b, _ := json.Marshal(in)
	var out Request
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out != *in {
		t.Fatalf("round-trip changed the request: %+v vs %+v", out, *in)
	}
	if !contract.Compatible(out.Contract) {
		t.Fatalf("stamp %q not compatible", out.Contract)
	}
}
