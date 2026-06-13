// Package session keeps many live sessions at once — local drivers and cloud
// providers, browsers and devices — each supervised by its own goroutine.
//
// The store is a PRIOR, not the truth. A stored entry is a hypothesis; the
// supervisor verifies it against the live wire and corrects or removes it.
// Concurrency here is structure, not speed (Pike): one goroutine per session,
// each mostly parked on a ticker or an I/O wait, owning its lifecycle alone.
package session

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/rrrishi123/http-mcp/internal/httpx"
)

// Kind selects the supervisor's behavior. The two subtypes differ only in how
// the wire treats an idle session: a local driver keeps it until the process
// dies; a cloud provider reaps it after an idle timeout.
type Kind string

const (
	Local Kind = "local"
	Cloud Kind = "cloud"
)

// Tunables. Cloud heartbeat must be shorter than the provider's idle timeout
// (LT/BS/SL default to ~90s); local watch only needs to notice death.
var (
	CloudHeartbeat = 60 * time.Second
	LocalWatch     = 15 * time.Second
)

// Session is fully described by (HubURL, ID). Everything else is bookkeeping.
type Session struct {
	ID        string         `json:"id"`
	HubURL    string         `json:"hub_url"`
	Kind      Kind           `json:"kind"`
	Caps      map[string]any `json:"caps,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	LastSeen  time.Time      `json:"last_seen"`
	Alive     bool           `json:"alive"`

	cancel context.CancelFunc
}

// Store owns the live set. In-memory by design: it is this process's view of
// what it is responsible for. Persistence, if ever added, is a snapshot for
// handoff — never trusted over a live probe.
type Store struct {
	mu       sync.Mutex
	sessions map[string]*Session
	client   *http.Client
	// OnDeath, if set, is called when a supervisor reaps a session.
	OnDeath func(id string, reason string)
}

func New() *Store {
	return &Store{
		sessions: map[string]*Session{},
		client:   &http.Client{Timeout: 20 * time.Second},
	}
}

// Add registers a session and spawns its supervisor goroutine.
func (s *Store) Add(sess *Session) {
	if sess.CreatedAt.IsZero() {
		sess.CreatedAt = time.Now()
	}
	sess.LastSeen = time.Now()
	sess.Alive = true
	ctx, cancel := context.WithCancel(context.Background())
	sess.cancel = cancel

	s.mu.Lock()
	s.sessions[sess.ID] = sess
	s.mu.Unlock()

	go s.supervise(ctx, sess)
}

// Get returns a snapshot copy (no internal pointers leak out).
func (s *Store) Get(id string) (Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return Session{}, false
	}
	return *sess, true
}

// List returns snapshot copies of every live session.
func (s *Store) List() []Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		out = append(out, *sess)
	}
	return out
}

// Delete cancels the supervisor and drops the entry. It does NOT call the
// remote DELETE /session — the caller decides whether to end the remote
// session or just stop tracking it.
func (s *Store) Delete(id string) bool {
	s.mu.Lock()
	sess, ok := s.sessions[id]
	if ok {
		delete(s.sessions, id)
	}
	s.mu.Unlock()
	if ok && sess.cancel != nil {
		sess.cancel()
	}
	return ok
}

// supervise is the per-session goroutine. It re-perceives the session on a
// cadence set by Kind: cloud sessions get a keep-warm heartbeat, local
// sessions get a death watch. Either way the live wire — not the stored
// entry — decides whether the session still exists.
func (s *Store) supervise(ctx context.Context, sess *Session) {
	interval := LocalWatch
	if sess.Kind == Cloud {
		interval = CloudHeartbeat
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if s.probe(sess) {
				s.mark(sess.ID, true)
				continue
			}
			// The wire says it's gone. The prior was stale; correct it.
			s.reap(sess.ID, "session_unreachable")
			return
		}
	}
}

// probe is the re-perception: a cheap read on the session. For cloud sessions
// this read also resets the provider's idle timer (keep-warm); for local it
// merely confirms the handle still answers. Returns true if the session lives.
func (s *Store) probe(sess *Session) bool {
	resp, err := httpx.Do(s.client, httpx.Request{
		Method: "GET",
		URL:    sess.HubURL + "/session/" + sess.ID + "/url",
	})
	if err != nil {
		return false
	}
	// 200 = alive. A WebDriver "invalid session id" (404 here) = dead.
	return resp.Status == 200
}

func (s *Store) mark(id string, alive bool) {
	s.mu.Lock()
	if sess, ok := s.sessions[id]; ok {
		sess.Alive = alive
		sess.LastSeen = time.Now()
	}
	s.mu.Unlock()
}

func (s *Store) reap(id, reason string) {
	s.mu.Lock()
	_, ok := s.sessions[id]
	delete(s.sessions, id)
	s.mu.Unlock()
	if ok && s.OnDeath != nil {
		s.OnDeath(id, reason)
	}
}
