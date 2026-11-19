Cross-model: YES (author: Claude Opus 5.5 and Sonnet 5.5 implementers, reviewer: Codex via run-reviewer.sh)
cli: codex · status: ok
Scope: phase 7 (Identity & Access), whole-phase review
Diff: 81a2c0b..83a7d75
Timing: before the phase close; sub-phase reviews in reviews/7.*

## Review

I found two P1 visibility bugs in Phase 7. Both can change a secure-mode response when an object the viewer cannot read changes.

### P1 — Correctness and security

- **Backend YAML reveals hidden consumers.** `assignBackends` adds a Backend to an install when any owned route references it. The admission index marks that Backend `Owned`, and the YAML endpoint accepts that flag without checking whether a referrer is readable. A viewer who can read the Backend sees its YAML endpoint change from 404 to 200 when its first, unreadable consumer appears. D-107 explicitly excludes Backend membership from the install-context exception to D-085. Apply the visible-consumer rule to YAML admission as well as the overview and extension views. [pkg/install/delegation.go:247](../../../../pkg/install/delegation.go:247) · [pkg/explore/admit.go:76](../../../../pkg/explore/admit.go:76) · [pkg/api/link.go:103](../../../../pkg/api/link.go:103) · [pkg/api/handlers.go:519](../../../../pkg/api/handlers.go:519)
- **The full-read fast path bypasses Events and CRD permissions.** Production excludes both kinds from the full-read proof. `probes()` and `readsGatewayCRD()` then grant access on that proof alone. A viewer who can list every kind in the proof can see probe failure counts and timestamps from unreadable Events, contrary to D-110, and Gateway CRD bundle and channel metadata without CRD access. The earlier ListenerSet fix covers the projected branch. The fast path still bypasses it. Require these permissions even when the viewer qualifies for the fast path, or include them in the proof. [pkg/app/kinds_sar.go:24](../../../../pkg/app/kinds_sar.go:24) · [pkg/api/processes.go:165](../../../../pkg/api/processes.go:165) · [pkg/api/details.go:423](../../../../pkg/api/details.go:423)

### P2 — Design or plan mismatch

- **`requests` omits API calls rejected by session middleware.** The audit wrapper sits inside the API handler. Session middleware can return a 401 before that handler runs, so those calls leave no `requests` record even when the level is enabled. Design §9.2 and the 7.6 brief promise every API request. Capture these responses with the request ID and status while retaining session attribution for authenticated requests. [pkg/app/mode.go:56](../../../../pkg/app/mode.go:56) · [pkg/auth/session/middleware.go:46](../../../../pkg/auth/session/middleware.go:46) · [pkg/api/handlers.go:112](../../../../pkg/api/handlers.go:112)
- **Correct the design and Task 26.** Keep the five-minute audit window fixed, document extra audiences under API-server setup, and use D-100’s burst default of **500**. Task 5 already leaves `replicas` and `hostNetwork` off the chart’s values interface. Document that the chart renders one replica in both modes and that the binary rejects local host networking when the downward-API pod and host IPs match. Keep the chart’s actual render refusals for local HTTPRoute and missing secure OIDC settings. [design.md:328](../../../../docs/phases/phase-7/design.md:328) · [implementation-plan.md:102](../../../../docs/phases/phase-7/implementation-plan.md:102) · [implementation-plan.md:416](../../../../docs/phases/phase-7/implementation-plan.md:416) · [deployment.yaml:7](../../../../deploy/helm/snellius/templates/deployment.yaml:7) · [pkg/app/mode.go:47](../../../../pkg/app/mode.go:47)

### P3 — Test gap

- **Browser e2e never compares the two RBAC views.** Go e2e checks Alice’s and Bob’s data access. Playwright signs in only Alice and checks account and logout behavior. Design §13 calls for both users’ browser views, leaving the rendered restricted state uncovered in secure kind. [test/e2e/signin_test.go:181](../../../../test/e2e/signin_test.go:181) · [.github/workflows/e2e.yaml:103](../../../../.github/workflows/e2e.yaml:103)

### P4 — Later-phase fragility

