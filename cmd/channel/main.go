// channel — the held BiDi/CDP wire (physics #2, done right).
//
// bidi_command opens a fresh socket per call. That's faithful to physics #1
// (HTTP really is stateless) but WRONG for the channel: CDP and WebDriver BiDi
// are stateful. A subscription lives on the socket; a CDP sessionId is bound to
// it; many effects arrive only as events, never as an id-matched response.
// Close the socket and all of that dies. So the channel's true atom is not
// open-send-close — it is a HELD connection + a command injector + an event
// fan-out.
//
// channel holds ONE long-lived WebSocket to a browser and fronts it with HTTP:
//
//	POST /command   {method, params}      -> the id-matched response (broker owns ids)
//	GET  /events    (Server-Sent Events)  <- every id-less frame: the stream
//	GET  /health                          -> uptime, last frame, in-flight, subscribers
//
// One reader goroutine routes incoming frames: a frame WITH an id resolves the
// command waiting on it; a frame WITHOUT an id is an event, fanned out to every
// /events subscriber. The socket stays open for the life of the browser, so a
// `session.subscribe` and its event stream survive across commands — which is
// exactly what bidi_command cannot do alone.
//
// The broker holds the channel; it never interprets a command or composes a
// sequence — that's the host's job. stdlib only + internal/wsx (the same
// hand-rolled RFC 6455 client the wire's bidi_command uses).
//
// Usage: channel -ws ws://127.0.0.1:9222/session/<id> -listen :4445
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rrrishi123/http-mcp/internal/wsx"
)

type hub struct {
	conn *wsx.Conn

	mu      sync.Mutex
	pending map[int]chan json.RawMessage // command id -> where its response is delivered
	subs    map[int]chan json.RawMessage // subscriber id -> its event channel

	nextCmd   int64 // atomic: broker owns the connection's id space
	nextSub   int64 // atomic
	started   time.Time
	lastFrame atomic.Int64 // unixnano of the last frame seen
	cmdCount  int64        // atomic
	evtCount  int64        // atomic
}

func newHub(conn *wsx.Conn) *hub {
	return &hub{
		conn:    conn,
		pending: map[int]chan json.RawMessage{},
		subs:    map[int]chan json.RawMessage{},
		started: time.Now(),
	}
}

// read is the single reader: it routes every frame for the life of the socket.
func (h *hub) read() {
	for {
		frame, err := h.conn.ReadText()
		if err != nil {
			log.Printf("channel: socket closed: %v", err)
			h.shutdown()
			return
		}
		h.lastFrame.Store(time.Now().UnixNano())
		var probe struct {
			ID *int `json:"id"`
		}
		_ = json.Unmarshal([]byte(frame), &probe)
		raw := json.RawMessage(frame)

		if probe.ID != nil { // a command response — deliver to whoever's waiting
			h.mu.Lock()
			ch := h.pending[*probe.ID]
			delete(h.pending, *probe.ID)
			h.mu.Unlock()
			if ch != nil {
				ch <- raw
			}
			continue
		}
		// an event — fan out, non-blocking (a slow consumer never stalls the wire)
		atomic.AddInt64(&h.evtCount, 1)
		h.mu.Lock()
		for _, sc := range h.subs {
			select {
			case sc <- raw:
			default:
			}
		}
		h.mu.Unlock()
	}
}

func (h *hub) shutdown() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, ch := range h.pending {
		close(ch)
		delete(h.pending, id)
	}
	for id, sc := range h.subs {
		close(sc)
		delete(h.subs, id)
	}
}

// handleCommand sends one command on the HELD socket and returns its response.
// The broker assigns the id (it owns the connection), so concurrent consumers
// never collide on the id space.
func (h *hub) handleCommand(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Method == "" {
		http.Error(w, `{"error":"method is required"}`, http.StatusBadRequest)
		return
	}
	if len(in.Params) == 0 {
		in.Params = json.RawMessage("{}")
	}
	id := int(atomic.AddInt64(&h.nextCmd, 1))
	cmd, _ := json.Marshal(map[string]any{"id": id, "method": in.Method, "params": in.Params})

	ch := make(chan json.RawMessage, 1)
	h.mu.Lock()
	h.pending[id] = ch
	h.mu.Unlock()

	if err := h.conn.WriteText(string(cmd)); err != nil {
		h.mu.Lock()
		delete(h.pending, id)
		h.mu.Unlock()
		http.Error(w, `{"error":"channel write failed: `+err.Error()+`"}`, http.StatusBadGateway)
		return
	}
	atomic.AddInt64(&h.cmdCount, 1)

	timeout := 30 * time.Second
	if q := r.URL.Query().Get("timeout_ms"); q != "" {
		if ms, err := time.ParseDuration(q + "ms"); err == nil && ms > 0 {
			timeout = ms
		}
	}
	w.Header().Set("Content-Type", "application/json")
	select {
	case resp, ok := <-ch:
		if !ok {
			http.Error(w, `{"error":"channel closed before response"}`, http.StatusBadGateway)
			return
		}
		w.Write(resp)
	case <-time.After(timeout):
		h.mu.Lock()
		delete(h.pending, id)
		h.mu.Unlock()
		http.Error(w, `{"error":"timeout waiting for response"}`, http.StatusGatewayTimeout)
	}
}

