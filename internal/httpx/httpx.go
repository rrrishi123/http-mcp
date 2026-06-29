// Package httpx is the primitive: a call is four fields, the response is three.
// No client library, no framework — just net/http.
package httpx

import (
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Request is the whole of what a protocol call is.
type Request struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
}

// Response is the whole of what comes back.
type Response struct {
	Status  int                 `json:"status"`
	Headers map[string][]string `json:"headers"`
	Body    string              `json:"body"`
}

// Auth is a header strategy, not a dependency. Apply it to a Request before Do.
type Auth struct {
	Type string `json:"type"` // basic | bearer | apikey
	User string `json:"user,omitempty"`
	Key  string `json:"key,omitempty"`
	// Header names the api-key header when Type == "apikey" (default "X-API-Key").
	Header string `json:"header,omitempty"`
}

// Apply writes the auth into the request headers. The credential crosses here
// and only here — the rest of the wire stays legible.
func (a Auth) Apply(r *Request) {
	if r.Headers == nil {
		r.Headers = map[string]string{}
	}
	switch strings.ToLower(a.Type) {
	case "basic":
		tok := base64.StdEncoding.EncodeToString([]byte(a.User + ":" + a.Key))
		r.Headers["Authorization"] = "Basic " + tok
	case "bearer":
		r.Headers["Authorization"] = "Bearer " + a.Key
	case "apikey":
		h := a.Header
		if h == "" {
			h = "X-API-Key"
		}
		r.Headers[h] = a.Key
	}
}

// unixScheme detects the wire's unix-socket form and splits it into the socket
// path and the rewritten http URL. A unix socket is not a new mode — it is the
// same CALL bytes over a different address family — so it lives in the wire, not
// an adapter. Form: http+unix:///var/run/app.sock:/v1/status
// (everything up to the first ":" after the authority is the socket path).
func unixScheme(rawURL string) (socket, httpURL string, ok bool) {
	for _, p := range []string{"http+unix://", "https+unix://", "unix://"} {
		if strings.HasPrefix(rawURL, p) {
			rest := strings.TrimPrefix(rawURL, p)
			i := strings.Index(rest, ":")
			if i < 0 { // no request path -> root
				return rest, "http://unix/", true
			}
			req := rest[i+1:]
			if !strings.HasPrefix(req, "/") {
				req = "/" + req
			}
			return rest[:i], "http://unix" + req, true
		}
	}
	return "", "", false
}

// unixClient dials the given unix socket for every request, ignoring the
// placeholder host. Stdlib only — net.Dial("unix", ...).
func unixClient(socket string, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: timeout}).DialContext(ctx, "unix", socket)
			},
		},
	}
}

// Do makes the call. That is the entire job.
func Do(client *http.Client, r Request) (*Response, error) {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	// Unix-socket transport: same CALL, different address family. Wire, not adapter.
	if socket, httpURL, ok := unixScheme(r.URL); ok {
		client = unixClient(socket, 60*time.Second)
		r.URL = httpURL
	}
	var body io.Reader
	if r.Body != "" {
		body = strings.NewReader(r.Body)
	}
	req, err := http.NewRequest(strings.ToUpper(r.Method), r.URL, body)
	if err != nil {
		return nil, err
	}
	for k, v := range r.Headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("Content-Type") == "" && r.Body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return &Response{Status: resp.StatusCode, Headers: resp.Header, Body: string(raw)}, nil
}
