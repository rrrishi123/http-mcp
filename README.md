# http-mcp

An MCP server whose tools are protocol calls.

A call is four fields: `method`, `url`, `headers`, `body`. The server makes
the call and returns the response. Nothing else.

No language binding. No framework. Single static Go binary, `net/http` stdlib.

## The primitive

```
http_request(method, url, headers, body) -> response
```

Auth is a header strategy, not a dependency: basic (`user:key` → base64),
bearer, api-key.

Everything else — WebDriver, Appium, Selenium Grid, any REST or RPC API —
is that one call against a different URL. A WebDriver session is
`POST /session`; tapping an element is `POST /session/{id}/element/{el}/click`.
There is no "Selenium client" or "Appium client" on the wire; there are
sessions, against a binary, with a route table.

## The channel

`http_request` is one physics: a call — request, response, done. Part of the
wire is a *different* physics: a channel — a WebSocket you produce commands into
and consume frames from, held open. **CDP** (Chrome DevTools Protocol) and
**WebDriver BiDi** are this shape, and Playwright, Puppeteer, and Selenium-BiDi
are composers over it the way Selenium/Appium are composers over HTTP. So one
more atom maps that whole family:

```
bidi_command(ws_url, method, params) -> response
```

The WebSocket client is hand-rolled in stdlib (`internal/wsx`) — no library, the
same way `httpx` is just `net/http`. `bidi_command` opens a fresh socket per
call, which is honest only for *stateless probes* (`browsingContext.getTree`,
a one-shot navigate). The channel is otherwise **stateful**: a `session.subscribe`
lives on the socket, a CDP `sessionId` is bound to it, and many effects arrive
only as events. Close the socket and all of that dies — so the channel's true
atom is not open-send-close but a **held** connection.

That held connection is `cmd/channel`: it holds one long-lived socket and fronts
it with `POST /command` (send on the held socket, get the id-matched response)
and `GET /events` (SSE — every id-less frame fanned out). One reader routes
frames: id → the waiting command, no-id → the event stream. Subscriptions and
session state survive across commands. The broker only holds, routes, and fans
out — it never interprets a command or composes a sequence; that's the host's
job. So `peer_ask`-style "ask a target, await its echo" is a *host* composition
over `bidi_command` + the held channel, never a wire primitive.

`GET /await` turns the afferent stream into one request/response: it blocks until
an id-less frame matching a substring filter arrives, so a plain caller can wait
for an event without streaming or polling.

**Known limitations (v1):** two race conditions, named here rather than hidden —
- **`/await` subscribe-after-send race.** A consumer that does `POST /command`
  *then* `GET /await` can miss an event that fires in the gap. Safe for slow
  replies (seconds); unsafe for fast ones (ms). v2 fix: subscribe *before*
  sending — a `?await=` parameter on `/command`.
- **Fan-out drop under burst.** Event delivery is non-blocking (a slow consumer
  is never allowed to stall the wire), so a burst can fill a subscriber's buffer
  and drop the awaited event. v2 fix: filter at the fan-out so a subscriber's
  buffer only holds events it asked for.

## Sessions

Many sessions run at once — local drivers and cloud providers, browsers and
devices, side by side. Each is fully described by `(hub_url, session_id)`.
A per-session goroutine supervises its lifecycle:

- **local** sessions: watched for death (the driver process or the session
  handle going away), reaped when gone.
- **cloud** sessions (BrowserStack / LambdaTest / SauceLabs): kept warm with a
  cheap heartbeat before the provider's idle timeout reaps them.

The session store is the *prior*, not the truth. A stored entry is a
hypothesis; the supervisor verifies it against the live wire and corrects or
removes it. Concurrency here is structure, not speed (Pike): one goroutine
per session, each mostly blocked on a ticker or an I/O wait, managing its own
lifecycle independently.

## specs/ — the mapped, then verified, territory

Route tables are harvested from version-addressable upstreams and stored
append-only as `<name>@<version>.json`:

```
go run ./cmd/harvest         # append snapshots for the latest published versions
```

A snapshot is *hearsay*. Probing turns it into *measurement* — every route in
the union of all snapshots is fired at a live binary with a bogus session id
(zero side effects), and the server's own answer classifies it:

```
go run ./cmd/probe http://localhost:4444 geckodriver@0.37.0
```

A `404`/plain-text means the route is absent; a W3C JSON error means it exists.
The probe verdict beats the snapshot claim, always.

## Commands

| command        | what it does                                              |
|----------------|-----------------------------------------------------------|
| `cmd/mcp`      | the MCP server (stdio): `http_request` + `bidi_command` + `discover` |
| `cmd/harvest`  | append route-table snapshots from upstream releases       |
| `cmd/probe`    | verify a snapshot against a live binary, zero side effects |
| `cmd/session`  | run one composed session end to end (reference client)    |
| `cmd/wire`     | transparent witness proxy — logs every call on the wire   |
| `cmd/channel`  | hold one BiDi/CDP socket; front it with `POST /command` + `GET /events` (SSE) — physics #2's held atom |

## Run the MCP server

```
go build -o http-mcp ./cmd/mcp
```

Register it as a stdio MCP server in your client (`.mcp.json` / Claude Code):

```json
{ "mcpServers": { "http-mcp": { "command": "/path/to/http-mcp" } } }
```

## Why a wire, not a binding

The Selenium and Appium clients are good software. They're how a person writes
automation comfortably in their own language — Java, Python, Ruby, JS. None of
this is meant to replace them or to say they did it wrong.

It's just that, underneath, they're all making the same moves on the same wire:

```
  Selenium-Java   Selenium-Python   WebdriverIO (JS)   Appium-Ruby   Playwright
        │               │                  │               │            │
        └───────────────┴────────┬─────────┴───────────────┴────────────┘
                                  │   each is a wrapper, per language;
                                  │   beneath them it's all one protocol
                                  ▼
   ┌──────────────────────────────────────────────────────────────────────┐
   │  the wire                                                              │
   │     W3C WebDriver · Appium      →  HTTP        (a call)                │
   │     CDP · WebDriver BiDi        →  WebSocket   (a channel)             │
   └──────────────────────────────────────────────────────────────────────┘
                                  ▲
                                  │   http-mcp lives here: http_request +
                                  │   bidi_command. Not above the bindings —
                                  │   beneath them, the layer they all already
                                  │   stand on, exposed so an agent can compose
                                  │   it directly, in any language or none.
```

A language binding gives one language ergonomic hands. The wire gives *anything*
that can speak HTTP or a WebSocket the same reach — which now includes a model.
That's the only claim here: same protocol, one layer down, nothing in the way.

## Philosophy

The tool surface is small on purpose. Rather than exposing one tool per
endpoint (hundreds of them, each a cost in context and attention), the server
exposes the primitive and lets the territory describe itself on the wire —
`GET /status`, capability negotiation, `404`-vs-`405`, a driver's own command
enumeration. The map (`specs/`) is a prior held lightly; the live call is the
verdict. The server stays dumb so everything real can grow on top of it.

## License

MIT.
