// http-mcp: an MCP server whose tools are protocol calls.
//
// Transport is stdio, newline-delimited JSON-RPC 2.0 — implemented in stdlib,
// no SDK. stdout is the protocol channel; all logging goes to stderr.
//
// Tool surface:
//   - http_request: the four-field primitive.
//   - discover: re-perceive what a hub actually serves, correcting the stored
//     prior (specs/) against the live wire.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rrrishi123/http-mcp/auth"
	"github.com/rrrishi123/http-mcp/internal/httpx"
)

const protocolVersion = "2024-11-05"

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcErr         `json:"error,omitempty"`
}

type server struct {
	out    *bufio.Writer
	client *http.Client
}

func logf(format string, a ...any) { fmt.Fprintf(os.Stderr, "http-mcp: "+format+"\n", a...) }

func (s *server) send(resp rpcResp) {
	resp.JSONRPC = "2.0"
	b, err := json.Marshal(resp)
	if err != nil {
		logf("marshal: %v", err)
		return
	}
	s.out.Write(b)
	s.out.WriteByte('\n')
	s.out.Flush()
}

func (s *server) ok(id json.RawMessage, result any) { s.send(rpcResp{ID: id, Result: result}) }
func (s *server) fail(id json.RawMessage, code int, msg string) {
	s.send(rpcResp{ID: id, Error: &rpcErr{Code: code, Message: msg}})
}

func toolText(v any) map[string]any {
	var text string
	switch t := v.(type) {
	case string:
		text = t
	default:
		b, _ := json.MarshalIndent(v, "", " ")
		text = string(b)
	}
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}
}

func toolErr(msg string) map[string]any {
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": msg}},
		"isError": true,
	}
}

func (s *server) tools() []any {
	str := func(d string) map[string]any { return map[string]any{"type": "string", "description": d} }
	return []any{
		map[string]any{
			"name":        "http_request",
			"description": "Make one protocol call: method + url + headers + body. Returns status, headers, body. This is the whole primitive — aim it at any HTTP API.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"method":  str("HTTP method (GET, POST, DELETE, ...)"),
					"url":     str("Full URL to call."),
					"headers": map[string]any{"type": "object", "description": "Header name -> value.", "additionalProperties": map[string]any{"type": "string"}},
					"body":    str("Request body (raw string; JSON by default)."),
					"auth":    map[string]any{"type": "object", "description": "Optional auth. Either {profile: \"prod:adminltqa\"} to resolve a Basic credential from the environment (LT_USERNAME/LT_ACCESS_KEY) or a gitignored auth/<profile>.json — the secret never passes through here — or a literal {type: basic|bearer|apikey, user, key, header}."},
				},
				"required": []any{"method", "url"},
			},
		},
		map[string]any{
			"name": "discover",
			"description": "Re-perceive what a hub actually serves. Fires the wire's own self-description: " +
				"GET /status, then (if a session_id is given) GET /session/:id to read live caps, " +
				"GET /session/:id/appium/commands for Appium self-enumeration, and a 404-vs-405 probe " +
				"of every route in the stored prior (specs/) to classify exists/absent/unknown. " +
				"Returns what the wire says, not what the spec claims. Probe verdict beats snapshot claim, always.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"hub_url":    str("Base hub URL, e.g. http://localhost:4444"),
					"session_id": str("Optional live session id — enables cap-read and Appium self-enumeration."),
					"spec":       str("Spec name to probe against, e.g. geckodriver@0.37.0 or webdriver@wdio-9.28.0. Omit to probe all known specs."),
				},
				"required": []any{"hub_url"},
			},
		},
	}
}

// discover fires the wire's self-description against hub_url and returns
// what the wire actually says. It is the re-perceiver: prior (specs/) is the
// hypothesis; the live wire is the verdict.
func (s *server) discover(args map[string]any) any {
	hub := str(args["hub_url"])
	if hub == "" {
		return toolErr("hub_url is required")
	}
	hub = strings.TrimRight(hub, "/")

	out := map[string]any{"hub_url": hub}

	// Layer 1: status
	resp, err := httpx.Do(s.client, httpx.Request{Method: "GET", URL: hub + "/status"})
	if err != nil {
		out["status"] = map[string]any{"error": err.Error()}
	} else {
		var statusBody map[string]any
		json.Unmarshal([]byte(resp.Body), &statusBody)
		out["status"] = map[string]any{"http": resp.Status, "body": statusBody}
	}

	// Layer 4: 404-vs-405 bogus-session probe against stored specs.
	// Uses a fake UUID — does NOT need a real session, always runs.
	specsDir := "/Users/rishirajs/Desktop/repos/http-mcp/specs"
	specFilter := str(args["spec"])
	probeResults := probeSpecs(s.client, hub, specsDir, specFilter)
	if probeResults != nil {
		out["probe"] = probeResults
	}

	sid := str(args["session_id"])
	if sid == "" {
		out["note"] = "pass session_id to enable live cap-read and Appium self-enumeration"
		return toolText(out)
	}

	// Layer 2: live caps (what the server actually granted)
	resp, err = httpx.Do(s.client, httpx.Request{Method: "GET", URL: hub + "/session/" + sid})
	if err != nil {
		out["caps"] = map[string]any{"error": err.Error()}
	} else {
		var capsBody map[string]any
		json.Unmarshal([]byte(resp.Body), &capsBody)
		out["caps"] = map[string]any{"http": resp.Status, "body": capsBody}
	}

	// Layer 3: Appium self-enumeration (only if server speaks it)
	appiumRoutes := map[string]any{}
	for _, path := range []string{"/appium/commands", "/appium/extensions", "/appium/capabilities"} {
		r, err := httpx.Do(s.client, httpx.Request{Method: "GET", URL: hub + "/session/" + sid + path})
		if err == nil && r.Status == 200 {
			var body map[string]any
			json.Unmarshal([]byte(r.Body), &body)
			appiumRoutes[path] = body
		}
	}
	if len(appiumRoutes) > 0 {
		out["appium_self_enumeration"] = appiumRoutes
	}

	return toolText(out)
}

