package httpx

// Transport self-test: proves the CALL atom (Do) across the transports the wire
// claims as "live" — plain HTTP, an object/JSON body round-trip, an SSE afferent
// stream, and a unix-domain socket (same CALL, different address family). These
// were "live but unprobed" until 2026-07-25; this pins them so a refactor can't
// silently drop one. Hermetic: no network, no browser — httptest + a unix listener.

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mux is the shared mock: /hello (GET), /echo (POST), /events (SSE), /ping (unix).
func mux() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"transport": "http", "mode": "CALL", "ok": true})
	})
	m.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		var in any
		_ = json.NewDecoder(r.Body).Decode(&in)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"echo": in})
	})
	m.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := range 3 {
			_, _ = w.Write([]byte("event: tick\ndata: {\"seq\":" + string(rune('0'+i)) + "}\n\n"))
		}
	})
	m.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"transport": "unix", "mode": "CALL", "ok": true})
	})
	return m
}

func TestCall_HTTP_and_ObjectBody(t *testing.T) {
	srv := httptest.NewServer(mux())
	defer srv.Close()

	resp, err := Do(nil, Request{Method: "GET", URL: srv.URL + "/hello"})
	if err != nil {
		t.Fatalf("http GET: %v", err)
	}
	if resp.Status != 200 || !strings.Contains(resp.Body, `"ok":true`) {
		t.Fatalf("http GET bad: %d %s", resp.Status, resp.Body)
	}

	// object body: http_request accepts a JSON object; it must round-trip intact.
	resp, err = Do(nil, Request{Method: "POST", URL: srv.URL + "/echo",
		Body: `{"probe":"object-body","n":42}`})
	if err != nil {
		t.Fatalf("http POST: %v", err)
	}
	if !strings.Contains(resp.Body, `"probe":"object-body"`) || !strings.Contains(resp.Body, `"n":42`) {
		t.Fatalf("echo did not round-trip: %s", resp.Body)
	}
}

func TestCall_SSE_afferent(t *testing.T) {
	srv := httptest.NewServer(mux())
	defer srv.Close()
	resp, err := Do(nil, Request{Method: "GET", URL: srv.URL + "/events"})
	if err != nil {
		t.Fatalf("sse GET: %v", err)
	}
	ct := ""
	if v := resp.Headers["Content-Type"]; len(v) > 0 {
		ct = v[0]
	}
	if !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("sse content-type = %q", ct)
	}
	if got := strings.Count(resp.Body, "event: tick"); got != 3 {
		t.Fatalf("sse frames = %d, want 3; body=%q", got, resp.Body)
	}
}

func TestCall_UnixSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "wire.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("unix listen: %v", err)
	}
	defer ln.Close()
	srv := &http.Server{Handler: mux()}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	time.Sleep(20 * time.Millisecond)

	// the wire's unix form: http+unix:///<sock>:/<route>
	resp, err := Do(nil, Request{Method: "GET", URL: "http+unix://" + sock + ":/ping"})
	if err != nil {
		t.Fatalf("unix GET: %v", err)
	}
	if resp.Status != 200 || !strings.Contains(resp.Body, `"transport":"unix"`) {
		t.Fatalf("unix bad: %d %s", resp.Status, resp.Body)
	}
	_ = os.Remove(sock)
}
