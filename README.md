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
| `cmd/mcp`      | the MCP server (stdio): `http_request` + session tools    |
| `cmd/harvest`  | append route-table snapshots from upstream releases       |
| `cmd/probe`    | verify a snapshot against a live binary, zero side effects |
| `cmd/session`  | run one composed session end to end (reference client)    |
| `cmd/wire`     | transparent witness proxy — logs every call on the wire   |

## Run the MCP server

```
go build -o http-mcp ./cmd/mcp
```

Register it as a stdio MCP server in your client (`.mcp.json` / Claude Code):

```json
{ "mcpServers": { "http-mcp": { "command": "/path/to/http-mcp" } } }
```

## Philosophy

The tool surface is small on purpose. Rather than exposing one tool per
endpoint (hundreds of them, each a cost in context and attention), the server
exposes the primitive and lets the territory describe itself on the wire —
`GET /status`, capability negotiation, `404`-vs-`405`, a driver's own command
enumeration. The map (`specs/`) is a prior held lightly; the live call is the
verdict. The server stays dumb so everything real can grow on top of it.

## License

MIT.
