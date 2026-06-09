# http-mcp

An MCP server that is only protocol calls.

## What it is

Four fields: `method + url + headers + body`. That is a curl call.
The MCP server's job: load an API doc, expose each endpoint as a tool,
when the tool fires — make the HTTP call.

No language binding. No framework. The protocol is the language.

## What moves in

- `specs/` — OpenAPI/Swagger docs for any HTTP API (LT, BS, SL, WebDriver, Appium, anything)
- `auth/` — credential strategies: basic (user:key → base64), bearer, api-key header
- `tools/` — the n primitives: `http_request`, `load_spec`, `set_auth`

## What this is not

It is not a test framework. It is not a browser automation library.
It is not WebdriverIO. It does not wrap anything.
It is the wire.

## Tool surface

Fixed (n primitives):
- `http_request(method, url, headers, body)` — raw call
- `load_spec(path_or_url)` — reflect k tools from a doc into the surface
- `set_credentials(auth_type, user, key)` — configure auth for a host

Dynamic (n+k, grows with each spec loaded):
- one tool per endpoint in every loaded spec, typed against that spec's schema

## Frustum

```
LLM (wide end — natural language intent)
        ↓
   http-mcp  (the frustum — collapses intent to protocol)
        ↓
HTTP  (narrow end — method + url + headers + body)
        ↓
anything with an HTTP API
```

## From ltqa-platform

These artifacts are protocol-level and move here:
- `data/api/specs/*.yaml` → `specs/lt/`
- auth concept from `ltqa/api/auth.py` → `auth/`
- endpoint walker concept from `ltqa/flows/endpoint_walk.py` → `tools/`
- env routing from `data/config/environments.yaml` → `specs/lt/environments.yaml`