type route struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

type specFile struct {
	Source  string  `json:"source"`
	Version string  `json:"version"`
	Routes  []route `json:"routes"`
}

// probeSpecs fires every route from the stored specs at hub with a bogus session id.
// 404/plain-text → absent; W3C JSON error body → exists (any error except unknown_command
// at the router level means the route matched a handler).
func probeSpecs(client *http.Client, hub, specsDir, filter string) map[string]any {
	bogus := "00000000-0000-0000-0000-000000000000"

	// Collect all spec files
	type specEntry struct {
		name string
		file specFile
	}
	var specs []specEntry

	for _, sub := range []string{"webdriver", "appium"} {
		dir := specsDir + "/" + sub
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".json")
			if filter != "" && !strings.Contains(name, filter) {
				continue
			}
			// skip probe results (they live in specs/probes/)
			b, err := os.ReadFile(dir + "/" + e.Name())
			if err != nil {
				continue
			}
			var sf specFile
			if err := json.Unmarshal(b, &sf); err != nil {
				continue
			}
			specs = append(specs, specEntry{name: sub + "/" + name, file: sf})
		}
	}

	if len(specs) == 0 {
		return nil
	}

	// Deduplicate routes across all selected specs
	seen := map[string]bool{}
	type routeKey struct{ method, path string }
	var routes []routeKey
	for _, sp := range specs {
		for _, r := range sp.file.Routes {
			k := r.Method + " " + r.Path
			if !seen[k] {
				seen[k] = true
				routes = append(routes, routeKey{r.Method, r.Path})
			}
		}
	}

	exists, absent, errored := 0, 0, 0
	sample := []map[string]any{}

	for _, r := range routes {
		path := strings.ReplaceAll(r.path, ":sessionId", bogus)
		path = strings.ReplaceAll(path, ":id", bogus)
		parts := strings.Split(path, "/")
		for i, p := range parts {
			if strings.HasPrefix(p, ":") {
				parts[i] = "bogus"
			}
		}
		path = strings.Join(parts, "/")

		resp, err := httpx.Do(client, httpx.Request{Method: r.method, URL: hub + path})
		if err != nil {
			errored++
			continue
		}
		// Discriminator: W3C JSON error → route matched a handler → EXISTS
		// plain-text 404/405 → router never matched → ABSENT
		ct := strings.Join(resp.Headers["Content-Type"], "")
		if strings.Contains(ct, "json") {
			exists++
			if len(sample) < 5 {
				sample = append(sample, map[string]any{"route": r.method + " " + r.path, "verdict": "exists", "status": resp.Status})
			}
		} else {
			absent++
		}
	}

	return map[string]any{
		"probed":  len(routes),
		"exists":  exists,
		"absent":  absent,
		"errored": errored,
		"sample":  sample,
		"note":    "probe verdict beats snapshot claim. exists=route matched a handler; absent=router never matched.",
	}
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func (s *server) callTool(name string, args map[string]any) (any, bool) {
	switch name {
	case "http_request":
		req := httpx.Request{
			Method: str(args["method"]),
			URL:    str(args["url"]),
			Body:   str(args["body"]),
		}
		if h, ok := args["headers"].(map[string]any); ok {
			req.Headers = map[string]string{}
			for k, v := range h {
				req.Headers[k] = str(v)
			}
		}
		if a, ok := args["auth"].(map[string]any); ok {
			// A profile name keeps the secret below the trust boundary: the
			// agent names a credential, the wire resolves it from env/file.
			// Literal {type,user,key} still works for ad-hoc calls.
			if p := str(a["profile"]); p != "" {
				resolved, err := auth.Resolve(p)
				if err != nil {
					return toolErr("auth: " + err.Error()), false
				}
				resolved.Apply(&req)
			} else {
				(httpx.Auth{Type: str(a["type"]), User: str(a["user"]), Key: str(a["key"]), Header: str(a["header"])}).Apply(&req)
			}
		}
		resp, err := httpx.Do(s.client, req)
		if err != nil {
			return toolErr("request failed: " + err.Error()), false
		}
		return toolText(resp), false

	case "discover":
		return s.discover(args), false
	}
	return toolErr("unknown tool: " + name), false
}

func (s *server) handle(req rpcReq) {
	switch req.Method {
	case "initialize":
		s.ok(req.ID, map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "http-mcp", "version": "0.2.0"},
		})
	case "notifications/initialized":
	case "ping":
		s.ok(req.ID, map[string]any{})
	case "tools/list":
		s.ok(req.ID, map[string]any{"tools": s.tools()})
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			s.fail(req.ID, -32602, "bad params: "+err.Error())
			return
		}
		result, _ := s.callTool(p.Name, p.Arguments)
		s.ok(req.ID, result)
	default:
		if len(req.ID) > 0 {
			s.fail(req.ID, -32601, "method not found: "+req.Method)
		}
	}
}

func main() {
	s := &server{
		out:    bufio.NewWriter(os.Stdout),
		client: &http.Client{Timeout: 120 * time.Second},
	}
	logf("started; stdio MCP; %d tools", len(s.tools()))

	r := bufio.NewReader(os.Stdin)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var req rpcReq
			if e := json.Unmarshal(line, &req); e != nil {
				logf("parse: %v", e)
			} else {
				s.handle(req)
			}
		}
		if err != nil {
			if err != io.EOF {
				logf("read: %v", err)
			}
			return
		}
	}
}
