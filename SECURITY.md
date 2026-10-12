# Security Policy

http-mcp is the **WIRE** of the four-system: a minimal MCP server exposing two
atoms — CALL (`http_request`) and CHANNEL (`bidi_command`) — plus a witnessing
MITM proxy (`cmd/wire`) that records browser-automation traffic to the `8`
collector.

## Supported versions

Active development is on `release/v0.0.2`; security fixes land there first.

## Handling of credentials

- The wire is a **MITM proxy**: it observes automation traffic (WebDriver / CDP)
  and forwards it. Credentials seen in flight are **redacted** before anything is
  written to the witnessed ledger (the `/witnessed` sink).
- Auth material is **not** embedded in this repo. `auth/auth.go` reads keys the
  operator exports from their own provider dashboard — nothing is baked in.
- Because the wire can see automation credentials in transit, run it only against
  endpoints you control, on loopback or a trusted network. Do not point it at
  traffic you are not authorized to observe.

## Reporting a vulnerability

Report privately via this repository's GitHub Security Advisories
(**Security → Report a vulnerability**), or open a minimal issue describing the
*class* of problem. Please do **not** include live credentials or a working
step-by-step exploit in a public issue.
