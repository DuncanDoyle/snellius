# Sub-phase 7.3 — Authoriser

Kind: planned
Base: 1263e1a9
Issues: none | Plan steps: implementation-plan.md, Sub-phase 7.3, Tasks 16 onward

## Problem / goal
Secure mode knows who is calling (7.2) but not what they may read. 7.3 adds the SubjectAccessReview authoriser: per-object checks against the API server, a per-identity cache with the D-099 TTL, the full-read fast path, and failing closed (503, never a partial view or a 404 during an outage). It also fixes the 7.2 review finding 1: every request with an ended session's cookie gets `session-ended`. The data gate stays closed in production; the authoriser is tested below it.

## Expected behaviour

| Input / action | Expected |
|---|---|
| Several requests in flight when a session ends | Every one answers `401 session-ended`; the UI shows "You've been signed out", never a silent new sign-in |
| Ref the identity may `list` in its namespace | Allowed, one SAR per resource and namespace per TTL |
| `list` denied, `get` on the named object allowed (`resourceNames` grant) | The object opens on its detail page and as a named ref of a readable object; it is not in lists, counts or facets unless `--authz-named-get-scan` is on (D-100) |
| List, count, facet or aggregate | Includes an object only when the viewer may `list` its resource in its namespace (cluster-wide for cluster-scoped kinds); no named `get` per hidden object (D-100) |
| Kind with no resource mapping | Denied |
| SAR transport error, timeout or `evaluationError` | The request answers `503 authz-unavailable`, no body from the handler; never a 404 or a partial list |
| Permission revoked | Visible until the cached allow expires (default 60 s, D-100), then hidden; no extension past expiry |
| New grant | Visible within 5 min (the denial TTL, D-100) |
| Allow used by a response expires before the response is written | Rebuilt once; if it expires again, `503 authz-unavailable` |
| Identity that may `list` every served kind cluster-wide | Fast path, asked only by requests that need a population: no per-object SARs; "true" expires at its earliest allow, "false" at its denying check |
| `/routes` on Large, viewer allowed 10 of 100 namespaces | At most 400 SARs cold and 150 per minute warm; every poll 200 without contention (D-100 targets, reported by the bench) |
| Many viewers at once | At most 32 SARs in flight and 100/s (burst 32) by default; the permission cache stays within 8 MiB |
| More than 500 distinct uncached checks in one request | `503 authz-unavailable` |
| Two identities with different groups | Never share a cached decision |
| `--authz-cache-ttl` outside 30 s to 15 min | Refused at startup |
| Local mode | Unchanged |

## Root cause (if known)
Session-ended: the server answers `session-ended` once and then forgets the session, so concurrent requests get `sign-in-required` (7.2 review finding 1, ruling R20). The rest is planned work.

## Acceptance tests
The tests named in the 7.3 tasks, at the HTTP handler, rendered chart and bench boundaries.

## Scope
In: `pkg/auth/sar` (new), `pkg/auth/session`, `pkg/api`, `pkg/app`, `pkg/cli`, `deploy/helm`, the bench, `web/packages/shell/src/auth` (session-ended only), README and chart README.
Out: visibility rules per endpoint (7.4), two signals and opening the gate (7.5), audit levels beyond `security` (7.6), RBAC watch (D-099).

## Plan / design impact
D-099: TTL for every decision, including the fast path; one setting; no RBAC watch. Recorded in design §6.2 and §6.4.
D-100 (after the Task 21 bench, `reviews/sar-cost-codex.md`): lists by `list` permission with named `get` only for details and named refs (flag `--authz-named-get-scan`); allows 60 s, denials 5 min; lazy fast path; validity at commit; an 8 MiB cache budget inside D-057's budget raised to 64 MiB; parallel resolution with global limits; measured targets replace 50 SARs/min. Recorded in design §6.1–§6.4 and §12; Tasks 22–28. The namespace scope selector moves to 7.4.

## Definition of done
- [ ] acceptance tests fail before, pass after (report both)
- [ ] full test suite passes (go, lint, web, helm)
- [ ] phase-7.3-complete.md written; phase-status.md updated
