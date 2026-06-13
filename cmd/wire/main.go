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
	"flag"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"time"
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
	flag.Parse()

	target, err := url.Parse(*upstream)
	if err != nil {
		panic(err)
	}
	fmt.Printf("wire: witnessing %s on %s\n", *upstream, *listen)

	handler := func(w http.ResponseWriter, req *http.Request) {
		rec := &record{}
		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.ModifyResponse = rec.observe
		t0 := time.Now()
		proxy.ServeHTTP(w, req)
		path := uuidRe.ReplaceAllStringFunc(req.URL.Path, func(s string) string {
			return s[:8]
		})
		fmt.Printf("%s  %-21s %-6s %-50s -> %d  %8dB  %6.0fms\n",
			t0.Format("15:04:05.000"), req.RemoteAddr, req.Method, path,
			rec.status, rec.bytes, float64(time.Since(t0).Microseconds())/1000)
	}

	if err := http.ListenAndServe(*listen, http.HandlerFunc(handler)); err != nil {
		panic(err)
	}
}
