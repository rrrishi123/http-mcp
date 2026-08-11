# One lean binary — the claim, restated honestly (#31/#38)

The hard core said "ship as ONE lean binary, each atom usable." Measured reality
(#31): 7 Go `package main` entrypoints (http-mcp `cmd/*` + the collector) plus a
real browser/device. The claim was **unrealized, not forbidden** — and it was
also **mis-stated**. This is the correction.

## The restated claim

> **One lean CONTROL-PLANE binary, driving an external SUBJECT.**

Not "one binary for everything." The four-body system has three separable layers,
and only two of them can (or should) collapse into a single executable:

- **Control plane** — the wire (CALL/CHANNEL), the witness, the probes. All Go,
  all `package main`, one module-family. These *can* be one binary: a busybox
  multi-call dispatching on subcommand. This is the part the claim is true of.
- **Subject** — the browser (Firefox/geckodriver) or device (Appium/a phone).
  Irreducibly external: you *drive* it, you never *contain* it. The subject
  forbids only its own inclusion — which was never the claim.
- **Arms** — pluggable adapters (some Python). External by design; that's what
  "pluggable" means.

## What is built (#38)

`cmd/eight` — a single-command dispatcher: `eight <atom> [args…]` over
`wire | mcp | channel | probe | session | harvest | witness`. This delivers the
one-**command** surface: one name to remember, not six. Built by `build.sh` to
`.bin/eight`.

## What remains (the mechanical remainder)

`cmd/eight` currently dispatches by **exec** to the per-atom binaries (a
launcher). The one-**binary** claim — all code compiled into a single self-
contained executable — requires converting each `cmd/*` `main` into an
importable package with a `Run([]string) int` entrypoint, then having the
dispatcher call them **in-process** and `go:embed` the built `web/dist`. That is
mechanical, not blocked: no atom's logic resists it; the collector is a separate
module and would join via `go.work` or a vendored move. Until then, the honest
statement is: **one command, several binaries — converging on one.**
