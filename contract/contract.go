// Package contract is the four-system's WIRE CONTRACT — the one thing every
// arm (8, http-mcp, pilot, adapters) agrees on, versioned on its own.
//
// The shared README ("Versioning") promises: independent semver per arm + a
// separately-versioned contract (trace format, RunRequest/RunResult); each arm
// declares which contract version it supports; the replay runner checks a
// trace's contract version before replaying. This package is that contract:
//
//   - contract.Version, Compatible          — the version every arm declares
//   - Mode / Dir / Atom                      — the two atoms the wire reduces to
//   - contract/transports                    — the 8-transports-over-2-atoms table
//     (the `transports` tool serves it verbatim; Coherent enforces its honesty)
//   - contract/trace                         — the neutral NDJSON trace 8 records
//     and an adapter replays (auth_slot, never a secret)
//   - contract/run                           — Request/Result between an adapter's
//     `up` and the host (pilot) that seats it and the witness (8) that watches
//
// It lives in http-mcp because the dependency arrow points at the wire, never
// away, and it is a package of the root module (not a nested module) because
// install.sh obtains the commands with `go install ...@version`, which forbids
// the replace directive a nested module would need. It has NO imports beyond
// the standard library — importing it pulls nothing else.
package contract

import "strings"

// Version is the contract version this tree speaks. Bumped on its own cadence,
// independent of the arms' tags. Baseline v0.0.2, the documented baseline.
const Version = "v0.0.2"

// Compatible reports whether an artifact stamped v (a trace, a run result) can
// be consumed by this contract: same major.minor. An empty stamp is compatible
// — pre-stamp artifacts did not change shape, only the stamp arrived.
func Compatible(v string) bool {
	if v == "" {
		return true
	}
	return majorMinor(v) == majorMinor(Version)
}

func majorMinor(v string) string {
	v = strings.TrimPrefix(v, "v")
	if i := strings.LastIndexByte(v, '.'); i > 0 {
		return v[:i]
	}
	return v
}

// The wire is two atoms. Every transport reduces to one of these two MODES
// (transports are dialects, not a third thing).
const (
	ModeCall    = "call"    // one request -> one response, then nothing
	ModeChannel = "channel" // one long-lived duplex stream: commands out, events in
)

// Dir is a frame's direction relative to the model/host. EFFERENT flows toward
// the target (an act); AFFERENT flows back (an observation). The model learns
// only from afferent frames — an act is known by its afferent result.
const (
	DirEfferent = "efferent"
	DirAfferent = "afferent"
)

// The two atoms by their tool names on the wire.
const (
	AtomCall    = "http_request"
	AtomChannel = "bidi_command"
)
