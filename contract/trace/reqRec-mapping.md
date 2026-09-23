# `reqRec` → `Frame` — the witness's trace vs the published contract (D1)

The wire contract publishes `trace.Frame` as the recording format. The witness
(8) does **not** emit `Frame`; it emits its own `reqRec` (8 `collector/main.go`,
~21 references — the ledger row, the SSE payload, the `/requests` response, the
dedupe key, and the series format, all at once). This is the "documented but
unbound" state named in the v0.0.2 audit (D1/R6).

**Decision for v0.0.x: ship unbound.** Binding buys nothing today — there is no
replay runner outside 8, and `/replay-series` reads `reqRec` back itself. The
contract's value is preventing *future* drift, and the cheap half of that
(identifiability) is now captured without adopting `Frame`: recorded series are
stamped `{"contract":"v0.0.2","frames":[…]}` (8 `writeSeries`), so every trace 8
writes can be identified later. This document is the other half: the spec a
future binder would implement.

## Field mapping

| `reqRec` (witness) | `Frame` (contract) | note |
|---|---|---|
| `TS` (RFC3339 string) | `TS` (int64 unix millis) | shape mismatch — mechanical |
| `Physics` (`call`/`channel`) | `Mode` (`ModeCall`/`ModeChannel`) | same values, different name |
| `Session` | `Session` | direct |
| `Method`, `URL` | `Method`, `URL` (efferent, CALL) | direct |
| `Status` | `Status` (afferent, CALL) | see cardinality gap |
| — (channel method) | `Command` (efferent, CHANNEL) | reqRec doesn't split CDP/BiDi method out |
| — | `Event` (afferent, CHANNEL) | reqRec has no afferent-event field |
| `RespPreview`, `RespBytes` | `Body` (afferent) | reqRec stores a preview + a byte count, **not** the body |
| `Seq` | `Seq` | direct |
| `Actor` (#29 declared identity) | — | **no home in Frame** |
| `Seat`, `Replayable`, `LatUS` | — | **no home in Frame** |

## The four gaps (why binding is a refactor, not an import)

1. **Shape** — `TS` RFC3339 vs unix millis; `Physics` vs `Mode`. Mechanical.
2. **Cardinality** — a `reqRec` is a full *round trip* (method, url, status, and a
   response preview in one row); a `Frame` is a *half* (one direction, `Dir`
   efferent or afferent). Binding means splitting every record into two frames —
   and the afferent half is incomplete: 8 stores `resp_preview` + `resp_bytes`,
   not the response body.
3. **`auth_slot`** — has zero references in 8. The witness has no concept of where
   a credential injects, so binding would emit a published contract field that is
   *always empty*. A contract that lies is worse than one that's absent.
4. **Provenance has no home** — `Frame` has no extension point for the witness's
   best data (`Actor`, `Seat`, `Replayable`, `LatUS`). Binding either drops
   provenance or changes the published `Frame` to accept it.

## If/when binding happens

Redesign `Frame` to carry provenance and decide the round-trip split; plumb
`auth_slot` from the wire into the witness; capture response bodies; then refactor
the 21-reference `reqRec` that four subsystems share — under no tag pressure, so a
published module isn't frozen on the wrong shape. When it does happen, the
contract belongs in its **own module below all four arms**, not imported from
http-mcp (avoids the `8/adapters/pilot → http-mcp` arrows the fleet has kept out).

*(This mapping and decision were sharpened by an independent review, 2026-09-23.)*
