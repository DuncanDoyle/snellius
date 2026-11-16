# Phase 7.3 — Authoriser (Complete)

**Implemented:** 2026-11-15 … 2026-11-16. **Closed:** 2026-11-16 on PR #50 (`duncandoyle/phase7Design`)
**Range:** 1263e1a9..HEAD of the close commit
**Close gate:** met. Go vet, gofmt, `go test ./...` (49 packages) and the race run on `pkg/auth/...` and `pkg/api/...` pass on `68b93ca`. Web typecheck, lint and 1,083 tests pass (web unchanged since `deb4f3a`).

## Summary
Secure mode now checks every object a person sees with SubjectAccessReview, as that person. Lists, counts and summaries show an object only when the viewer may `list` its resource in its namespace. Detail pages and refs named in a readable object also check a named `get` (D-100). A viewer who may list every served kind cluster-wide takes a lazy full-read fast path. Allowed answers are cached 60 s (`auth.authz.cacheTTL`, 30 s–15 min), denied ones 4–5 min, anchored at the check's start and never extended. There is no RBAC watch (D-099). Every response is buffered and committed only if its checks are still valid, the session is still signed in, and the identity is the session's current, fresh one. Otherwise it is rebuilt once or answers `503 authz-unavailable`, never a partial page or a 404. Checks run 8 per request in parallel, behind a global limit (32 in flight, 100/s, burst 500) and a per-request cap of 1,000. The permission cache has an 8 MiB byte budget. Sessions keep only the refresh token, with at most 1,000 by default (`auth.session.max`, D-101). Every request carrying an ended session's cookie hears `session-ended`.

## Sub-phases (phase record only)
n/a

## Reviews
- Plan: `reviews/plan-7.3-codex.md` and `reviews/plan-7.3b-codex.md` (D-100 tasks), triaged before implementation.
- Design changes: `reviews/d099-codex.md` (D-099), `reviews/sar-cost-codex.md` (D-100).
- Claude task reviews per task (Tasks 16–30).
- Codex: `reviews/7.3-codex.md`: 2 P1, 1 P2, all accepted and fixed. The re-review found 1 new P2, accepted and fixed (`68b93ca`). The controller checked that last fix; Codex did not re-review it.

## Waivers
None.

## Deliverables
- `pkg/auth/sar`: authoriser, global byte-bounded cache, full-read path, limiter, parallel batches.
- `pkg/api`: commit wrapper (`commit.go`), visibility (`visible.go`), roots-first prefetch on every list, `CheckLimit` 1,000.
- Flags and chart values:
  - `--authz-cache-ttl` / `auth.authz.cacheTTL`;
  - `--authz-named-get-scan` / `namedGetScan`;
  - `--authz-max-in-flight`, `--authz-rate`, `--authz-burst` / `maxInFlight`, `rate`, `burst`;
  - `--session-max` / `auth.session.max`.
- RBAC: `create subjectaccessreviews` in secure mode.
- Audit: `authz-unavailable` with reasons `check-failed`, `check-limit`, `full-read-failed`, `expired`, `identity-stale`.
- Decisions: D-099, D-100, D-101.
- Bench (`-tags bench`, Large fixture, viewer allowed 10 of 100 namespaces, on the fix commits):

  | Page | Cold SARs (denied-list / namespace grants) | Target |
  |---|---|---|
  | /routes | 286 / 337, warm 57 / 92 per min | ≤ 400 cold, ≤ 150/min warm |
  | /paths | 286 / 391 | ≤ 400 |
  | /backends | 386 / 496 | ≤ 550 |
  | /gateways, /listenersets | ≤ 126 | ≤ 400 |

  All 72 of 72 polls answer 200 on every page. Integrated retained heap: +56.8 MiB median against 64 MiB (settled 47.2, cache 8.7, 1,000 sessions 1.1).

## Rulings
R21 to R25 are in the SDD ledger, and R23 to R25 are folded into D-100. The others:
- **R26:** sessions keep only the refresh token (D-101).
- **R27:** a deny lives uniformly 4 to 5 min, so cold-page denials don't all refresh in the same minute.

## Notes for later phases
- 7.4 builds the namespace scope selector (D-100 item 8) on the list enumeration.
- `api.WithCommitCheck` returns the request to build with. A session check can swap in a new identity.

## Deferred / open
- **7.4 carries:**
  - EndpointSlice `fullRead` once EndpointSlices become an authorised ref;
  - remove `deniedInInstall`;
  - hidden findings in `visibleHealth`.
- **(P3)** The /routes prefetch fixes for `Via` and dependency sets have no dedicated test; the existing suites and the bench pass.
- **(P3)** With 50 viewers opening cold pages at once at 100/s, the last first answer takes about 3 min, and about 3% of polls answer `503 expired` meanwhile. This is documented (raise `auth.authz.rate`), and the default is unchanged.
- **Pre-existing:** `TestBudgetSpecEditStream` CPU budget fails under load (about 35% against 25%); it passed in the last runs.
