# Contributing

## Build

`./build.sh` produces SHA-stamped binaries into `.bin/` (the boot path builds on
demand — never commit built binaries). CI (`.github/workflows/ci.yml`) gates
`go build` + `go vet` + `go test` + `gofmt`, and runs a live chromedriver
route-floor probe.

## Branch model

Work targets `release/v0.0.2`. Do not push to `main` or mint tags without an
explicit release decision — the four-system holds the v0.0.3 cut behind an
operator greenlight.

## Conventions

- The wire is **two atoms only**: CALL (`http_request`) and CHANNEL
  (`bidi_command`). Keep the waist thin — capability grows above it, not in it.
- Every act is **witnessed**: responses carry an `X-8-Witness` receipt. Prefer the
  witnessed path over ad-hoc clients.
- Credentials are **redacted** in witnessed logs; never weaken that.

## Pull requests

Small, reviewable commits. `gofmt` clean, `go vet` clean, `go test ./...` green.
