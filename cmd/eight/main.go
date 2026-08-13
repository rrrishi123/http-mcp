// Command eight is the single-entry-point dispatcher for the four-body system —
// a busybox-style multi-call: `eight <atom> [args...]`. It gives ONE command
// surface over the wire (CALL/CHANNEL), the witness, and the arms, so an
// operator needs to remember one name, not six binaries.
//
// HONEST SCOPE (#38): this dispatches by EXEC to the per-atom binaries built by
// build.sh (a launcher). It delivers the one-COMMAND claim. It does NOT yet
// deliver the one-BINARY claim (all code compiled into this single executable) —
// that is the mechanical remainder: each cmd/* main must become an importable
// package with a Run([]string) entrypoint, then this switch calls them in-process.
// The #31 finding stands: unrealized, not forbidden — and the SUBJECT (browser/
// device) and pluggable ARMs stay external by design.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/rrrishi123/http-mcp/internal/probe"
)

// subcommand -> the built binary that serves it. "witness" is the collector,
// which lives in the sibling 8 repo; the rest are this module's cmd/* in .bin/.
var atoms = map[string]string{
	"wire":    ".bin/wire",                // the HTTP witness proxy (CALL, transparent)
	"mcp":     ".bin/http",                // the MCP server (the lean wire agents launch)
	"channel": ".bin/channel",             // the BiDi broker (CHANNEL)
	"probe":   ".bin/probe",               // discover — probe, don't assume
	"session": ".bin/session",             // device/session bring-up
	"harvest": ".bin/harvest",             // artifact capture
	"witness": "../8/collector/collector", // the collector (8) — the witness
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	sub := os.Args[1]
	// #77 PROOF: probe runs IN-PROCESS (no exec) via the importable internal/probe
	// package — the mechanism the full one-binary merge generalizes. The other
	// atoms still exec (their mains aren't yet importable); converting each is the
	// same rote move proven here.
	if sub == "probe" {
		os.Exit(probe.Run(os.Args[2:]))
	}
	rel, ok := atoms[sub]
	if !ok {
		fmt.Fprintf(os.Stderr, "eight: unknown atom %q\n\n", sub)
		usage()
		os.Exit(2)
	}
	// resolve the target relative to THIS binary's directory, so `eight` works
	// wherever it's installed as long as the built atoms sit beside it.
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "eight:", err)
		os.Exit(1)
	}
	base := filepath.Dir(self)
	// .bin/eight -> repo root is one up from .bin
	target := filepath.Clean(filepath.Join(base, "..", rel))
	if _, err := os.Stat(target); err != nil {
		// fall back to CWD-relative (running from the repo root)
		target = rel
	}
	cmd := exec.Command(target, os.Args[2:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "eight:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "eight — one command over the four-body system")
	fmt.Fprintln(os.Stderr, "usage: eight <atom> [args...]")
	fmt.Fprintln(os.Stderr, "atoms: wire | mcp | channel | probe | session | harvest | witness")
}
