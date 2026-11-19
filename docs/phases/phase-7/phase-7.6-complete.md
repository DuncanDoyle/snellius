# Phase 7.6 — Audit, docs, release (Complete)

**Implemented:** 2026-11-18 … 2026-11-19. **Closed:** 2026-11-19 on PR #50 (`duncandoyle/phase7Design`)
**Range:** 1e9f443..HEAD of the close commit
**Close gate:** met. Locally on the close commits: gofmt, go vet (also `-tags e2e` and `-tags bench`), golangci-lint v2.15.0 (0 issues), `go test ./...` (exit 0), `-race` under `TZ=UTC` on the touched packages, chart tests and `helm lint`, the SAR and engine budget benches, web typecheck, biome and vitest. CI on PR #50: see the phase record.

## Summary
Secure mode now keeps a full audit trail (design §9), the README explains the trust model and secure-mode setup, and the phase review's findings are fixed. A full queue answers with its own `503 authz-busy` and an honest message (user's wording, 2026-11-18). The phase review found two visibility leaks that remained after 7.5; both are closed, and the full read no longer covers Events or CRDs (D-120).

## Sub-phases (phase record only)
n/a

## Reviews
- `reviews/7.6-audit-codex.md`: audit levels, one fix round.
- `reviews/phase-codex.md`: the whole-phase review, two stabilization rounds and a final check.

## Waivers
None new. The D-044 timing-regression gate on PR #50 stays waived under D-115.

## Deliverables
- **Audit (design §9).** Levels security, reads, summaries (5-minute windows, flush on shutdown, 10,000 keys), opt-in disclosure (typed references only) and requests (including 401s from the session gate); denials collapse per minute; only well-formed, accepted filter values; never tokens, cookies, raw query strings or bodies; `/metrics` counts written and dropped records. Local mode writes only the startup record.
- **Busy answer.** A full admission queue (D-116) answers `503 authz-busy`, audited `queue-full`; the page says "Snellius is busy right now" and retries.
- **Docs.** README trust model, secure-mode setup, capacity and tuning, audit, probe port and metrics, `v0.8.0` upgrade notes; chart README and values match the code; design §12 and the plan match the chart.
- **Phase-review fixes.** Backend YAML needs a readable consumer route (D-107); a full reader without Events or CRD access sees nothing derived from them, including admission, check text and timestamps (D-120); refused calls leave a `requests` record; the browser e2e signs in alice and bob.
- **Cold permission checks** (namespace grants, Large): `/routes` 375, `/paths` 394–396, `/backends` 499; full-read `/routes` 24.

## Rulings
In the review files' triage tables; the design-level ones are D-120 and the busy answer in D-116 item 4.

## Notes for later phases
- `/paths` sits at 394–398 of its 400 cold-check target.
- Extensions disclose nothing at the disclosure level unless they report typed references.

## Deferred / open
- **(P3, Phase 8)** The full-read-without-Events-or-CRDs test should deny each grant separately and cover backend and route detail.
- **(P4, Phases 8–9)** Extensions get the raw publication beside per-object helpers (phase review PH6).
- The 7.5 open items (named-only trace, map fallback past the query caps, a stalled upload's connection, web test flakes) carry over.
