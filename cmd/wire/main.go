// wire — a transparent witness for HTTP traffic.
//
// Sits between any client and any HTTP server, forwards verbatim, and logs
// one line per call: who (source port), what (method, path), verdict
// (status), size, and latency. Clients that point at the witness instead of
// the upstream become observable without knowing it; the upstream sees no
// difference. The four fields go through untouched — this is a witness,
// not a participant.
//
// Usage: wire -listen :4724 -upstream http://localhost:4723
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"time"

	"github.com/rrrishi123/http-mcp/internal/host"
)

var uuidRe = regexp.MustCompile(
	`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

type record struct {
	status int
	bytes  int
}

func (r *record) observe(resp *http.Response) error {
	r.status = resp.StatusCode
	r.bytes = int(resp.ContentLength)
	return nil
}

func main() {
	listen := flag.String("listen", ":4724", "address the witness listens on")
	upstream := flag.String("upstream", "http://localhost:4723", "server being witnessed")
	// #70: when set, the wire POSTs every observed call into 8's ledger so a MITM'd
	// smoke becomes a witnessed replayable record (not just a stdout line). The
	// collector redacts URL credentials before persisting.
	witness := flag.String("witness", "", "collector URL to record observed calls into 8 (e.g. http://127.0.0.1:7070)")
	actor := flag.String("actor", "wire-mitm", "X-8-Actor to attribute the witnessed calls to")
	flag.Parse()

	target, err := url.Parse(*upstream)
	if err != nil {
		panic(err)
	}
	fmt.Printf("wire: witnessing %s on %s\n", *upstream, *listen)

	handler := func(w http.ResponseWriter, req *http.Request) {
		rec := &record{}
		t0 := time.Now()
		// #325: the one endpoint the wire serves ITSELF still goes through the same
		// log line + witness POST as proxied calls — every act leaves a trace, the
		// witness's own acts included. Only the upstream round-trip is skipped.
		witnessedURL := target.Scheme + "://" + target.Host + req.URL.Path
		if req.URL.Path == "/host" { // #287: host-resources basic, served locally (not proxied)
			buf, _ := json.Marshal(host.Read())
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(buf)
			rec.status = http.StatusOK
			rec.bytes = len(buf)
			witnessedURL = "wire://self/host" // the wire answered, not the upstream
		} else {
			proxy := httputil.NewSingleHostReverseProxy(target)
			proxy.ModifyResponse = rec.observe
			proxy.ServeHTTP(w, req)
		}
		latUS := float64(time.Since(t0).Microseconds())
		path := uuidRe.ReplaceAllStringFunc(req.URL.Path, func(s string) string {
			return s[:8]
		})
		fmt.Printf("%s  %-21s %-6s %-50s -> %d  %8dB  %6.0fms\n",
			t0.Format("15:04:05.000"), req.RemoteAddr, req.Method, path,
			rec.status, rec.bytes, latUS/1000)
		// #70: fire-and-forget the observed call into 8's ledger.
		if *witness != "" {
			full := witnessedURL
			body := fmt.Sprintf(`{"physics":"call","method":%q,"url":%q,"status":%d,"latency_us":%.0f,"resp_bytes":%d,"actor":%q,"session":"mitm"}`,
				req.Method, full, rec.status, latUS, rec.bytes, *actor)
			go func() {
				r2, _ := http.NewRequest("POST", *witness+"/witnessed", bytes.NewReader([]byte(body)))
				if r2 != nil {
					r2.Header.Set("Content-Type", "application/json")
					r2.Header.Set("X-8-Actor", *actor)
					if resp, e := (&http.Client{Timeout: 3 * time.Second}).Do(r2); e == nil {
						resp.Body.Close()
					}
				}
			}()
		}
	}

	if err := http.ListenAndServe(*listen, http.HandlerFunc(handler)); err != nil {
		panic(err)
	}
}
