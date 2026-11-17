# Notes for the Snellius user documentation

Topics the user-facing documentation must cover when it is written. Each entry says what to explain and why. Add an entry whenever a decision creates something an operator needs to know.

## Delegation edge cap and memory (2026-10-27, D-025)

- **What it is.** The UI computes route delegation (which child routes each parent selects) up to a cap on selection edges per install. An edge is one parent ref selecting one child route. A wildcard or label ref that matches many routes makes many edges.
- **What users see past the cap.** Parents past the cap read "too large to compute". Rows already computed keep their outcomes with "Some paths not computed". Checks that report something missing (a missing child status entry, the computed KGWUI-011) are not raised anywhere in the install. View A and the controller's own status are unaffected.
- **Caps are shared.** The caps count the whole install, routes the viewer cannot see included. So a large hidden part can make a visible route read "too large to compute".
- **Depth.** A delegation path deeper than 100 routes is also "too large to compute". This bound is fixed.
- **Inherited headers.** A child that inherits its parent's matcher copies every header and query match above it. Very deep inherit chains with many headers therefore reach the matcher cap sooner and read "too large to compute" earlier.
- **Known limits.** Very long inherited path segments across deep chains, and very long header values in failure reasons, can use a lot of memory; tracked for a later phase.
- **How to raise it.** The cap is configurable (binary flag and chart value; see the chart README). Raising it lets the UI compute denser delegation, but memory grows with it: during a recompute the old and new models are both in memory. Users who raise the cap must also raise the pod's memory limit. The binary sets its Go memory target to 90 % of the pod limit unless `GOMEMLIMIT` is set, so the two move together.
- **Sizing guidance.** Give measured numbers (peak RSS per cap value on the dense benchmark, `docs/phases/phase-2/bench/`) rather than a formula. Measured on the densest benchmark (100 wildcard parents × 1,000 routes, no `GOMEMLIMIT`; `bench/2.4-dense-memory.txt`): the default 10,000 peaks at about 425–465 MiB RSS, inside the default 512Mi limit; 20,000 at about 510–565 MiB; 50,000 at about 700–760 MiB. Less dense installs use less at the same cap.

## Scalability: what the UI watches and holds (2026-11-06, D-031)

- **Cluster-wide watches.** Services and EndpointSlices are watched across the cluster, not only the ones routes reference. A Kubernetes watch can filter by namespace, label or field, never by "referenced by a route"; watching per Service would put far more load on the API server.
- **Cut down on arrival.** Each object is reduced to about 200 bytes at the informer (Service: name, type, ports; EndpointSlice: ports and ready counts). At 5,000 Services and 6,000 EndpointSlices this is about 2 MiB. Checks look only at Services a route references.
- **Turning it off.** The `diagnostics.endpoints` chart value turns both watches off; KGWUI-023 then reads "not available".
- **Sizing guidance.** Give measured numbers per scale tier from the Phase 3 bench records, not a formula.

## Other Gateway API controllers: class-only installs (2026-11-09, Phase 5 design §5)

- **What it is.** With `installs.classOnly` on (flag `--class-only-installs`), each GatewayClass `controllerName` that no pack recognises becomes an install, read from Gateway API status alone. Off by default, so a kgateway user sees no new installs.
- **What users see.** The install is named "Gateway API controller `<name>`". Its ID escapes each byte outside `A-Z a-z 0-9 . -` as `~` and two hex digits, which shows in URLs. The scope reads "assumed: all namespaces". There is no version, edition, computed badge, Delegation tab, data plane section or Processes block; the Gateway page says "Not available for this controller".
- **What it does not claim.** Snellius is built for kgateway. A class-only install shows what the controller wrote to status, judged by the core checks only. Explain which finding codes can appear (the core ones, design §6) and which never do.
- **Assumed scope.** A route outside the controller's real discovery scope shows as not accepted (KGWUI-003). Say this is what the user sees from the cluster, and that Snellius cannot read the controller's settings.
- **The core-only build.** `cmd/snellius-core` (`make build-core`, `make image-core`) is the core with no pack and class-only installs always on. It is for testing the core's independence, not a supported product build; say so if it is documented at all.