- **Extensions receive the raw publication alongside per-object visibility helpers.** `VisibleTo` requires inputs whose membership already satisfies D-085. That precondition matters for Phase 8’s inherited policies and Phase 9’s winning-route predictions, where a hidden input can change a readable result. Reuse viewer-scoped computations at those entry points and in MCP. [pkg/extension/deps.go:18](../../../../pkg/extension/deps.go:18) · [pkg/api/viewer.go:54](../../../../pkg/api/viewer.go:54) · [docs/roadmap.md:140](../../../../docs/roadmap.md:140)

I reviewed the requested design, plan, decisions, sub-phase records and earlier dispositions against the base-to-HEAD diff, prioritizing auth, API, app, audit, explore signals, Helm and README. The local loopback and Host/Origin guards, OIDC transaction and TokenReview flow, SAR expiry and buffered commit, D-119 narrowing, and permission-based path incompleteness looked sound in the paths inspected. I also verified that `kind-secure` selects `TestAuditRecords`. No files were edited or tests run. The test results are author-reported, and the new audit e2e remains unverified in CI.
## Triage

| # | Finding | Verdict | Resolution |
|---|---|---|---|
| PH1 | P1 Backend YAML admission skips the visible-consumer rule (D-107) | accept | Stabilization round |
| PH2 | P1 the full-read fast path grants Events and Gateway CRD data without their permissions | accept | Stabilization round |
| PH3 | P2 the requests level misses 401s from the session middleware | accept | Stabilization round |
| PH4 | P2 design §12 and the plan disagree with the chart and code | accept | design.md and implementation-plan.md corrected: fixed 5-minute window, extra audiences as API-server setup, burst 500, one replica in both modes, only the binary refuses host networking |
| PH5 | P3 the browser e2e signs in only alice | accept | Stabilization round: alice and bob in Playwright on secure kind |
| PH6 | P4 extensions get the raw publication beside per-object helpers | defer | Carry-over for Phase 8 (inherited policies) and Phase 9 (winning-route predictions) |

## Check of the stabilization round (4df2d6e..93218be)

- **PH1 — Fixed for the original 404→200 leak.** Backend YAML now requires a list-readable consumer for a restricted viewer. Overview and extension Backend counts use readable consumers. Hidden referrers still add scan work and can trigger cold SARs, so latency can vary. A cap-induced 503 falls under D-112’s accepted exception. [link.go:93](../../../../pkg/api/link.go:93) · [handlers.go:307](../../../../pkg/api/handlers.go:307) · [decisions.md:1642](../../../../docs/decisions.md:1642)
- **PH2 — Partly fixed.** Probe fields require cluster-wide Events `list`. ListenerSet CRD fields require CRD `list` or named Gateway CRD access. Remaining leaks are below. [processes.go:165](../../../../pkg/api/processes.go:165) · [details.go:423](../../../../pkg/api/details.go:423)
- **PH3 — Fixed.** The audit wraps the gate, records refused 401s, and preserves session attribution for admitted requests. I found no secure API bypass through the main listener, extension mounts, auth routes, or probe listener. [handlers.go:110](../../../../pkg/api/handlers.go:110) · [audit.go:302](../../../../pkg/api/audit.go:302) · [app.go:583](../../../../pkg/app/app.go:583)
- **PH5 — Fixed.** Secure-kind Playwright signs in both users and checks Alice’s rendered routes and Bob’s empty table and hidden-details notice. [restricted.spec.ts:17](../../../../web/e2e/restricted.spec.ts:17) · [e2e.yaml:103](../../../../.github/workflows/e2e.yaml:103)

### Remaining findings

- **P1 — Controller probe failures still change YAML admission.** KGWUI-020 can be the only finding admitting a controller Deployment. `findingShown` accepts any finding under the full-read proof, so Deployment YAML and its breadcrumb link can appear for a viewer denied Events. [process.go:79](../../../../pkg/health/process.go:79) · [admit.go:94](../../../../pkg/explore/admit.go:94) · [link.go:122](../../../../pkg/api/link.go:122)
- **P1 — CRD presence can still change check text.** With an unknown or unmapped controller version, the CRD comparison note appears only when the CRD slice is non-nil. Withholding removes CRD findings but preserves that note in `/health` and `/diagnostics`. [skew.go:127](../../../../pkg/health/skew.go:127) · [skew.go:351](../../../../pkg/health/skew.go:351) · [rollup.go:122](../../../../pkg/health/rollup.go:122)
- **P2 — Recompute timestamps still expose hidden input activity.** `/installs` `computedAt` and per-check timestamps retain model times for these readers. D-109 permits response `collectedAt` to move. [handlers.go:348](../../../../pkg/api/handlers.go:348) · [diagnostics.go:461](../../../../pkg/api/diagnostics.go:461)
- **P3 — Browser assertions are loose.** Alice’s namespace check matches anywhere in the row, and the signal checks cover only headers. Assert the route-name cells and a rendered signal value. [restricted.spec.ts:22](../../../../web/e2e/restricted.spec.ts:22)

