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
| `list` denied, `get` on the named object allowed (`resourceNames` grant) | Allowed for that object only |
| Kind with no resource mapping | Denied |
| SAR transport error, timeout or `evaluationError` | The request answers `503 authz-unavailable`, no body from the handler; never a 404 or a partial list |
| Permission revoked | Visible until the cached decision's TTL ends (default 30 s), then hidden; no extension past expiry |
| Identity that may `list` every served kind cluster-wide | Fast path: no per-object SARs; the decision expires with the TTL and is rechecked on the next request |
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

## Definition of done
- [ ] acceptance tests fail before, pass after (report both)
- [ ] full test suite passes (go, lint, web, helm)
- [ ] phase-7.3-complete.md written; phase-status.md updated