## Traffic: the path list (2026-11-12, Phase 6 design §4–§5)

- **What a path is.** One row per Gateway, listener, route (and delegated routes), rule and backendRef. Hostnames are a list on the row. A rule with no backend shows its written action (redirect, direct response, no backend); a delegation ref shows "delegate".
- **Where a problem comes from.** The worst existing signal along the whole chain: Gateway, listener, route status, delegation, backend findings, endpoints. Phase 6 adds no checks. Explain "unknown" (a check it depends on is unavailable, or a finding is still held) and why it is never shown as healthy.
- **Share.** Only within one rule, as Gateway API defines weights; never a share of Gateway traffic. Weight 0 is "no traffic". No share on rules that delegate, redirect or have no backend.
- **Counts and visibility.** The summary counts only paths the viewer may see, and says when some are hidden. A class-only install has no paths; the page says why.
- **Known over-report.** A delegated path can show an error that belongs to another rule of its root route. It never hides one.

## Backends and endpoints (2026-11-12, Phase 6 design §8–§9)

- **What is listed.** Every kgateway Backend, and every Service or other backend a route or GatewayExtension names. Unreferenced Services are not listed.
- **Endpoints.** The list counts ready endpoints per backend port from the cached facts, as KGWUI-023 does. Backend detail reads that one Service's EndpointSlices on request (at most about 1,000 endpoints; more reads "partial"). Only a complete read establishes zero. Explain "partial", "timed out", "busy, retry" and "not applicable" (ExternalName, Backend resources).
- **Port chain.** backendRef port → Service port → targetPort → slice ports. Named targetPorts are not resolved to container ports; explain why (no Pod watch).
- **Reference explanations.** When a ref does not resolve, the page shows the controller's reason and the fix (for `RefNotPermitted`, the ReferenceGrant). When the controller's status does not say which ref failed, every candidate says so; Snellius does not guess.
- **RBAC.** The live read uses the EndpointSlice `list` the `diagnostics.endpoints` value already grants; with it off, the section shows "unavailable".

## Route trace (2026-11-12, Phase 6 design §7)

