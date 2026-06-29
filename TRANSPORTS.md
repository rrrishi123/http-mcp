# TRANSPORTS — the wire's extent (7 → 2)

> **For any agent (local or cloud — Claude, DeepSeek, any MCP-tool client) and any human.**
> Machine-readable source of truth: [`cmd/mcp/transports.json`](./cmd/mcp/transports.json), returned verbatim by the http-mcp MCP `transports` tool.
> This file is the prose mirror.

## The one rule

**The wire is two atoms.** Everything else reduces to them, or is an adapter.

| Atom | Mode | Means | Implementation |
|---|---|---|---|
| `http_request` | **CALL** | one request → one response, then nothing | `internal/httpx` (`net/http`, stdlib) |
| `bidi_command` | **CHANNEL** | one long-lived bidirectional stream | `internal/wsx` (hand-rolled RFC 6455, stdlib) |

> **raw bytes = wire. framing / routing / negotiation = adapter.**
> The wire is deliberately lean: **zero external dependencies** in any `go.mod`. It never grows. New protocols are reached by adapters that present CALL or CHANNEL — the wire never learns about them.

## The full candidate list, mapped

```mermaid
flowchart LR
  subgraph WIRE["WIRE — 2 atoms, stdlib-only, never grows"]
    direction TB
    CALL["CALL — http_request (httpx)"]
    CHAN["CHANNEL — bidi_command (wsx)"]
  end
  subgraph ADP["ADAPTERS — dialects (carry framing/negotiation)"]
    direction TB
    G["grpc — proto + HTTP/2 framing"]
    M["mqtt — topic routing + QoS"]
    W["webrtc — SDP/ICE negotiation"]
  end

  HTTP["HTTP ✅ live"] --> CALL
  UNIXc["Unix socket ✅ live (unix dialer)"] --> CALL
  WS["WebSocket ✅ live"] --> CHAN
  UNIXch["Unix socket ✅ live"] --> CHAN
  SSE["SSE ✅ live (afferent)"] --> CALL
  MJPEG["MJPEG ✅ live (afferent media)"] --> CALL

  G --> CALL
  G --> CHAN
  M --> CHAN
  W -. "SDP signalled over a CALL" .-> CALL
  W --> CHAN
```

| # | Transport | Mode | Where | Status | Why |
|---|---|---|---|---|---|
| 1 | **HTTP** | CALL | wire | ✅ live | raw request/response — `net/http` |
| 2 | **WebSocket** | CHANNEL | wire | ✅ live | raw full-duplex bytes — `wsx` |
| 3 | **SSE** | CHANNEL (afferent) | wire | ✅ live | server-push over httpx (`text/event-stream`); 8's `/feed` |
| 4 | **MJPEG** | CHANNEL (afferent) | wire | ✅ live | media-push over httpx (`multipart/x-mixed-replace`); 8's `/stream` |
| 5 | **Unix socket** | CALL \| CHANNEL | wire | ✅ live | same bytes, `unix` dialer instead of `tcp` — no framing |
| 6 | **gRPC** | CALL \| CHANNEL | adapter | needs adapter | `.proto` + length-prefixed framing are capability-shaped |
| 7 | **MQTT** | CHANNEL | adapter | needs adapter | topic hierarchy + QoS are application routing |
| 8 | **WebRTC** | CHANNEL | adapter | needs adapter | SDP/ICE negotiation is capability-shaped; only the DataChannel is wire-like |

(7 candidates + MJPEG, which the system already runs as a sibling of SSE.)

## How the four-body system "knows" all of them, leanly

| Arm | Repo | Role in the transport story |
|---|---|---|
| **WIRE** | `http-mcp` | owns the 2 atoms + the `unix` transport option; serves `transports.json` via the `transports` tool |
| **WITNESS** | `8` | observes any channel read-only (SSE/MJPEG/ws); never drives |
| **HOST** | `pilot` | composes atoms; imports an adapter when a dialect is needed |
| **ADAPTERS** | `adapters` | one folder per dialect (`grpc/`, `mqtt/`, `webrtc/`), each with `capabilities.json` declaring how it maps to CALL/CHANNEL |

## What an agent reads, in order

1. **`transports` tool** (or this repo's `transports.json`) → the whole 7→2 map in one call.
2. **`adapters/<name>/capabilities.json`** → only if you need a non-wire transport.
3. **This file** → prose + diagram fallback, linked from every README.

> **One line:** *the wire is `http_request` (CALL) + `bidi_command` (CHANNEL); `discover`/`transports` returns the full map; Unix is wire; gRPC/MQTT/WebRTC are adapters because they carry framing.*
