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
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rrrishi123/http-mcp/internal/host"
)

var uuidRe = regexp.MustCompile(
	`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

type record struct {
	status   int
	bytes    int
	respBody string
}

// bodyCap bounds captured request/response bodies in the replay record.
// WebDriver/Appium payloads (capabilities, locators, error values) fit well
// under this; anything larger is truncated, never dropped.
const bodyCap = 4096

func (r *record) observe(resp *http.Response) error {
	r.status = resp.StatusCode
	r.bytes = int(resp.ContentLength)
	// #116: the failure a replay must catch lives in the RESPONSE body
	// (WebDriver errors are {"value":{"error":...}}). Tee up to bodyCap.
	if resp.Body != nil {
		peek := make([]byte, bodyCap)
		n, _ := io.ReadFull(resp.Body, peek)
		rest := resp.Body
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(peek[:n]), rest), rest}
		r.respBody = string(peek[:n])
	}
	return nil
}

// secretRe redacts credential-bearing query params (LT hubs pass user/key in
// the URL) and basic-auth userinfo before anything is persisted.
var secretRe = regexp.MustCompile(`(?i)((?:key|accesskey|password|token|secret)=)[^&]+`)

func redact(s string) string {
	return secretRe.ReplaceAllString(s, "${1}REDACTED")
}

// replayLog appends full-fidelity records to a local ndjson sidecar. The
// collector's schema stays untouched (it serves the live fleet); the ledger
// row carries seq so a witnessed call can be joined back to its replay line.
type replayLog struct {
	mu  sync.Mutex
	f   *os.File
	seq atomic.Int64
}

func (l *replayLog) write(rec map[string]any) int64 {
	if l.f == nil {
		return -1
	}
	seq := l.seq.Add(1)
	rec["seq"] = seq
	buf, err := json.Marshal(rec)
	if err != nil {
		return -1
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.f.Write(append(buf, '\n'))
	return seq
}

func main() {
	listen := flag.String("listen", ":4724", "address the witness listens on")
	upstream := flag.String("upstream", "http://localhost:4723", "server being witnessed")
	// #70: when set, the wire POSTs every observed call into 8's ledger so a MITM'd
	// smoke becomes a witnessed replayable record (not just a stdout line). The
	// collector redacts URL credentials before persisting.
	witness := flag.String("witness", "", "collector URL to record observed calls into 8 (e.g. http://127.0.0.1:7070)")
	actor := flag.String("actor", "wire-mitm", "X-8-Actor to attribute the witnessed calls to")
	// #116: full-fidelity capture (query + request/response bodies, redacted)
	// goes to a local replay sidecar; the ledger row carries the seq pointer.
	replayPath := flag.String("replay", "", "ndjson file for full-fidelity replay records (e.g. ~/.8/wire-replay.ndjson)")
	flag.Parse()

	replay := &replayLog{}
	if *replayPath != "" {
		f, err := os.OpenFile(*replayPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			panic(err)
		}
		replay.f = f
	}

	target, err := url.Parse(*upstream)
	if err != nil {
		panic(err)
	}
	fmt.Printf("wire: witnessing %s on %s\n", *upstream, *listen)

	handler := func(w http.ResponseWriter, req *http.Request) {
		rec := &record{}
		t0 := time.Now()
		// #116: capture the request body up front (capped), restore it for the proxy.
		var reqBody string
		if req.Body != nil {
			peek := make([]byte, bodyCap)
			n, _ := io.ReadFull(req.Body, peek)
			rest := req.Body
			req.Body = struct {
				io.Reader
				io.Closer
			}{io.MultiReader(bytes.NewReader(peek[:n]), rest), rest}
			reqBody = string(peek[:n])
		}
		// #325: the one endpoint the wire serves ITSELF still goes through the same
		// log line + witness POST as proxied calls — every act leaves a trace, the
		// witness's own acts included. Only the upstream round-trip is skipped.
		fullPath := req.URL.Path
		if req.URL.RawQuery != "" {
			fullPath += "?" + redact(req.URL.RawQuery)
		}
		witnessedURL := target.Scheme + "://" + target.Host + fullPath
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
			// #69: witnessing an HTTPS hub (LambdaTest, any cloud grid) needs two
			// things NewSingleHostReverseProxy doesn't do: (1) send the UPSTREAM's
			// Host header — it preserves the inbound 127.0.0.1 host, which the hub
			// rejects (GitHub/LT → 400); (2) inject the upstream credential. Both
			// come from the -upstream URL at runtime (https://user:key@hub/), so no
			// secret ever lives in code, and the userinfo is not in the witnessed
			// URL (that uses target.Host only).
			orig := proxy.Director
			proxy.Director = func(rq *http.Request) {
				orig(rq)
				rq.Host = target.Host
				if u := target.User; u != nil {
					if pw, ok := u.Password(); ok {
						rq.SetBasicAuth(u.Username(), pw)
					}
				}
			}
			proxy.ServeHTTP(w, req)
		}
		latUS := float64(time.Since(t0).Microseconds())
		path := uuidRe.ReplaceAllStringFunc(req.URL.Path, func(s string) string {
			return s[:8]
		})
		seq := replay.write(map[string]any{
			"ts":         t0.UTC().Format(time.RFC3339Nano),
			"method":     req.Method,
			"url":        redact(witnessedURL),
			"req_body":   redact(reqBody),
			"resp_body":  redact(rec.respBody),
			"status":     rec.status,
			"latency_us": int(latUS),
		})
		fmt.Printf("%s  %-21s %-6s %-50s -> %d  %8dB  %6.0fms\n",
			t0.Format("15:04:05.000"), req.RemoteAddr, req.Method, path,
			rec.status, rec.bytes, latUS/1000)
		// #70: fire-and-forget the observed call into 8's ledger.
		if *witness != "" {
			full := redact(witnessedURL)
			session := "mitm"
			if seq >= 0 {
				session = fmt.Sprintf("mitm#%d", seq) // join key into the replay sidecar
			}
			body := fmt.Sprintf(`{"physics":"call","method":%q,"url":%q,"status":%d,"latency_us":%.0f,"resp_bytes":%d,"actor":%q,"session":%q}`,
				req.Method, full, rec.status, latUS, rec.bytes, *actor, session)
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
