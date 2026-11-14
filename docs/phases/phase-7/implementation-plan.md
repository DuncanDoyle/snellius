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

Tasks (briefed at 7.2 start):
1. `pkg/auth/oidc`: discovery with issuer match and HTTPS endpoints; login transaction store (one-use, 10 min); PKCE S256; callback validation order (design §4.1); TokenReview with `audiences`; tests with an `httptest` issuer and a fake TokenReview server.
2. `pkg/auth/session`: in-memory store with idle/absolute expiry, global bound 5,000, sweeper; refresh policy (design §4.4) with single-flight and logout-wins; cookie attributes; `POST /auth/logout` with `Origin` check; RP-initiated logout.
3. Middleware: secure mode requires a session for `/api/`; `401 {"login": …}`; audit security records (login, logout, failure, expiry); `audit.RequestIDTrusting` with `--trusted-proxy-cidrs`.
4. Configuration and readiness: OIDC flags validated at startup; discovery retried; a "sign-in configured" state feeds readiness and the UI's "Sign-in is not configured" page.
5. Chart: `auth.oidc.*`, client secret file mount, `auth.session.*`, `auth.trustedProxyCIDRs`; secure mode fails rendering without `issuerURL`, `clientID` or `redirectURL`; ClusterRole gains `create tokenreviews` in secure mode.
5. Web: 401 redirect, `retryEveryPoll` stops on 401, polling pauses on hidden tabs, sign-in states, user menu with Who am I and Sign out.
6. `test/oidc-issuer`: test-only issuer (discovery, JWKS, authorize with a user picker, token with refresh), standard library crypto; kind API server structured authentication configuration; e2e sign-in with two users.

## Sub-phase 7.3 — Authoriser (data still gated)

1. `pkg/auth/sar`: `SARAuthorizer` implementing `api.Authorizer` and `api.ErrAuthorizer`; list-then-get; cluster-scoped kinds; kinds registry mapping; outcome table (design §6.3); per-identity LRU (10,000), 30 s TTL never extended; merged concurrent checks; global identity bound 2,000.
2. Full-read fast path: at sign-in and refresh, `list` on every served kind cluster-wide → allow-all projection.
3. `pkg/api`: `ErrAuthorizer` in `view`; buffered response wrapper; per-request uncached-SAR cap 500 → `503 authz-unavailable`; 404 paths through the wrapper.
4. ClusterRole gains `create subjectaccessreviews` in secure mode.
5. Bench: denied-list workload on the large fixture, report-only.

## Sub-phase 7.4 — Visibility everywhere (data still gated)

1. `pkg/health`: `Finding.Basis` and check-result basis; every analyzer declares it (inputs, absence checks, populations); a table test fails for any analyzer without a declaration.
2. `pkg/api` endpoints per design §7.2: remove restricted placeholders, `DeniedInInstall`, `hidden` flags in secure mode; counts over visible objects; ancestors and per-listener counts visible only; findings by basis; install context per §7.4; `/meta` per viewer; extension `Deps` viewer helper.
3. EndpointSlices and Pods join the backend dependency set.
4. Permissions note per viewer (design §7.5) from the full-read check.
5. Path incompleteness from readable refs and permissions (design §8.1), shared with 7.5.
6. The D-085 rule test per surface: two viewers, fixed clock, named masks, the change list of design §13.

## Sub-phase 7.5 — Two signals (the gate opens)

1. `pkg/explore`: `config` and `path` per route row; `path.incomplete`; legacy `health` per the compatibility matrix (design §8.3); `path-incomplete` reason kind.
2. API: route list, route detail, Gateway and ListenerSet route tables; state filters for both signals; golden-v1 stays byte-equal for full-read viewers on existing fields.
3. Web: Configuration and Traffic path columns, route header badges, problem strip, map route nodes via the path signal; wording picker.
4. Open the gate: `WithSecureMode(complete: true)`, with the 7.4 rule tests extended to the new fields.

## Sub-phase 7.6 — Audit, docs, release

1. Audit levels: reads, summaries (5 min windows, 10,000 keys, flush on shutdown), disclosure (opt-in), requests (opt-in), denial collapsing.
2. Docs: README trust model and secure-mode setup (both audience options, `kubectl auth whoami`), chart README, upgrade notes, `docs/documentation-notes.md`; snellius.dev (P-006).
3. Phase review (Codex), stabilization if needed.
4. `v0.8.0` only after the user has decided the claim-mapping scope (design-codex #11, plan-codex #8): either 7.7 ships first, or the release notes state that clusters whose API server cannot accept Snellius's audience stay on local mode.

## Sub-phase 7.7 — Claim-mapping fallback (only if the user approves the JWT dependency and wants it in `v0.8.0`)
