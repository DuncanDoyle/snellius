# Phase 7.2 — Identity and sessions (Complete)

**Implemented:** 2026-11-14. **Closed:** 2026-11-15 on PR #50 (`duncandoyle/phase7Design`)
**Range:** 0728020..af7737d
**Close gate:** met. The first green `kind-secure` run is [run 43701271348](https://github.com/DuncanDoyle/snellius/actions/runs/43701271348), after two fixes it found: the API server manifest was staged on the node's tmpfs, and the TokenReview set `audiences` (D-097).

## Summary
Secure mode now signs people in with OIDC through the issuer the cluster trusts. Snellius checks the ID token's claims, then asks the API server (TokenReview) who the person is. It keeps a server-side session: idle 30 min, absolute 8 h, identity refreshed at most every 15 min, at most 5,000 sessions. Sign-out removes the session and sends the browser to the IdP's logout page without an `id_token_hint`. The web UI redirects to sign-in, shows pages for "not set up", "provider unreachable", "not accepted by this cluster", "sign-in failed" and "signed out", and adds an account menu. Data stays closed: `401` without a session, `503 secure-mode-incomplete` with one.

## Sub-phases (phase record only)
n/a

## Reviews
- Plan: `reviews/plan-7.2-codex.md` (8 findings, all accepted before implementation).
- Claude task reviews per task; Codex security reviews of Tasks 10 (open redirect, P1, fixed) and 11 (two P2, fixed).
- Codex: `reviews/7.2-codex.md`: 1 P1, 3 P2, 1 P3, 1 P4. Fixed: 2, 3, 4, 6. Partly fixed and deferred: 1. Documented: 5.
- Codex: `reviews/7.2-tokenreview-codex.md` (D-097 fix): no P1; 2 P2 and 1 P3, all accepted and fixed.

## Waivers
None.

## Deliverables
- Commits `f5bfb13` … `af7737d` (25).
- Routes outside `/api/`: `/auth/status`, `/auth/login`, `/auth/callback`, `/auth/me`, `POST /auth/logout`; SPA route `/signin-failed`.
- Flags: `--oidc-issuer-url`, `--oidc-client-id`, `--oidc-client-secret-file`, `--oidc-redirect-url`, `--oidc-scopes`, `--oidc-ca-file`, `--cookie-secure`, `--session-idle`, `--session-absolute`, `--trusted-proxy-cidrs`.
- Chart values: `auth.oidc.*` (issuer, client, redirect, scopes, client secret Secret, CA ConfigMap), `auth.session.*`, `auth.trustedProxyCIDRs`, `hostAliases`; `create tokenreviews` in secure mode only; secret and CA as directory mounts so rotation reaches the file.
- Metrics: `snellius_sessions_active`. Audit: login success and failure, logout, session end, eviction.
- `test/oidc-issuer`, a standard-library test issuer; `SECURE=1` kind setup; `TestSignIn`, `signin.spec.ts`; `kind-secure` CI job.
- Gates on `af7737d`: `go test ./...` all pass; `make lint` 0 issues; chart tests pass; web 1064/1064, lint, typecheck.
- `kind-secure` CI: secure kind setup, `TestSignIn` and `signin.spec.ts` pass ([run 43701271348](https://github.com/DuncanDoyle/snellius/actions/runs/43701271348)).

## Notes for later phases
- 7.3 reads the identity from the request context (`session.IdentityFrom`).
- Rulings R14–R20 are in the SDD ledger summary of the phase record at phase close.

## Deferred / open
- First in 7.3: answer `session-ended` to every request with the ended session's cookie, so concurrent requests agree (7.2 review finding 1).
- The secure Playwright run skips browser TLS verification of the test issuer; the Go test and the API server verify it.
- Minor: secret volume file mode is the default; sessions gauge counts unswept expired sessions until the next sweep; refresh contexts can outlive shutdown by up to 30 s; tooltip repeats the visible username.
- Pre-existing: explore detail, list and map web tests time out under heavy machine load; fix at the root in its own PR.
