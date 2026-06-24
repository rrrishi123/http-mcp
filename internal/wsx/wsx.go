// Package wsx is the second wire physics: the channel.
//
// httpx is a call — one request, one response, then nothing. wsx is a
// connection — you produce frames into it and consume frames out of it, and it
// stays open. CDP (Chrome DevTools Protocol) and WebDriver BiDi both ride this
// shape; Playwright, Puppeteer, Selenium-BiDi are composers over it, the way
// Selenium/Appium clients are composers over HTTP. So mapping this one channel
// maps all of them.
//
// No client library, no framework — just net + a hand-rolled RFC 6455 handshake
// and frame codec, the same way httpx is just net/http. A channel is an upgrade
// over a socket and a small framing rule; we speak it directly.
package wsx

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"
)

// wsGUID is the RFC 6455 magic string mixed into the accept-key proof.
const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// Conn is one open WebSocket. Clients MUST mask the frames they send (§5.3);
// servers MUST NOT mask theirs — the codec below honors both directions.
//
// A held channel has one reader (which may emit pong frames inline) and many
// command writers; wmu serializes all writes so their frames never interleave.
// Reads are single-reader by contract, so they need no lock.
type Conn struct {
	raw net.Conn
	r   *bufio.Reader
	wmu sync.Mutex
}

// write serializes every frame onto the socket — WriteText (callers) and the
// inline pong (the reader) can both fire, and a half-written frame corrupts the
// stream for everyone.
func (c *Conn) write(b []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err := c.raw.Write(b)
	return err
}

// Dial opens ws:// or wss:// and completes the upgrade handshake. The returned
// Conn is ready to WriteText/ReadText. timeout bounds the dial + handshake.
// Dial opens a WebSocket with the standard handshake (no extra headers).
func Dial(rawURL string, timeout time.Duration) (*Conn, error) {
	return DialWithHeaders(rawURL, timeout, nil)
}

// DialWithHeaders opens a WebSocket, adding caller-supplied request headers to
// the opening handshake. Some channels gate the UPGRADE itself on a header
// (e.g. LambdaTest /playwright wants x-playwright-browser, else 400) — these are
// normal WS upgrade headers, not protocol; wsx stays a generic channel atom. The
// headers wsx owns (Host/Upgrade/Connection/Sec-WebSocket-*) cannot be overridden.
func DialWithHeaders(rawURL string, timeout time.Duration, headers map[string]string) (*Conn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	host := u.Host
	switch u.Scheme {
	case "ws":
		if !strings.Contains(host, ":") {
			host += ":80"
		}
	case "wss":
		if !strings.Contains(host, ":") {
			host += ":443"
		}
	default:
		return nil, fmt.Errorf("wsx: unsupported scheme %q (want ws or wss)", u.Scheme)
	}

	var raw net.Conn
	if u.Scheme == "wss" {
		raw, err = tls.DialWithDialer(&net.Dialer{Timeout: timeout}, "tcp", host, &tls.Config{ServerName: hostOnly(host)})
	} else {
		raw, err = net.DialTimeout("tcp", host, timeout)
	}
	if err != nil {
		return nil, err
	}
	raw.SetDeadline(time.Now().Add(timeout))

	// Client opening handshake: an HTTP/1.1 Upgrade with a random nonce; the
	// server proves it speaks WebSocket by echoing sha1(nonce+GUID) back.
	var nonce [16]byte
	rand.Read(nonce[:])
	key := base64.StdEncoding.EncodeToString(nonce[:])
	path := u.RequestURI()
	if path == "" {
		path = "/"
	}
	var extra strings.Builder
	for k, v := range headers {
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "host", "upgrade", "connection", "sec-websocket-key", "sec-websocket-version":
			continue // wsx owns these; never let a caller forge the handshake
		}
		extra.WriteString(k + ": " + v + "\r\n")
	}
	req := "GET " + path + " HTTP/1.1\r\n" +
		"Host: " + u.Host + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		extra.String() + "\r\n"
	if _, err := raw.Write([]byte(req)); err != nil {
		raw.Close()
		return nil, err
	}

	br := bufio.NewReader(raw)
	status, err := br.ReadString('\n')
	if err != nil {
		raw.Close()
		return nil, err
	}
	if !strings.Contains(status, " 101") {
		raw.Close()
		return nil, fmt.Errorf("wsx: upgrade refused: %s", strings.TrimSpace(status))
	}
	want := acceptKey(key)
	accepted := false
	for { // drain + verify response headers up to the blank line
		line, err := br.ReadString('\n')
		if err != nil {
			raw.Close()
			return nil, err
		}
		t := strings.TrimSpace(line)
		if t == "" {
			break
		}
		if k, v, ok := strings.Cut(t, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "Sec-WebSocket-Accept") {
			accepted = strings.TrimSpace(v) == want
		}
	}
	if !accepted {
		raw.Close()
		return nil, fmt.Errorf("wsx: server did not prove the handshake (bad Sec-WebSocket-Accept)")
	}
	raw.SetDeadline(time.Time{}) // clear; caller sets per-exchange deadlines
	return &Conn{raw: raw, r: br}, nil
}