Finding detail, groups, counts, search, overview tints, diagnostic rollups, and extension `Viewer.Shown` use filtered findings. With both input grants, the precomputed paths remain available. I found no payload regression there. Read-only source review. No files edited or tests rerun.
### Triage (stabilization check)

| # | Finding | Verdict | Resolution |
|---|---|---|---|
| Q1 | P1 a probe finding can admit a controller Deployment for a full reader without Events | accept | Stabilization round 2 |
| Q2 | P1 CRD presence changes the skew check text | accept | Stabilization round 2 |
| Q3 | P2 recompute timestamps move with hidden Events or CRD activity | accept | Stabilization round 2: D-109's treatment |
| Q4 | P3 browser assertions are loose | accept | Stabilization round 2 |
| PH2′ | partly fixed | accept | Closed by Q1–Q3 |

## Check of stabilization round 2 (93218be..3ffd82c)

- **Q1 fixed.** Controller Deployment YAML and its links now require a visible finding when a finding is their only basis for admission. [pkg/api/link.go:103](../../../../pkg/api/link.go:103)
- **Q2 fixed.** `/health` and `/diagnostics` drop the unknown or unmapped controller-version note when CRD listing is denied. [pkg/health/skew.go:127](../../../../pkg/health/skew.go:127) · [pkg/health/rollup.go:134](../../../../pkg/health/rollup.go:134)
- **Q3 fixed.** Install `computedAt`, check timestamps, and cached endpoint read times use the publication’s confirmation time when either input is withheld. [pkg/api/handlers.go:348](../../../../pkg/api/handlers.go:348) · [pkg/api/diagnostics.go:461](../../../../pkg/api/diagnostics.go:461) · [pkg/api/visible.go:550](../../../../pkg/api/visible.go:550)
- **Q4 fixed.** Playwright checks every route-name namespace and both rendered signal values on `shop/good`. [web/e2e/restricted.spec.ts:25](../../../../web/e2e/restricted.spec.ts:25)

**P2 docs.** D-120 matches the findings and timestamp logic. Its check-text promise should explicitly preserve the collection-status exception. CRD watch failure or recovery still changes the skew check’s text and availability, and `/meta` reports both watches’ status. The README’s Times paragraph also omits full readers missing either grant. [docs/decisions.md:1729](../../../../docs/decisions.md:1729) · [pkg/health/skew.go:94](../../../../pkg/health/skew.go:94) · [pkg/app/app.go:125](../../../../pkg/app/app.go:125) · [README.md:153](../../../../README.md:153)

**P3 coverage.** The paired-world test denies both grants together and omits backend list/detail and route detail. It also lacks a pre-fix byte baseline for the fully granted reader. [pkg/api/rule_test.go:794](../../../../pkg/api/rule_test.go:794) · [pkg/api/secure_lists_test.go:489](../../../../pkg/api/secure_lists_test.go:489)

No new P1. I traced findings, groups, rollups, process fields, and path consumers and found no further changes driven by Event records or CRD bundle metadata the caller cannot read. Served-kind discovery remains visible under D-108. Path filtering and restamping rebuild per request without writing into the shared memo. With both grants, the nil filter returns the original projection, timestamp and admission branches preserve their prior behavior, and the new skew fields are private. Source inspection found no payload regression. [docs/decisions.md:1601](../../../../docs/decisions.md:1601) · [pkg/paths/project.go:58](../../../../pkg/paths/project.go:58)

Read-only review of `93218be..HEAD`. `git diff --check` passed. No files edited or tests rerun.
### Triage

| # | Finding | Verdict | Resolution |
|---|---|---|---|
| R1 | P2 D-120 and the README omit the collection-status exception and the timestamp rule for these readers | accept | D-120 and README corrected |
| R2 | P3 the test denies both grants together and skips backend and route detail | defer | Carry-over for Phase 8 |
