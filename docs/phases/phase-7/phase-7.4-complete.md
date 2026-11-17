# Phase 7.4 — Visibility everywhere (Complete)

**Implemented:** 2026-11-16 … 2026-11-17. **Closed:** 2026-11-17 on PR #50 (`duncandoyle/phase7Design`)
**Range:** 99510dd2..HEAD of the close commit
**Close gate:** met. Go build, vet (with and without `-tags bench`), gofmt and `go test ./...` (51 packages) pass on the close commit; the race run on health, api, explore, paths, computed, diagnose, app, extension, auth and limits passed on the fix commits. Web typecheck, lint and 1,148 tests pass (65 files). Reopened the same day: the close missed golangci-lint (7 findings) and CI's bench, which failed the D-044 timing-regression gate and the analyzer-stage allocation budgets. Fixed in [`a0b2ce3`](https://github.com/DuncanDoyle/snellius/commit/a0b2ce3380ec6211376168061521453ae53cf3e3), [`f6a9011`](https://github.com/DuncanDoyle/snellius/commit/f6a90118ca70395e571e42c1259c2909fddb8546), [`6296934`](https://github.com/DuncanDoyle/snellius/commit/62969341b4e7464eb7f40fdb7ce1c056b9e97dbd) and [`01853e3`](https://github.com/DuncanDoyle/snellius/commit/01853e39637159b66a08813b657f5df29d5c7016): golangci-lint 0 issues, every engine budget test passes, and the remaining A/B regression is waived (D-115, Waivers).

## Summary
In secure mode, every surface shows a viewer without the full read only what their RBAC lets them read, and a change to an object they cannot read does not change what they see (D-085), apart from the stated exceptions (D-102, D-103, D-106 to D-110, D-112). Findings about the viewer's own objects come from a typed own-object package and show when their resource and basis are readable; every other finding and check result reads "Not checked" (D-106). Delegation analysis is derived and reads "Not checked" below the full read, marked per response with `delegationNotChecked` (D-114). Lists, details, counts, installs, `/overview`, `/meta`, paths, backends and extension handlers (`Deps.View`) follow the same rule, with no placeholders for hidden objects. The header shows a permissions note (`limitedView`). A namespace scope (`namespaces=` on seven lists, `/namespaces`, a header selector saved per browser) narrows lists and their permission checks. Path incompleteness is computed from readable objects and permissions only (D-111), for 7.5. Snellius counts and logs hits on its own size limits (`snellius_limit_hits_total`).

## Sub-phases (phase record only)
n/a

## Reviews
- Plan: `reviews/plan-7.4-codex.md`, 8 passes, triaged before implementation.
- Claude task reviews per task (Tasks 31–38), with fix rounds; not cross-model.
- Codex: `reviews/7.4-codex.md`: 5 P1 and 2 P3. Accepted; P1 3 and 5 accepted as the user-approved exception D-112. Seven re-reviews followed; finding 1 (delegation through hidden routes) hit the five-round circuit breaker and the user chose D-114. The last re-review found no P1–P3.
- Codex on the follow-up commits (limit-hit logging `36f54b7`, SAR fix `360a8f5`): 2 P2 in the logging, fixed in `28aa070` and confirmed; none in the SAR fix.
- Codex: `reviews/7.4-perf-codex.md` on the CI fixes: 2 P1 and 1 P3 in the faster registry, all accepted and fixed; the re-review found 1 P3, fixed. Codex recommended D-115.

## Waivers
- D-044's timing-regression gate on PR #50, for 7.4's remaining engine cost (D-115). The user, 2026-11-17: "Yes, accept and waive", to: "Accept 7.4's remaining engine cost (about 20% at the Large test size, well inside the time budgets) as D-115, and waive the 15% regression gate for PR #50 only, with a tracked follow-up to reduce it?"

## Deliverables
- `pkg/health/own`: own-object analyzers behind a typed boundary; `runOwn`, `fromOwn`, `Project`, `confine`, a deep-print fingerprint guarding the own-object class.
- `pkg/api`: projected `/health`, `/findings`, `/diagnostics`, lists, details, installs, `/overview`, `/meta` (`limitedView`), paths and backends over the viewer's visible rows; `api.Viewer`, `ViewerOf`, `VisibleTo`, `Viewer.Backends` for extensions; `delegationNotChecked`; `namespaces=` and `/namespaces`; the D-085 rule harness (`rule_test.go`, `export_rule_test.go`) with per-step sensitivity toggles.
- `pkg/explore`: viewer states, visible counts and ancestors, `PathIncomplete` (D-111).
- `pkg/limits`: `snellius_limit_hits_total{limit}` and a rate-limited warning per limit and install.
- Web: "Not checked" states, the permissions note, neutral install names, the namespace selector with an active marker and scoped empty states.
- Decisions: D-102 to D-115.
- Bench (`-tags bench`, Large, viewer allowed 10 of 100 namespaces, cold SARs, 3 runs, on the close commits):

  | Page | Denied-list | Namespace grants | Target |
  |---|---|---|---|
  | /routes | 286 | 347–355 | ≤ 400 |
  | /routes, scoped to one namespace | — | 27 | — |
  | /paths | 286–292 | 391–398 | ≤ 400 |
  | /backends | 385–392 | 496–500 | ≤ 550 |
  | /installs, viewer allowed nothing | Large 97, Extreme 304–309 | | ≤ 1,000 |

  Load bench (50 cold namespace-grants viewers polling /routes): 96.8–97.1 % of polls answer 200, last first answer 3m3s (7.3: 96.8 %). Retained heap +48.5 MiB against 64 (integrated with cache and 1,000 sessions: +59.1). A restricted viewer's projection on large-paths costs 45–50 ms per request.

## Rulings
R28 to R44 are in the SDD ledger; the design-level ones are D-108 to D-111 and D-113.

## Notes for later phases
- 7.5 opens the gate on these surfaces. `delegationNotChecked` is the per-response marker the web uses; new withheld fields need the same.
- The rule harness is the guard: every new field on a secure surface needs a rule case.
- `ShowsRow` (`pkg/paths`) repeats `Backends.rows()` admission checks; keep them in step.

## Deferred / open
- **(P2, 7.5)** Under 50 cold viewers, `/paths` and `/backends` fail the load bench, as at 7.3: the first build crosses the 4-minute deny floor (about 70 s margin on `/routes`), and `/backends` hits the per-request check cap. Fix in the retry and deny-floor design before the gate opens.
- **(P3)** `/meta` asks the full read in secure mode; cost not benchmarked beyond 2 cold checks.
- **(P3)** 7.4's engine cost (D-115): warm recompute at Large is 14–26 % above `main` before Phase 7. Share one route walk across the own analyzers; acceptance criteria in D-115.
- **Issues:** #52 delegation analysis for restricted viewers (D-114 follow-up); #53 diagnostic bundle for bug reports.
- **Pre-existing:** about 40 older review files on main link to a local path; fixed in PR #54.