// handleEvents is the afferent fan-out: an SSE stream of every id-less frame.
// Subscribe here, then POST a session.subscribe — the events arrive on the held
// socket and stream out to you, no polling, no fresh connection.
func (h *hub) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	id := int(atomic.AddInt64(&h.nextSub, 1))
	ch := make(chan json.RawMessage, 256)
	h.mu.Lock()
	h.subs[id] = ch
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.subs, id)
		h.mu.Unlock()
	}()

	fmt.Fprintf(w, ": subscribed %d\n\n", id)
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", ev)
			flusher.Flush()
		}
	}
}

// handleAwait blocks until an id-less frame whose text contains `contains`
// arrives, then returns that one event. It turns the afferent stream into a
// single request/response, so a plain caller (http_request — the call atom)
// can consume the notification without streaming or polling. This is what lets
// "wait for the peer's reply" be one GET, not a curl SSE or a shell sleep.
func (h *hub) handleAwait(w http.ResponseWriter, r *http.Request) {
	contains := r.URL.Query().Get("contains")
	timeout := 60 * time.Second
	if q := r.URL.Query().Get("timeout_ms"); q != "" {
		if ms, err := time.ParseDuration(q + "ms"); err == nil && ms > 0 {
			timeout = ms
		}
	}
	id := int(atomic.AddInt64(&h.nextSub, 1))
	ch := make(chan json.RawMessage, 256)
	h.mu.Lock()
	h.subs[id] = ch
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.subs, id)
		h.mu.Unlock()
	}()

	deadline := time.After(timeout)
	w.Header().Set("Content-Type", "application/json")
	for {
		select {
		case <-r.Context().Done():
			return
		case <-deadline:
			http.Error(w, `{"error":"timeout waiting for event"}`, http.StatusGatewayTimeout)
			return
		case ev, ok := <-ch:
			if !ok {
				http.Error(w, `{"error":"channel closed"}`, http.StatusBadGateway)
				return
			}
			if contains == "" || strings.Contains(string(ev), contains) {
				w.Write(ev)
				return
			}
		}
	}
}

func (h *hub) handleHealth(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	inflight, subs := len(h.pending), len(h.subs)
	h.mu.Unlock()
	last := "never"
	if n := h.lastFrame.Load(); n > 0 {
		last = time.Since(time.Unix(0, n)).Round(time.Millisecond).String() + " ago"
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"alive":       true,
		"uptime":      time.Since(h.started).Round(time.Second).String(),
		"last_frame":  last,
		"commands":    atomic.LoadInt64(&h.cmdCount),
		"events":      atomic.LoadInt64(&h.evtCount),
		"in_flight":   inflight,
		"subscribers": subs,
	})
}

func main() {
	wsURL := flag.String("ws", "", "the browser channel to hold (ws://host:port/... for BiDi or CDP)")
	listen := flag.String("listen", ":4445", "HTTP address consumers reach the broker on")
	flag.Parse()
	if *wsURL == "" {
		log.Fatal("channel: -ws is required (e.g. ws://127.0.0.1:9222/session/<id>)")
	}

	conn, err := wsx.Dial(*wsURL, 10*time.Second)
	if err != nil {
		log.Fatalf("channel: dial %s: %v", *wsURL, err)
	}
	h := newHub(conn)
	go h.read()

	mux := http.NewServeMux()
	mux.HandleFunc("/command", h.handleCommand)
	mux.HandleFunc("/events", h.handleEvents)
	mux.HandleFunc("/await", h.handleAwait)
	mux.HandleFunc("/health", h.handleHealth)

	log.Printf("channel: holding %s, serving on %s (POST /command, GET /events, GET /health)", *wsURL, *listen)
	log.Fatal(http.ListenAndServe(*listen, mux))
}
