package main

// Transport conformance — the one question the wire must be able to answer about
// itself: "for every transport I advertise, does a live probe agree with the
// status I claim?" transports.json is a PRIOR (a snapshot). discover's creed is
// "probe verdict beats snapshot claim, always" — this test holds the manifest to
// that same creed.
//
// Two guards, so advertisement and reality cannot drift apart:
//   1. TestManifest_Coherent — the manifest is internally honest (wire ⇒ stdlib &
//      live; adapter ⇒ needs-adapter & names a provider). Pure, no network.
//   2. TestManifest_LiveWireTransportsActuallyWork — every transport the manifest
//      calls a LIVE WIRE transport is round-tripped through its atom against a
//      hermetic local server. A live-wire transport with no prober FAILS the
//      test: you cannot advertise a new live transport without also proving it.
//
// Hermetic: httptest + net.Listen only; no browser, no external host. The two
// CHANNEL transports (websocket, unix-channel) run against a ~40-line RFC6455
// echo server below — enough to prove wsx round-trips, not a general WS server.

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rrrishi123/http-mcp/internal/httpx"
	"github.com/rrrishi123/http-mcp/internal/wsx"
)

type transportEntry struct {
	Name       string `json:"name"`
	Mode       string `json:"mode"`
	Where      string `json:"where"`
	Atom       string `json:"atom"`
	Status     string `json:"status"`
	StdlibOnly bool   `json:"stdlib_only"`
	ProvidedBy string `json:"provided_by"`
}

type transportManifest struct {
	Transports []transportEntry `json:"transports"`
}

// loadManifest parses the SAME embedded bytes the `transports` tool serves.
func loadManifest(t *testing.T) transportManifest {
	t.Helper()
	var m transportManifest
	if err := json.Unmarshal(transportsJSON, &m); err != nil {
		t.Fatalf("transports.json (the advertised artifact) does not parse: %v", err)
	}
	if len(m.Transports) == 0 {
		t.Fatal("manifest advertises zero transports")
	}
	return m
}

// TestManifest_Coherent: the advertisement is internally honest. No over-promise.
func TestManifest_Coherent(t *testing.T) {
	for _, e := range loadManifest(t).Transports {
		if e.Name == "" || e.Mode == "" || e.Where == "" || e.Status == "" {
			t.Fatalf("incomplete manifest entry: %+v", e)
		}
		switch e.Where {
		case "wire":
			if e.Atom == "" {
				t.Errorf("%s: a wire transport must name the atom that serves it", e.Name)
			}
			if !e.StdlibOnly {
				t.Errorf("%s: a wire transport must be stdlib_only (no deps is the whole claim)", e.Name)
			}
			if e.Status != "live" {
				t.Errorf("%s: a wire transport advertised %q — the wire only claims what it serves now", e.Name, e.Status)
			}
		case "adapter":
			if e.Status != "needs-adapter" {
				t.Errorf("%s: an adapter transport must be status=needs-adapter, got %q", e.Name, e.Status)
			}
			if e.ProvidedBy == "" {
				t.Errorf("%s: an adapter transport must name its provided_by so the claim is traceable", e.Name)
			}
			if e.StdlibOnly {
				t.Errorf("%s: an adapter transport cannot be stdlib_only", e.Name)
			}
		default:
			t.Errorf("%s: unknown where %q (want wire|adapter)", e.Name, e.Where)
		}
		if e.Atom != "" { // adapters may omit atom; validate only when present
			switch e.Atom {
			case "http_request", "bidi_command", "http_request|bidi_command":
			default:
				t.Errorf("%s: atom %q is not one of the two atoms", e.Name, e.Atom)
			}
		}
	}
}

// TestManifest_LiveWireTransportsActuallyWork: THE question, as code.
func TestManifest_LiveWireTransportsActuallyWork(t *testing.T) {
	probers := map[string]func(*testing.T){
		"http":        probeHTTP,
		"websocket":   probeWebSocket,
		"sse":         probeSSE,
		"mjpeg":       probeMJPEG,
		"unix_socket": probeUnixSocket,
	}
	for _, e := range loadManifest(t).Transports {
		if e.Where != "wire" || e.Status != "live" {
			continue // adapters are asserted by TestManifest_Coherent, not round-tripped here
		}
		p, ok := probers[e.Name]
		if !ok {
			t.Fatalf("manifest advertises live wire transport %q but no probe exists — "+
				"advertisement and proof must move together; add a prober", e.Name)
		}
		t.Run(e.Name, p)
	}
}

// --- probers: each stands up a hermetic server and round-trips one message ---

func probeHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()
	resp, err := httpx.Do(nil, httpx.Request{Method: "GET", URL: srv.URL})
	if err != nil || resp.Status != 200 || !strings.Contains(resp.Body, `"ok":true`) {
		t.Fatalf("CALL over http failed: err=%v resp=%+v", err, resp)
	}
}

func probeSSE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := range 3 {
			fmt.Fprintf(w, "event: tick\ndata: {\"seq\":%d}\n\n", i)
		}
	}))
	defer srv.Close()
	resp, err := httpx.Do(nil, httpx.Request{Method: "GET", URL: srv.URL})
	if err != nil {
		t.Fatalf("sse: %v", err)
	}
	if ct := resp.Headers["Content-Type"]; len(ct) == 0 || !strings.HasPrefix(ct[0], "text/event-stream") {
		t.Fatalf("sse content-type = %v", resp.Headers["Content-Type"])
	}
	if n := strings.Count(resp.Body, "event: tick"); n != 3 {
		t.Fatalf("sse afferent frames = %d, want 3", n)
	}
}

