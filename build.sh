#!/usr/bin/env bash
# build.sh — the canonical build. Each cmd/ goes to its OWN name in .bin/, so no
# binary can clobber another. mcp -> .bin/http-mcp (the name ~/.claude.json's MCP
# server config expects). This exists because a hand-run `go build -o .bin/http-mcp
# ./cmd/wire` once overwrote the MCP server with the witness (cmd/wire), breaking
# every agent's /mcp with -32000. Build with THIS, not ad-hoc `go build -o`.
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p .bin
go build -o .bin/http-mcp ./cmd/mcp      # the MCP server (stdio) — the wire agents launch
go build -o http-mcp      ./cmd/mcp      # #27: ALSO write the CONFIG path (~/.claude.json runs ./http-mcp) so
                                         # `./build.sh` + /reconnect deploys what was just built, not a stale binary
go build -o .bin/wire     ./cmd/wire     # the HTTP witness (:4724)
go build -o .bin/channel  ./cmd/channel  # the BiDi broker
go build -o .bin/harvest  ./cmd/harvest
go build -o .bin/probe    ./cmd/probe
go build -o .bin/session  ./cmd/session
go build -o .bin/eight     ./cmd/eight  # #38 the single-command dispatcher: `eight <atom>` over all bodies
echo "built: $(ls .bin/ | tr '\n' ' ')"
