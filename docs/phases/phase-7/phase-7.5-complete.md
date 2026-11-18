# Phase 7.5 — Two signals (the gate opens) (Complete)

**Implemented:** 2026-11-17 … 2026-11-18. **Closed:** 2026-11-18 on PR #50 (`duncandoyle/phase7Design`)
**Range:** 40932fd..HEAD of the close commit
**Close gate:** met, with one CI note. Locally on the close commits: gofmt, go vet (also `-tags bench` and `-tags e2e`), golangci-lint v2.15.0 (0 issues), `go test ./...`, `-race` under `TZ=UTC` on the touched packages, the full SAR and engine budget benches, web typecheck, biome and vitest. CI on PR #50: kind (7 jobs) and kind-secure pass on `acac95f`; web passes (a rerun after two known load flakes); `go` fails only on two test races that predate Phase 7 (`TestStaleMetricsAreNeverConfirmed`, `TestNamespacesNilUnchanged`), fixed in PR #55; the bench A/B stays waived (D-115).

## Summary
Secure mode now serves data: each signed-in viewer sees only what their Kubernetes RBAC allows (the gate opened in `cd667e0`). Before opening it, 7.5 fixed the load cliff: 50 cold viewers on `/paths` or `/backends` all got answers again (D-116 to D-118). Every route carries two signals, configuration and traffic path, in the API and the web (D-087, design §8). The D-115 engine-cost follow-up brought warm recompute back within +15 % of `main` before Phase 7.

## Sub-phases (phase record only)
n/a

## Reviews
All cross-model (Codex via `run-reviewer.sh`), each triaged in its file:
- `reviews/7.5-load-codex.md`: design consult on the load cliff (D-116).
- `reviews/7.5-d116-codex.md`: D-116 to D-118, a review, a re-review and a fix check.
- `reviews/7.5-d115-codex.md`: the D-115 follow-up.
- `reviews/7.5-signals-codex.md`: the two signals in `pkg/explore` and the API, three fix rounds.
- `reviews/7.5-web-gate-codex.md`: the web and the gate, two fix rounds (D-119).

## Waivers
None new. The D-044 timing-regression gate on PR #50 stays waived under D-115 (7.4); D-115's own target is now met.

## Deliverables
- **Load (D-116, D-117, D-118).** Requests that need uncached permission checks are admitted a few at a time, oldest first; cached polls never wait; a slow client holds no place; a request body is read (1 MiB, 10 s) before admission. Cache entries shrank from 468 to about 210 B, so 50 viewers fit the 8 MiB budget. The default rate's capacity is accepted and documented. Load bench (50 cold viewers, Large): every viewer gets a first page within its bound on `/routes`, `/paths` and `/backends`; at least 95 % of each viewer's later polls answer 200; no `check-limit`.
- **Engine cost (D-115 follow-up).** One shared route walk and a registry entry carried on each own finding: Compute/large +5.5 %, large-contradicting +10.8 %, large-contradicting-worst +12.6 %, large-hit +8.3 % against `39c47c9`.
- **Two signals (D-087, D-093).** Route rows, route detail and Gateway and ListenerSet route tables carry `config` and `path` (with `incomplete`); `config=` and `path=` filters; legacy `health` keeps D-082's meaning, and a restricted viewer's is never green over a path they cannot assess. New reasons `path-incomplete`, `no-path`, `not-attached`, `path-problem`. golden-v1 unchanged.
- **Web.** Configuration and Traffic path columns and filter chips, both badges in the route header, the problem strip naming the deciding signal (wording chosen by the user, 2026-11-18), map route nodes showing the summary.
- **Gate (design §2).** `WithSecureMode` serves data; a missing authoriser denies. In secure mode a build's own authoriser only narrows SAR, extensions fail closed without one, and full-read answers are kept per authoriser (D-119). The kind-secure e2e checks that alice sees `shop/good` and bob sees nothing.
- **Cold permission checks** (namespace grants, Large): `/routes` 376, `/paths` 393–398, `/backends` 503 (targets 400, 400, 550).

## Rulings
In the review files' triage tables and the implementer reports; the design-level ones are D-116 to D-119 and the design §8.3 note on D-082's lift.

## Notes for later phases
- `/paths` cold checks sit at 393–398 of 400, and `TestBudgetPathsRequests` at 31.5 of 32 MiB: little margin.
- kgwctl and any caller without computed views get `path: unknown, no-path` for an accepted route without a fault; mention it in the `v0.8.0` consumer notice.
- A downstream build's authoriser now only narrows SAR (D-119); one without list answers hides everything gated on list permission.

## Deferred / open
- **(P3)** A route readable by name only shows an empty trace on its detail page (`reviews/7.5-signals-codex.md`, U3).
- **(P3)** Map nodes past 20 delegating parents or 200 routes per query fall back to path-row state.
- **(P3)** A stalled upload still holds its connection (only a header-read timeout); it no longer holds an admission place or buffer for long.
- **(P3, after `v0.8.0`)** Compare how other Kubernetes UIs authorise per user (D-118).
- **Separate PRs:** #55 fixes the engine and cache test races; web vitest flakes under load (`details.test.tsx`: the failed-route sub-filters test and the polling focus test, and 18 tests once locally) still need their own PR.