// probeMJPEG finally exercises the media plane the manifest calls "live" — the
// afferent multipart/x-mixed-replace channel riding httpx. (Uses a finite stream;
// real /stream is unbounded, but the framing + content-type are what we prove.)
func probeMJPEG(t *testing.T) {
	const boundary = "frameboundary"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary="+boundary)
		for range 2 {
			fmt.Fprintf(w, "--%s\r\nContent-Type: image/jpeg\r\n\r\n", boundary)
			w.Write([]byte{0xFF, 0xD8, 0xFF, 0xD9}) // minimal JPEG: SOI + EOI
			fmt.Fprint(w, "\r\n")
		}
	}))
	defer srv.Close()
	resp, err := httpx.Do(nil, httpx.Request{Method: "GET", URL: srv.URL})
	if err != nil {
		t.Fatalf("mjpeg: %v", err)
	}
	if ct := resp.Headers["Content-Type"]; len(ct) == 0 || !strings.HasPrefix(ct[0], "multipart/x-mixed-replace") {
		t.Fatalf("mjpeg content-type = %v", resp.Headers["Content-Type"])
	}
	if n := strings.Count(resp.Body, "--"+boundary); n != 2 {
		t.Fatalf("mjpeg parts = %d, want 2", n)
	}
}

func probeUnixSocket(t *testing.T) {
	// A unix socket path must fit sun_path (104 bytes on macOS); the default
	// $TMPDIR + long subtest name overflows it, so use a short /tmp dir.
	dir, err := os.MkdirTemp("/tmp", "w")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	defer os.RemoveAll(dir)

	// CALL half: http+unix — same CALL bytes over a unix dialer.
	callSock := filepath.Join(dir, "call.sock")
	ln, err := net.Listen("unix", callSock)
	if err != nil {
		t.Fatalf("unix listen: %v", err)
	}
	defer ln.Close()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"unix":true}`)
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	time.Sleep(20 * time.Millisecond)
	resp, err := httpx.Do(nil, httpx.Request{Method: "GET", URL: "http+unix://" + callSock + ":/ping"})
	if err != nil || resp.Status != 200 || !strings.Contains(resp.Body, `"unix":true`) {
		t.Fatalf("CALL over unix failed: err=%v resp=%+v", err, resp)
	}

	// CHANNEL half: ws+unix — the manifest says unix_socket is CALL|CHANNEL, so
	// prove the channel half too, not just the call half.
	chanSock := filepath.Join(dir, "chan.sock")
	uln, err := net.Listen("unix", chanSock)
	if err != nil {
		t.Fatalf("unix chan listen: %v", err)
	}
	defer uln.Close()
	go acceptEcho(uln)
	c, err := wsx.Dial("ws+unix://"+chanSock+":/ws", 3*time.Second)
	if err != nil {
		t.Fatalf("CHANNEL dial over unix: %v", err)
	}
	defer c.Close()
	roundtripWS(t, c)
}

func probeWebSocket(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go acceptEcho(ln)
	c, err := wsx.Dial("ws://"+ln.Addr().String()+"/ws", 3*time.Second)
	if err != nil {
		t.Fatalf("CHANNEL dial: %v", err)
	}
	defer c.Close()
	roundtripWS(t, c)
}

func roundtripWS(t *testing.T, c *wsx.Conn) {
	t.Helper()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	if err := c.WriteText(`{"id":1,"method":"ping"}`); err != nil {
		t.Fatalf("channel write: %v", err)
	}
	got, err := c.ReadText()
	if err != nil {
		t.Fatalf("channel read: %v", err)
	}
	if !strings.Contains(got, `"method":"ping"`) {
		t.Fatalf("channel echo mismatch: %q", got)
	}
}

// --- minimal RFC6455 echo server: handshake, read one masked client text frame,
//     write it back unmasked. Just enough to prove wsx round-trips a CHANNEL. ---

func acceptEcho(ln net.Listener) {
	conn, err := ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	var key string
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if strings.HasPrefix(strings.ToLower(line), "sec-websocket-key:") {
			key = strings.TrimSpace(line[len("sec-websocket-key:"):])
		}
	}
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	fmt.Fprintf(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\n"+
		"Connection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n",
		base64.StdEncoding.EncodeToString(sum[:]))
	payload, ok := readClientFrame(br)
	if !ok {
		return
	}
	writeServerFrame(conn, payload)
}

func readClientFrame(br *bufio.Reader) ([]byte, bool) {
	if _, err := br.ReadByte(); err != nil { // FIN|opcode — text, unfragmented
		return nil, false
	}
	b1, err := br.ReadByte()
	if err != nil {
		return nil, false
	}
	masked := b1&0x80 != 0
	n := int(b1 & 0x7f)
	if n == 126 {
		var ext [2]byte
		if _, err := io.ReadFull(br, ext[:]); err != nil {
			return nil, false
		}
		n = int(ext[0])<<8 | int(ext[1])
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(br, mask[:]); err != nil {
			return nil, false
		}
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(br, payload); err != nil {
		return nil, false
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return payload, true
}

func writeServerFrame(w io.Writer, payload []byte) {
	n := len(payload)
	hdr := []byte{0x81} // FIN + text
	if n < 126 {
		hdr = append(hdr, byte(n))
	} else {
		hdr = append(hdr, 126, byte(n>>8), byte(n))
	}
	_, _ = w.Write(hdr)
	_, _ = w.Write(payload)
}