func acceptKey(clientKey string) string {
	h := sha1.Sum([]byte(clientKey + wsGUID))
	return base64.StdEncoding.EncodeToString(h[:])
}

func hostOnly(hostport string) string {
	if i := strings.LastIndex(hostport, ":"); i >= 0 {
		return hostport[:i]
	}
	return hostport
}

// SetDeadline bounds the whole exchange (applies to every later read/write).
func (c *Conn) SetDeadline(t time.Time) error { return c.raw.SetDeadline(t) }

// WriteText sends one masked text frame.
func (c *Conn) WriteText(s string) error {
	payload := []byte(s)
	n := len(payload)
	var hdr []byte
	switch {
	case n < 126:
		hdr = []byte{0x81, byte(0x80 | n)}
	case n < 1<<16:
		hdr = []byte{0x81, 0x80 | 126, byte(n >> 8), byte(n)}
	default:
		hdr = make([]byte, 10)
		hdr[0], hdr[1] = 0x81, 0x80|127
		binary.BigEndian.PutUint64(hdr[2:], uint64(n))
	}
	var mask [4]byte
	rand.Read(mask[:])
	buf := make([]byte, 0, len(hdr)+4+n)
	buf = append(buf, hdr...)
	buf = append(buf, mask[:]...)
	for i, b := range payload {
		buf = append(buf, b^mask[i&3])
	}
	return c.write(buf)
}

// ReadText returns the next text-frame payload, answering pings inline and
// skipping pong/binary/continuation frames. Returns io.EOF on a close frame.
func (c *Conn) ReadText() (string, error) {
	for {
		op, payload, err := c.readFrame()
		if err != nil {
			return "", err
		}
		switch op {
		case 0x1: // text
			return string(payload), nil
		case 0x8: // close
			return "", io.EOF
		case 0x9: // ping → pong
			if err := c.writeControl(0xA, payload); err != nil {
				return "", err
			}
		}
	}
}

func (c *Conn) readFrame() (opcode byte, payload []byte, err error) {
	var h [2]byte
	if _, err = io.ReadFull(c.r, h[:]); err != nil {
		return
	}
	opcode = h[0] & 0x0f
	masked := h[1]&0x80 != 0
	n := int(h[1] & 0x7f)
	switch n {
	case 126:
		var e [2]byte
		if _, err = io.ReadFull(c.r, e[:]); err != nil {
			return
		}
		n = int(binary.BigEndian.Uint16(e[:]))
	case 127:
		var e [8]byte
		if _, err = io.ReadFull(c.r, e[:]); err != nil {
			return
		}
		n = int(binary.BigEndian.Uint64(e[:]))
	}
	var mask [4]byte
	if masked {
		if _, err = io.ReadFull(c.r, mask[:]); err != nil {
			return
		}
	}
	payload = make([]byte, n)
	if _, err = io.ReadFull(c.r, payload); err != nil {
		return
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i&3]
		}
	}
	return
}

func (c *Conn) writeControl(opcode byte, payload []byte) error {
	if len(payload) > 125 {
		payload = payload[:125]
	}
	var mask [4]byte
	rand.Read(mask[:])
	buf := make([]byte, 0, 6+len(payload))
	buf = append(buf, 0x80|opcode, byte(0x80|len(payload)))
	buf = append(buf, mask[:]...)
	for i, b := range payload {
		buf = append(buf, b^mask[i&3])
	}
	return c.write(buf)
}

// Close sends a close frame and tears down the socket.
func (c *Conn) Close() error {
	c.writeControl(0x8, nil)
	return c.raw.Close()
}
