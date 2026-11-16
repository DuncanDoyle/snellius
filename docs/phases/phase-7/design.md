# Phase 7 — Identity & Access: Design

**Status:** Revised after the Codex design review (`reviews/design-codex.md`, 14 findings, all accepted; #11 is a user decision). Shaped with the user on 2026-11-14 (brainstorm, recorded in D-084 … D-089). Calls marked (our call) are open to change.
**Date:** 2026-11-14
**Base:** `81a2c0b8` (main after #44; the design adds no code)
**Roadmap:** `docs/roadmap.md`, Phase 7, principles 9 and 10
**Decisions:** D-006, D-013, D-016, D-077, D-082, D-083 and D-084 … D-089 in `docs/decisions.md`. This design proposes D-090 … D-096 (§14).
**Code refs:** `file:line` at `81a2c0b8`, from the Phase 7 authorisation survey.
**Ships as:** `v0.8.0` (user, 2026-11-14).

**One home per contract.** Modes: §3. Identity: §4. Sessions: §5. Authorisation: §6. Visibility: §7. Two signals: §8. Audit: §9. Agents and kgwctl: §10. Web: §11. Chart and RBAC: §12. Testing: §13.

## Decisions for this phase

| # | Decision | Marker | Where |
|---|---|---|---|
| 1 | Visibility follows Kubernetes RBAC exactly; a change to an object the viewer cannot read never changes what they see | (user, D-085) | §7 |
| 2 | Local mode by default (loopback only, banner); secure mode for production, required for any exposure | (user, D-086) | §3 |
| 3 | OIDC login through the issuer the cluster trusts; identity from TokenReview | (user) | §4 |
| 4 | The claim-mapping fallback needs a JWT library, a new dependency. Whether it ships in `v0.8.0` changes which clusters can use secure mode: **queued for the user** (Codex #11) | (user decision pending) | §4.3 |
| 5 | SubjectAccessReview checks: lists by `list` permission, named `get` for details and named refs; allows cached 60 s, denials 4 to 5 min, never extended; an unavailable check fails closed | (Codex; user, D-099, D-100) | §6 |
| 6 | Two signals per route: route configuration and traffic path | (user, D-087) | §8 |
| 7 | The legacy `health` field keeps its meaning (D-082 lift); restricted viewers get at least unknown over an incomplete path | (our call; Codex #10) | §8.3 |
| 8 | Every finding and check result carries a basis; a restricted viewer sees it only when its object and whole basis are readable | (our call; Codex #1, #2) | §7.3, §7.6 |
| 9 | No permission beyond object RBAC: an install is reachable through any readable object; its metadata shows only what the viewer can read | (our call; Codex #8) | §7.4 |
| 10 | One permissions note per viewer, independent of hidden objects | (our call, from D-085) | §7.5 |
| 11 | Sessions held in memory, one replica in secure mode; a restart signs everyone out | (our call) | §5 |
| 12 | Audit levels and defaults as agreed; JSON lines on stdout with a fixed marker; OTLP export later | (user; OTLP our call) | §9 |
| 13 | A probe listener on the pod IP with `/healthz`, `/readyz` and `/metrics`, no cluster data | (our call; Codex condition) | §3.3 |
| 14 | The e2e OIDC issuer is a small test-only Go program in `test/`, not a third-party image | (our call; no new dependency) | §13 |
| 15 | OpenShift's built-in OAuth, remote MCP, multi-cluster and a trusted-proxy mode are out of scope | (user) | §2 |

## 1. Intent and why

Snellius reads cluster state with its own ServiceAccount. Today whoever reaches it sees all of that (D-013). For a release the user wants to promote, the question to answer is: "can I give my teams this UI without giving them, or a compromised session, more than Kubernetes already lets them read?" In the AI era a read-only view is enough to reason over and infer from, so least privilege is the core of this phase (principle 9).

The people:
- **Platform operator** installs Snellius, wants a five-minute demo, then a production setup with SSO that their security review accepts.
- **App-team member** signs in with SSO and sees their own routes and their health, nothing more.
- **Security reviewer** asks what an attacker with a stolen session or an agent learns, and who looked at what.

## 2. Goals, non-goals and anti-goals

**Goals**
1. Local and secure modes, with the guardrails of D-086 (§3).
2. OIDC login with identity from TokenReview (§4) and sessions (§5).
3. A SubjectAccessReview `Authorizer` that fails closed (§6).
4. The D-085 rule on every endpoint and computed view, including the pages from before Phase 6 (§7).
5. Two signals per route in the API and the UI (§8).
6. An audit stream with the agreed levels (§9).
7. The visibility and audit contract every MCP form follows (§10).

**Non-goals**
- OpenShift's built-in OAuth (after Phase 7, once a test cluster exists).
- Remote MCP (after Phase 7, D-089) and local MCP (kgwctl, edge E10).
- Multi-cluster, a trusted-proxy identity mode, app-team views beyond RBAC (Phase 13).
- Reading Secrets. Phase 10's certificate inventory needs secure mode; it is not built here.

**Anti-goals**
- A permission model of Snellius's own, or summaries of objects a viewer cannot read (D-085).
- Any open fallback in secure mode: missing config or an unavailable check never shows data.
- Forwarding a user's token to the API server for reads (passthrough). Snellius reads with its ServiceAccount and filters.
- A green signal while a contradicting signal is active for the viewer (D-006).

## 3. Modes

`--auth-mode=local|secure`, chart value `auth.mode`, default `local`.

### 3.1 Local mode (D-086)

- The API and UI listener binds to `127.0.0.1:8080`. The binary enforces it: in local mode any `--listen` whose host is not a loopback address is a startup error, whatever `extraArgs` says.
- Loopback is not a browser boundary: a hostile site can rebind a hostname to 127.0.0.1 and read a port-forwarded API (Codex #4). Local mode serves only requests whose `Host` is `localhost`, `127.0.0.1` or `[::1]` (any port), plus names in `--allowed-hosts`; others get `421`. Cross-origin requests are not served: no CORS headers, and a request with an `Origin` that is not the request's own host is refused.
- The ClusterIP Service keeps its port, because `kubectl port-forward svc/…` resolves through it (Codex #9). Service traffic still reaches nothing: the pod IP has only the probe listener (§3.3).
- It refuses `hostNetwork`, where loopback is the node's: the chart refuses `hostNetwork: true` in local mode, and the binary refuses to start when `POD_IP` equals `HOST_IP`, both from the downward API (our call).
- The chart adds `sidecar.istio.io/inject: "false"` and the ambient opt-out label in local mode, and documents that a mesh sidecar shares the pod's loopback (our call).
- Visibility is D-013's: everything Snellius reads. A banner on every page: "Local mode: anyone who can port-forward to Snellius sees everything it reads." The Helm NOTES and a startup warning log say the same, with the bind address.
- The chart refuses `httpRoute.enabled`, an ingress, a LoadBalancer or NodePort Service type, or `hostNetwork` in local mode (`fail` in the template).

### 3.2 Secure mode

- The API and UI listener binds to `:8080` on the pod IP, behind the Service.
- Every API request needs a session (§5) or, for MCP later, an OAuth access token (§10). Without one: `401` with a login URL.
- Missing or invalid OIDC configuration: Snellius starts, serves the probe listener, reports not ready, and the UI shows "Sign-in is not configured" with the missing values. No data.
- **Fail-closed invariant (Codex #12).** Secure mode serves data only when every security component is in place: identity, sessions, the SAR authoriser and the secure-mode projection of every endpoint. Until the sub-phase that completes them (7.5, after the restricted-viewer summary of §8.3), a build started in secure mode answers every data request, extension routes included, with `503 secure-mode-incomplete`. A nil `Authorizer` never becomes `AllowAll` in secure mode (today it does, `handlers.go:43`).

### 3.3 Probe listener (our call)

A second listener on the pod IP, `:8081`, in both modes: `/healthz`, `/readyz` and `/metrics`. No cluster data: `/readyz` drops the rejected install IDs it returns today (`handlers.go:158-166`) and returns only ready or not. `/metrics` is a hand-written Prometheus text exposition (no new dependency) with a few low-cardinality series: `snellius_auth_mode{mode}`, audit events written and dropped, authorisation checks by outcome, sessions active. The chart moves the probes to it.

## 4. Identity

### 4.1 Login flow

1. An unauthenticated request gets `401 {"login":"/auth/login?return=…"}`; the SPA redirects.
2. `/auth/login` starts the OIDC authorization code flow with PKCE and a `state` and `nonce` held in a short-lived, HttpOnly cookie. Scopes: `openid` plus configured extras (default `profile email groups offline_access`).
3. `/auth/callback` validates, in order (OIDC Core §3.1.3.7; Codex #5):
   1. The login transaction: a one-use record, server-side, keyed by a random ID in an HttpOnly, `SameSite=Lax`, short-lived (10 min) cookie. It holds `state`, `nonce`, the PKCE S256 verifier and the return path. A missing, expired or reused transaction ends the login.
   2. `state` equals the transaction's; the transaction is deleted.
   3. The code is exchanged at the discovered token endpoint over HTTPS with the verifier (`golang.org/x/oauth2`, already in the module graph).
   4. The ID token's claims, decoded without trusting them yet: `iss` equals the configured issuer, `aud` contains the configured client ID, `azp` equals it when present with several audiences, `exp` and `iat` are current (2 minutes skew), `nonce` equals the transaction's.
   5. TokenReview (§4.2) is the signature and Kubernetes-identity authority: Snellius trusts the claims only after the API server accepts the token. The TokenReview sets no `audiences` (D-097): the API server checks a JWT against its own configured audiences. Snellius binds the token to its client with the `aud` and `azp` check in step 4, which it trusts once TokenReview has verified the signature.
   6. The return path must be a same-origin relative path; otherwise `/`.
4. Discovery is read from `{issuer}/.well-known/openid-configuration` at startup and retried; its `issuer` must equal the configured issuer, and every endpoint must be HTTPS. The issuer URL, client ID and redirect URL are configuration. A client secret, when the client is confidential, is read from a file (a mounted Secret); Snellius still never reads Secrets through the API (principle 8).

### 4.2 Identity from TokenReview (default)

Snellius sends the ID token in a `TokenReview` to the API server. The API server validates signature, issuer, audience and expiry with its own authenticator, and returns the exact username, groups and extra that its RoleBindings use. Snellius never builds a Kubernetes identity itself in this mode, so it cannot drift from the cluster.

Requirement: the cluster must accept the ID token's audience. Either Snellius uses the same OIDC client ID as the cluster, or the cluster's structured authentication configuration lists Snellius's client ID as an extra audience (GA in Kubernetes 1.34). The docs show both, and how to check with `kubectl auth whoami`.

A TokenReview that returns `authenticated: false` ends the login with "Your sign-in is not accepted by this cluster", naming the likely cause: the cluster does not accept the client ID as an audience.

### 4.3 Claim-mapping fallback

For clusters where neither option is possible (some managed clusters), Snellius would verify the ID token itself and apply the cluster's claim and prefix rules from its own configuration. That needs a JWT verification library, a new dependency (Snellius has none today). Not in `v0.8.0` (D-098): such clusters stay on local mode, and #51 explores sending the token they already accept to TokenReview instead.

The brainstorm said this fallback is "checked at startup". There is no API to read the API server's authentication configuration, so the check becomes a "Who am I" panel (username, groups) and documented `kubectl auth whoami` comparison (our call).

### 4.4 Identity refresh

Groups change. Session validity policy (Codex #6):
- An identity is fresh until the earlier of its ID token's `exp` and 15 minutes after its last TokenReview. A request with a stale identity is not served from it.
- With a refresh token (server-side only), Snellius refreshes ahead of staleness, one refresh per session at a time; the new ID token must carry the same `iss` and `sub`, then passes a new TokenReview, which replaces groups. Any failure ends the session.
- Without a refresh token, the session ends when the identity goes stale.
- Logout wins over an in-flight refresh: a refresh that completes after logout is discarded.
- Refreshed groups are only as fresh as the IdP's token; the docs say so.

## 5. Sessions

- A random 256-bit session ID in a cookie `snellius_session`: `HttpOnly`, `Secure` (configurable off only for plain-HTTP port-forward testing), `SameSite=Lax`, `Path=/`.
- The server holds the session: identity, tokens, expiry, audit session ID. In memory, so the chart pins `replicas: 1` in secure mode, and a restart signs everyone out; documented (our call). A shared store is future work if HA is needed.
- Idle expiry 30 minutes, absolute 8 hours; both configurable. A UI poll counts as activity only when the tab is visible (§11).
- Logout (`POST /auth/logout`) deletes the server session; RP-initiated logout at the IdP when its discovery document offers `end_session_endpoint`.
- CSRF: the API is read-only `GET`. The only state-changing routes are login, callback and logout; logout requires `POST` with an `Origin` check (our call).

## 6. Authorisation

### 6.1 The check

A `SARAuthorizer` implements `api.Authorizer`. It asks the API server a `SubjectAccessReview` with the session's user, groups, extra and UID. Lists and named refs are checked differently (D-100):
- **Populations** (list rows, counts, facets, aggregates): an object is included only when the viewer may `list` its resource in its namespace, or cluster-wide for a cluster-scoped kind (GatewayClass, Namespace). Hidden objects are never scanned one by one, so the cost follows what the viewer can see.
- **Named refs** (a detail page's subject, and an object named in a readable object, including a target that does not exist): `list` first, then `get` on the named object, which honours `resourceNames` grants.
- `--authz-named-get-scan` (chart `auth.authz.namedGetScan`, default off) brings back the named `get` for population members, for clusters that rely on `resourceNames` read grants. The per-request cap still applies.
- The ref's group and kind map to a resource through the kinds registry. A kind without a mapping is denied.
- A named check shares the request's population memo only when the authoriser has no separate named check (`canNamed`, `visible.go`). Production always separates them, so a named answer never enters the population memo.
- Known under-show: a Service readable only by name opens its detail page but shows no exposure paths, because path rows check their dependencies as populations.

### 6.2 Cache

Per identity (hash of username, groups, extra, UID): results keyed by verb, group, resource, namespace and name. Every answer is anchored at the SAR's start and never extended; an expired entry is checked again, not served (Codex). TTLs differ by answer (D-100):
- **Allowed:** 60 s by default (chart `auth.authz.cacheTTL`, `--authz-cache-ttl`, range 30 s to 15 min). This bounds how long a revoked permission stays visible.
- **Denied:** 4 to 5 min, picked at random per answer so refreshes spread. A stale denial only hides; a new grant shows within 5 min.
- **Errors:** never cached.
- **Full read:** a cached "false" keeps the expiry of a denying check; a cached "true" expires at its earliest allowing check.

No RBAC watch invalidates the cache (D-099): a watch event says RBAC changed, not that every API server has applied it. The cache is bounded by bytes across all identities (§6.4).

### 6.3 Failing closed

`Authorizer.Authorize` returns only a bool today. A new optional interface (additive to the Go API):

```go
// ErrAuthorizer can say that it could not decide. The API then answers 503
// "authorisation unavailable" instead of an empty or partial view.
type ErrAuthorizer interface {
	AuthorizeErr(r *http.Request, ref objects.Ref) (bool, error)
}
```

`view` (`pkg/api/visible.go:32`) uses it when present. One error aborts the request: no page shows a subset because some checks failed.

Response-commit protocol (Codex #7): in secure mode the identity's decisions for a request are resolved before the handler writes anything. The handler builds its response into a buffer; a wrapper writes it only when no authorisation error was recorded, else `503 authz-unavailable`. Handlers that answer 404 on denial (`backends.go:134` and others) go through the same wrapper, so a denial during an outage never becomes a 404.

Validity at commit (D-100): a response carries the earliest expiry of the allows it used. If that time has passed when the response would be written, it is rebuilt once with fresh checks; if it expires again, the answer is `503 authz-unavailable`.

Identity at commit: an ended session answers 401, then a recorded authorisation error answers 503. Only after that is the identity checked. An identity past its freshness answers 503. If a refresh replaced the identity during the request, the response is rebuilt once with the new identity, within the same rebuild budget. Each 503 writes a `security` audit record `authz-unavailable` with its reason: `check-failed`, `check-limit`, `full-read-failed`, `expired` or `identity-stale`.

SAR outcomes: `allowed: true` is allow; `allowed: false` (with or without `denied`) is deny; an HTTP error, a timeout or a non-empty `evaluationError` is an error, never a deny. A `list` check that errors is not followed by a `get` check: the request fails.

### 6.4 Cost

A page polls every 5 seconds and checks many refs. The 7.3 bench showed that scanning hidden objects with named `get`s cost thousands of SARs per page; D-100 replaced it with the population rule of §6.1.

Budgets and bounds (Codex #14, D-100):
- **Full-read fast path, lazy.** Only a request whose handler needs a population asks whether the identity may `list` every served kind cluster-wide. The decision is cached with the expiries of §6.2 and rechecked only when the identity next needs it (D-099). If so, the request uses the precomputed allow-all projection, as `AllowAll` does (`authz.go:37`), and no per-object SARs run. Most platform operators take this path. With no served kinds the full read is false (fails closed). An uncached full read spends one check per served kind against the per-request cap.
- **Parallel resolution:** a request resolves its checks into a request-local table with 8 workers, then renders single-threaded (`view` is never shared between goroutines). Globally at most 32 SARs in flight and 100 SARs/s (burst 500), configurable; waiting is bounded too.
- **Targets** (Large fixture, a viewer allowed 10 of 100 namespaces): `/routes` at most 400 SARs cold and 150 per minute warm, every poll answered 200 without contention; every other list page at most 400 SARs cold, `/backends` at most 550 (D-100 item 7, amended).
- **Hard limits:** per request, at most 1,000 distinct uncached SARs (D-100 item 6); beyond it `503 authz-unavailable`, never a partial view. Identical concurrent checks are merged. At most 1,000 sessions by default (`auth.session.max`, D-101).
- **Memory:** the permission cache (decisions, identity data, full-read entries) has an 8 MiB byte budget with least-recently-used eviction across identities, inside D-057's retained-heap budget, raised to 64 MiB over the 2.1 baseline. In-flight checks and queues are bounded separately.
- **Bench:** the denied-list workload on the large fixture, rerun against these targets.

## 7. Visibility everywhere

### 7.1 The rule

D-085: with the viewer's readable objects and permissions unchanged, a change to an object they cannot read never changes what they see. Every response is built only from objects the viewer can read. Placeholders for hidden objects (restricted rows, restricted findings, `{restricted:true}` entries) are removed in secure mode; hidden objects are simply absent.

### 7.2 Endpoint contract

| Surface | Today | In secure mode |
|---|---|---|
| `/installs` | health and `GroupRows` over all findings (`handlers.go:267-270`) | installs per §7.4; health and counts over visible findings |
| `/overview` | tiles visible; `Unattached`, controller readiness, scope unfiltered | all from visible objects; controller readiness only if the controller Deployment is readable |
| `/health`, `/diagnostics` | restricted findings with identity; verdict and checks over all | visible findings only (§7.3); checks as §7.6 |
| `/findings`, groups, detail | restricted rows listed, counted, searchable | visible findings only; search over them |
| `/processes` | `DeniedInInstall`; controller counts from a hidden Deployment | visible objects only; no hidden counts |
| `/routes`, `/gateways`, `/listenersets` | `DeniedInInstall`; member counts include hidden routes; flags from hidden objects | visible rows; counts of visible members; two signals (§8) |
| Gateway, ListenerSet, route detail | per-listener counts include hidden routes (`explain.go:198`); ancestors show hidden routes (`details.go:752`) | visible only; an ancestor the viewer cannot read is omitted |
| `/paths`, `/backends`, backend detail | D-077 row dependency sets | unchanged, plus EndpointSlices and Pods (§7.6) and no `hidden` flag (§7.5) |
| YAML | denied and absent give the same 404 | unchanged; admission ignores findings decided by hidden objects |
| `/meta` | capabilities of the ServiceAccount | unchanged for local mode; in secure mode, capabilities that describe cluster objects are reported per viewer or dropped (plan decides per capability) |
| `/api/ext/…` | up to each extension | the extension `Deps` gain the viewer and a helper that enforces the rule; the docs make it a requirement |

The list endpoints (`/routes`, `/gateways`, `/listenersets`, `/paths`, `/backends`, `/findings`, `/processes`) take a namespace scope, `namespaces=A,B`, in both modes (our call, D-100 item 8). Rows, counts and facets come only from those namespaces, and no root bucket outside them is checked. A new `/installs/{id}/namespaces` lists the namespaces of rows the viewer can see, under the same rule.

### 7.3 Findings and their basis (our call; Codex #2)

`Finding.Evidence` lists the objects a finding quotes, not everything that decided it. Phase 7 adds a separate **basis** to every finding: the dependency set that decided its existence and severity, covering positive inputs, absence checks (the Service a KGWUI-013 looked for) and aggregate populations (the stuck objects behind a freshness finding, `freshness.go:183-207`). Every analyzer declares its basis; a finding whose analyzer declares none has unknown basis.

A restricted viewer sees a finding only when they can read its resource and its whole basis. Otherwise it is left out and does not raise any state. Unknown basis counts as unreadable. Full-read viewers (§6.4) see every finding.

Amended by D-106: only own-object findings, from analyzers behind a typed boundary (the resource, the objects it names, the install context), are shown per finding; every other finding needs the full read.

### 7.4 Installs (our call; Codex #8)

No permission beyond the viewer's object RBAC is required. An install is reachable when the viewer can read any object Snellius attributes to it. Install metadata shows only what the viewer can read: its GatewayClasses' names when they can read them, controller readiness and version when they can read the controller Deployment, scope details when they can read Namespaces. Otherwise the install shows a neutral name and "Details not visible to you".

Amended by D-102 and D-103: the install ID keeps the controller's namespace and name, and every signed-in viewer sees every install (no reachability check). An install with nothing the viewer can read shows empty pages, never a 404.

### 7.5 The permissions note

Per viewer, not per hidden object: in secure mode, a viewer who cannot `list` every served kind in every namespace sees one note in the header area: "You see what your Kubernetes permissions allow." It is decided by SARs about the viewer's own rights, never by the presence of hidden objects. The per-page "some paths are hidden" summaries (D-077, `PathSummary.tsx:98`, `BackendsPage.tsx:151`, `ExposureSection.tsx:86`, `ObjectTable.tsx:274`) and the `hidden`/`deniedInInstall` fields go away in secure mode.

### 7.6 Derived inputs

- EndpointSlices and Pods behind a Service's endpoint state join the dependency set: endpoint counts and KGWUI-023 need `list` on EndpointSlices in the Service's namespace.
- Checks mix two things (Codex #1). Collection and access failures (a watch denied, the cache not synced) are Snellius's own state and stay visible, as code and state. Object-derived results (grace holds, pending counts and deadlines, `checks.go:549`, `finding.go:139`) carry a basis like findings (§7.3) and follow the same rule; a restricted viewer sees a hold only when they can read its whole basis.
- Install-wide unknown (`requiredNotOK`, `explore.go:481`) is raised for a restricted viewer only by collection and access failures, never by an object-derived pending result they cannot fully read.
- Amended by D-106: a check result other than a collection failure is shown only to a full-read viewer, and is "Not checked" for everyone else; KGWUI-023 is derived, while endpoint counts keep the EndpointSlice rule above.

## 8. Two signals per route (D-087)

### 8.1 Definitions

- **Route configuration**: the route's own status conditions and the findings on the route whose evidence is readable (§7.3). Never raised by another object.
- **Traffic path**: the worst state over the route's path rows (Gateway, listener, route chain, backends, endpoints). `gateway-rejecting` and `backend-problem` move here. When the path is incomplete for the viewer: `unknown`, with "You don't have permission to check the whole path." When a readable object shows a fault: that state, with "Assessment incomplete with your current permissions." when the path is also incomplete.

**Incompleteness is decided without looking at hidden objects (Codex #3).** It comes only from what the viewer can read and their permissions:
- each parentRef and backendRef written in the route (and in readable routes of its chain): incomplete when the viewer may not `get` the named object, whether or not it exists;
- each kind the path needs to evaluate (Gateways in a parentRef's namespace, EndpointSlices in a backend's namespace, HTTPRoutes in a delegation target namespace): incomplete when the viewer may not `list` it there;
- never from the presence, absence or findings of a hidden object, and never from hidden path rows.

Cases: a readable ref to an object that does not exist, with permission to read it, is a normal finding (for example KGWUI-013). An unreadable ref is incomplete, whether the object exists or not. A route with no readable path rows and no unreadable refs has its ordinary path state (for example "not attached"). The test matrix covers absent, unreadable, unattached and zero-row routes.

### 8.2 API (additive)

Route rows and route detail gain `config` and `path`, each `{state, reason}`; `path` also has `incomplete: bool`. The `state=` filter gains `config-state` and `path-state` (names left to the plan). Gateway and ListenerSet route tables carry both.

### 8.3 Legacy `health` (our call)

`health` stays with today's meaning (Codex #10): the route's configuration state, lifted to at least warning by a path problem (`lifted()`, `rows.go:142`), exactly as D-082 defines it. The one addition is for restricted viewers: when the path is incomplete for them, `health` is at least `unknown`, so it is never green over a path they cannot assess. For a full-read viewer `health`, `reason`, flags, sort order and every existing `state=` value are unchanged.

| Viewer | config | path | `health` |
|---|---|---|---|
| any | healthy | healthy | healthy |
| any | healthy | warning or error | warning (today's lift) |
| any | error | any | error |
| restricted | healthy | unknown, incomplete | unknown |
| restricted | warning | unknown, incomplete | warning |

`reason` keeps today's kinds; `backend-problem` and `gateway-rejecting` are only given to a viewer who can read the backend or Gateway (else the reason is `path-incomplete`, a new kind). kgwctl and other API users keep working; the API docs mark `health` as the summary.

### 8.4 UI

- Route list: a "Configuration" column and a "Traffic path" column, as the Gateway table has its state and Data plane columns.
- Route detail header: both badges; the problem strip names the deciding signal.
- Map: route nodes show the summary (§8.3); the D-083 rule becomes "the path signal", so the map and the route page agree for every viewer.
- Wording through the wording picker per UI string.

## 9. Audit

### 9.1 Stream

JSON lines on stdout, each with `"log":"snellius.audit"`, written by a dedicated `slog` JSON handler; operational logs stay on the default handler. OTLP export is later (our call). Snellius does not store or rotate audit records; retention and tamper evidence belong to the collector or SIEM (documented).

### 9.2 Levels

| Level | Default | Records |
|---|---|---|
| security | on | login, logout, failed login, session expiry, denial (a refused request, such as a detail page the viewer cannot open), authorisation unavailable, mode at startup |
| agent | on | each MCP tool call (contract for later; nothing emits it in Phase 7) |
| reads | on | each detail, YAML or export read |
| summaries | on | list and overview access per session × endpoint × filter set per 5 minutes, with a count |
| disclosure | off | the object references a response returned |
| requests | off | every API request |

Rows left out of a list are filtering, not denials. Repeated denials of the same request within a window collapse into one record with a count.

### 9.3 Fields

`schema` (version), `id`, `time` (UTC), `level`, `action`, `outcome` and `reason`; `user` (issuer and subject), `kube` (effective username, groups), `session` (opaque audit ID, not the cookie); `agent` and `onBehalfOf` (later, MCP); `request` (ID, method, route template, allow-listed filter names and values); `install`, `server` (instance); `refs` (for reads and disclosure); `authz` (cache revision); `source` (IP, from the connection, or from `X-Forwarded-For` only when a trusted proxy CIDR is configured); `userAgent` (truncated). Never tokens, cookies, raw query strings or response bodies.

### 9.4 Operation

Summaries flush every 5 minutes and on shutdown; at most 10,000 open summary keys, then the oldest flush early (our call). `/metrics` counts written and dropped records. Local mode writes only the mode-at-startup record.

## 10. Agents and kgwctl (D-089)

Phase 7 builds no MCP server. It sets the contract any MCP form follows:
- An agent acts for an identity: a person (OAuth on behalf of a user) or a configured service identity with its own RBAC. It never sees more than that identity's RBAC.
- Remote MCP calls go through the same `Authorizer`, the same rule (§7) and the audit `agent` level.
- Tokens for remote MCP are audience-bound to Snellius and never forwarded.

kgwctl reads with the user's kubeconfig. Phase 7 changes nothing in kgwctl; the two signals (§8) are computed in `pkg/` so kgwctl can show them, with "incomplete" when its own reads were forbidden.

## 11. Web

- `client.ts` `request()`: `401` redirects to the login URL; `503` with `authz-unavailable` shows a new `PageState` "Can't check your permissions right now"; `retryEveryPoll` stops on `401` (`hooks.ts:16-29`).
- After data has loaded, a poll answered `authz-unavailable` shows as a `PageNotice` over the rows already loaded (our call): the browser keeps what it had, and no new data leaves the server.
- Polling pauses while the tab is hidden, so an idle tab doesn't keep a session alive (our call).
- Header: the signed-in user with a menu (Who am I, Sign out) in secure mode; the local-mode banner in local mode (`Shell.tsx:118-125`).
- The header health tint uses visible findings only (`Header.tsx:8`, through `/installs`).
- A namespace selector in the header, in both modes (our call): choices from `/namespaces` plus typed entry, default all, kept per browser. It scopes every list page; overview, health, diagnostics and the map stay install-wide.
- Sign-in states: "Signing in", "Sign-in is not configured", "Your sign-in is not accepted by this cluster", "Your session has ended".

## 12. Chart and RBAC

- `auth.mode: local|secure` (default `local`).
- `auth.oidc.issuerURL`, `clientID`, `clientSecret.existingSecret`/`key` (mounted as a file), `redirectURL`, `scopes`, `extraAudiences` (documentation only).
- `auth.session.idle`, `absolute`, `cookieSecure`.
- `auth.trustedProxyCIDRs` for `X-Forwarded-For` in audit only.
- `auth.authz.cacheTTL` (allowed answers, default 60 s, 30 s to 15 min), `auth.authz.namedGetScan` (default off), and the global SAR limits `auth.authz.maxInFlight`, `rate`, `burst` (defaults 32, 100/s, 32) (D-100). The denied TTL (4 to 5 min) and the cache's byte budget are not settings.
- `audit.levels`, `audit.summaryWindow`.
- In secure mode the ClusterRole gains `create` on `tokenreviews` and `subjectaccessreviews`. Local mode does not get them.
- `templates/NOTES.txt` and `values.yaml` describe both modes; the D-013 text moves to local mode.
- The chart fails rendering for: local mode with `httpRoute.enabled`, ingress or `hostNetwork`; secure mode without `issuerURL`, `clientID` or `redirectURL`; secure mode with `replicas` above 1.

## 13. Testing

- **Unit, the D-085 rule as a test:** each surface in §7.2 with two viewers over the same model, then a change to an object only the first can read: the second viewer's response is equal before and after. The clock and publication sequence are fixed in the test, and the comparison masks only fields the test names as time-of-read (Codex #13); a masked field must itself not depend on hidden objects. Changes covered: hidden object created, deleted, made faulty, made pending; aggregate thresholds crossed by hidden objects; a hidden Gateway rejecting. Mutation checks as in v0.7.1.
- **Adversarial:** permission revoked and TTL expiry; two users sharing a memo entry; SAR failing mid-request; `evaluationError`; invalid, expired, wrong-audience and wrong-nonce ID tokens; callback replay; logout racing refresh; audit redaction, counting and drop behaviour.
- **Authoriser:** a fake SAR server: list then get, `resourceNames`, TTL expiry, no extension past expiry, error → 503.
- **Identity:** a fake TokenReview server; login with state and nonce checks; refresh; logout; expiry.
- **Modes:** local mode refuses non-loopback `--listen`; the chart refuses forbidden combinations (helm unit tests).
- **e2e (kind):** a test-only OIDC issuer (`test/oidc-issuer`, Go, standard library crypto) signs ID tokens; the kind API server trusts it through a structured authentication configuration, with the issuer's test CA in the configuration. The issuer's hostname must resolve for both the browser (Playwright on the host) and the API server (in the kind node); the plan picks the mechanism (a host-published port with a matching certificate SAN). Two users with different RoleBindings sign in through the real flow; Playwright checks what each sees. Local mode: direct pod and Service access refused, port-forward works. The issuer is test-only and adds no dependency (our call).
- **Bench:** the SAR budget (§6.4) and the memory of the per-identity caches against D-057.

## 14. Proposed decisions

- **D-090** Identity from TokenReview; claim mapping only with an approved JWT dependency (§4). Amended by D-097: the TokenReview sets no audiences.
- **D-091** SubjectAccessReview authoriser: list-then-get, 30 s TTL, fail closed, `ErrAuthorizer` (§6).
- **D-092** Findings decided by hidden objects are omitted; installs visible through GatewayClasses; one permissions note per viewer (§7.3–§7.5).
- **D-093** Two signals in the API; `health` is their summary (§8).
- **D-094** Audit stream, levels and fields (§9).
- **D-095** Probe listener and `/metrics` (§3.3).
- **D-096** In-memory sessions, one replica in secure mode (§5).

## 15. Sub-phases

| Sub-phase | Scope |
|---|---|
| 7.1 | Modes and groundwork: loopback and Host enforcement, probe listener and `/metrics`, request IDs and audit stream skeleton (security level), chart modes and refusals, local-mode banner, the secure-mode `503 secure-mode-incomplete` gate |
| 7.2 | Identity and sessions: OIDC flow, TokenReview, refresh, logout, web sign-in states and user menu, test issuer, e2e sign-in (data still gated) |
| 7.3 | Authoriser: SAR, cache, full-read fast path, `ErrAuthorizer` and the response-commit wrapper, bench case (data still gated) |
| 7.4 | Visibility everywhere: §7.2 table, finding and check basis, installs, permissions note, EndpointSlices, path incompleteness; the rule test per surface (data still gated) |
| 7.5 | Two signals: `pkg/` model, API compatibility matrix, route list, detail, map, Gateway and ListenerSet tables; the gate opens |
| 7.6 | Audit levels complete, docs (README trust model, chart README, upgrade notes, snellius.dev), phase review, `v0.8.0` |
| 7.7 (if approved) | Claim-mapping fallback with the approved JWT library |

## 16. Left to the builder

- State filter names for the two signals.
- Which `/meta` capabilities become per viewer.
- The SAR cache bound and the summary key bound, within the stated budgets.
- Copy, through the wording picker.

## 17. Open questions

- The JWT dependency for the claim-mapping fallback (queued for the user).
- Whether `v0.8.0` should ship before 7.7 if the dependency is not approved (our call: yes).
