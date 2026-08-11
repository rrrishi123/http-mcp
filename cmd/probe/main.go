// Thin wrapper (#77): the probe logic now lives in internal/probe as an
// importable Run([]string) int, so cmd/eight can call it IN-PROCESS. This binary
// stays for standalone use and is behaviour-identical to before.
package main

import (
	"os"

	"github.com/rrrishi123/http-mcp/internal/probe"
)

func main() { os.Exit(probe.Run(os.Args[1:])) }
