// Package trace defines the NEUTRAL, provider-agnostic session trace — the
// contract between 8 (the witness, which RECORDS) and an adapter's replay runner
// (which MATERIALIZES the trace into a provider-specific, replayable suite).
// Moved intact from adapters/trace (v0.0.2): the arrow points at the wire.
//
// Design (locked with the peer):
//   - 8 observes the wire across every session and emits Frames. It never learns
//     which provider a trace came from — it records raw wire events only.
//   - An adapter reads the trace and re-executes it through http-mcp's two MODES
//     (CALL = http_request, CHANNEL = bidi_command), interpreting placeholders and
//     provider shape through its own spec.json.
//   - A Frame NEVER carries a credential. Authorization is stripped to an AuthSlot
//     naming WHERE the resolved profile credential must be injected at replay time.
//     The secret stays below the boundary — the trace is safe to store and share.
//
// Wire format: NDJSON (one Frame per line) so it streams and appends cheaply.
package trace

import (
	"bufio"
	"encoding/json"
	"io"

	"github.com/rrrishi123/http-mcp/contract"
)

// Version and Compatible are the contract's (contract.Version): every emitted
// Frame is stamped with it, and a reader refuses a trace it cannot replay.
const Version = contract.Version

// Compatible reports whether a trace stamped v can be replayed here.
func Compatible(v string) bool { return contract.Compatible(v) }

// Mode and Dir values are the contract's atoms, re-exported for callers.
const (
	ModeCall    = contract.ModeCall
	ModeChannel = contract.ModeChannel
	DirEfferent = contract.DirEfferent
	DirAfferent = contract.DirAfferent
)

// Frame is one neutral event in a recorded session.
type Frame struct {
	Contract string `json:"contract,omitempty"` // Version at emission; a reader checks Compatible
	Seq      int    `json:"seq"`                // monotonic order within a session
	TS       int64  `json:"ts"`                 // unix millis at observation
	Session  string `json:"session"`            // opaque id for one held context / build
	Mode     string `json:"mode"`               // ModeCall | ModeChannel
	Dir      string `json:"dir"`                // DirEfferent | DirAfferent

	// CALL mode
	Method string `json:"method,omitempty"` // efferent: HTTP method
	URL    string `json:"url,omitempty"`    // efferent: target; may carry {placeholders}
	Status int    `json:"status,omitempty"` // afferent: HTTP status

	// CHANNEL mode
	Command string `json:"command,omitempty"` // efferent: protocol method (CDP/BiDi/...)
	Event   string `json:"event,omitempty"`   // afferent: event/result name

	// shared
	Headers  map[string]string `json:"headers,omitempty"`   // auth-stripped
	Body     json.RawMessage   `json:"body,omitempty"`      // request/response/params/result
	AuthSlot string            `json:"auth_slot,omitempty"` // names WHERE a credential injects; never the secret
}

// Writer emits frames as NDJSON.
type Writer struct {
	w   io.Writer
	seq int
}

// NewWriter wraps w for NDJSON frame emission.
func NewWriter(w io.Writer) *Writer { return &Writer{w: w} }

// Emit assigns the next sequence number, stamps the contract Version, and
// writes the frame as one JSON line. The caller sets Mode/Dir/payload; Seq
// and Contract are owned by the Writer.
func (e *Writer) Emit(f Frame) error {
	f.Contract = Version
	f.Seq = e.seq
	e.seq++
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if _, err := e.w.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

// Read consumes an NDJSON trace into frames. Blank lines are skipped so a trace
// can be concatenated or partially flushed without breaking the reader.
func Read(r io.Reader) ([]Frame, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var frames []Frame
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var f Frame
		if err := json.Unmarshal(line, &f); err != nil {
			return frames, err
		}
		frames = append(frames, f)
	}
	return frames, sc.Err()
}
