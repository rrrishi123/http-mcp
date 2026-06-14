// http-mcp: an MCP server whose tools are protocol calls.
//
// Transport is stdio, newline-delimited JSON-RPC 2.0 — implemented in stdlib,
// no SDK. stdout is the protocol channel; all logging goes to stderr.
//
// Tool surface is small on purpose: the http_request primitive plus session
// lifecycle. The territory is reached by aiming the primitive at a URL, not by
// exposing one tool per endpoint.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rrrishi123/http-mcp/internal/httpx"
	"github.com/rrrishi123/http-mcp/internal/session"
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
	store  *session.Store
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

// toolText wraps a value as a single text content block (the MCP tools/call shape).
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
					"auth":    map[string]any{"type": "object", "description": "Optional auth: {type: basic|bearer|apikey, user, key, header}."},
				},
				"required": []any{"method", "url"},
			},
		},
		map[string]any{
			"name":        "create_session",
			"description": "POST a new-session payload to a hub and track it. kind=local (watched for death) or cloud (kept warm before idle timeout). Returns the session id.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"hub_url":      str("Base hub URL, e.g. http://localhost:4444"),
					"kind":         str("local | cloud (default local)"),
					"capabilities": map[string]any{"type": "object", "description": "W3C capabilities object posted as {capabilities:{alwaysMatch:...}}."},
					"body":         str("Raw new-session body (overrides capabilities if given)."),
				},
				"required": []any{"hub_url"},
			},
		},
		map[string]any{
			"name":        "list_sessions",
			"description": "List every tracked session with its kind, age, and live status.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
		map[string]any{
			"name":        "delete_session",
			"description": "Stop tracking a session. With end_remote=true, also DELETE it on the hub.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":         str("Session id."),
					"end_remote": map[string]any{"type": "boolean", "description": "Also send DELETE /session/{id} to the hub."},
				},
				"required": []any{"id"},
			},
		},
		map[string]any{
			"name":        "broadcast",
			"description": "Fan out one request to every tracked session concurrently. :id in path is replaced with each session's id. Returns one result per session.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"method":      str("HTTP method (GET, POST, ...)."),
					"path":        str("Path template — :id is replaced with each session id. E.g. /session/:id/screenshot"),
					"body":        str("Request body, same for all sessions."),
					"session_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Subset of session ids to target. Empty = all tracked sessions."},
				},
				"required": []any{"method", "path"},
			},
		},
		map[string]any{
			"name":        "create_fleet",
			"description": "Start multiple sessions concurrently — one goroutine per hub. Returns a result for each entry.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"sessions": map[string]any{
						"type":        "array",
						"description": "One entry per session to create.",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"hub_url":      str("Base hub URL, e.g. http://localhost:4723"),
								"kind":         str("local | cloud (default local)"),
								"capabilities": map[string]any{"type": "object", "description": "W3C capabilities object."},
								"body":         str("Raw new-session body (overrides capabilities)."),
							},
							"required": []any{"hub_url"},
						},
					},
				},
				"required": []any{"sessions"},
			},
		},
	}
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
			(httpx.Auth{Type: str(a["type"]), User: str(a["user"]), Key: str(a["key"]), Header: str(a["header"])}).Apply(&req)
		}
		resp, err := httpx.Do(s.client, req)
		if err != nil {
			return toolErr("request failed: " + err.Error()), false
		}
		return toolText(resp), false

	case "create_session":
		hub := str(args["hub_url"])
		kind := session.Local
		if str(args["kind"]) == "cloud" {
			kind = session.Cloud
		}
		body := str(args["body"])
		if body == "" {
			caps, _ := args["capabilities"].(map[string]any)
			b, _ := json.Marshal(map[string]any{"capabilities": map[string]any{"alwaysMatch": caps, "firstMatch": []any{map[string]any{}}}})
			body = string(b)
		}
		resp, err := httpx.Do(s.client, httpx.Request{Method: "POST", URL: hub + "/session", Body: body})
		if err != nil {
			return toolErr("create_session failed: " + err.Error()), false
		}
		var env struct {
			Value struct {
				SessionID    string         `json:"sessionId"`
				Capabilities map[string]any `json:"capabilities"`
			} `json:"value"`
		}
		json.Unmarshal([]byte(resp.Body), &env)
		if env.Value.SessionID == "" {
			return toolErr("no sessionId in response: " + resp.Body), false
		}
		sess := &session.Session{ID: env.Value.SessionID, HubURL: hub, Kind: kind, Caps: env.Value.Capabilities}
		s.store.Add(sess)
		return toolText(map[string]any{"session_id": sess.ID, "kind": string(kind), "hub_url": hub}), false

	case "list_sessions":
		return toolText(s.store.List()), false

	case "delete_session":
		id := str(args["id"])
		sess, ok := s.store.Get(id)
		if end, _ := args["end_remote"].(bool); end && ok {
			httpx.Do(s.client, httpx.Request{Method: "DELETE", URL: sess.HubURL + "/session/" + id})
		}
		if s.store.Delete(id) {
			return toolText(map[string]any{"deleted": id}), false
		}
		return toolErr("unknown session: " + id), false

	case "broadcast":
		method := str(args["method"])
		path := str(args["path"])
		body := str(args["body"])

		var targetIDs map[string]bool
		if ids, ok := args["session_ids"].([]any); ok && len(ids) > 0 {
			targetIDs = make(map[string]bool, len(ids))
			for _, v := range ids {
				targetIDs[str(v)] = true
			}
		}

		sessions := s.store.List()
		if targetIDs != nil {
			var filtered []session.Session
			for _, sess := range sessions {
				if targetIDs[sess.ID] {
					filtered = append(filtered, sess)
				}
			}
			sessions = filtered
		}
		if len(sessions) == 0 {
			return toolText([]any{}), false
		}

		type bResult struct {
			SessionID string `json:"session_id"`
			HubURL    string `json:"hub_url"`
			Status    int    `json:"status,omitempty"`
			Body      string `json:"body,omitempty"`
			Error     string `json:"error,omitempty"`
		}
		results := make([]bResult, len(sessions))
		var wg sync.WaitGroup
		for i, sess := range sessions {
			wg.Add(1)
			go func(i int, sess session.Session) {
				defer wg.Done()
				url := sess.HubURL + strings.ReplaceAll(path, ":id", sess.ID)
				resp, err := httpx.Do(s.client, httpx.Request{Method: method, URL: url, Body: body})
				if err != nil {
					results[i] = bResult{SessionID: sess.ID, HubURL: sess.HubURL, Error: err.Error()}
					return
				}
				results[i] = bResult{SessionID: sess.ID, HubURL: sess.HubURL, Status: resp.Status, Body: resp.Body}
			}(i, sess)
		}
		wg.Wait()
		return toolText(results), false

	case "create_fleet":
		type entry struct {
			HubURL string         `json:"hub_url"`
			Kind   string         `json:"kind"`
			Caps   map[string]any `json:"capabilities"`
			Body   string         `json:"body"`
		}
		type fResult struct {
			SessionID string `json:"session_id,omitempty"`
			HubURL    string `json:"hub_url"`
			Kind      string `json:"kind"`
			Error     string `json:"error,omitempty"`
		}

		rawSessions, _ := args["sessions"].([]any)
		if len(rawSessions) == 0 {
			return toolErr("sessions must be a non-empty array"), false
		}
		entries := make([]entry, len(rawSessions))
		for i, v := range rawSessions {
			m, _ := v.(map[string]any)
			entries[i] = entry{
				HubURL: str(m["hub_url"]),
				Kind:   str(m["kind"]),
				Body:   str(m["body"]),
			}
			if caps, ok := m["capabilities"].(map[string]any); ok {
				entries[i].Caps = caps
			}
		}

		results := make([]fResult, len(entries))
		var wg sync.WaitGroup
		for i, e := range entries {
			wg.Add(1)
			go func(i int, e entry) {
				defer wg.Done()
				kind := session.Local
				if e.Kind == "cloud" {
					kind = session.Cloud
				}
				body := e.Body
				if body == "" {
					b, _ := json.Marshal(map[string]any{"capabilities": map[string]any{
						"alwaysMatch": e.Caps,
						"firstMatch":  []any{map[string]any{}},
					}})
					body = string(b)
				}
				resp, err := httpx.Do(s.client, httpx.Request{Method: "POST", URL: e.HubURL + "/session", Body: body})
				if err != nil {
					results[i] = fResult{HubURL: e.HubURL, Kind: string(kind), Error: err.Error()}
					return
				}
				var env struct {
					Value struct {
						SessionID string `json:"sessionId"`
					} `json:"value"`
				}
				json.Unmarshal([]byte(resp.Body), &env)
				if env.Value.SessionID == "" {
					results[i] = fResult{HubURL: e.HubURL, Kind: string(kind), Error: "no sessionId: " + resp.Body[:min(len(resp.Body), 120)]}
					return
				}
				sess := &session.Session{ID: env.Value.SessionID, HubURL: e.HubURL, Kind: kind}
				s.store.Add(sess)
				results[i] = fResult{SessionID: sess.ID, HubURL: e.HubURL, Kind: string(kind)}
			}(i, e)
		}
		wg.Wait()
		return toolText(results), false
	}
	return toolErr("unknown tool: " + name), false
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func (s *server) handle(req rpcReq) {
	switch req.Method {
	case "initialize":
		s.ok(req.ID, map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "http-mcp", "version": "0.1.0"},
		})
	case "notifications/initialized":
		// notification, no response
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
	store := session.New()
	store.OnDeath = func(id, reason string) { logf("reaped %s (%s)", id, reason) }
	s := &server{
		out:    bufio.NewWriter(os.Stdout),
		store:  store,
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
