package main

import _ "embed"

// transportsJSON is the single machine-readable source of truth for the wire's
// transport extent — the 7-candidate list mapped onto the two atoms. The
// `transports` MCP tool returns it verbatim; TRANSPORTS.md is the prose mirror.
//
//go:embed transports.json
var transportsJSON []byte
