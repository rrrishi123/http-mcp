# Transport self-tests

**Hermetic (CI):** `internal/httpx/transport_test.go` proves the CALL atom (`httpx.Do`)
across the transports the wire calls "live" — plain HTTP, a JSON object-body round-trip,
an SSE afferent stream, and a unix-domain socket. No network, no browser.

    go test ./internal/httpx/ -run TestCall -v

**Live end-to-end (manual, through the wire):** `test/wire-mock.py` stands up the same
four endpoints on `127.0.0.1:8791` + a unix socket, so you can drive them through the
actual MCP tools (`http_request`) exactly as an agent would — the way pilot uses the wire:

    python3 test/wire-mock.py &
    #  http_request GET  http://127.0.0.1:8791/hello
    #  http_request POST http://127.0.0.1:8791/echo   body={"probe":"x"}
    #  http_request GET  http://127.0.0.1:8791/events           (SSE frames)
    #  http_request GET  http+unix:///tmp/wire-mock.sock:/ping  (unix socket)

Verified live 2026-07-25: all four returned 200 through the http_request tool;
`bidi_command` (CHANNEL) proven against Firefox BiDi; `discover` re-probed geckodriver.

**Still needs an adapter (no wire path yet):** gRPC, MQTT, WebRTC, and generic
non-CDP/BiDi WebSocket protocols — the CHANNEL atom speaks CDP/BiDi framing.
