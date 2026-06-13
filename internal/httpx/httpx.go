// Package httpx is the primitive: a call is four fields, the response is three.
// No client library, no framework — just net/http.
package httpx

import (
	"encoding/base64"
	"io"
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

// Do makes the call. That is the entire job.
func Do(client *http.Client, r Request) (*Response, error) {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
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