- **What it shows.** Per rule of a route: its match, filters as written, a share bar and each backend with port, weight, share, endpoints and state, for all four route kinds.
- **Wording per protocol.** "Requests fail" for HTTP and gRPC, "connections fail" for TCP and TLS. No status code is promised.
- **What it does not show yet.** The effective filter chain (later phase) and per-proxy endpoints (after GA, #35).

## Gateway map (2026-11-12, Phase 6 design §6)

- **What it is.** One Gateway's paths as columns: Gateway → listeners → routes → rules → backends. It ignores the Overview tab's listener picker.
- **Reading it.** Edge colour is the worst state of its paths; a dashed edge with an error icon means every path on it fails; a solid error edge with a hollow ring means some do. Under a state filter no edge is drawn as broken. Animation is off with reduced motion.
- **Interactions.** Hover, side panel, expand into rules, collapse delegated routes, "Show only its paths", search and chips. Expand, collapse and focus live in the URL, so a link reproduces the view.
- **Large Gateways.** More than 60 routes or backends opens on problems only, with a prompt to narrow.
- **Keyboard and accessibility.** One tab stop; arrow keys along the paths, Enter for details, Escape to close. The Table button is the accessible equivalent.
- **Export.** SVG or PNG of the whole drawn graph (filters, focus and collapse applied), with a title line and legend; not the visible viewport.

## Local and secure mode (2026-11-14, Phase 7 sub-phase 7.1)

- **Two modes.** Flag `--auth-mode`, chart value `auth.mode`, default `local`. Explain what each means for who sees what.
- **Local mode.** No sign-in. Snellius listens on `127.0.0.1:8080` in the pod, so only `kubectl port-forward` reaches it. Whoever can port-forward (needs `pods/portforward` in its namespace) sees everything Snellius reads. A banner on every page says so.
- **Refusals.** A Host that is not localhost (or in `auth.allowedHosts`) gets 421; a cross-origin request gets 403. Explain both, and that the binary refuses a non-loopback `--listen` or host networking, and the chart refuses `httpRoute.enabled`.
- **Secure mode.** OIDC sign-in with visibility that follows Kubernetes RBAC, from `v0.8.0`. Until then it answers `/api/` with 503 `secure-mode-incomplete`. Write the setup once the release exists.
- **Upgrade break.** An install with `httpRoute.enabled` needs `auth.mode: secure`. Explain why (an HTTPRoute exposed an unauthenticated view up to `v0.7.x`; local mode is loopback-only), give the two choices (secure mode from `v0.8.0`, or port-forward), and say not to upgrade it to a development build.

## Audit stream (2026-11-14, Phase 7 sub-phase 7.1)

- **Format.** JSON lines on stdout, each with `"log":"snellius.audit"`. Document the record fields once more than the startup record exists.
- **Levels.** `--audit-levels` (chart `audit.levels`), default `security,agent,reads,summaries`. Explain what each level records.
- **Request ID.** Every response from the UI and API listener carries `X-Request-Id` (the probe listener does not); say how to match it to audit lines.

## Probe port and metrics (2026-11-14, Phase 7 sub-phase 7.1)

- **Probe listener.** `--probe-listen`, chart container port `probe` (8081). It serves `/healthz`, `/readyz` and `/metrics`, and the probes use it.
- **Readiness body.** `/readyz` lists no install IDs; they go to the log.
- **Metrics.** `snellius_auth_mode{mode}`, `snellius_audit_records_total{level}`, `snellius_audit_dropped_total{level}`. Say how to scrape them and that a NetworkPolicy must allow it.

## Secure-mode sign-in (2026-11-14, Phase 7 sub-phase 7.2)

- **IdP setup per provider.** Later, add one short walkthrough per common IdP: the client, the redirect and post-logout URIs, the scopes and the groups claim. The README has the provider-neutral steps.
- **Audience configuration.** Explain the two options in depth: reuse the client ID the cluster trusts, or add Snellius's client ID to `audiences` in the structured authentication configuration. Say why Snellius needs it: the API server validates the ID token through TokenReview. Cover how the claim mappings and prefixes set the username and groups that RBAC sees.
- **Troubleshooting `/signin-failed`.** One entry per reason. `unauthenticated`: the IdP or the API server rejects the user, including when the API server does not accept Snellius's client ID as an audience (D-097). `invalid`: a missing or expired login attempt, a failed code exchange or ID token check, or a failed token review. Say which log lines to read and that the browser never sees more than the reason.
- **Sessions.** What a session is (one signed-in browser), and the three settings: `auth.session.idle` (30m), `auth.session.absolute` (8h) and `auth.session.max` (1,000; past it, the session unused for longest ends). Say that the IdP's refresh-token lifetime also ends a session, whichever comes first. Cover the identity check at least every 15 minutes, and that a restart signs everyone out. Explain why one replica.
- **Sign-out.** RP-initiated logout: `client_id` and the app root as `post_logout_redirect_uri`, no ID token hint. Say what happens when the IdP has no end-session endpoint (the browser returns to the app root).
- **HTTPS and cookies.** `cookieSecure=false` only for a localhost redirect. Behind a gateway, the route must carry TLS and operator authentication.

## Permission checks (2026-11-15, Phase 7 sub-phase 7.3)

- **How access is checked.** Lists, counts and summaries include an object only when the user may `list` its resource in its namespace (cluster-wide for a cluster-scoped kind). A detail page, and an object named in a readable object, checks `list` and then `get` on that object, which honours `resourceNames` grants. Answers are cached per identity. A user who may list every served kind cluster-wide takes the full-read path and needs no per-object checks (D-100).
- **Cache TTL.** `--authz-cache-ttl` (chart `auth.authz.cacheTTL`), `30s` to `15m`, default `60s`, for allowed answers. Denied ones last 4 to 5 min (D-100). It is the only bound on how long a revoked permission stays visible. There is no RBAC watch (D-099). Document both directions: tighten with `30s` and shorter session lifetimes (idle expiry does not end a polling session), relax up to `15m` (a removed RoleBinding stays effective that long).
- **Named-get scan.** `--authz-named-get-scan` (chart `auth.authz.namedGetScan`, default off) shows objects granted only by `resourceNames` in lists again, at one check per hidden object. Explain it for clusters that rely on named read grants, and that it is an upgrade-notes behavior change.
- **Load and rate settings.** `--authz-max-in-flight` (32), `--authz-rate` (100/s, `1` to `10000`) and `--authz-burst` (500), chart `auth.authz.maxInFlight`, `rate` and `burst`, across all users. Checks use their own client with no client-side throttle, so these are the only limits. Say when to raise them (large clusters, many users) and that the API server must take the load. Measured at the default 100/s: 50 viewers opening cold pages at once wait up to about 3 min for the last first answer, and about 3% of polls see `authz-unavailable` (`expired`) meanwhile. The permission cache has an 8 MiB memory budget inside the 64 MiB retained-heap budget over the 2.1 baseline (D-057, D-100).
- **Failing closed.** A failed check answers `503 authz-unavailable`, never a partial page or a 404, and writes a `security` audit record `authz-unavailable` with the reason (`check-failed`, `check-limit`, `full-read-failed`, `expired`, `identity-stale`). Explain the 1,000-check limit per request.
- **RBAC.** Secure mode adds `create` on `subjectaccessreviews`. Say which kinds a user needs `list` on for the full-read path.
- **Metrics.** `snellius_authz_checks_total{verb,outcome}`, `snellius_authz_check_seconds_total`, `snellius_authz_cache_total{result}`, `snellius_authz_identities`.

## Visibility for restricted viewers (2026-11-17, Phase 7 sub-phase 7.4)

- **The basis rule.** A finding about one object shows to a viewer without the full read only when they can read that object and every object it names (D-106). Findings decided by many objects (listener counts, skew, proxy stats, processes, probes, extension checks) need the full read. Give one example per class, so an application team knows why a platform-wide finding is missing for them. Say that the basis is decided per resource and analyzer, so a route rejected by a readable and a hidden parent shows no finding to that viewer (D-108 item 4).
- **"Not checked".** What it means in "What was checked", and that it is never a pass. Collection failures still show to everyone, with fixed text.
- **Below-full-read exceptions.** The listener check with a readable route population, process counts with `list` on Deployments and Pods, probe facts with cluster-wide `list` on Events, and endpoint counts with `list` on EndpointSlices (D-110). Give the RBAC rules a team needs for each.
- **The permissions note.** The header note "You see what your Kubernetes permissions allow." shows to a viewer without the full read (`/api/v1/meta` `limitedView`). Say which permissions make up the full read, so a platform team can grant it.
- **Install context.** List what every viewer sees regardless of RBAC: the install list and IDs (D-102, D-103), controller settings (D-106), membership (D-107), collection failures and Snellius's own RBAC (D-108), the response time (D-109). Say plainly that these are accepted exceptions.
- **Times.** For a viewer without the full read, per-result times and `/installs` `computedAt` are the confirmation time (D-109).
- **Backend detail read time.** Endpoint reads are kept for 5 s, so a backend detail page can show that someone opened the same Service just before. Document it as a known side channel that reveals no hidden object.

## Namespace scope (2026-11-17, Phase 7 sub-phase 7.4)

- **What it does.** The header selector limits rows, counts, facets and summaries to the chosen namespaces, for every viewer. Findings with no namespace fall outside every scope. Cross-namespace dependencies are still checked.
- **Where it lives.** One browser setting for every install, kept across reloads. Explain the marker on the header and the scoped empty state with "Show all namespaces".
- **Why use it.** A scoped page asks about a tenth of the permission checks on a large cluster. Give the measured numbers from the 7.4 bench.
- **API.** `namespaces=` on the seven lists (at most 100 names, `400` on a bad value) and `GET /api/v1/installs/{id}/namespaces`.
