# Changelog — http-mcp (the wire)

All four arms of the four-system (8, http-mcp, pilot, adapters) version
independently; the wire contract carries its own version (`contract.Version`,
canonical in this repo). Tags are unsigned.

## v0.0.3 (unreleased, branch `release/v0.0.2`)

Release-readiness follow-ups to the v0.0.2 line:

- MCP `serverInfo.version` is derived from the module build info (the tag
  `go install` stamped), falling back to `v0.0.3`, instead of a stale literal.
- Tenant account name scrubbed from exported doc comments and the tool schema;
  the profile-slot mechanics (`<env>:<account>` → env or gitignored
  `auth/<profile>.json`) are unchanged.
- CI also runs `./build.sh` so the shippable `.bin/` layout is verified.
- `contract.Version` marked `TODO(contract-dedup)`: adapters/trace duplicates
  the literal; making one source of truth needs a module-arrow decision.
- This changelog.

## v0.0.2 — 2026-09

- **contract/**: the wire contract as code — `Version`, `Compatible`, run and
  trace shapes, transports — shared across the arms.
- **wire**: capture fidelity (query + bodies to the replay sidecar, redacted);
  HTTPS-hub upstream with Host rewrite + basic-auth injection; `/host` goes
  through the recorder; host-resources basic (`internal/host`, `GET /host`);
  `-witness` posts observed MITM calls into 8's ledger.
- **channel** (BiDi broker): every `:4445` response witnessed as a receipt.
- **eight**: the single-command dispatcher over all bodies; `mcp` importable
  and merged in-process.
- **painguard**: PUSH-witness prototype — classify failures from the body and
  surface them unprompted.
- **harvest**: follows `@appium/base-driver`'s `routes.js` → `routes/` split.
- **hygiene**: `build.sh` is the canonical build (each cmd to its own `.bin/`
  name, plus the config path), committed-binary boot fix, gofmt gate.

## v0.0.1

First cut: `http_request` with self-witnessing reafference, `discover`,
`become`, record/replay.
