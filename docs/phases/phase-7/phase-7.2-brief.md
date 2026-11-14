# Sub-phase 7.2 — Identity and sessions

Kind: planned
Base: b9c742243a95b0b245b380641b75ebe373e8e6f7
Issues: none | Plan steps: implementation-plan.md, Sub-phase 7.2, Tasks 9–15 (revised after reviews/plan-7.2-codex.md)

## Problem / goal
Secure mode needs to know who is calling, as the cluster sees them. 7.2 adds OIDC sign-in through the issuer the cluster trusts, identity from TokenReview, server-held sessions with refresh and logout, the web sign-in states, chart values and a test issuer. The data gate stays closed: every `/api/` request still answers 503 `secure-mode-incomplete`.

## Expected behaviour

| Input / action | Expected |
|---|---|
| Secure mode, OIDC not configured | Not ready (reason in the log); `/auth/status` `signIn: not-configured`; UI "Sign-in is not configured" |
| `/api/…` without a session | 401 `sign-in-required` with a login URL; the UI redirects once |
| Sign-in through the IdP, cluster accepts the token's audience | Session; `/auth/me` shows the cluster's username and groups; `/api/` answers 503 `secure-mode-incomplete` |
| Cluster does not accept the audience | `/signin-failed?reason=audience`; no session |
| Replayed callback, wrong state or nonce, expired token, open-redirect return path | Refused; audit failure record; no session |
| Idle 30 min, absolute 8 h, refresh with a different `sub` | Session ends; next request 401 `session-ended` |
| Sign out | Session deleted; IdP logout URL without `id_token_hint` |
| Local mode | Unchanged from 7.1 |

## Root cause (if known)
n/a (planned work).

## Acceptance tests
The tests named in Tasks 9–14, at the HTTP handler, rendered chart, browser and (CI) kind boundaries.

## Scope
In: `pkg/auth/oidc`, `pkg/auth/session`, `pkg/app`, `pkg/cli`, `pkg/audit` (trusted request IDs), `deploy/helm`, `web/packages/shell`, `test/oidc-issuer`, `test/e2e`, `.github/workflows/e2e.yaml`, README and chart README.
Out: the SAR authoriser (7.3), visibility (7.4), two signals and opening the gate (7.5), claim-mapping fallback (7.7, user decision).

## Plan / design impact
New flag `--oidc-ca-file` and chart values `auth.oidc.ca`, `hostAliases` (plan-codex 7.2 #4); `/auth/status` (#3). Recorded in the plan.

## Definition of done
- [ ] acceptance tests fail before, pass after (report both)
- [ ] full test suite passes (go, lint, web, helm)
- [ ] phase-7.2-complete.md written; phase-status.md updated
