# Phase 7 — Identity & Access: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Each sub-phase gets a brief (`phase-7.<M>-brief.md`) at its start; this plan fixes the task split, interfaces and tests. Steps use `- [ ]`.

**Goal:** Snellius `v0.8.0`: local and secure modes, OIDC sign-in, SubjectAccessReview authorisation, the D-085 rule on every surface, two signals per route, and an audit stream.

**Architecture:** Middleware around the existing mux carries mode, Host checks, request IDs, sessions and audit. A `SARAuthorizer` plugs into `api.Authorizer`, with an additive `ErrAuthorizer` and a buffered response wrapper. Findings and check results gain a basis dependency set; every endpoint projects only readable objects. The two route signals are computed in `pkg/explore` so kgwctl shares them.

**Tech stack:** Go (net/http, log/slog, k8s client-go `authentication/v1`, `authorization/v1`, `golang.org/x/oauth2` already in the module graph), React + TypeScript (shell package), Helm chart with Go chart tests, kind e2e with Playwright.

**Spec:** `docs/phases/phase-7/design.md` (revised after `reviews/design-codex.md`).

## Global constraints

- No new dependency without the user's approval. `golang.org/x/oauth2` moves from indirect to direct; it is already in `go.mod`.
- Secure mode never serves data until 7.5 opens the gate (design §3.2; moved from 7.4 after `reviews/plan-codex.md` #7, so the restricted-viewer summary lands first).
- Public repo: no vendor or edition names in code, files, commits or PR text.
- Product principles 2 (read-only), 8 (no Secrets through the API), 9 (least privilege), 10 (one operator app).
- Comments 1–2 lines; tests assert behaviour at the highest practical boundary (HTTP handler, rendered chart, browser).
- UI strings go through the wording picker; copy lives in `web/packages/shell/src/copy/`.
- Budgets: D-057 memory gate; the existing bench budgets stay green.

## Review focus

1. A response for a restricted viewer that changes when a hidden object changes (the D-085 rule). Tested per surface in 7.4 with fixed clocks.
2. Secure mode serving any data before 7.4, or after a failed check. Tested in 7.1 (gate) and 7.3 (503 path).
3. Local mode reachable from anything other than a loopback `Host` through port-forward. Tested in 7.1 (unit) and e2e.
4. Sign-in accepting a token meant for another audience, a replayed callback or a stale identity. Tested in 7.2.
5. Legacy `health` changing for a full-read viewer. Tested in 7.5 with the compatibility matrix.
6. A cached permission decision served to another identity, or after its TTL (a cache or merge key that misses part of the identity, an expiry that moves). Tested in 7.3 with a fake clock.

---

## Sub-phase 7.1 — Modes and groundwork

### Task 1: Auth mode, loopback and Host enforcement

**Files:** `pkg/cli/flags.go`, `pkg/app/app.go`, new `pkg/app/mode.go`, new `pkg/api/guard.go`; tests `pkg/app/mode_test.go`, `pkg/api/guard_test.go`.

**Interfaces:**
- `app.Options.AuthMode string` (`"local"` default, `"secure"`), `app.Options.AllowedHosts []string`, `app.Options.ProbeListen string`.
- Flags: `--auth-mode`, `--allowed-hosts` (comma list), `--probe-listen` (default `:8081` in-cluster, `127.0.0.1:8081` locally).
- `func checkListen(mode, addr string, podIP, hostIP string) error` — local mode: host must be a loopback address (`127.0.0.0/8`, `::1`, `localhost`); `podIP != "" && podIP == hostIP` is an error. `podIP`/`hostIP` from env `POD_IP`, `HOST_IP`.
- `func HostGuard(allowed []string, next http.Handler) http.Handler` — serves only `Host` of `localhost`, `127.0.0.1`, `[::1]` (any port) or an allowed name; else `421`. Refuses a request whose `Origin` host differs from its `Host` with `403`. Applied in local mode only.

- [ ] Write failing tests: `TestCheckListenLocalRefusesNonLoopback` (":8080", "0.0.0.0:8080", "10.0.0.5:8080" → error; "127.0.0.1:8080", "[::1]:8080", "localhost:8080" → nil), `TestCheckListenRefusesHostNetwork`, `TestHostGuardRebinding` (Host `evil.example` → 421; `localhost:18182` → 200; allowed name → 200), `TestHostGuardCrossOrigin`.
- [ ] Implement; `RunWith` defaults `Listen` to `127.0.0.1:8080` in local mode in-cluster too, and calls `checkListen` before listening.
- [ ] `go test ./pkg/app/... ./pkg/api/...`; commit.

### Task 2: Probe listener and `/metrics`

**Files:** `pkg/app/app.go` (`serve`), new `pkg/obs/metrics.go`; `pkg/api/handlers.go` (`readyz`); tests `pkg/obs/metrics_test.go`, `pkg/api/api_test.go`, `pkg/app/serve_test.go`.

**Interfaces:**
- `obs.Registry` with `Counter(name, help string, labels ...string) *obs.CounterVec`, `Gauge(...)`, and `ServeHTTP` writing Prometheus text format 0.0.4. No dependency.
- `api.ProbeHandler(src ModelSource, reg *obs.Registry) http.Handler` serving `/healthz`, `/readyz`, `/metrics`.
- `readyz` no longer writes install IDs: `503` with body `not ready` (main and probe listener).
- Series: `snellius_auth_mode{mode}` gauge = 1.
- Readiness is shared state: `api.ProbeHandler` takes a `func() (ready bool, reason string)` built in `app` from "model synced, no rejected installs" plus, from 7.2, "sign-in configured in secure mode". The reason goes to the operational log, never to the response body. The rejected install IDs stay in the log (D-016 evidence).

- [ ] Update `TestInvalidInstallIDsAreRejected` (`api_test.go:1307-1312`): it asserts `503` with a generic body, and the IDs in the log.
- [ ] Tests: `TestReadyzHidesRejectedInstalls`, `TestMetricsExposition` (text format, sorted, escaped labels), `TestServeStartsProbeListener` (both answer over HTTP; the probe listener has no `/api/`), `TestServeSecondBindFails` (the first listener is closed, `serve` returns the error), `TestServeOneServerFails` (the other stops; nothing keeps serving), `TestServeDrainsBoth` (an in-flight request on each finishes on shutdown). Extend the existing lifecycle tests at `app_test.go:575-644`.
- [ ] Implement `serve` with two listeners; shutdown drains both; either failing stops both.
- [ ] Commit.

### Task 3: Request IDs and the audit skeleton

**Files:** new `pkg/audit/audit.go`, `pkg/audit/record.go`; `pkg/app/app.go`; tests `pkg/audit/audit_test.go`.

**Interfaces:**
- `audit.Record` (schema `1`): fields of design §9.3; `Level` one of `security|agent|reads|summaries|disclosure|requests`.
- `audit.Logger` with `New(w io.Writer, levels audit.Levels, reg *obs.Registry)`, `Emit(ctx, Record)`; JSON lines with `"log":"snellius.audit"`; counters `snellius_audit_records_total{level}`, `snellius_audit_dropped_total{level}`; never blocks the request (bounded channel, drop with count).
- `audit.RequestID(next http.Handler) http.Handler`: always generates `X-Request-Id` in 7.1. 7.2 adds `audit.RequestIDTrusting(cidrs []netip.Prefix, next)` once `--trusted-proxy-cidrs` exists, keeping a valid incoming ID only from those sources.
- Startup record: `action:"startup"`, `mode`, `listen`.
- Flags: `--audit-levels` (default `security,agent,reads,summaries`).

- [ ] Tests: `TestRecordJSONSchema` (golden line), `TestDisabledLevelEmitsNothing`, `TestDropCountsWhenFull`, `TestRequestIDGenerated`, `TestRecordNeverCarriesSecrets` (a record built from a request with `Cookie`/`Authorization` contains neither).
- [ ] Commit.

### Task 4: Secure-mode gate

**Files:** `pkg/api/handlers.go` (`NewHandler`), `pkg/app/app.go`; tests `pkg/api/gate_test.go`.

**Interfaces:**
- `api.WithSecureMode(complete bool)` handler option; `NewHandler`'s signature does not change. In secure mode with `complete == false`, the outer gate answers every `/api/` route (extension mounts included) with `503 {"error":"secure-mode-incomplete"}`. The gate is authoritative: nothing behind it runs.
- In secure mode, `app.Build` never passes a nil `Authorizer`: until 7.3 it wires `api.DenyAll{}` (new, denies every ref). `NewHandler` keeps mapping nil to `AllowAll` only when secure mode is off.
- The process starts, serves the probe listener and reports ready in secure mode in 7.1 (sign-in readiness arrives in 7.2).

- [ ] Tests: `TestSecureModeGateBlocksEveryAPIRoute` (iterate the registered routes, including an ext mount), `TestSecureModeWiresDenyAll` (`Build` with secure mode and no registry authoriser yields a handler whose authoriser denies), `TestLocalModeUnchanged`.
- [ ] Commit.

### Task 5: Chart modes and refusals

**Files:** `deploy/helm/snellius/values.yaml`, `templates/deployment.yaml`, `templates/service.yaml`, `templates/NOTES.txt`, `templates/_helpers.tpl`, `deploy/helm/snellius/README.md`; tests `deploy/helm/chart_test.go`.

Install surface (stated, per the team rule):
- **New values:** `auth.mode` (`local` default, `secure`) → `--auth-mode`; `auth.allowedHosts` (list, default empty) → `--allowed-hosts`; `audit.levels` (list, default `[security, agent, reads, summaries]`) → `--audit-levels`.
- **New port:** container port `probe` 8081 → `--probe-listen=:8081`; liveness and readiness move to it. The named port `http` 8080 and Service port 80 stay in both modes, so `port-forward svc/…` keeps resolving (plan-codex #1).
- **Changed arg:** `--listen=127.0.0.1:8080` in local mode, `:8080` in secure mode.
- **New env:** `POD_IP`, `HOST_IP` from the downward API (the binary's host-network check).
- **Changed behaviour:** `httpRoute.enabled` in local mode fails rendering with a message pointing to `auth.mode: secure`. This breaks an upgrade that keeps `httpRoute.enabled`; the upgrade notes say so (Task 8).
- **Not added:** the chart has no `service.type`, `hostNetwork` or `replicas` value today, so there is nothing to refuse; a user who adds them through `extraArgs`-style overrides is caught by the binary's checks. No new value for pod metadata: local mode adds the fixed pod annotation `sidecar.istio.io/inject: "false"` and label `istio.io/dataplane-mode: none`.
- NOTES: local mode states its trust model and the port-forward command; secure mode says sign-in configuration arrives with 7.2 values (introduced there, plan-codex #3).

- [ ] Update the chart tests that assert `--listen=:8080` (`chart_test.go` around lines 219, 374, 403, 417) to the per-mode value.
- [ ] Tests: `TestLocalModeDefaults` (args, both ports, probes on `probe`), `TestLocalModeRefusesHTTPRoute`, `TestSecureModeArgs`, `TestMeshOptOutInLocalMode`, `TestValuesWiredToFlags` (`auth.allowedHosts`, `audit.levels`), and move `TestHTTPRouteIsOptIn` and `TestHTTPRouteRequiresParentRefs` to secure mode.
- [ ] Commit.

### Task 6: Mode in `/meta` and the local-mode banner

**Files:** `pkg/api/types.go` (`MetaResponse` at `:55`, `AuthMode`, additive), `pkg/api/handlers.go`; web `api/types.ts`, the exhaustive `metaShape` in `types.golden.test.ts` (`:597`), new `shell/ModeBanner.tsx`, `shell/Shell.tsx`, `copy/shell.ts`; tests `pkg/api/api_test.go` and the meta golden, `web/.../shell/ModeBanner.test.tsx`, e2e `web/e2e/shell.spec.ts`.

- Banner in local mode on every page, non-dismissable, muted warning style with icon and text (wording picker); hover/active styles per team rules; light and dark checked; axe scan stays clean.

- [ ] Tests: banner shows for `authMode: "local"`, absent for `"secure"`; the golden `meta` response gains the field.
- [ ] Commit.

### Task 7: e2e for local mode

**Files:** `test/e2e/setup.sh`, `test/e2e/api_test.go` (or a new `modes_test.go`).

- Port-forward to the Service and to the pod both serve `/api/v1/meta`.
- From a pod in the cluster, the pod IP on 8080 refuses the connection; on 8081 only `/healthz`, `/readyz`, `/metrics` answer; the Service's ClusterIP on port 80 refuses.
- A request with `Host: evil.example` through port-forward gets `421`.

- [ ] Write the tests; run the e2e locally on kind; commit.

### Task 8: Docs

- README: trust model (local vs secure), the upgrade note (an install with `httpRoute.enabled` must set `auth.mode: secure`, available from 7.2's flow; until `v0.8.0` ships, main is a development build). Chart README values. `docs/documentation-notes.md` topics.
- [ ] Commit; 7.1 complete record.

---

## Sub-phase 7.2 — Identity and sessions (data still gated)

The data gate from 7.1 stays closed. 7.2 adds identity: who is calling, proven by the cluster. New routes live under `/auth/` (outside `/api/`, so outside the gate).

### Task 9: OIDC configuration and discovery

**Files:** new `pkg/auth/oidc/config.go`, `discovery.go`; `pkg/cli/flags.go`, `pkg/app/app.go`; tests `pkg/auth/oidc/discovery_test.go`, `pkg/cli/flags_test.go`.

**Interfaces:**
- Flags (secure mode only; ignored with a warning in local mode): `--oidc-issuer-url`, `--oidc-client-id`, `--oidc-client-secret-file` (optional; public client when empty), `--oidc-redirect-url`, `--oidc-scopes` (default `openid,profile,email,groups,offline_access`).
- `oidc.Config` and `func (c Config) Validate() error`: issuer and redirect URLs must be absolute `https` (the redirect may be `http://localhost…` or `http://127.0.0.1…` for testing); client ID required.
- `oidc.Provider` with `Discover(ctx, *http.Client) (*Endpoints, error)`: reads `{issuer}/.well-known/openid-configuration`; its `issuer` must equal the configured issuer exactly; `authorization_endpoint`, `token_endpoint` must be `https` (same `http`-loopback exception for tests); `end_session_endpoint` optional. A `Provider.Ready()` state: discovered or not, with the last error; retried with backoff (1 s doubling to 1 min) until it succeeds.
- Readiness (the 7.1 readiness func) in secure mode also requires `Validate()` passing and discovery done; the reason ("sign-in not configured: …") goes to the log only.
- `--oidc-ca-file` (optional): a PEM bundle added to the system roots for discovery and token calls, for issuers behind a private CA (plan-codex 7.2 #4).
- Public auth status, outside `/api/` and the gate (plan-codex 7.2 #3): `GET /auth/status` → `{"mode":"local"|"secure","signIn":"ready"|"not-configured"|"unavailable"}`. No reasons, URLs or errors in the body.

- [ ] Tests: `TestValidateConfig` (table), `TestDiscoveryIssuerMismatch`, `TestDiscoveryRejectsHTTPEndpoints`, `TestDiscoveryRetries` (fails twice then succeeds, injected clock), `TestSecureModeNotReadyWithoutOIDC`.

### Task 10: Sign-in flow and TokenReview

**Files:** new `pkg/auth/oidc/login.go`, `transaction.go`, `tokenreview.go`; tests alongside.

**Interfaces:**
- `oidc.Reviewer` interface: `Review(ctx, idToken string) (Identity, error)`; production implementation creates an `authentication.k8s.io/v1` `TokenReview` without `spec.audiences` (D-097) through client-go and returns `Identity{Username, UID, Groups, Extra}` only when `status.authenticated` is true.
- Transaction store: one-use records keyed by a random 128-bit ID in cookie `snellius_login` (HttpOnly, SameSite=Lax, Secure per `--cookie-secure`, Path `/auth/`, Max-Age 600); holds state, nonce, PKCE S256 verifier, return path; at most 10,000 open transactions (oldest evicted).
- `GET /auth/login?return=<path>`: return path must be a same-origin relative path starting with `/` and not `//`; else `/`. Redirects to the authorization endpoint with `response_type=code`, `client_id`, `redirect_uri`, `scope`, `state`, `nonce`, `code_challenge`, `code_challenge_method=S256`.
- `GET /auth/callback`: the order of design §4.1 exactly (transaction, state, code exchange with verifier, ID-token claims `iss`/`aud`/`azp`/`exp`/`iat` ±2 min/`nonce` decoded without trust, TokenReview, return path). Any failure: an error page state (no session), audit `security` record `login` with `outcome: failure` and a reason code, never the token.
- The ID token is never logged or sent to the browser.
- A failed callback redirects to `/signin-failed?reason=<code>`, codes `unauthenticated` (TokenReview false), `invalid` (anything else). No other detail reaches the browser.

- [ ] Tests (with an `httptest` issuer and a fake `Reviewer`): happy path; replayed callback; wrong state; expired transaction; wrong `aud`; `azp` mismatch with several audiences; expired token; wrong nonce; TokenReview `authenticated: false`; open-redirect return paths (`//evil`, `https://evil`, `/\evil`); the redirect reason codes.
- [ ] The production `Reviewer` adapter is tested against a fake API server (`httptest` serving `/apis/authentication.k8s.io/v1/tokenreviews` behind a client-go config): the request carries no `spec.audiences` (D-097); `authenticated: false`; transport failure (plan-codex 7.2 #7).

### Task 11: Sessions, refresh and logout

**Files:** new `pkg/auth/session/store.go`, `refresh.go`, `middleware.go`; `pkg/app/mode.go`; tests alongside.

**Interfaces:**
- `session.Store`: 256-bit IDs; cookie `snellius_session` (HttpOnly, SameSite=Lax, Secure per `--cookie-secure`, Path `/`); idle 30 min (`--session-idle`), absolute 8 h (`--session-absolute`); at most 5,000 sessions (oldest idle evicted, audited); a sweeper every minute; an opaque audit session ID per session, distinct from the cookie value.
- Freshness per design §4.4: fresh until min(ID token `exp`, last TokenReview + 15 min); refresh ahead of staleness, single-flight per session; new ID token must keep `iss` and `sub`; then TokenReview replaces groups; failure ends the session; logout wins over an in-flight refresh. A request with a stale identity gets `401`.
- Middleware in secure mode, in front of the 7.1 gate: `/api/` without a valid session → `401 {"error":"sign-in-required","login":"/auth/login?return=<path>"}`; a session that ended (idle, absolute, refresh failure) → `401 {"error":"session-ended","login":…}` once, then `sign-in-required`; with a session the request carries the identity in its context (for 7.3) and still hits the gate's 503.
- `GET /auth/me` → `{username, groups}` of the session (not cluster data); `401` without a session.
- `POST /auth/logout`: requires an `Origin` header equal to the origin of the configured redirect URL — the public origin, which stays correct behind TLS termination (plan-codex 7.2 #1); deletes the session; responds `{"next": url}` with the IdP's `end_session_endpoint` when discovered, using `client_id` and `post_logout_redirect_uri` (the app root) and **no** `id_token_hint`, so no token reaches the browser (plan-codex 7.2 #2); else `/`. The docs say to register the post-logout redirect URI at the IdP.
- Audit `security` records: login success, logout, session expired (idle, absolute, refresh failure), eviction.
- Gauge `snellius_sessions_active` on the 7.1 registry, updated on create, logout, expiry and eviction (plan-codex 7.2 #6).
- `audit.RequestIDTrusting(cidrs, next)` with `--trusted-proxy-cidrs` (also the only source of `X-Forwarded-For` for the audit `source`).

- [ ] Tests (injected clock): idle and absolute expiry; bound and eviction; refresh single-flight under concurrency (`-race`); refresh with a different `sub` ends the session; logout during refresh; `/auth/me`; 401 bodies (`sign-in-required`, `session-ended`); logout `Origin` against an `https` redirect URL with a plain-HTTP upstream request, missing Origin refused; logout URL has no `id_token_hint`; the sessions gauge; trusted request IDs only from configured CIDRs.

### Task 12: Chart for secure mode

**Files:** `deploy/helm/snellius/values.yaml`, `templates/deployment.yaml`, `templates/clusterrole.yaml`, `templates/_helpers.tpl`, `templates/NOTES.txt`; tests `deploy/helm/chart_test.go`.

Install surface:
- **New values:** `auth.oidc.issuerURL`, `auth.oidc.clientID`, `auth.oidc.redirectURL`, `auth.oidc.scopes` (list), `auth.oidc.clientSecret.existingSecret` and `.key` (mounted read-only at `/etc/snellius/oidc/client-secret`, passed as `--oidc-client-secret-file`), `auth.oidc.ca.existingConfigMap` and `.key` (mounted at `/etc/snellius/oidc/ca.pem`, passed as `--oidc-ca-file`), `auth.session.idle`, `auth.session.absolute`, `auth.session.cookieSecure` (default true), `auth.trustedProxyCIDRs` (list), and `hostAliases` (list, rendered into the pod spec; needed by the e2e and by clusters whose IdP name is not in DNS). All read nil-safe (`dig`), as in 7.1.
- Secure mode fails rendering without `issuerURL`, `clientID` or `redirectURL`.
- ClusterRole gains `create` on `authentication.k8s.io/tokenreviews` in secure mode only.
- NOTES in secure mode: the redirect URL to register at the IdP, and the audience requirement (same client ID as the cluster, or an extra audience in the API server's authentication configuration), with `kubectl auth whoami` as the check.

- [ ] Tests: renders every flag; secret mount only when `existingSecret` is set; required-values failures; tokenreviews rule only in secure mode; `--reuse-values` without the new keys still renders (local mode).

### Task 13: Web sign-in

**Files:** `web/packages/shell/src/api/client.ts`, `api/hooks.ts`, new `auth/` components, `shell/Header.tsx`, `shell/Shell.tsx`, `copy/`; tests alongside and `web/e2e`.

- `request()`: a `401` with `login` redirects the browser to that URL (once; no loop); `retryEveryPoll` stops on 401.
- SWR polling pauses while `document.visibilityState` is hidden, and scheduled error retries and focus revalidation wait until the tab is visible again (plan-codex 7.2 #8).
- Pages, each driven by a real backend response (plan-codex 7.2 #3): "Signing in" (redirecting); "Sign-in is not configured" (`/auth/status` `signIn: not-configured`, generic text plus the doc link); "Can't reach the sign-in provider" (`unavailable`); "Your sign-in is not accepted by this cluster" (`/signin-failed?reason=unauthenticated`, D-097); "Sign-in failed" (`reason=invalid`); "Your session has ended" (`401 session-ended`).
- Header in secure mode: the user's name with a menu: "Who am I" (username and groups from `/auth/me`) and "Sign out" (`POST /auth/logout`, then follow the returned URL). Hover and active styles; tooltips; light and dark checked.
- Every new string through the wording picker (controller supplies the copy).

- [ ] Tests: redirect once on 401; no retry loop; polling pause, including a tab hidden after an error (no retry fires while hidden); menu renders identity; sign-out posts and navigates; each sign-in state page from its backend response.

### Task 14: Test issuer and e2e

**Files:** new `test/oidc-issuer/` (Go, `package main`, standard library only), `test/e2e/setup.sh`, new `test/e2e/kind-auth.yaml` (structured authentication configuration), `test/e2e/signin_test.go`, `web/e2e/signin.spec.ts`, `.github/workflows/e2e.yaml`.

- Issuer: HTTPS with a generated test CA; discovery; JWKS (RSA); `/authorize` with a form to pick a test user; `/token` with code + PKCE verification and refresh tokens; `/end_session`; users and groups from flags.
- Connectivity, one hostname `oidc.snellius.test` for all three clients (plan-codex 7.2 #4):
  - **Browser** (Playwright on the runner): the issuer runs on the runner on a fixed port; the hostname resolves to 127.0.0.1 through Chromium's `--host-resolver-rules`.
  - **API server** (kind control-plane node): the kind config adds an `/etc/hosts` entry on the node mapping the hostname to the docker network gateway, where the runner's issuer port is reachable; the structured authentication configuration carries the test CA.
  - **Snellius** pod: `hostAliases` maps the hostname to the same gateway address; `auth.oidc.ca` supplies the test CA as a ConfigMap.
  - The issuer certificate's SAN is `oidc.snellius.test`.
- e2e: a `SECURE=1` setup variant installs Snellius in secure mode; two users sign in through the real flow; `/auth/me` shows the cluster's view of each (prefixed username and groups); `/api/` still answers `503 secure-mode-incomplete`; logout ends the session.
- CI (plan-codex 7.2 #5): a new `kind-secure` job in `.github/workflows/e2e.yaml` runs `SECURE=1` setup and only the sign-in tests (`-run TestSignIn`, `signin.spec.ts`); the existing jobs skip them when `SECURE` is unset.
- If kind cannot run locally, the controller runs the Go-level pieces against unit fakes and CI runs the kind job on the PR.

- [ ] Tests as listed; the issuer has its own unit tests for PKCE and token signing.

### Task 15: Docs for sign-in

- README and chart README: configuring secure mode step by step (numbered), both audience options, `kubectl auth whoami`, client secret Secret, cookie and HTTPS notes, the in-memory session limitation (one replica, restart signs everyone out).
- `docs/documentation-notes.md` topics.
- [ ] Commit; 7.2 complete record (controller).

## Sub-phase 7.3 — Authoriser (data still gated)

The data gate stays closed: `app.Build` keeps `WithSecureMode(false)`. 7.3 first closes the 7.2 session-ended race, then adds the SubjectAccessReview authoriser (D-091 as amended by D-099) and the response-commit protocol. Tests reach the authoriser below the gate by building the handler with `WithSecureMode(true)`, the value 7.5 ships; no production path sets it.

**Codex plan review:** `reviews/plan-7.3-codex.md` (2 P1, 2 P2, 1 P3, 1 P4), all accepted; the tasks below include the changes.

### Task 16: `session-ended` for every request with the ended cookie

Ruling R20 (7.2 review finding 1). Today `takeNotice` (`refresh.go:65`) deletes the notice on first read and `fromRequest` (`middleware.go`) clears the cookie, so concurrent requests get `sign-in-required` and the browser can restart sign-in by itself.

**Files:** `pkg/auth/session/refresh.go`, `middleware.go`, `store.go`; web `auth/authState.ts`, `api/client.ts`, `auth/SignInPages.tsx`; tests `pkg/auth/session/middleware_test.go`, `store_test.go`, `refresh_test.go`, web `auth/client401.test.tsx`, `auth/fixround.test.tsx`.

**Interfaces:**
- An end notice is no longer consumed: every `/api/` and `/auth/me` request carrying the ended session's cookie gets `401 {"error":"session-ended","login":…}` until a new sign-in replaces the cookie or the notice expires.
- A `session-ended` response no longer clears the cookie; `sign-in-required` still does. The cookie maps to no session, so keeping it grants nothing.
- Bound (the existing one, now stated): a notice lives for `--session-absolute` (8 h) after the end, at most 5,000 notices (the session bound); at the bound the oldest is forgotten, and that cookie then hears `sign-in-required`.
- `Complete` and `Logout` delete the notice for the cookie they replace or clear.
- Web: the existing coordination stays (`trackAuthRequest`, `requestsSettled`, the sticky `ended` state). A notice forgotten at the bound can still reorder answers, so the client adds a rule of its own:
  - A tab that had a session shows the signed-out page ("You've been signed out", with an explicit "Sign in again") on `sign-in-required` as well as on `session-ended`. It never redirects by itself.
  - Only a tab that never had a session redirects to sign-in automatically.
  - "Had a session": a successful `/auth/me` or `/api/` response in this tab, kept in `sessionStorage` (`snellius.hadSession`) so a reload keeps it; cleared only by "Sign in again". "Sign out" replaces it with a signed-out state (`snellius.signedOut`) that also suppresses automatic redirects until "Sign in again", so a late `sign-in-required` after logout shows the signed-out page instead of starting sign-in. When storage throws, both live for the page load only.

- [ ] Regression first, at the HTTP boundary: change the last assertion of `TestMiddleware` (`middleware_test.go:126`) to expect `session-ended` on the second request; see it fail.
- [ ] Tests (injected clock): `TestEndedSessionAnswersEveryRequest` (idle expiry, then 20 concurrent `/api/` requests and 5 sequential ones with the cookie: all `session-ended`, none sets a clearing cookie; `-race`); the same for absolute expiry and refresh failure in `refresh_test.go`; `TestEndedNoticeClearedBySignIn` (sign-in with the old cookie: new session works, the old value hears `sign-in-required`); `TestLogoutClearsEndedNotice`; `TestEndedNoticeExpires` (after 8 h: `sign-in-required`, cookie cleared); `TestEndedNoticeBound` (notice 5,001 forgets the oldest). Update the `wantMe` sequences in `store_test.go`/`refresh_test.go` that expect `sign-in-required` after the first ended answer.
- [ ] Web tests with a backend fake that answers like the server (every request with the cookie `session-ended`): concurrent requests show the signed-out page and never navigate; reversed answers across notice eviction (deterministic release order: request A is answered `session-ended` but held, the notice is evicted, B's `sign-in-required` is delivered first, then A's: the signed-out page shows, nothing navigates); a tab that had a session gets only `sign-in-required` (signed-out page, no navigation); a tab that never had one is redirected once; the flag survives a reload and is cleared by "Sign in again"; after "Sign out", a delayed `sign-in-required` arriving after logout shows the signed-out page and never navigates (deterministic order). Replace the one-shot fake in `client401.test.tsx:72-120` with the persistent one.
- [ ] `go test ./pkg/auth/... -race`, web tests, lint; commit.

### Task 17: SAR authoriser and its cache

**Files:** new `pkg/auth/sar/authorizer.go`, `cache.go`, `checker.go`, `identity.go`, `metrics.go`; `pkg/api/authz.go` (`ErrAuthorizer`, `SpendCheck`); tests `pkg/auth/sar/authorizer_test.go`, `cache_test.go`, `checker_test.go`.

**Interfaces:**
- `pkg/api` (additive): `type ErrAuthorizer interface { AuthorizeErr(r *http.Request, ref objects.Ref) (bool, error) }` (design §6.3); `func SpendCheck(r *http.Request) error` counts one uncached check for r's request and returns `api.ErrCheckLimit` past 500; nil outside a committed request (Task 19 installs the counter).
- `sar.Attributes{Verb, Group, Resource, Namespace, Name string}`; `sar.Checker` interface `Check(ctx, oidc.Identity, Attributes) (bool, error)`.
- `sar.KubeChecker` (`NewKubeChecker(kubernetes.Interface)`): creates an `authorization.k8s.io/v1` SubjectAccessReview with `user`, `groups`, `extra`, `uid` and `resourceAttributes`; per-call timeout 10 s, below the 30 s minimum TTL. Outcomes (design §6.3): `allowed: true` → allow; `allowed: false` (with or without `denied`) → deny; non-empty `evaluationError` (even with `allowed: true`), HTTP error or timeout → `sar.ErrCheck` wrapping a fixed category, never a deny. Errors are logged by category, never with the identity's groups.
- `sar.Kind{Group, Resource string; Namespaced bool}`; `sar.Kinds` interface `Lookup(group, kind string) (Kind, bool)` and `All() (kinds []Kind, key string)` (Task 18 uses `All`).
- `sar.Options{Checker, Kinds, TTL, Clock clock.WithTicker, Metrics *obs.Registry, IdentityFrom func(context.Context) (oidc.Identity, bool)}`; `IdentityFrom` defaults to `session.IdentityFrom`, tests inject. `sar.New(Options) *Authorizer`; `(*Authorizer).Run(ctx)` sweeps every minute.
- `AuthorizeErr`: no identity in the context → `sar.ErrNoIdentity`; unmapped kind, or a namespaced kind with an empty namespace → deny without a SAR; else `list` on the resource in the ref's namespace (`""` for cluster-scoped kinds); allowed → allow; denied → `get` on the named object; a `list` error returns the error without the `get`. `Authorize` is `AuthorizeErr` with an error reported through `api.ReportAuthzError` (Task 19) and answered as deny.
- Cache: key = identity hash × attributes. Identity hash: SHA-256 over a length-prefixed encoding of username, UID, sorted groups and sorted extra (keys and values). An entry holds the decision and `expires = start + TTL`, `start` read before the SAR is sent; a hit never moves `expires`; an expired entry is checked again, not served. Errors are never cached. A result whose `expires` is not after its completion time is discarded as an error.
- Merged checks: one in-flight SAR per (identity hash, attributes); waiters get its result, anchored at its start, so no waiter sees a decision older than the TTL allows. Identities never share a flight.
- Each cache miss calls `api.SpendCheck(r)` first (own SAR or merged wait); a hit does not.
- List permission (design §7.6, §8.1; used from 7.4): `pkg/api` (additive) `type ListAuthorizer interface { CanList(r *http.Request, gk objects.GroupKind, namespace string) (bool, error) }`, `namespace ""` meaning cluster-wide. `(*Authorizer).CanList` asks only `list`, never a named `get`, through the same identity hash, cache, TTL, merged flights and `SpendCheck`; an unmapped kind is false. `view.canList` records an error like `view.can` (Task 19).
- Bounds: 10,000 entries per identity (LRU), 2,000 identities (least recently used identity dropped); the sweep drops expired entries and empty identities.
- Metrics on the 7.1 registry: `snellius_authz_checks_total{verb,outcome}` (`allowed|denied|error`), `snellius_authz_check_seconds_total` (SAR wall time; with the count it gives mean latency, as `obs` has no histogram), `snellius_authz_cache_total{result}` (`hit|miss|merged`), gauge `snellius_authz_identities`.

- [ ] `checker_test.go` against a fake API server (`httptest` serving `/apis/authorization.k8s.io/v1/subjectaccessreviews` behind a client-go config, as `tokenreview_test.go`): `TestKubeCheckerRequest` (user, groups, extra, uid, attributes in the body); `TestKubeCheckerOutcomes` (table: allowed; denied; denied with `denied: true`; `evaluationError` with `allowed` false and true; HTTP 500; a handler slower than the timeout).
- [ ] `authorizer_test.go` (fake `Checker` counting calls, fake clock): `TestListThenGet` (list allowed → no get; list denied → get by name; a `resourceNames`-only grant shows the named object only); `TestClusterScopedKinds` (GatewayClass, Namespace ask with namespace `""`); `TestUnmappedKindDenied` (no SAR sent); `TestListErrorStopsGet`; `TestNoIdentityIsError`; `TestAuthorizeReportsError`; `TestCanListIgnoresNamedGet` (a `resourceNames` get grant: `AuthorizeErr` allows the object, `CanList` in its namespace is false); `TestCanListClusterWide`; `TestCanListSharesCache` (a `list` answered for `AuthorizeErr` is a hit for `CanList`, same expiry).
- [ ] `cache_test.go`: `TestTTLAnchoredAtStart` (a SAR taking 5 s with TTL 30 s expires 25 s after it returns); `TestNoExtensionPastExpiry` (hits every second do not move expiry; at start + TTL a new SAR goes out); `TestRevokedAfterTTL` (revoke in the fake: allowed until start + TTL, denied after); `TestIdentitiesNeverShare` (identities differing only in groups, extra or UID, and the `("a","bc")`/`("ab","c")` groups pair, each send their own SAR); `TestMergedChecks` (50 goroutines, one identity and key → one SAR; a second identity is not merged; `-race`); `TestMergedWaiterAnchor` (a waiter joining 9 s into a flight gets `expires = start + TTL`); `TestLateResultDiscarded` (a SAR returning after its expiry is an error and not cached); `TestErrorsNotCached`; `TestPerIdentityLRU` (entry 10,001 evicts the least recently used); `TestIdentityBound` (identity 2,001 evicts the least recently used identity); `TestSweep` (fake ticker; expired entries and empty identities go; gauge follows); `TestSpendOnMissOnly`; `TestMetrics` (exposition through `obs.Registry.ServeHTTP`).
- [ ] `go test ./pkg/auth/sar/... ./pkg/api/... -race`; commit.

### Task 18: Full-read fast path

**Files:** new `pkg/auth/sar/fullread.go`; `pkg/api/authz.go` (`FullReader`); tests `pkg/auth/sar/fullread_test.go`.

**Interfaces:**
- `pkg/api` (additive): `type FullReader interface { FullRead(r *http.Request) (bool, error) }`.
- `(*Authorizer).FullRead`: `list` with namespace `""` on every kind of `Kinds.All()`, at most 8 SARs at a time, each through the Task 17 cache. True only when all are allowed; it stops at the first denial. An error in any one returns the error, never false and never true (the request fails, Task 19).
- Every constituent returns its decision with its own `expires` (its SAR start + TTL, from a fresh SAR or a cache hit). The aggregate's `expires` is the minimum over the constituents asked (D-099).
- Immediately before returning, `FullRead` compares the aggregate's `expires` with the clock, for the evaluating caller and for every merged waiter alike. An expired aggregate is evaluated once more (expired constituents are now misses); if that one is expired too, `FullRead` returns `sar.ErrExpired` (the request answers 503). It never returns an expired decision.
- The result (true or false) is cached per identity hash and kinds `key` with that `expires`; a hit is checked against the clock the same way; never extended; rechecked only when the identity next calls `FullRead` (no background SARs). A changed kinds `key` (a CRD served or removed) is a miss.
- Concurrent `FullRead` calls for one identity and key share one evaluation.
- `snellius_authz_cache_total{result}` counts fast-path lookups under `fullread-hit|fullread-miss`.

- [ ] Tests (fake `Checker`, fake clock): `TestFullReadAllowed` (N SARs, then 0 within the TTL); `TestFullReadExpiresFromEarliest` (constituents start at t, t+1 s … t+3 s; the decision expires at t + TTL); `TestFullReadLazy` (advancing the clock and ticker past expiry sends nothing until the next call); `TestFullReadDeniedStopsEarly` (false, cached with the same rule); `TestFullReadErrorFails` (error, not cached; the next call asks again); `TestFullReadRevoked` (cluster `list` revoked: true until expiry, false after); `TestFullReadKindsChange`; `TestFullReadMerged` (`-race`); `TestFullReadNearExpiryHit` (a cached allow expiring at t+30 s is read at t+29 s, the next constituent's SAR completes at t+31 s: the aggregate is evaluated again, never returned expired); `TestFullReadExpiredTwice` (both evaluations expire → `ErrExpired`); `TestFullReadMergedWaiterExpiry` (a waiter released after the aggregate's expiry gets the re-evaluated decision or the error, never the expired one).
- [ ] Commit.

### Task 19: Fail closed in `pkg/api` (response-commit protocol)

**Files:** new `pkg/api/commit.go`; `pkg/api/visible.go`, `handlers.go`, `ext.go` (`Visible`), `authz.go`; `pkg/auth/session/middleware.go` (`StillSignedIn`); web `api/client.ts` (`ApiError.code`), new `api/errorState.ts(x)`, `states/PageState.tsx`, `states/PageNotice.tsx`, `explore/ListFrame.tsx`, `shell/RootRedirect.tsx`, `resource/ResourceYaml.tsx`, `pages/ResourcePage.tsx`, `pages/ClusterLanding.tsx`, `pages/install/InstallPage.tsx`, `pages/explore/detailParts.tsx`, `pages/findings/FindingPage.tsx`, `copy/`; tests new `pkg/api/commit_test.go`, new `pkg/auth/sar/handler_test.go` (package `sar_test`, the real authoriser behind a real handler), `pkg/auth/session/middleware_test.go`, web alongside.

**Interfaces:**
- `NewHandler` with `WithSecureMode(true)` and an `ErrAuthorizer` wraps every `/api/` route, extension mounts included, in the commit wrapper. Local mode and the closed gate are unchanged (no buffer, no wrapper).
- Every secure `/api/` response is buffered, full readers included. Cost: one response body held per in-flight request, bounded by today's response sizes (Task 21 reports it).
- Wrapper order: (1) if the authoriser is a `FullReader`, call it; an error is recorded and the handler is skipped; true → the view is allow-all, the precomputed projection as `AllowAll` uses (`authz.go:37`); (2) the handler writes into a buffer (status, headers, body); (3) after it returns, or after a full-read error, the commit check runs first (below), so an ended session answers 401 even when an authorisation error was recorded; (4) a recorded authorisation error, on either path, discards the buffer and answers `503 {"error":"authz-unavailable"}` with `Cache-Control: no-store`; otherwise the buffer is written.
- Commit check: `WithCommitCheck(func(w http.ResponseWriter, r *http.Request) bool)` handler option; false means the check wrote its own answer and the buffer is dropped. Task 20 wires `(*session.Store).StillSignedIn`: the request's session ended while it was in flight (idle, absolute, refresh failure, logout, eviction) → `401 session-ended` (logout: `sign-in-required`) instead of the buffered body. It reads the store only; it never refreshes or waits.
- Per-request state in the context: the uncached-check count (`SpendCheck`, limit 500, 1,000 after D-100, `api.ErrCheckLimit`), the first error, and the full-read mark. `func ReportAuthzError(r *http.Request, err error)` records an error (no-op outside the wrapper).
- `view.can`, `view.canList` (allow-all under a full read; false for an authoriser without `ListAuthorizer`), and `Visible` for extensions, use the error-returning form when present: an error is recorded and answered as deny, and later checks in the request return deny without asking. So a denial during an outage never reaches the client as a 404 (`backends.go:135`, `:143`, `details.go:143`, `:439`, `:491`, `:974`, `:978`, `findings.go:291`, `handlers.go:409`): the wrapper turns it into the 503.
- `WithAuthzAudit(func(r *http.Request, reason string))` handler option; reasons `check-failed`, `check-limit`, `full-read-failed`. Task 20 wires it to an audit `security` record `authz-unavailable`.
- Web (design §11): `ApiError` gains `code` (the body's `error`). A shared helper `apiErrorState(error)` classifies an error as `authz-unavailable`, `connecting` (other 503s), `not-found` or `failed`, and `<ApiErrorState>` renders it. Every caller that now tests `status === 503` uses it: the files listed above. `authz-unavailable` shows "Can't check your permissions right now" (wording picker; controller supplies the copy) and polling continues, so the page recovers on the next good poll. It is unreachable in production until 7.5 opens the gate.

- [ ] `commit_test.go` (fake `ErrAuthorizer` and `FullReader`, handler built with `WithSecureMode(true)`): `TestCommitOutageNeverPartial` (an error on the Nth ref of each list route: exact 503 body, nothing else written); `TestCommitOutageNever404` (each 404-on-denial route above: error → 503, deny → 404); `TestCommitCheckLimit` (500 uncached checks → 200; 501 → 503); `TestCommitFullReadSkipsChecks` (no `AuthorizeErr` calls; body byte-equal to the `AllowAll` handler's); `TestCommitFullReadError` (503; the route handler never runs); `TestCommitExtMount` (an extension calling `Visible` with an erroring authoriser, and one that writes then has `Authorize` report an error → 503, no partial body; both with `FullRead` false and true); `TestCommitCheckReplacesBody` (a commit check answering false: its answer only); `TestCommitAuditReasons`; `TestGateStillAuthoritative` (`WithSecureMode(false)` with the authoriser: `503 secure-mode-incomplete`, no checks made).
- [ ] `handler_test.go` (real `sar.Authorizer`, fake `Checker`, `IdentityFrom` injected, scale fixture): `TestTwoViewersOverSAR` (a namespace `list` grant and a `resourceNames`-only grant see different route lists, each only its own); `TestSARFailsMidRequest` (the third SAR errors → 503); `TestEvaluationErrorIs503`; `TestRevokedAfterTTLAtHandler` (fake clock: visible until start + TTL, gone after); `TestFullReaderMatchesAllowAll` (routes, gateways, backends byte-equal to `AllowAll`; zero per-object SARs).
- [ ] `middleware_test.go`: `TestSessionEndsInFlight` (also with a full-read SAR blocked across the session end: 401 `session-ended`, not 503) (barrier: N `/api/` requests pass the middleware and block in a downstream handler behind the commit wrapper; the session ends by idle expiry, refresh failure and logout in turn; the handlers are released; every request answers `401 session-ended`, or `sign-in-required` after logout, and no buffered body is written; `-race`).
- [ ] Web tests through the real pages with a fetch fake: a list page (routes through `ListFrame`) and a detail page (route detail through `detailParts`) each show the permission-check state on `503 authz-unavailable`, including after a successful poll, and recover on a later good poll; a plain 503 still shows connecting; a table test of `apiErrorState`.
- [ ] Commit.

### Task 20: Wiring, flag, chart and RBAC

**Files:** `pkg/cli/flags.go`, `pkg/app/app.go`, `pkg/app/mode.go`, new `pkg/app/kinds_sar.go`; `deploy/helm/snellius/values.yaml`, `templates/deployment.yaml`, `templates/clusterrole.yaml`, `templates/_helpers.tpl`, `templates/NOTES.txt`, `deploy/helm/snellius/README.md`, `README.md`, `docs/documentation-notes.md`; tests `pkg/cli/flags_test.go`, `pkg/app/app_test.go`, `deploy/helm/chart_test.go`.

Install surface (stated, per the team rule):
- **New flag:** `--authz-cache-ttl` (default `30s`; outside 30 s–15 min is a startup error; ignored in local mode like the other secure-mode flags) → `app.Options.AuthzCacheTTL`.
- **New value:** `auth.authz.cacheTTL` (default `""`, the binary's 30 s) → `--authz-cache-ttl`, rendered in secure mode only, read nil-safe through a `snellius.authzValue` helper like `snellius.sessionValue`, so `helm upgrade --reuse-values` without the key renders.
- **Changed RBAC:** the ClusterRole gains `create` on `authorization.k8s.io/subjectaccessreviews` in secure mode only, in the block with `tokenreviews`; its comment says these are the only write verbs.
- Nothing else changes: no new port, env or mount.

**Interfaces:**
- `app.Build` in secure mode: when the registry sets no authoriser (`noAuthorizer`), it wires `sar.New` with `sar.NewKubeChecker(c.Kube)`, the TTL, the 7.1 metrics registry and the app clock, and starts its sweeper in `Start`. A registry authoriser is kept, as `DenyAll` is today. `WithSecureMode(false)` stays.
- Extension `Deps.Authorizer` gets the same authoriser as the handler. Today `app.go:314` passes `reg.Authorizer` before the secure-mode substitution at `:343`, so extensions would see `AllowAll` in secure mode.
- `sar.Kinds` from the watched served kinds plus a fixed core table of every non-CRD kind the cache watches (Service, EndpointSlice, Pod, Deployment, Namespace, and the rest found in `pkg/cache`); the kinds `key` follows the served-kinds monitor's catalog key.
- `WithCommitCheck(sessions.StillSignedIn)` in secure mode (Task 19).
- `WithAuthzAudit` → `audit.Record{Level: security, Action: "authz-unavailable", Outcome: "failure", Reason: <reason>}` with the request's session and kube identity.
- NOTES and README: the TTL as the bound on how long a revoked permission stays visible, and a longer TTL as the operator's trade of freshness for load (D-099).

- [ ] Tests: `TestAuthzCacheTTLFlag` (default 30 s; `29s` and `16m` refused; `15m` accepted); `TestSecureModeWiresSARBehindGate` (`Build` in secure mode: the authoriser is `*sar.Authorizer`, `/api/` still answers `503 secure-mode-incomplete`); `TestSecureModeDepsShareAuthorizer`; `TestRegistryAuthorizerKept`; `TestSARKindsCoverModelRefs` (every group and kind of every ref in the Large fixture's index maps, so no kind is denied by a missing entry); `TestAuthzUnavailableAudited`; `TestAuthzMetricsOnProbe` (the series on the probe listener's `/metrics`).
- [ ] Chart tests: `TestAuthzCacheTTLValue` (secure mode renders the flag; local mode does not); `TestRendersWithoutAuthzValues` (secure set plus `auth.authz=null`); `TestReviewRulesOnlyInSecureMode` (extends `TestTokenReviewRuleOnlyInSecureMode` with `subjectaccessreviews`).
- [ ] Commit.

### Task 21: Bench: denied-list workload

**Files:** new `pkg/api/export_bench_test.go` (`//go:build bench`, package `api`: exports a Large-tier handler builder that takes handler options), new `pkg/api/sar_bench_test.go` (`//go:build bench`, package `api_test`, so it can import `pkg/auth/sar` without a cycle); `Makefile` only if the new test name does not match `make bench`'s `-run 'Budget'`.

- `TestBudgetSARDeniedList`, report-only (logs, never fails): the Large fixture behind `WithSecureMode(true)` and a real `sar.Authorizer`; a fake `Checker` that denies every `list` and allows `get` on the objects of one namespace in ten (`resourceNames` grants only). Per page (`/routes`, `/gateways`, `/backends`, `/paths`, deepest route detail, backend detail): SARs per request cold and warm (inside the TTL), whether a cold request hits the per-request cap (500, 1,000 after D-100) (503), and p50/p95 latency with fake SAR latency 0 and 2 ms.
- The same run with namespace-level `list` grants reports SARs per user per minute at a 5 s poll, against the design's typical target of 50 (§6.4).
- Memory: heap per cached decision with one identity filled to 10,000 entries, and the projection at 2,000 identities; and the largest buffered response per page (Task 19 holds one per in-flight request), reported against D-057.
- The existing bench budgets stay green: `make bench` over `pkg/api` with the export file present.

- [ ] Run; paste the numbers into the 7.3 complete record; commit.

### Permission cost (D-100)

The Task 21 bench (`reviews/sar-cost-codex.md`) showed list pages needing up to ~2,700 SARs, 503 at the cap, and cache bounds allowing ~6.5 GiB. D-100 changes the check model; Tasks 22–28 implement its items 1–7 and 9. Item 8 (namespace scope selector) is in 7.4. The gate stays closed; tests use `WithSecureMode(true)` as in Tasks 16–21.

### Task 22: Lists by `list` permission; named `get` for details and named refs

**Files:** `pkg/api/authz.go`, `visible.go`, `details.go`, `backends.go`, `handlers.go`, `findings.go`, `link.go` (named-ref call sites); `pkg/auth/sar/authorizer.go`; `pkg/cli/flags.go`, `pkg/app/app.go`; chart `values.yaml`, `templates/deployment.yaml`, `templates/_helpers.tpl`; tests `pkg/auth/sar/authorizer_test.go`, `handler_test.go`, new `pkg/api/named_test.go`, `pkg/cli/flags_test.go`, `deploy/helm/chart_test.go`.

Install surface: **new flag** `--authz-named-get-scan` (bool, default false; secure mode only) → `sar.Options.NamedGetScan`; **new value** `auth.authz.namedGetScan` (default false) → the flag, rendered only when true and in secure mode, read nil-safe through `snellius.authzValue`. Nothing else changes.

**Interfaces:**
- `pkg/api` (additive): `type NamedAuthorizer interface { AuthorizeNamedErr(r *http.Request, ref objects.Ref) (bool, error) }`.
- `sar.Authorizer.AuthorizeErr` becomes the population check: `list` on the ref's resource in its namespace (cluster-wide for cluster-scoped kinds); a denial is final unless `NamedGetScan`, which adds the named `get`. `AuthorizeNamedErr` is today's list-then-get. `Authorize` (extensions) keeps population semantics.
- `view.can` stays the population predicate, used by every list, count, facet and aggregate. New `view.canNamed(ref)` (errors recorded like `can`) for: the subject of each detail route (`backends.go:134`, `details.go` route, Gateway, ListenerSet and delegation subjects, `handlers.go:409` resource YAML, `findings.go:291`), and refs written in a readable object (a readable route's `parentRefs`, `backendRefs` and delegation targets, a ListenerSet's parent), whether or not the target exists.
- **Separate memos (plan-7.3b #1).** Population and named answers never share storage: `view` holds `seen` (population) and `seenNamed`, each with its own pooled map (`seenPool`, `namedPool`) and its own dependency memos (`deps`/`tableDeps` stay population; named lookups never write them). A named allow never answers a population question, and a population denial never answers a named one. Shared helpers that do both (`findingRow`: `l.linked` then `v.can`, `findings.go:174-176`; the linker in `link.go`) call each predicate for its own purpose.
- **Detail subject vs population (plan-7.3b #3).** Admitting a detail subject is separate from population membership. Backend detail admits its subject with `canNamed`, then passes the admitted backend to `paths.Backends` as a named subject (a new `BackendsInput.Subject` admitted without `Can`); discovered consumers, routes and other backends keep `Can = v.can` (`backends.go:36`, `pkg/paths/backends.go:141`, `:187`). The backend link (`link.go:41`) uses `canNamed` for a ref written in a readable object and `can` for population rows.

- [ ] Tests: `TestPopulationIsListOnly` (sar: list denied → no `get` sent; with `NamedGetScan` the `get` is sent); `TestNamedIsListThenGet`; `TestResourceNamesGrantDetailNotList` (handler, real authoriser: a `resourceNames`-only grant on one HTTPRoute → its detail 200, `/routes` and the counts omit it; with the flag the list includes it); `TestNamedRefInReadableRoute` (a readable route's backendRef to a Service granted only by name is shown on the route detail; an absent target the viewer may `get` is a normal finding, one it may not is withheld); `TestNamedCallSitesTable` (table of detail routes × grant: subject via named check, population via list); `TestNamedGetScanFlag` (default off; `--authz-named-get-scan` sets it); chart `TestAuthzNamedGetScanValue` (rendered only when true and secure; `auth.authz=null` renders).
- [ ] Memo separation through a real handler (list denied, get allowed on one ref): `TestNamedThenPopulation` (one request asks `canNamed` then `can` on the same ref: the population answer is deny; the finding is absent from `/findings` and its counts); `TestPopulationThenNamed` (reverse order: the named subject is still admitted); both with the pooled memos reused across requests.
- [ ] Named-only backends with `namedGetScan` off: `TestNamedOnlyServiceDetail` and `TestNamedOnlyBackendDetail` (detail 200; `/backends` omits it); `TestNamedOnlyBackendLink` (a readable route's backendRef links to the detail; the backend list has no row for it).
- [ ] Commit.

### Task 23: Asymmetric TTLs and the lazy full read

**Files:** `pkg/auth/sar/cache.go`, `fullread.go`, `authorizer.go`; `pkg/api/commit.go`, `visible.go`; `pkg/cli/flags.go`; chart `values.yaml` comment, `NOTES.txt`; tests `pkg/auth/sar/cache_test.go`, `fullread_test.go`, `handler_test.go`, `pkg/api/commit_test.go`, `pkg/cli/flags_test.go`.

Install surface: `--authz-cache-ttl` and `auth.authz.cacheTTL` keep their names and range (30 s–15 min); the default moves from 30 s to **60 s** and now covers allowed answers only. No new setting.

**Interfaces:**
- `sar.Options.TTL` → `AllowTTL` (default `sar.DefaultAllowTTL = 60 s`); `sar.DenyTTL = 5 * time.Minute` (constant). Both anchored at the SAR's start, never extended; errors never cached; the late-result rule (Task 17) uses the TTL of the answer.
- Full read: a "false" aggregate is cached with the expiry of the denying constituent (its witness); a "true" aggregate with the earliest allowing constituent's expiry. Incidental allows never shorten a "false". The Task 18 expiry check before return stays.
- Lazy: the commit wrapper no longer calls `FullRead` before the handler. The view asks it the first time a population predicate (`can`, `canList`, `recount`, `allowDeps`, `allowTable`, `visible`) runs; `canNamed` alone never triggers it. The answer is memoised in the request state; an error is recorded (503 as today).

- [ ] Tests: `TestAllowAndDenyTTL` (allow expires at start + 60 s, deny at start + 5 min); `TestMixedListGetExpiry` (list denied, named get allowed: the get's allow expires at its own 60 s while the list denial lasts 5 min; after 60 s a new `get` is sent; renewed → detail still 200; revoked → 404); `TestFullReadFalseWitness` (a denying constituent at t with allows at t+1 s: "false" expires at t + 5 min, not t + 61 s); `TestFullReadTrueEarliestAllow`; `TestErrorsNeverCached` (both verbs); `TestNamedOnlyDoesNotProbeFullRead` (handler: resource YAML, a purely named operation, sends no cluster-wide `list`; route detail may, since it counts children); `TestListProbesFullReadOnce` (handler: `/routes` asks it once, memoised); `TestAuthzCacheTTLDefault60s`.
- [ ] Commit.

### Task 24: Validity at commit

**Files:** `pkg/api/authz.go`, `commit.go`; `pkg/auth/sar/cache.go`, `fullread.go`; `pkg/app/app.go`; tests `pkg/api/commit_test.go`, `pkg/auth/sar/handler_test.go`.

**Interfaces:**
- `func NoteAllow(r *http.Request, expires time.Time)` (pkg/api): the authoriser reports the expiry of every allow it returns (fresh, cached, merged, full-read "true"); the request state keeps the earliest. Denials are not noted: a stale denial only hides.
- `WithCommitClock(func() time.Time)` handler option; `app.Build` passes the app clock.
- Build/validate loop (plan-7.3b #2), at most two attempts. Each attempt starts from a reset request state (full-read memo, earliest allow expiry, recorded error, both view memos, a new buffer) and keeps only the per-request check count. After **every** build, in order: (1) session check (ended → its 401, nothing written); (2) recorded authorisation error → 503; (3) earliest allow expiry not after now → next attempt, or after the second `503 authz-unavailable` with audit reason `expired` (new constant `AuditExpired`). Only when all three pass is the buffer written.

- [ ] Tests (fake clock, fake authoriser that steps the clock inside the handler): `TestCommitValidResponseWritten`; `TestCommitExpiredRebuilt` (an allow expires mid-build: the handler runs twice, the second body is written); `TestCommitExpiredTwice503`; `TestCommitRebuildSharesCap` (400 checks + 400 on rebuild → 503 `check-limit`); `TestCommitDenyNotTracked`; `TestLogoutDuringRebuild` (the first build expires, logout happens during the second: `401 sign-in-required`, no body); `TestExpiredFullReadRebuild` (a full-read "true" expires during the first build: the second attempt asks `FullRead` again and uses its new answer, not the memo); handler level with the real authoriser: `TestRevokedDuringSlowRequest` (an allow used at t, a SAR delayed until past t + 60 s, revocation in between: the response never shows the revoked object).
- [ ] Commit.

### Task 25: Permission-cache byte budget and bounded waiting

**Files:** `pkg/auth/sar/cache.go`, `fullread.go`, new `size.go`, `metrics.go`; `pkg/api/budget_test.go`, `pkg/engine/budget_test.go` (D-057 figure); tests `pkg/auth/sar/cache_test.go`.

**Interfaces:**
- The per-identity (10,000) and identity-count (2,000) bounds are replaced by one global budget: `sar.Options.MaxBytes` (default `8 << 20`). Accounted bytes per decision, identity and full-read entry are a measured fixed cost plus the lengths of their strings (`size.go`, one comment citing the bench). Least-recently-used eviction across identities: one LRU list over all decisions **and full-read entries**; an identity with no entries is dropped with its metadata.
- Backing storage (plan-7.3b #7): map capacity survives deletion, so the budget also covers it. Each map (per-identity decisions, identities, full-read entries) is replaced by a fresh one sized to its live entries when its live count falls below half of its high-water count; the accounting charges the high-water count until then.
- Flights: at most 4,096 in flight across identities; a new flight beyond it fails with `sar.ErrOverloaded` (503). Merging into an existing flight is not counted.
- Metrics: gauge `snellius_authz_cache_bytes`, counter `snellius_authz_cache_evictions_total`; the `snellius_authz_identities` gauge stays.
- D-057 (as amended by D-100): the retained-heap budget tests move from 48 to 64 MiB over the 2.1 baseline (`pkg/api/budget_test.go:1558-1565`, `pkg/engine/budget_test.go:567-587`), messages cite D-100.

- [ ] Tests: `TestByteBudgetEvictsGlobalLRU` (identities A and B fill the budget; touching A's entries evicts B's oldest first); `TestMultiIdentityChurn` (500 identities × 200 checks in a loop: accounted bytes never exceed the budget, hits stay correct per identity, no identity sees another's decision; `-race`); `TestDenialsEvictedEarlyAreRechecked` (an evicted 5 min denial is asked again, never served from elsewhere); `TestFlightBound` (4,097th concurrent distinct flight → `ErrOverloaded`); `TestAccountedBytesMatchHeap` (fill 10,000 decisions: accounted within 25 % of the `HeapAlloc` delta); `TestRetainedHeapAfterChurn` (retained heap after two GCs stays within the 8 MiB budget plus 25 % after each of: identity churn (5,000 identities through the cache), full-read-only churn (5,000 identities that only ask `FullRead`), and eviction back down to a few identities, which must also show the maps compacted); `TestFullReadEntriesEvicted` (full-read entries take part in the global LRU).
- [ ] The integrated D-057 measurement (Task 28) runs with the cache at its budget and 5,000 representative sessions, against 64 MiB.
- [ ] Commit.

### Task 26: Parallel resolution into a request-local table

**Files:** new `pkg/auth/sar/limit.go`, `pkg/auth/sar/batch.go`; `pkg/api/authz.go`, `visible.go`, `lists.go`, `paths.go`, `backends.go` (prefetch call sites); `pkg/cli/flags.go`, `pkg/app/app.go`; chart `values.yaml`, `templates/deployment.yaml`; tests `pkg/auth/sar/limit_test.go`, `batch_test.go`, `pkg/api/prefetch_test.go`, `handler_test.go`, flags and chart tests.

Install surface: **new flags** (secure mode only) `--authz-max-in-flight` (default 32), `--authz-rate` (SARs per second, default 100), `--authz-burst` (default 32); each must be ≥ 1. **New values** `auth.authz.maxInFlight`, `rate`, `burst` (default `""`, the binary's), nil-safe. Per-request workers stay a constant (8).

**Interfaces:**
- `pkg/api` (additive): `type ListQuery struct { Kind objects.GroupKind; Namespace string }`; `type BatchAuthorizer interface { ResolveLists(r *http.Request, qs []ListQuery) ([]bool, error) }`. The sar implementation resolves through the cache with 8 workers per request, every miss charged to `SpendCheck` and noted with `NoteAllow`.
- Global limiter in `limit.go`, standard library only (a semaphore and a token bucket; `golang.org/x/time` stays indirect): at most `maxInFlight` SARs sent at once and `rate` per second with `burst`; at most 1,024 checks waiting, beyond that `sar.ErrOverloaded`.
- **One admission boundary (plan-7.3b #6).** Every SAR actually sent (batch, serial population, named, full-read constituent) passes the limiter at one point: inside the flight's leader, just before `Checker.Check`. Order: (1) queue for a permit and a token under the leader's request context; cancelled while queued → the flight fails with the context error, nothing is sent, the queue slot is freed; (2) once admitted, the SAR runs on the detached context (as `cache.go:150` today) so merged waiters of other requests still get the answer; a cancelled request stops waiting but the running SAR completes and is cached. A merged waiter whose leader was cancelled while queued becomes the leader of a new attempt. Permits and queue slots are released on every exit (answer, error, panic, timeout) with `defer`; a token is spent only on admission.
- `view.prefetchLists(qs)` resolves into the request-local table before rendering; `canList` and the population `can` then read the table, and `view` stays single-threaded.
- **Scanning flag and the table (plan-7.3b #4).** A prefetched list denial is final only with `NamedGetScan` off. With it on, a denied bucket's members fall back to the per-ref named `get` through the same cache and limiter, serially inside the view; a list allow is final either way.
- **Roots first (plan-7.3b #5).** List handlers (`/routes`, `/gateways`, `/listenersets`, `/backends`, `/paths`) prefetch in two rounds: (1) the distinct (kind, namespace) buckets of the root rows; denied roots are pruned; (2) the buckets of dependencies required by admitted roots only. A hidden root never adds a bucket. The 7.4 namespace scope is applied before round 1. Any error in a round is recorded and the whole buffered response is discarded (503).

- [ ] Tests: `TestLimiterInFlight` (64 concurrent checks, never more than 32 sent at once); `TestLimiterRate` (fake clock: 100/s, burst 32); `TestLimiterQueueBound` (1,025th waiter → `ErrOverloaded`); `TestLimiterQueuedCancel` (a request cancelled while queued sends nothing and frees its slot); `TestLimiterOverloadRecovers` (after `ErrOverloaded`, the queue drains and new checks are admitted); `TestLimiterReleasesOnCheckerErrorAndPanic` (permits return to 32 after errors and a recovered panic); `TestMergedFlightLeaderCancelled` (queued leader cancelled: a waiter from another request leads a new attempt; running leader cancelled: the SAR completes and the waiter gets it); `TestMixedTrafficSharesLimit` (full-read, named and batch checks together never exceed 32 in flight or the rate).
- [ ] `TestBatchResolve` (replaces a worker-count test): results equal the serial answers per bucket; never more than 8 in flight per request; cancelling the request stops queued work and returns the context error; with one bucket erroring the handler answers the exact 503 body and writes nothing of the page.
- [ ] `TestParallelErrorDiscardsResponse` (handler: one of 300 prefetched checks errors → exact 503 body, nothing of the page written); `TestPrefetchMatchesSerial` (`/routes`, `/backends`, `/paths` byte-equal with and without prefetch for three grant sets, with `namedGetScan` off and on); `TestHiddenCreateDeleteUnchanged` (viewer allowed 10 namespaces: creating and deleting objects in a namespace where `list` is denied leaves `/routes`, `/gateways`, `/backends`, `/paths` byte-equal, masking only `deniedInInstall`, which 7.4 removes); `TestHiddenRootAddsNoBucket` (a hidden route whose backends sit in a new namespace: no SAR for that namespace; the same with the checker set to error on that bucket, and with the request one check from the cap: still 200 and byte-equal); `TestViewSingleGoroutine` (`-race` over the prefetch path); flag and chart tests for the three limits.
- [ ] Commit.

### Task 27: Docs for permission checks

**Files:** `README.md`, `deploy/helm/snellius/README.md`, `deploy/helm/snellius/values.yaml` comments, `templates/NOTES.txt`, `docs/documentation-notes.md`.

- The defaults are the secure choice: allowed answers 60 s, denials 5 min, lists by `list` permission.
- **Tighten:** `auth.authz.cacheTTL: 30s` (a revoked permission shows for at most 30 s, about twice the SARs); shorter `auth.session.absolute`. Say plainly that `auth.session.idle` does not end a session while a visible tab keeps polling.
- **Relax:** `cacheTTL` up to 15 min, with its cost: a revoked permission stays visible that long.
- Global limits (`maxInFlight`, `rate`, `burst`) and when to raise them (large clusters, many viewers).
- Upgrade notes: `resourceNames`-only read grants no longer make objects appear in lists; the objects still open on their detail pages; `auth.authz.namedGetScan: true` restores the old behaviour at a higher check cost.
- [ ] Commit.

### Task 28: Bench rerun against the D-100 targets

**Files:** `pkg/api/sar_bench_test.go`, `export_bench_test.go`.

- `TestBudgetSARDeniedList` rerun, report-only, with the D-100 defaults (allow 60 s, deny 5 min, list enumeration, 8 workers, global limits) for a viewer allowed 10 of the Large fixture's 100 namespaces. Targets, stated in the report next to each number:
  1. `/routes`: at most 400 SARs cold; at most 150 SARs per minute warm at a 5 s poll; every poll 200 without contention.
  2. `/gateways`, `/listenersets`, `/paths`: at most 400 SARs cold each, `/backends` at most 550, and every poll 200 without contention (D-100 item 7, amended).
  3. Details: cold and warm SARs reported.
- Also with `--authz-named-get-scan` on: SARs and whether `/routes` hits the cap (expected; reported).
- Latency p50/p95 at fake SAR latency 0 and 2 ms, cold and warm.
- Load: 50 viewers polling at once: peak SARs in flight (≤ 32) and per second (≤ 100 plus burst), and the share of polls answered 200.
- Memory: accounted cache bytes at the 8 MiB budget against the measured heap after identity churn, full-read-only churn and eviction; and the retained-heap D-057 figure (≤ 64 MiB over the 2.1 baseline) with the cache full and 5,000 representative sessions.
- [ ] Run; paste the numbers into the 7.3 complete record; a missed target goes to the user before 7.3 closes.

## Sub-phase 7.4 — Visibility everywhere (data still gated)

1. `pkg/health`: `Finding.Basis` and check-result basis; every analyzer declares it (inputs, absence checks, populations); a table test fails for any analyzer without a declaration.
2. `pkg/api` endpoints per design §7.2: remove restricted placeholders, `DeniedInInstall`, `hidden` flags in secure mode; counts over visible objects; ancestors and per-listener counts visible only; findings by basis; install context per §7.4; `/meta` per viewer; extension `Deps` viewer helper.
3. EndpointSlices and Pods join the backend dependency set.
4. Permissions note per viewer (design §7.5) from the full-read check.
5. Path incompleteness from readable refs and permissions (design §8.1), shared with 7.5.
6. The D-085 rule test per surface: two viewers, fixed clock, named masks, the change list of design §13.
7. Namespace scope selector (D-100 item 8): a persistent selector in the web and an API parameter that limit rows, counts and facets to the chosen namespaces, so population checks run only there; the scope is applied before the Task 26 prefetch rounds. Cross-namespace dependencies keep their own checks; restricted ones stay unknown or incomplete.
8. Secure mode drops the exact hidden counts `deniedInInstall` (D-085): creating a hidden object changes them today.
9. `visibleHealth` counts only findings the viewer may see; hidden findings no longer reach health aggregates.
10. EndpointSlices join the full-read set (`pkg/app/kinds_sar.go`, `fullRead: true`) once they are an authorised population (item 3); test: a full reader denied EndpointSlices takes the per-object path.

## Sub-phase 7.5 — Two signals (the gate opens)

1. `pkg/explore`: `config` and `path` per route row; `path.incomplete`; legacy `health` per the compatibility matrix (design §8.3); `path-incomplete` reason kind.
2. API: route list, route detail, Gateway and ListenerSet route tables; state filters for both signals; golden-v1 stays byte-equal for full-read viewers on existing fields.
3. Web: Configuration and Traffic path columns, route header badges, problem strip, map route nodes via the path signal; wording picker.
4. Open the gate: `WithSecureMode(complete: true)`, with the 7.4 rule tests extended to the new fields.

## Sub-phase 7.6 — Audit, docs, release

1. Audit levels: reads, summaries (5 min windows, 10,000 keys, flush on shutdown), disclosure (opt-in), requests (opt-in), denial collapsing.
2. Docs: README trust model and secure-mode setup (both audience options, `kubectl auth whoami`), chart README, upgrade notes, `docs/documentation-notes.md`; snellius.dev (P-006).
3. Phase review (Codex), stabilization if needed.
4. `v0.8.0` release; the notes say that clusters whose API server cannot accept Snellius's token stay on local mode (D-098).

Sub-phase 7.7 (claim mapping) is dropped: D-098 settled that `v0.8.0` ships without it.
