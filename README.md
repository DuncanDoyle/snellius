<h1 align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/snellius-logo-horizontal-dark.svg">
    <source media="(prefers-color-scheme: light)" srcset="docs/assets/snellius-logo-horizontal-light.svg">
    <img alt="Snellius" src="docs/assets/snellius-logo-horizontal-light.svg" width="720">
  </picture>
</h1>

<p align="center"><strong>See the whole picture.</strong><br>Kubernetes Gateway Management &amp; Observability<br><a href="https://snellius.dev">snellius.dev</a> · <a href="https://snellius.dev/docs/quick-start/">Quick start</a> · <a href="https://snellius.dev/docs/">Docs</a></p>

Snellius is a read-only UI for Kubernetes Gateway API, built for kgateway. It shows the health of each controller install at a glance, why an install is unhealthy, and an inventory of what it manages: GatewayClasses, Gateways, routes and policies.

Kubernetes is the only provider today. The API and the SPA are built so other platforms can be added later.

**Naming.** The name Snellius is not trademark-cleared. SURF's Dutch national supercomputer is also named Snellius.

## Why Snellius?

Snellius is named after [Willebrord Snellius](https://en.wikipedia.org/wiki/Willebrord_Snellius) (1580–1626), a Dutch mathematician and astronomer known for Snell's law of refraction and his pioneering work in triangulation and navigation. Just as Snellius used observations from multiple points to understand and map the physical world, this project brings together Kubernetes Gateway API resources, controller state, proxy configuration, and runtime telemetry to provide a coherent picture of gateway infrastructure. The connection to refraction is equally fitting: understanding how traffic is directed and transformed as it flows through different layers of the system. Snellius helps operators see the whole picture, from configuration intent to actual dataplane behavior.

## Security and trust model

Snellius runs in one of two modes. Set it with `--auth-mode` or the chart value `auth.mode` (default `local`).

**Local mode (the default).** There is no sign-in.

- The UI and API listen on `127.0.0.1:8080` inside the pod. From outside the pod, only `kubectl port-forward`, to the Service or the pod, reaches them.
- Containers in the pod share its network, so a sidecar or a chart `extraContainers` entry can reach the local-mode listener too. Add only containers you trust with everything Snellius can read.
- Whoever can port-forward to Snellius sees everything Snellius can read. Port-forwarding needs `pods/portforward` in the Snellius namespace.
- That is everything its ServiceAccount can read across the cluster, Deployments included. It can be more than that person's own Kubernetes RBAC allows.
- A request whose Host is not `localhost`, `127.0.0.1` or `[::1]` gets 421. Add other names with `auth.allowedHosts` (`--allowed-hosts`). A request whose Origin is not its own scheme, host and port gets 403.
- Every page shows the banner "Local mode: no sign-in. Anyone with port-forward access sees everything Snellius can read."
- At startup, local mode logs a warning with the bind address that anyone who can port-forward sees everything Snellius reads.
- The binary refuses a non-loopback `--listen` and a pod on host networking in local mode. The chart refuses `httpRoute.enabled` in local mode.

**Secure mode.** Users sign in with OIDC, and each user sees only what their own Kubernetes RBAC allows. Snellius answers `401` to an `/api/` request without a session. Setup steps are in [Secure mode](#secure-mode). Use it for any install that you expose through a Gateway.

| | Local mode | Secure mode |
|---|---|---|
| Sign-in | None | OIDC |
| Listens on | `127.0.0.1:8080` in the pod | `:8080` on the pod IP |
| Reached through | `kubectl port-forward` | A Gateway route, or the Service |
| What a user sees | Everything Snellius can read | What the user's own RBAC allows |
| Permission checks | None | SubjectAccessReview, on the API server |
| ClusterRole extras | None | `create` on `tokenreviews` and `subjectaccessreviews` |

**Both modes.**

- **What Snellius reads.** Snellius reads the cluster with its own ServiceAccount, never with a user's token. The ClusterRole grants only `get`, `list` and `watch` (see [What it reads](#what-it-reads)). A user can never see more than that, whatever their own RBAC allows.
- **Where checks happen.** In secure mode, Snellius asks the API server whether the signed-in user may read each object (a SubjectAccessReview as that user), and filters its own data with the answer. The API server stays the only place that evaluates RBAC. Snellius keeps no permission model of its own.
- **No Secrets.** The UI never reads Secrets, and its ClusterRole excludes them. No other field is redacted, because a secret can sit in any field, such as an inline token or a literal env var.
- **Audit stream.** Snellius writes JSON audit lines to stdout. See [Audit stream](#audit-stream).
- **Request IDs.** Every response from the UI and API listener carries an `X-Request-Id`. The probe listener does not.
- **Downstream builds.** In secure mode, an authoriser that a downstream build registers can only narrow what a user sees. Every decision needs both Kubernetes RBAC and the build's authoriser to allow it.

### Secure mode

Secure mode signs users in with your IdP and shows the data of the Kubernetes identity that the IdP issues. It needs an IdP that your cluster already trusts for API access.

Managed clusters whose API server accepts only the cloud's own tokens, such as GKE and AKS with Entra ID, are not supported yet; use local mode there ([#51](https://github.com/DuncanDoyle/snellius/issues/51)).

1. **Register Snellius as an OIDC client at your IdP.**
   - Redirect URI: `https://<host>/auth/callback`
   - Post-logout redirect URI: `https://<host>/`
   - Use the authorization code flow with PKCE. A client secret is optional.
   - Allow the scopes `openid`, `profile`, `email`, `groups` and `offline_access`, or set your own with `auth.oidc.scopes`. The list must include `openid`.
2. **Make the cluster accept the client's tokens.** Snellius sends the user's ID token to the API server, so the API server must accept the client as an audience. Do one of these:
   - Register Snellius with the same client ID that the cluster already trusts. Then the API server needs no change.
   - Add Snellius's client ID to `audiences` in the API server's structured authentication configuration. Two audiences need `audienceMatchPolicy: MatchAny`. This example keeps the claim mappings that the cluster already uses. Yours may differ. It uses `apiserver.config.k8s.io/v1`, which needs Kubernetes 1.34 or later. Kubernetes 1.30 to 1.33 takes the same file as `v1beta1`.

     ```yaml
     apiVersion: apiserver.config.k8s.io/v1
     kind: AuthenticationConfiguration
     jwt:
       - issuer:
           url: https://idp.example.com
           audiences:
             - kubectl            # the client ID the cluster already trusts
             - snellius           # the Snellius client ID
           audienceMatchPolicy: MatchAny   # required with more than one audience
         claimMappings:
           username:
             claim: email
             prefix: "oidc:"
           groups:
             claim: groups
             prefix: "oidc:"
     ```
3. **Create the optional Secret and ConfigMap.** A client secret is only for a confidential client. The CA bundle is only for an IdP behind a private CA.

   ```sh
   kubectl create namespace snellius
   kubectl -n snellius create secret generic snellius-oidc --from-literal=client-secret='<secret>'
   kubectl -n snellius create configmap snellius-oidc-ca --from-file=ca.pem=./idp-ca.pem
   ```
4. **Install with the secure-mode values.** The issuer and the redirect URL must be https. The client ID must match step 1.

   ```yaml
   auth:
     mode: secure
     oidc:
       issuerURL: https://idp.example.com
       clientID: snellius
       redirectURL: https://snellius.example.com/auth/callback
       clientSecret:
         existingSecret: snellius-oidc   # omit for a public client
       ca:
         existingConfigMap: snellius-oidc-ca   # omit for a public CA
     trustedProxyCIDRs:
       - 10.42.0.0/16   # the Gateway's proxy pods; omit when nothing sits in front of Snellius
   httpRoute:
     enabled: true
     parentRefs:
       - name: my-gateway
         namespace: gateway-system
     hostnames:
       - snellius.example.com
   ```

   `auth.trustedProxyCIDRs` (`--trusted-proxy-cidrs`) lists the proxies that sit in front of Snellius. For a request from one of them, Snellius keeps its `X-Request-Id`, and the audit `source` is the right-most `X-Forwarded-For` address that is not itself a listed proxy. For any other peer, the source is the connection's address and both headers are ignored. The setting changes only the audit stream and the request ID. It never changes who can sign in or what they see. Without it, every audit `source` is the Gateway's address.

   Put the Gateway's own sign-in or access control on the route too, so that only the people you intend reach the sign-in page.
5. **Check the result.** Run `kubectl auth whoami` as the same user, with the same IdP. Open Snellius, sign in, and select Account details in the user menu. The username and groups match the `kubectl auth whoami` output.

Notes:

- **HTTPS everywhere.** The browser reaches Snellius over https, and the issuer and its endpoints use https. The one exception is a redirect URL on `localhost` or `127.0.0.1`, which can use http for testing. Then also set `auth.session.cookieSecure=false`.
- **Sessions live in memory.** The chart runs one replica. A restart signs everyone out. A session ends after 30 minutes without a request or 8 hours after sign-in (`auth.session.idle` and `auth.session.absolute`). Snellius keeps at most 1,000 sessions (`auth.session.max`). The identity provider's refresh-token lifetime can end a session sooner, whichever comes first. It checks each user's identity again at least every 15 minutes.
- **Permission checks.** Snellius checks each user's access with SubjectAccessReview. It reuses an allowed answer for 60 seconds (`auth.authz.cacheTTL`, `30s` to `15m`) and a denied one for 4 to 5 minutes. A revoked permission stays visible for at most the allowed TTL. A new grant shows within 5 minutes. The defaults are the secure choice.
- **What lists show.** An object appears in a list, count or summary only when the user may `list` its kind in its namespace, or cluster-wide for a cluster-scoped kind. An object the user may only `get` by name (a `resourceNames` grant) still opens on its detail page, and when an object the user can read names it. Set `auth.authz.namedGetScan: true` to show such objects in lists too. It costs one check per object the user cannot list.
- **Faster revocation.** For faster permission revocation, set `auth.authz.cacheTTL: 30s` and shorten `auth.session.idle` and `auth.session.absolute`. The cache TTL limits how long an allowed permission check is reused. Identity-provider changes and authorization caches in the API server can take longer. Idle expiry does not end a session that keeps polling, so use a shorter absolute lifetime where that matters. A `30s` TTL repeats each allowed check twice as often as the default.
- **Fewer checks.** A `cacheTTL` up to `15m` trades freshness for fewer checks. A removed RoleBinding then stays effective in Snellius for up to that TTL.
- **Check load.** Snellius limits the checks it sends to the API server. See [Capacity and tuning](#capacity-and-tuning).
- **Sign-out.** The Sign out menu item ends the session and sends the browser to the IdP's end-session endpoint, if the IdP publishes one. Snellius sends `client_id` and a `post_logout_redirect_uri` that is the app root. It sends no ID token hint.
- **Keycloak.** Keycloak has no `groups` scope, and it rejects a sign-in that asks for a scope it does not know. Set `auth.oidc.scopes` to `[openid, profile, email, offline_access]`, and add a Group Membership mapper to the client for the `groups` claim, with the full group path off. Each user needs the realm role `offline_access`, which the realm's default roles include. The [Snellius demo](https://github.com/DuncanDoyle/snellius-demo) has a working realm.
- **Sign-in errors.** A failed sign-in lands on `/signin-failed` with the reason `unauthenticated` (the API server does not accept the token, for example because it does not accept the client ID as an audience) or `invalid` (the login attempt has no open transaction or a wrong state, the code exchange or ID token check fails, or the token review itself fails).
- **Routes.** Secure mode serves `/auth/login`, `/auth/callback`, `/auth/me` and `POST /auth/logout`. `/auth/status` reports the mode and whether sign-in is ready. Local mode serves only `/auth/status`.
- **Data follows RBAC.** Every `/api/` request answers `401` without a session. With one, the user sees only what their Kubernetes RBAC allows.
- **No secrets in the browser.** The client secret, the ID token, the refresh token and the user's bearer token never reach the browser or the logs. The browser holds only an opaque session cookie.

#### What each user sees

In secure mode each user sees only what their own Kubernetes RBAC lets them read. A user who may `list` cluster-wide every served Gateway API and kgateway kind, plus Namespaces, Pods, Deployments, Services and EndpointSlices, has the full read and sees everything Snellius reads. Events and CRDs are not part of it. Everyone else is a restricted viewer, and these rules apply to them:

- **No trace of hidden objects.** Rows, counts, facets, summaries and health show only objects the user can read. There are no hidden counts, placeholders or "restricted" markers. A change to an object the user cannot read does not change what they see, apart from the exceptions below.
- **Findings.** Only findings about a user's own objects show below the full read: a Gateway's or route's own status, a route's parentRefs, backendRefs and rules. Such a finding shows when the user can read its object and every object that check named for it, such as all of a route's parents. Every other finding needs the full read, for example listener-count disagreement, version skew, proxy stats, process and probe findings, Backend and endpoint findings such as "no ready endpoints", and every finding from an extension check.
- **"Not checked".** In "What was checked", every check reads "Not checked" for a user without the full read. It means Snellius did not show the result to this user, not that the check passed or failed. Collection failures, such as a watch Snellius may not run, show to every user with fixed text.
- **Listener checks.** A listener's route-count check shows below the full read only when the user may `list` every route kind in every namespace the listener admits: its own namespace for `Same`, cluster-wide for `All` and `Selector`. A `Selector` listener also needs cluster-wide `list` on Namespaces. Otherwise the listener shows the count of routes the user can read, with no check.
- **Delegation.** Delegation analysis reads every route in the install, so it needs the full read. Below the full read, the Delegation tab, the counts on a delegation ref and a child route's effective matches and paths read "Not checked". The Traffic and Backends pages show no paths through a parent route, and no route is marked dropped, so the dropped filter finds nothing. A route's own status, including which parent routes it names, still shows. Responses that leave delegation analysis out say so with `delegationNotChecked: true`: route detail, `…/delegation`, `/routes`, `/paths`, `/backends` and backend detail.
- **Processes and probes.** Process counts need `list` on Deployments and Pods in their namespace. Probe facts need `list` on Events cluster-wide, because Snellius caps Events across the cluster.
- **Events and CRDs under the full read.** The full read does not include them. Probe facts need cluster-wide `list` on Events, and CRD bundle-skew findings need cluster-wide `list` on CustomResourceDefinitions, for every user, including one with the full read.
- **Endpoints.** Endpoint counts need `list` on EndpointSlices in the Service's namespace. Without it, the Traffic and Backends pages say "You can't read the EndpointSlices in this namespace", with no counts.
- **Installs.** Every signed-in user sees every install's ID (`<namespace>.<name>` of the controller Deployment) and a neutral name. Its GatewayClass names, controller readiness and version, and scope details show only when the user can read their source objects. Otherwise the header says "Details not visible to you". An install with nothing the user can read shows empty pages, never a 404.
- **Times.** A response's `collectedAt` is the real snapshot time for every user. For a user without the full read, or with it but without cluster-wide `list` on Events or CRDs, per-result times and the `/installs` `computedAt` are the time Snellius last confirmed the data, not the time it last computed the model.
- **Permissions note.** The header shows "You see what your Kubernetes permissions allow." to a user without the full read, as text or, on a narrow window, as an icon with a tooltip. `/api/v1/meta` reports this as `limitedView`.

Some things are install context and change what every user sees, even when the user cannot read the object that changed:

- the install list and each install's ID;
- the controller's settings: its scope, version and rule set;
- install membership: which Gateways, ListenerSets, routes and policies belong to an install, decided by GatewayClasses, parent Gateways and ListenerSets, and Namespace labels;
- collection failures, and which kinds Snellius itself can read, which follow Snellius's own RBAC;
- the response's `collectedAt`, which moves when anything in the install changes.

One more thing is visible to other users. A backend detail page shows when its endpoints were read, and Snellius keeps that read for 5 seconds. So a user can learn that someone else opened the same Service within the last 5 seconds. It never shows who, or anything from an object the user cannot read.

#### Namespace scope

The namespace selector in the header limits the list pages to the namespaces you choose. Rows, counts, facets and summaries then cover only objects in those namespaces, for every user. Findings without a namespace, such as one about a CRD, fall outside every scope. A dependency in another namespace, such as a route's Gateway, is still checked and shown when the user can read it.

- **One setting per browser.** The choice is stored in the browser and applies to every install. While a scope is active, the header button names it, and on a narrow window its icon shows a count. An empty list under a scope says so and offers "Show all namespaces".
- **Fewer checks.** A scoped list checks permissions only in the chosen namespaces. On a large cluster this cuts the checks for a cold page about tenfold.
- **API.** The lists `/routes`, `/gateways`, `/listenersets`, `/paths`, `/backends`, `/findings`, `/processes` and `/policies` take `namespaces=a,b`, in both modes. Give it once, with up to 100 namespace names. An empty or invalid value answers `400`. `GET /api/v1/installs/{id}/namespaces` lists the namespaces that hold routes, Gateways or ListenerSets the user can see.

#### Capacity and tuning

Each restricted viewer's page needs permission checks, and Snellius sends them to the API server within these limits. They apply across all users, and checks use their own client, so these are the only throttle.

| Setting | Flag | Default | Meaning |
|---|---|---|---|
| `auth.authz.maxInFlight` | `--authz-max-in-flight` | `32` | Checks in flight at once |
| `auth.authz.rate` | `--authz-rate` | `100` | Checks per second, `1` to `10000` |
| `auth.authz.burst` | `--authz-burst` | `500` | Checks allowed above the rate before it applies |
| `auth.authz.cacheTTL` | `--authz-cache-ttl` | `60s` | How long an allowed answer is reused, `30s` to `15m` |

- **Who is affected.** A user with the full read (the `list` rights in [What each user sees](#what-each-user-sees)) needs no per-object checks. So do polls that the cache answers. Only restricted viewers whose cache is cold or expired send checks.
- **Many viewers at once.** Page loads that need new checks wait in turn, oldest first. After a restart, when many restricted viewers load a page at the same moment, the last one can wait minutes. When too many requests wait, Snellius answers `503 authz-busy`. The page then says Snellius is busy and retries on its next poll. A failed check answers `503 authz-unavailable` instead.
- **How much the default carries.** On a cluster of about 100 namespaces, 50 restricted viewers who poll the Backends page every 5 seconds need about 95 checks per second. That is close to the default of 100. Their polls then slow to about 12 seconds, and still answer. Sustained demand above the rate answers `503 authz-unavailable`.
- **What to do.** Raise `auth.authz.rate` when many restricted viewers use a large cluster, and make sure the API server can take the load. Ask users to pick a namespace in the header: a scoped page checks only those namespaces, which cuts the checks for a cold page about tenfold on a large cluster.
- **Memory.** The permission cache holds about 40,000 answers in 8 MiB. Its size is not a setting.
- **Watch it.** `snellius_authz_checks_total{verb,outcome}`, `snellius_authz_cache_total{result}` and `snellius_authz_cache_evictions_total` on `/metrics` show the load and the cache. A rising `authz-unavailable` count in the audit stream, with reason `expired` or `check-limit`, says the limits are too low for the demand.

### Audit stream

Snellius writes audit records as JSON lines on stdout, each with `"log":"snellius.audit"`. Operational logs use a different format. Snellius does not store, rotate or sign the records. Keep and protect them in your log collector or SIEM.

Select the levels with `--audit-levels` (chart `audit.levels`). The default is `security,agent,reads,summaries`.

| Level | Default | Records |
|---|---|---|
| `security` | on | Startup (with the mode and listen address), sign-in success and failure, sign-out, a session that ends, a session evicted at the limit, a refused request (`denial`), and `authz-unavailable` with its reason |
| `agent` | on | Each agent tool call. Nothing emits it yet. |
| `reads` | on | Each detail, YAML or export read |
| `summaries` | on | List and overview access, counted per session, endpoint and filter set |
| `disclosure` | off | The object references that a response returned |
| `requests` | off | Every API request |

- **Collapsing.** Repeated denials of the same request collapse into one record per minute, with a `count`. Summaries flush every 5 minutes and at shutdown, with a `count`. Rows left out of a list are filtering, not denials, so they are not recorded.
- **Fields.** `schema`, `id`, `time` (UTC), `level`, `action`, `outcome`, `reason`, `user` (issuer and subject), `kube` (username and groups), `session` (an opaque ID, not the cookie), `request` (ID, method, route template and allow-listed filters), `install`, `server`, `refs`, `source`, `userAgent` and `count`. Empty fields are left out.
- **Never recorded.** Tokens, cookies, raw query strings and response bodies.
- **Matching a response.** Every response carries `X-Request-Id`. The same value is in the `request.id` of the audit records that the request wrote, so a user's report maps to its lines.
- **Local mode.** It writes only the startup record.
- **Drops.** A record that cannot be queued or written counts in `snellius_audit_dropped_total{level}`. A window holds at most 10,000 open keys. Past that, the oldest flushes early.

### Probe port and metrics

The probe listener serves `/healthz`, `/readyz` and `/metrics` on container port `probe` (8081, `--probe-listen`), in both modes. It serves no cluster data, and `/readyz` lists no install IDs. The chart's probes use it. Scrape `/metrics` on that port. If a NetworkPolicy limits traffic to the pod, allow your scraper to reach 8081.

| Metric | Meaning |
|---|---|
| `snellius_auth_mode{mode}` | The running mode |
| `snellius_audit_records_total{level}`, `snellius_audit_dropped_total{level}` | Audit records written and dropped |
| `snellius_sessions_active` | Signed-in sessions in memory |
| `snellius_authz_checks_total{verb,outcome}`, `snellius_authz_check_seconds_total` | Permission checks sent, by result, and their time |
| `snellius_authz_cache_total{result}`, `snellius_authz_cache_evictions_total`, `snellius_authz_cache_bytes`, `snellius_authz_identities` | The permission cache |
| `snellius_limit_hits_total{limit}` | Hits of Snellius's own size limits |

## Install

```sh
# Install the chart from ghcr
helm install ui oci://ghcr.io/duncandoyle/charts/snellius --version 0.8.2 \
  -n snellius --create-namespace

# Open the UI on http://localhost:8080
kubectl -n snellius port-forward svc/ui-snellius 8080:80
```

The image is `ghcr.io/duncandoyle/snellius`. Installing from a checkout needs `--set image.tag=<version>`; see the [chart README](deploy/helm/snellius/README.md#install). To pin the UI to one install, set `install.pin`. To expose it through a Gateway:

1. Read [Security and trust model](#security-and-trust-model).
2. Set `auth.mode: secure`.
3. Set `httpRoute.enabled`, `httpRoute.parentRefs` and `httpRoute.hostnames`.

The [chart README](deploy/helm/snellius/README.md) lists every value.

## Run locally

```sh
# Build the SPA and the binary with the SPA embedded
make build

# Serve on http://127.0.0.1:8080 against your current cluster
./bin/snellius --kubeconfig ~/.kube/config
```

Flags:

- `--kubeconfig`: path to a kubeconfig; empty uses in-cluster config, else the default loading rules.
- `--install`: pin the UI to one install (install ID or `namespace/name`).
- `--listen`: listen address (default `127.0.0.1:8080`; `:8080` in-cluster in secure mode). Local mode requires a loopback address.
- `--auth-mode`: `local` (default) or `secure`; see [Security and trust model](#security-and-trust-model).
- `--allowed-hosts`: extra Host names accepted in local mode.
- Secure mode: `--oidc-issuer-url`, `--oidc-client-id`, `--oidc-client-secret-file`, `--oidc-redirect-url`, `--oidc-ca-file`, `--oidc-scopes`, `--cookie-secure`, `--session-idle`, `--session-absolute`, `--session-max`, `--authz-cache-ttl`, `--authz-named-get-scan`, `--authz-max-in-flight`, `--authz-rate` and `--authz-burst`. The chart's `auth.*` values set them; see the [chart README](deploy/helm/snellius/README.md#values).
- `--trusted-proxy-cidrs`: comma-separated CIDRs of proxies whose `X-Request-Id` and `X-Forwarded-For` Snellius trusts (chart `auth.trustedProxyCIDRs`).
- `--probe-listen`: listen address of the probe listener (`/healthz`, `/readyz`, `/metrics`). The chart uses container port `probe` (8081).
- `--audit-levels`: audit levels written to stdout (default `security,agent,reads,summaries`).
- `--poll-interval`: maximum time between model recomputes, the safety timer (default 30s). The model recomputes on object, access, metrics and served-kind changes; `2s` restores the earlier fixed cadence as a floor.
- `--metrics-interval`: controller metrics scrape interval (default 10s).
- `--delegation-edge-cap`: selected delegation edges computed per install (default 10000; 0 keeps the default). Past the cap, parents read "too large to compute". A higher cap needs a higher memory limit.
- `--class-only-installs`: also show one install per GatewayClass `controllerName` that no detector recognises, from Gateway API status alone (default off; chart value `installs.classOnly`).

**Size limits.** Snellius caps its own work: at most 1,000 uncached permission checks per request, and per install the delegation edges, contexts, matcher steps, path depth and kept contexts it computes. Each hit adds one to `snellius_limit_hits_total{limit}` on `/metrics` (`authz-checks`, `delegation-edges`, `delegation-contexts`, `delegation-matchers`, `delegation-depth` or `delegation-retained-contexts`). Snellius also logs the warning `size limit hit` with the limit, the install ID and, for a request, the session's audit ID, at most once a minute per limit and install, with a count of the hits it skipped. Besides the install ID, the warning names no object or namespace. A rising count is worth investigating: the cluster may have outgrown the limits, or someone may be creating objects to push other teams' pages into `503` or cut their delegation analysis.

`make test`, `make lint` and `make e2e` run the Go tests, the linters and the kind end-to-end suite. `make help` lists all targets.

With `SECURE=1`, the e2e setup installs the UI in secure mode against a test OIDC issuer. Its Playwright run skips browser TLS verification of that issuer; the Go sign-in test and the API server verify it.

## What it reads

- **Read-only RBAC.** The ClusterRole grants only `get`, `list` and `watch`: Gateway API kinds, all `gateway.kgateway.dev` kinds, namespaces, pods, Deployments, Warning Events, Gateway API CRD metadata, ReferenceGrants, and Services and EndpointSlices. Each of the last three has a chart toggle. `diagnostics.controllerLogs` (off by default) adds `get` on `pods/log` in the controller namespace.
- **No Secrets, no ConfigMaps, no `*/proxy`, no `pods/portforward`.**
- **Controller metrics on port 9092.** In-cluster, the UI scrapes the controller pods directly. If a NetworkPolicy blocks that, the UI reports the signal as unavailable instead of green. Locally, it goes through the pods/proxy API with your kubeconfig's permissions.

## About page

Click the info icon left of the theme button in the header to open the About page. It shows the UI version, each watched install with its version and controller, and links to the source, docs and licence. A downstream `Brand` can set `descriptor` and `tagline` for the lines under its name, and `links` for its own Project links; without `links` the page shows a small "Built on Snellius" link.

## Scope

The UI honours the controller's `discoveryNamespaceSelectors`. It only counts resources in namespaces the controller watches, and the install page shows the scope as "N of M namespaces".

## Findings

[docs/findings.md](docs/findings.md) has one entry per finding code: its cause, its evidence and how to fix it. "Learn more" on a finding's page opens that entry. A `since` the UI derives resets when the UI restarts.

## Traffic and backends

These pages answer "where does this traffic go, and is it getting there?" They add views, not checks: every problem shown comes from an existing finding, condition or capability state.

- **Traffic** (`/i/{id}/traffic`) lists every path of an install, one row per Gateway, listener, route, rule and backendRef. Problems sort first. Filter by Gateway, route, backend, namespace, state, kind or text; the summary counts the affected routes, Gateways and backends. API: `GET /api/v1/installs/{id}/paths`.
- **Backends** (`/i/{id}/backends`) lists every kgateway Backend and every backend a route or GatewayExtension names, with ready endpoints per port. Backend detail shows how the backend is exposed, its endpoints read on request from that Service's EndpointSlices, the port chain, and why a reference to it fails.
- **Route trace.** Route detail, for HTTPRoute, GRPCRoute, TCPRoute and TLSRoute, shows each rule's match and backends, with each backend's share of the rule and its state.
- **Gateway map.** The Map tab on Gateway detail draws Gateway → listeners → routes → backends. Hover or select a node to light its paths, expand a route into its rules, collapse delegated routes, or show only one node's paths. A Gateway with more than 60 routes or backends opens on problems only.
  - Keyboard: the map is one tab stop. Arrow keys move along the paths, Enter opens details, Escape closes them. The Table button shows the same paths as a table.
  - Export: SVG or PNG of the whole drawn map, with the current filters, focus and collapse applied.

## Policies

These pages answer "which policies name this object, and what does kgateway report about them?" They show what a spec declares and what the controller writes in status. Snellius never decides whether a policy applies: it does not work out attachment, inheritance, precedence, merges or overrides.

- **Policies** (`/i/{id}/policies`) lists the install's policies, DirectResponse included: targets as written, Accepted, "Reported attached on" and the features each one sets. Filter by kind, feature, namespace or "Has problems". The header's [namespace scope](#namespace-scope) applies too, and the namespace filter narrows within it. The list is sorted by namespace and name, not by precedence. API: `GET /api/v1/installs/{id}/policies`.
- **Policy detail**, DirectResponse included, shows each targetRef with its check (found, not found, section not found or not checked) and each label selector as written, with no check. It also shows kgateway's status per ancestor as written, the configuration by feature, precedence annotations as written, and the YAML. Ancestor entries from another controller or install, stale or still waiting for status stay visible but count in no summary. API: `GET /api/v1/installs/{id}/policies/{group}/{kind}/{ns}/{name}`, with `-` for the core group.
- **On other pages.** Gateway, ListenerSet, route and backend pages have a Policies section with up to three groups:
  - "Targeting this object": targetRefs name the object or one of its sections.
  - "Targeting a parent" (routes and ListenerSets): targetRefs name a parent that the object's own parentRefs declare. A route whose parentRef names a ListenerSet can also list that ListenerSet's Gateway's unsectioned policies, when both are readable. kgateway decides whether and how these apply.
  - "Reported attached here" (Gateways, ListenerSets and backends): the controller's current status reports the policy attached on this object.

  Each group lists only policies you can read. Snellius never matches label selectors to build these groups; the section links to the Policies list for policies that select by label. API: `policyRelations` on each detail response.

  On a Gateway or ListenerSet page, an expanded listener row also lists "Policies that name this listener": the policies whose targetRef names that listener as its section. Policies on the whole Gateway or ListenerSet stay in the page's Policies section.

  A policy name links to its policy detail when the install owns or lists the policy and you may read it by name. A policy that a route names from another install links to its YAML.
- **Sources.** Every value carries a tag: Declared (written in a spec), Reported (the controller's status, as written), Inferred (worked out by Snellius, such as a target check or the Accepted summary) or Unknown (nothing can tell, or you cannot read the input).

## Extending

Downstream builds extend the UI without forking it. The backend takes options from `pkg/extension`. This is [examples/downstream/main.go](examples/downstream/main.go), which the test suite builds and runs; the types it names are in [acme.go](examples/downstream/acme.go). It takes the stock binary's flags from `pkg/cli` and embeds its own web build: a build from the Go module cache embeds only a placeholder page.

```go
package main

import (
	"context"
	"embed"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"

	"k8s.io/utils/clock"

	"github.com/DuncanDoyle/snellius/pkg/api"
	"github.com/DuncanDoyle/snellius/pkg/app"
	"github.com/DuncanDoyle/snellius/pkg/cli"
	"github.com/DuncanDoyle/snellius/pkg/extension"
	"github.com/DuncanDoyle/snellius/pkg/objects"
)

//go:embed all:dist
var dist embed.FS

func main() {
	ui, err := fs.Sub(dist, "dist")
	if err != nil {
		log.Fatal(err)
	}
	// The stock binary's flags; the UI is this build's own
	kubeconfig, o, err := cli.Parse(flag.CommandLine, os.Args[1:])
	if err != nil {
		os.Exit(2)
	}
	o.UI = ui
	if err := app.Run(context.Background(), kubeconfig, o, extensions(clock.RealClock{})...); err != nil {
		log.Fatal(err)
	}
}

// options are the stock binary's defaults with ui as the UI.
func options(ui fs.FS) app.Options {
	_, o, err := cli.Parse(flag.NewFlagSet("downstream", flag.ContinueOnError), nil)
	if err != nil {
		panic(err)
	}
	o.UI = ui
	return o
}

// extensions are what this build adds to the stock UI. The types are in
// acme.go; clk drives the collector, so tests can step it.
func extensions(clk clock.WithTicker) []extension.Option {
	return []extension.Option{
		// Watch, count and serve an extra policy kind
		extension.WithKinds(acmePolicy),
		// Hide more per request; in secure mode it only narrows the viewer's RBAC
		extension.WithAuthorizer(api.AuthorizerFunc(func(r *http.Request, ref objects.Ref) bool {
			return ref.Namespace != "acme-system"
		})),
		// Replace the upstream GatewayParameters resolution
		extension.WithProxyModeResolver(acmeProxyMode{}),
		// Publish a value between recomputes, for checks to read
		extension.WithCollectors(&acmeWidgets{clock: clk}),
		// Add checks; their finding codes start with ACME-
		extension.WithChecks("ACME-", acmeWidgetCheck{}),
		// Serve endpoints under /api/ext/acme/
		extension.WithHandlers("acme", acmeHandlers),
		// Report a feature's state in /api/v1/meta
		extension.WithCapabilities(acmeCapabilities),
	}
}
```

The other options are `WithDetectors`, `WithScopeProvider`, `WithScopeVerifier` and `WithComputedRules`.

- **Checks.** `WithChecks` takes a finding-code prefix, such as `ACME-`. It is a capital letter, then one to fifteen capitals or digits, then a dash. Each prefix registers once, and `KGWUI-` is reserved. A bad or repeated prefix makes `app.Build` fail. In secure mode, a user without the full read sees an extension check's results as "Not checked" and none of its findings.
- **Endpoints.** `WithHandlers("acme", …)` serves its routes under `/api/ext/acme/`, so they never collide with API v1. `Deps.Current` returns the published model. Reply with `api.WriteJSON` and `api.WriteError`.
- **What a caller may see.** A handler that serves cluster objects must filter them through `Deps.View(r)`, the caller's `api.Viewer`. It answers as the core handlers do: `api.VisibleTo` filters a list, `Can` decides a row or count, `CanNamed` a detail page's subject, `CanList` a kind in a namespace, and `Shown` a finding. Then a change to an object the caller cannot read never changes the response. Read an install's Backends with `Viewer.Backends`, not `Install.Owned.Backends`: which Backends an install owns depends on consumer routes the caller may not read. Don't implement `Viewer` yourself: new methods may be added to it.
- **Collectors.** `WithCollectors` starts each collector with the app. It publishes values through `Deps.Publish(key, value, fingerprint, ttl)` or marks them failing with `Deps.Fail`. A check declares the keys it reads as `<collector>/<key>` in `Inputs().Collected` and reads them with `ctx.Collected(key)`. A new fingerprint triggers a recompute. An expired value, or one from a stopped collector, reads as unavailable.
- **Capabilities.** `WithCapabilities` adds entries to `capabilityStates` in `/api/v1/meta`. A state is `on`, `off`, `denied` or `failing`. Names must not clash with core ones. Every user gets the same states, so a source reports only the extension's own state, never anything read from cluster objects.

Kinds added with `WithKinds`, and anything a collector reads, need matching rules in the chart's ClusterRole. Add them with `rbac.extraRules`, as [values-acme.yaml](examples/downstream/chart/values-acme.yaml) does. The chart also takes `extraEnv`, `extraVolumes`, `extraVolumeMounts` and `extraContainers`.

A one-shot caller, such as a CLI or a test, builds its own configuration with `pkg/setup` and runs `cache.Start(ctx)` itself. Cancel `ctx` when done, then call `Cache.Wait()`: it returns once every goroutine `Start` launched has exited. Called before `Start` or a second time, it returns at once.

A caller with `list` but not `watch` RBAC uses `cache.ListOnce` instead. It fills the same `Snapshot` from one LIST per input and never watches. A kind it cannot list shows in `Access` as denied or failed. The error return is only for a missing client or a cancelled `ctx`.

```go
sc, err := setup.Build(clients, setup.Options{MetricsInterval: 10 * time.Second}, extension.New())
// One LIST per input, no WATCH; snap.Mode is cache.ModeList
snap, err := cache.ListOnce(ctx, sc.Cache)
for gvr, a := range snap.Access {
	fmt.Println(gvr.Resource, a.OK, a.Denied) // a kind the caller cannot list
}
```

A caller with namespace-scoped RBAC sets `cache.Config.Namespaces`. Namespaced inputs are then read per namespace; GatewayClass and Namespace stay cluster-wide. `Access` holds the worst state over the namespaces, and `NamespaceAccess` the state per namespace. Snellius itself leaves `Namespaces` empty, which means cluster-wide.

```go
// Read namespaced kinds only in team-a and team-b
sc.Cache.Namespaces = []string{"team-a", "team-b"}
snap, err := cache.ListOnce(ctx, sc.Cache) // the same Config you give cache.New
for gvr, byNS := range snap.NamespaceAccess {
	fmt.Println(gvr.Resource, byNS["team-b"].Denied) // denied in team-b only
}
```

The frontend renders `Shell` from `@snellius/shell` with a `ShellExtension`. `make shell-pack` builds the package as a tarball, and each release attaches it. Its JS uses extensionless imports, so build the app with a bundler such as Vite. [examples/downstream/web](examples/downstream/web) installs the tarball and tests against it; run it with `make example-web`.

```tsx
import { defaultPages, kgatewayWebPack, renderWhenFontsReady, Shell, type ShellExtension } from "@snellius/shell";

const acme: ShellExtension = {
  // Product identity in the header
  brand: { productName: "acme", logo: null },
  // The backend serves kgateway (extension.New): its copy and Delegation tab, shown where /meta lists the pack
  packs: [kgatewayWebPack],
  // Render extra detail for findings from acme's own checks
  findingRenderers: { "ACME-": (f) => <span>{f.message}</span> },
  // Platform copy; entries left out keep the Kubernetes defaults
  vocabulary: { landingTitle: "acme installs" },
  // In-page slots, keyed "group/Kind" where they belong to one kind; WidgetsTab is in acme.tsx
  contributions: {
    detailTabs: {
      "gateway.networking.k8s.io/Gateway": [
        { id: "widgets", label: "Widgets", render: ({ ref }) => <WidgetsTab namespace={ref.namespace} /> },
      ],
    },
  },
};

// Waits for the theme fonts, so a late font swap cannot shift the layout
void renderWhenFontsReady(document.getElementById("root") as HTMLElement, <Shell extension={acme} pages={defaultPages} />);
```

An app that embeds `Shell` must render it with `renderWhenFontsReady(root, element)`. If its themes use other fonts, pass them as the third argument.

A `ShellExtension` can also add `themes`, `defaultThemeId`, `routes`, `navEntries` and `findingsDocUrl`. "Learn more" on a finding links the release tag's [docs/findings.md](docs/findings.md) when `/api/v1/meta` reports a release version, else `main`; a build with its own version sets `findingsDocUrl` to its own copy. Its `contributions` also take `sideSections` per kind, `findingActions` and `installPanels`. A slot with no contribution renders nothing. `CorePages.DiagnosticsSection` replaces the install page's Needs attention and Processes block; it gets the install ID, the diagnostics and whether they are stale.

### Packs and the core

The backend is a Gateway API core plus implementation packs. A pack (`extension.Pack`) bundles one implementation's kinds, detectors, checks, collectors and per-install hooks (`extension.Impl`). The detector that claims an install picks its pack, and that pack's checks and hooks run only on its installs.

- `extension.New(opts...)` is the core plus the kgateway pack; `app.Run` uses it. The `With…` options above set the kgateway pack's hooks.
- `extension.NewCore(opts...)` is the core alone: Gateway API kinds, the core checks and no detectors. `extension.WithPack` adds a pack, which must bring every hook it needs. Serve a registry with `app.RunWith(ctx, kubeconfig, opts, reg)`.
- `cmd/snellius-core` serves the core alone, with class-only installs on. `make build-core` and `make image-core` build it.
- `go test -tags compat ./test/compat/...` smoke-tests a running core-only build against any controller; [its Go doc](test/compat/compat.go) lists the `COMPAT_*` inputs. `web/e2e/compat.spec.ts` is its Playwright half.

**Class-only installs.** A GatewayClass `controllerName` that no detector claims can become an install of its own. Its ID is the controller name with each byte outside `A-Z a-z 0-9 . -` written as `~` and two hex digits: `example.com/gateway` becomes `example.com~2Fgateway`. It is read from Gateway API status alone. Its scope is all namespaces, assumed, and only the core checks run. It has no version, computed views, delegation, data plane or processes. The default binary shows class-only installs only with `--class-only-installs` (chart value `installs.classOnly`, off by default).

### Policy interpreters

A pack reads its own policy kinds through `Impl.PolicyInterpreter`, a `policy.Interpreter`. Per kind, it supplies a label for each spec section, the precedence annotation keys, which ancestor is the controller's synthetic missing-target entry, and a reading of each ancestor condition: a warning or info, a note such as `policy.NoteGatewayWide`, and the wording its finding uses. `policy.Read` asks it only about kinds for which `Handles` is true. In the example, `acmePolicy` is the pack's `kinds.Descriptor` and `acmeDetector` its own `install.Detector`; you supply both.

```go
// acmePolicies reads acme's own policy kinds. Read asks it nothing about other kinds.
type acmePolicies struct{}

func (acmePolicies) Handles(gk objects.GroupKind) bool { return gk.Group == "acme.example.com" }

// A label per spec section; "" shows the key as written
func (acmePolicies) FeatureLabel(_ objects.GroupKind, key string) string {
	return map[string]string{"rateLimit": "Rate limiting"}[key]
}

// Shown as written; Snellius never evaluates them
func (acmePolicies) PrecedenceAnnotations(objects.GroupKind) []string {
	return []string{"acme.example.com/priority"}
}

// The controller's synthetic missing-target ancestor, if it writes one
func (acmePolicies) Summary(objects.GroupKind, objects.ParentRef) bool { return false }

// A condition read as a warning, or a Note* key; the zero Reading reads nothing
func (acmePolicies) Condition(_ objects.GroupKind, _ objects.ParentRef, c metav1.Condition) policy.Reading {
	return policy.Reading{Warning: c.Reason == "Degraded"}
}

var reg = extension.NewCore(extension.WithPack(extension.Pack{
	Name: "acme", Kinds: []kinds.Descriptor{acmePolicy}, Detectors: []install.Detector{acmeDetector{}},
	Impl: extension.Impl{PolicyInterpreter: acmePolicies{}},
}))
```

Without an interpreter, nothing is interpreted: features show their spec keys, no annotation is picked out, no ancestor is a missing-target entry, and conditions get no reading. The detail response then has `interpreted: false`. Raw targets, status entries and relations come from the core either way. The interpreter's readings can change derived results, such as "Has problems", which status counts in the summaries, and the severity and message of a policy's KGWUI-002 (see [docs/findings.md](docs/findings.md)). The kgateway pack's interpreter reads only `gateway.kgateway.dev` kinds, so BackendTLSPolicy and kinds added with `WithKinds` get none. An interpreter labels and reads what is written; it never decides whether a policy applies.

A one-shot caller, such as a CLI, builds the same model with `policy.Read`, `policy.CheckTargets` and `policy.Relations`, and passes its own `policy.Guard` and `policy.Lookup`. It also passes the kinds whose descriptor is `ListOnly`, such as DirectResponse, as `policy.Inputs.ListOnlyKinds`. `Relations` then gives those kinds no `attached-on-ancestor` relation, as the API does.

## Upgrading from kgateway-ui

Snellius is the new name of kgateway-ui. Releases from v0.5.0 use only the new names. v0.4.0 stays available under the old names. The UI holds no state, so a reinstall is cheap.

1. Save your values: `helm get values <release> -n kgateway-ui -o yaml > snellius-values.yaml`. Change any `kgateway-ui` name in it, such as an `image.repository` pin, to `snellius`.
2. Run `helm uninstall <release> -n kgateway-ui`. The selector is immutable and every object name changes, so `helm upgrade` cannot work.
3. Install the new chart with your values: `helm install ui oci://ghcr.io/duncandoyle/charts/snellius --version <version> -n snellius --create-namespace -f snellius-values.yaml`. Settings such as `install.pin`, `httpRoute` and extra RBAC carry over this way.
4. Move image and chart pins to `ghcr.io/duncandoyle/snellius` and `oci://ghcr.io/duncandoyle/charts/snellius`. The old paths get no further releases.
5. Go importers switch to `github.com/DuncanDoyle/snellius` at the first tag on the new path. Old tags still resolve under the old path through the module proxy.
6. npm consumers switch from `@kgateway-ui/shell` to `@snellius/shell`.
7. Rename `KGWUI_MEMORY_LIMIT` to `SNELLIUS_MEMORY_LIMIT`. The old name works for one more minor release and logs a deprecation line.

Saved theme choices carry over. Finding codes keep the `KGWUI-` prefix, so API v1 and the finding anchors do not change.

## Upgrade notes

### v0.9.2

- **Detail pages.** Detail pages show section cards with a section nav, in one fixed order. The right-hand column from 1600 px is gone. Extension side sections now appear in the nav under "Related", with `#ext-<id>` anchors, and a side section that fails shows a short line instead of disappearing. No API fields change.

### v0.9.0: policies (Phase 8)

New Policies pages, policy sections on Gateway, ListenerSet, route and backend pages, and policies in each expanded listener row: see [Policies](#policies). The new endpoints and fields are additive to API v1. Two existing wire values change: policy `link` values, and the IDs of policy-ancestor KGWUI-002 findings. Both are below.

- **New endpoints.** `GET /api/v1/installs/{id}/policies` (filters `kind`, `feature`, `namespace` and `hasProblems`, and the namespace scope `namespaces`, which the `namespace` filter narrows; paging; facets) and `GET /api/v1/installs/{id}/policies/{group}/{kind}/{ns}/{name}`. The audit stream records the list under `summaries` and the detail under `reads`.
- **`policyRelations`.** Gateway, ListenerSet, route and backend detail responses gain `policyRelations`. `/api/v1/meta` `views` lists the policy kinds.
- **Rejected policies.** A current rejection from kgateway (`Accepted=False`) whose `Attached` made the ancestor entry pending or stale used to read "Not yet processed" (KGWUI-003). It now raises KGWUI-002 on kgateway 2.3, 2.4 and 2.5, or KGWUI-016 when the 2.5 rules read `TargetNotFound`. An ancestor entry whose `Accepted` is stale or pending still raises KGWUI-003. An accepted policy no longer reads "Not yet processed" while kgateway's `Attached` is pending or older than the policy. See [docs/findings.md](docs/findings.md) (KGWUI-002, 003 and 016) for the exact rules.
- **Policy reason readings.** Only a pack's policy interpreter reads a policy condition's reason, such as `PartiallyValid` or `Overridden` (see [Policy interpreters](#policy-interpreters)). kgateway policies keep their warning and info findings. A policy kind without an interpreter, such as a downstream kind, used to get them too. Now it gets the generic error as a fallback, whatever the reason. Freshness and the more specific findings, such as KGWUI-016, apply first, and `Pending` is never an error. See KGWUI-002 in [docs/findings.md](docs/findings.md) for the exact rules.
- **DirectResponse findings.** A DirectResponse can now raise KGWUI-002, like a TrafficPolicy. Its install assignment stays list-only, and routes that use it keep their signals. It has a policy detail page like other policies.
- **Policy-ancestor KGWUI-002 IDs (wire change).** The ID now hashes the install, the code, the policy, the ancestor with its section and port, and the condition type, so other ancestors no longer move it. Every existing ID of these findings changes once, so a saved finding link answers `404`.
- **Policy links (wire change).** Every policy `link` is now the policy endpoint path, `/api/v1/installs/{id}/policies/{group}/{kind}/{ns}/{name}`, instead of `yaml`. It is set only when the install owns or lists the policy and the viewer may `get` it by name. Otherwise rows and relations leave it out, and findings carry `none`. A policy that the install only references, such as one a route names from another install, keeps its `yaml` link. This changes the values in findings, diagnostics and finding details. A `@snellius/shell` older than v0.9.0 does not know the new value and shows policy names without a link. Upgrade the shell with the backend.
- **Secure mode: who sees a policy-ancestor KGWUI-002.** A viewer with full read sees these findings without further checks. Any other viewer now also needs access to every object the finding rests on, such as the ancestor, not only to the policy. A hidden second ancestor no longer withholds the finding about a readable one. See KGWUI-002, "Who sees it", in [docs/findings.md](docs/findings.md).
- **Secure mode: authorization outages.** When a permission check fails, rather than denies, a request may answer `503 authz-unavailable`, also when the check was for an object the viewer cannot read. With every check answering, a hidden object changes a response only through install context (see [What each user sees](#what-each-user-sees)). For example, a hidden GatewayClass change can move a Gateway out of the install. That can change a readable policy's summaries, ancestor marks, target links and findings.

### v0.8.2

- **Intro in local mode.** Local mode has no sign-in, so the intro plays on the first page load in each browser tab instead. A reload in the same tab does not replay it.
- **Sign-in intro.** The finished prism and name now hold for about 0.7 s before the intro fades, and the prism's outline closes at its apex. No values, flags or API fields change.

### v0.8.1

- **Sign-in intro.** In secure mode, a fresh sign-in plays a short animation of the Snellius prism while the overview loads. A click or key press skips it, and it never plays with reduced motion, on a reload, or on later navigation. The sign-in redirect now ends in `#signed-in`, which the UI reads once and removes. No values, flags or API fields change.

### v0.8.0: identity and access (Phase 7)

- **Upgrade break: `httpRoute.enabled` needs `auth.mode: secure`.** Up to `v0.7.x`, an HTTPRoute let anyone who reached its hostname see everything the Snellius ServiceAccount can read, with no sign-in. The new default, local mode, listens only on the pod's loopback. An HTTPRoute would then either reach nothing, or, with the listener opened, expose that same unauthenticated view through the gateway. So the chart refuses to render the HTTPRoute in local mode, instead of breaking the route silently or exposing it. Secure mode is the mode for a gateway: users sign in, and each user sees only what their own Kubernetes RBAC allows. Before you upgrade such an install, choose one:
  1. Set `auth.mode: secure` with the `auth.oidc.*` values. See [Secure mode](#secure-mode).
  2. Set `httpRoute.enabled: false`, and reach the UI with `kubectl port-forward`.

  A cluster whose API server cannot accept Snellius's token, such as GKE and AKS with Entra ID, has only the second choice in `v0.8.0` ([#51](https://github.com/DuncanDoyle/snellius/issues/51)).
- **Default mode is `local`.** The UI listens on `127.0.0.1:8080` in the pod, so from outside the pod only `kubectl port-forward` reaches it. Anything that called the UI through the Service or the pod IP, such as another pod or a proxy, no longer reaches it. Upgrades with `--reuse-values` from older releases work.
- **Replicas.** The chart runs one replica. Sessions live in memory, and a restart signs everyone out.
- **Secure-mode ClusterRole.** It gains `create` on `tokenreviews` and `subjectaccessreviews`. Local mode does not get them.
- **Downstream authorisers.** In secure mode, an authoriser that a build registers with `extension.WithAuthorizer` now only narrows what a user may read. Kubernetes RBAC decides first, through SubjectAccessReview. A policy that cannot answer list questions hides everything that depends on `list` permission, such as Pods and Events. Local mode is unchanged.
- **What users see.** Restricted viewers see only findings about their own objects, and "Not checked" where a check needs the full read. See [What each user sees](#what-each-user-sees).
- **Events and CRDs.** The full read does not include them. Probe facts need cluster-wide `list` on Events, and CRD bundle-skew findings need cluster-wide `list` on CustomResourceDefinitions, for every user.
- **Two signals per route.** Route rows, route detail and the Gateway and ListenerSet route tables gain `config` and `path` (with `incomplete`), and the route list takes `config=` and `path=` filters. `health` keeps its meaning as the summary; for a restricted user whose path is incomplete it reads at least `unknown`. New reason kinds: `path-incomplete`, `no-path`, `not-attached`, `path-problem`. An API client without computed views, such as kgwctl, gets `path: unknown` with reason `no-path` for an accepted route without a fault.
- **Request bodies.** In secure mode, a request other than GET or HEAD has its body read before its permission checks: up to 1 MiB within 10 seconds. A larger body answers `413`, an unreadable or stalled one `400`.
- **Probe port.** Liveness and readiness probes use the new `probe` port (8081). `/readyz` no longer lists install IDs; they go to the log.
- **`/metrics`.** The probe listener serves Prometheus text. See [Probe port and metrics](#probe-port-and-metrics).
- **Audit stream.** JSON lines on stdout, set with `--audit-levels` (chart `audit.levels`). See [Audit stream](#audit-stream). Every response from the UI and API listener carries an `X-Request-Id`.
- **API.** `/api/v1/meta` gains `authMode`. `index.html` carries `data-auth-mode`.
- **Sign-in (7.2).**
  - New flags: `--oidc-issuer-url`, `--oidc-client-id`, `--oidc-client-secret-file`, `--oidc-redirect-url`, `--oidc-ca-file`, `--oidc-scopes`, `--cookie-secure`, `--session-idle`, `--session-absolute` and `--trusted-proxy-cidrs`.
  - New chart values: `auth.oidc.*`, `auth.session.*`, `auth.trustedProxyCIDRs` and `hostAliases`. Secure mode needs `auth.oidc.issuerURL`, `clientID` and `redirectURL`.
  - New routes in secure mode: `/auth/login`, `/auth/callback`, `/auth/me`, `POST /auth/logout` and `/signin-failed`. `/auth/status` exists in both modes.
  - The secure-mode ClusterRole gains `create` on `tokenreviews`.
  - Sessions live in memory; the chart keeps one replica. See [Secure mode](#secure-mode).
- **Permission checks (7.3).**
  - New flag `--authz-cache-ttl` (default `60s`, `30s` to `15m`) and chart value `auth.authz.cacheTTL`. It is how long an allowed check is reused, so how long a revoked permission stays visible. A denied check is reused for 4 to 5 minutes.
  - **Named read grants:** objects granted only through `resourceNames` (named) read rules do not appear in lists, counts or summaries. They open on their detail page, and when an object the viewer can read names them. `auth.authz.namedGetScan: true` (flag `--authz-named-get-scan`) also includes them in lists, at the cost of one check per object the viewer cannot list.
  - New flags `--authz-max-in-flight`, `--authz-rate` and `--authz-burst` (chart `auth.authz.maxInFlight`, `rate` and `burst`, defaults 32, 100 and 500) limit checks across all users. See [Capacity and tuning](#capacity-and-tuning).
  - A full admission queue answers `503 authz-busy`, which the page shows as "busy" and retries.
  - The secure-mode ClusterRole gains `create` on `subjectaccessreviews`.
  - New metrics: `snellius_authz_checks_total{verb,outcome}`, `snellius_authz_check_seconds_total`, `snellius_authz_cache_total{result}` and `snellius_authz_identities`.
- **Visibility everywhere (7.4).** See [What each user sees](#what-each-user-sees).
  - In secure mode the API no longer sends `deniedInInstall`, `hidden`, `restricted` entries or the computed `restricted` state. Local mode is unchanged.
  - Install IDs keep the controller's namespace and name in both modes, so install URLs and `--install` values stay the same.
  - In secure mode `/api/v1/meta` asks for the full read. It answers `503 authz-unavailable` when that check fails, like the other routes, and gains `limitedView`.
  - `/installs` and the install header gain `detailsHidden`. Endpoint summaries gain the state `not-permitted`, sent when the user may not list EndpointSlices in the Service's namespace.
  - The seven list routes take `namespaces=`, and `GET /api/v1/installs/{id}/namespaces` is new. See [Namespace scope](#namespace-scope).
  - Extensions: `Deps.View` gives a handler the caller's `api.Viewer`, and a handler that serves cluster objects must use it. Use `Viewer.Backends` in place of `Install.Owned.Backends`. See [Extending](#extending).
- **Chart.**
  - New values `auth.mode`, `auth.allowedHosts`, `audit.levels`, `auth.oidc.*`, `auth.session.*`, `auth.authz.*`, `auth.trustedProxyCIDRs` and `hostAliases`. The [chart README](deploy/helm/snellius/README.md#values) lists each with its default.
  - The chart fails to render for `httpRoute.enabled` in local mode, and for secure mode without `auth.oidc.issuerURL`, `clientID` or `redirectURL`.
  - In local mode the pod gets the annotation `sidecar.istio.io/inject: "false"` and the label `istio.io/dataplane-mode: none`.

### v0.7.1

API v1, additive: Gateway, ListenerSet and route detail gain an optional `reason`, the list row's reason plus `finding`, the deciding finding's id (D-080).

A route that sends traffic to a backend with an error or warning finding, such as a Service with no ready endpoints (KGWUI-023), is now at least a warning and carries the `backend-problem` flag. Its reason names the backend and the finding. The routes list takes `state=backend-problem` (D-082).

### v0.7.0: traffic flow and backends (Phase 6)

API v1 changes are additive. New endpoints: `…/paths`, `…/backends` and `…/backends/{group}/{kind}/{ns}/{name}`. Route detail serves GRPCRoute, TCPRoute and TLSRoute instead of 404, and `BackendRefDTO` gains optional `share`, `shareState`, `problem` and `endpoints`. The install nav gains Traffic and Backends.

### Backend judgement in the kgateway pack (sub-phase 6.3)

Nothing changes in the UI or API v1 for kgateway installs. Class-only installs no longer judge kgateway Backends: no KGWUI-002 on a Backend, and route details show its Accepted condition without a state.

- The Backend status check (KGWUI-002 on Backends) moves from `health.Core()` to `health.Kgateway()`. `health.Upstream()` keeps its order.
- `computed.BackendJudge` is a new optional rule set interface, read through `Selection.BackendJudge()`. The kgateway rule sets implement it with `kgwbackend.Judge`.
- A wrapping rule set forwards `BackendJudge`, as it forwards `TargetStatusReporter`. One that only embeds `computed.Rules` shows Backends without a state.
- `health.BackendState` keeps its signature and meaning; it calls the kgateway judgement directly.

### v0.6.0: generic Gateway API core (Phase 5)

Nothing changes in the UI or API v1 for kgateway installs. Go callers meet two deliberate breaks: controller metrics leave `engine.Config` and `health.Input`, and `computed.Rules` loses `Delegation`. Both are below.

#### Additions

- `extension.NewCore`, `extension.WithPack`, `extension.Pack` and `extension.Impl`, and `app.RunWith` ([Packs and the core](#packs-and-the-core)). `extension.New` keeps its meaning: the core plus the kgateway pack.
- Class-only installs, behind `--class-only-installs` and the chart value `installs.classOnly` (off by default).
- API v1, additive: `features` on each install, `packs` in `/api/v1/meta`, and `label` and `ruleSetLabel` beside a computed fact's rule set.
- The legacy `With…` options set the default pack's hooks. A pack added with `WithPack` must bring the hooks it needs.

#### Frontend: register the kgateway web pack

The Delegation tab and the kgateway copy (such as "No kgateway install found") moved out of the shell's defaults into `kgatewayWebPack`. A downstream app whose backend serves kgateway adds it, or its route pages lose the Delegation tab and `?tab=delegation` opens Overview:

```tsx
import { kgatewayWebPack, type ShellExtension } from "@snellius/shell";

const acme: ShellExtension = {
  // Shown where /api/v1/meta lists the "kgateway" pack; an older server runs every pack
  packs: [kgatewayWebPack],
};
```

Each install's surfaces now follow its `features` from `/api/v1/installs`: the Delegation tab needs `delegation`, computed badges `computed`, the data plane and Processes sections `dataplane` and `processes`.

#### Controller metrics on the collector seam

Controller metrics are now a value of the kgateway pack's `controller-metrics` collector, under `ctrlmetrics.Key`. Checks, the scope verifier and the reported version read them from the collected values only.

For downstream builds and one-shot callers, these names are removed:

| Removed | Use instead |
|---|---|
| `engine.Config.Metrics`, `ctrlmetrics.Static`, `ctrlmetrics.Source` | `ctrlmetrics.Publish(cfg.Collected, ctrlmetrics.CollectOnce(…))`, with `setup.Config.Collected` |
| `engine.Config.MetricsMaxAge` | `ctrlmetrics.Results.MaxAge`, set by `CollectOnce` and the collector |
| `engine.Model.MetricsRead`, `engine.MetricsRead` | `Model.Collected` and `ctrlmetrics.Lookup` |
| `engine.Source.Metrics` | `Source.Collected` |
| `health.Input.Metrics`, `HasMetrics`, `MetricsMaxAge`; `InputSet.Metrics` | declare `ctrlmetrics.Key` in `Inputs().Collected`, then `ctrlmetrics.Lookup` on `ctx.Collected(ctrlmetrics.Key)` |
| `scopeverify.Input.Metrics`, `HasMetrics`, `MetricsMaxAge` | `scopeverify.Input.Collected` |
| `ctrlmetrics.Collector.Get`, `Collector.OnResult` | `Collector.Run` takes a publish function; the store notifies |

- `health.AllInputs()` no longer declares metrics.
- A pack's checks run only on the installs it claimed; a class-only install runs the core checks. `WithChecks` adds checks to the default pack, kgateway in `extension.New`. `Registry.ChecksFor` lists an install's checks.
- `extension.Impl` gains `Reported` (the version the controller reports) and `Conditions` (route condition types beyond Gateway API's). A pack without `RulesFor` gives its installs no rule set: no computed views and no delegation.
- The pack's collector names, `controller-metrics`, `proxy-stats` and `controller-logs`, stay reserved. `WithCollectors` cannot take them.
- A one-shot caller collects once. The `ctrlmetrics` Go doc has the full sequence:

```go
// After setup.Build, a settled cache and one Snapshot (see the setup Go doc)
insts, _ := install.Discover(snap, reg.Detectors)
insts = slices.DeleteFunc(insts, func(i install.Install) bool { return i.Impl != "kgateway" })
ctrlmetrics.Publish(cfg.Collected, ctrlmetrics.CollectOnce(ctx, ctrlmetrics.PodProxyScraper{Kube: kube}, insts, setup.MetricsMaxAge(interval)))
// Compute over that same snapshot
cfg.Engine.Snapshots = fixedSnapshot{snap}
model := engine.New(cfg.Engine).Compute()
```

#### Delegation as an optional rule set capability

For Go callers:

- `computed.Rules` no longer has `Delegation`. Views B and C come from the optional `computed.Delegator`. A caller that holds a `computed.Rules` asserts it first, or uses `Selection.Delegator()`, which is nil without one:

```go
// Before: res := rules.Delegation(in)
var res computed.DelegationResult
if d, ok := rules.(computed.Delegator); ok {
	res = d.Delegation(in)
}
```

- A rule set without `Delegator` keeps view A, its cross-check and KGWUI-012. It has no views B and C, no KGWUI-011, and `GET …/delegation` answers 404. A backendRef to an HTTPRoute is then an ordinary ref, and `install.Resolved.NoDelegation` is set.
- A class-only install never has a rule set, whatever its hooks: status attachment only, no computed facts.
- A rule set that wraps a kgateway set must forward `Delegation` to keep delegation, as it forwards `ReportsTargetNotFound`.
- View A is shared in `pkg/computed/viewa`; `viewa.Attach` takes the set's route kinds per listener protocol.

### v0.5.1 (sub-phase 4.3)

- Data age and "stale" now read the server's clock (from each response's `Date` header), so a cluster clock ahead of or behind the browser no longer marks fresh data stale or hides stale data.

### Stabilization (sub-phase 3.5)

- On kgateway 2.5, a policy selector that matches nothing is a computed KGWUI-016 warning again; named targets still come from the controller's `TargetNotFound`.
- A controller whose version the UI cannot read now runs on the 2.5 rules ("Controller version unknown; using the 2.5 rules").
- KGWUI-018 no longer shows a status-sync count.
- A KGWUI-024 error shows as "Config withheld" in the overview's Data plane column and the Gateway page's Data plane headline, counts in `diagnostics.dataPlane.withheld`, and makes the Gateway's proxies an error on the Processes page.
- Adding or removing only DirectResponse, GatewayExtension or ReferenceGrant CRDs no longer raises KGWUI-008.

For downstream builds:

- `pkg/ctrlmetrics` drops `StatusSyncsTotal` and `PodMetrics.StatusSyncs` / `StatusSyncsPresent`.
- `kinds.Change` gains `ListOnlyAdded`, `ListOnlyRemoved` and `Judged()`; `Added` and `Removed` hold judged kinds only.
- With a rule set that maps Envoy names to objects (`computed.EnvoyNamer`), proxy-stat names in KGWUI-001's evidence are filtered as those objects, as log names are.

### Backends and kgateway 2.5 (sub-phase 3.4)

- New finding codes:
  - KGWUI-023 `backend-no-ready-endpoints` (warning): a route's core Service backend has no ready endpoint on the port it names for 30 s. Only refs the controller resolved count.
  - KGWUI-024 `xds-config-withheld` (kgateway 2.5 only): proxies withheld from config updates (error), or config released before every cluster was ready (warning). On 2.3 and 2.4 it reads "Not available on this version".
- KGWUI-013 explains a missing Service or a missing port, and suggests the port when the Service has exactly one. KGWUI-014 keeps the ReferenceGrant explanation.
- The UI now reads Services and EndpointSlices (`diagnostics.endpoints`, on by default). Both watches are cluster-wide and keep about 250 bytes per object. When the toggle is off, denied or failing, 023 reads "not available".
- kgateway 2.5 has its own rule set. Policy targets come from the controller's `TargetNotFound`, so the UI's computed 016 is off there. The controller version comes from `kgateway_build_info` when it reports one.
- DirectResponse, GatewayExtension and ReferenceGrant are listed and shown. The UI does not judge them, and denied access to them does not affect the install's state. The ClusterRole gains `referencegrants`.
- `pkg/cache` gains `Cache.Wait()`, which returns once every goroutine `Start` launched has exited. `pkg/setup.Options` gains `NoEndpoints`.

For downstream builds:

- A rule set that wraps the 2.5 rule set must forward `computed.TargetStatusReporter`, or 016 falls back to the controller's 002s.
- A process provider that observes no Pods now reports its checks as "Not observed".

### Health overview (sub-phase 3.2)

- KGWUI-001 is re-specified. It is one finding per Gateway, with the rejected xDS types as evidence. Only the controller's `rejects_active` gauge raises it; proxy stats add evidence. When a reject disappears during a controller restart, the `xDS rejects` check stays pending for 60 s, so the install reads unknown, not healthy.
- A route, ListenerSet or policy whose Gateway has a KGWUI-001 is never shown healthy: it is at least a warning and carries the `gateway-rejecting` flag. The routes list takes `state=gateway-rejecting`.
- New finding codes, all warnings:
  - KGWUI-019 `process-restarting`: a controller or proxy container restarted in the last 15 min.
  - KGWUI-020 `probe-failing`: 3 or more probe failures on one Pod in 15 min, from Events.
  - KGWUI-021 `rollout-stuck`: a controller or proxy Deployment passed its progress deadline.
  - KGWUI-022 `version-skew`: proxies on another minor than the controller, controller replicas on two versions, or Gateway API CRDs that do not match the expected bundle.
  - KGWUI-025 `proxy-config-warming`: a Ready proxy still loads config after 60 s.
  - KGWUI-026 `proxy-disconnected`: a Ready proxy lost its controller connection for 60 s.
- New watches: proxy Pods, Warning Events on controller and proxy Pods, and Gateway API CRD metadata. The UI also reads Envoy stats from each proxy Pod on port 9091, so the UI Pod must reach that port.
- Each input has a chart toggle and a flag: `diagnostics.events` (`--diagnostics-events`), `diagnostics.crds` (`--diagnostics-crds`) and `diagnostics.proxyStats` (`--diagnostics-proxy-stats`), all on by default. When one is off, denied or failing, its checks read "not available" with the reason, and the install state does not change. `/meta` lists each running input under `capabilities`.
- `diagnostics.endpoints` (on) grants Services and EndpointSlices; KGWUI-023 reads them from 3.4. `diagnostics.controllerLogs` (off) adds a `pods/log` Role in each of `diagnostics.controllerLogsNamespaces`; the UI then reads controller logs for derived signals (see [docs/findings.md](docs/findings.md)). It keeps fixed fields only and never stores or shows log text.
- A `since` the UI derives, for KGWUI-001 and the new codes, is when the UI first saw the problem. It resets when the UI restarts, except that with controller logs on, KGWUI-001 keeps a `since` from the last 15 minutes of logs.
- New endpoints: `GET /api/v1/installs/{id}/diagnostics` (the verdict, up to 10 ranked groups, the process and data-plane summaries) and `GET /api/v1/installs/{id}/processes` (the controller's processes and a page of Gateways' proxies). The Gateway detail gains `dataPlane` and `proxies`; overview Gateway rows gain `dataPlane`.

For downstream builds:

- Next-step commands come from Go: replace the default kubectl commands with `extension.WithCommands`.
- The install page lists ranked groups of findings, not single findings.
- Finding actions now render on the finding page, not the install page. `FindingRow` still renders them.
- Finding renderers now render on the finding page, through the evidence view, for evidence kinds the shell does not know, or when a finding has no evidence. `FindingRow` still renders them.
- Each `GET /api/v1/installs` entry now has `groupRows`: how many groups the install's diagnostics list for you, at most 10. The install page reserves that many rows while diagnostics load.
- `renderWhenFontsReady` now sets the `font-fallback` class on the root element when its 3 s font wait runs out. The theme then leaves out the web fonts for the session, so a late font does not reflow the page. A theme's font stacks must lead with their web font.

### Findings (sub-phase 3.1)

- Every finding now has an `id`, such as `f1-…`, and a `group`. Both appear in every response, `/health` included, and on restricted rows. Treat the `id` as opaque: compare it, never parse it.
- An `id` survives rewording, a severity change and a UI restart. It can change when another finding of the same code on the same object appears or clears.
- A finding with no status timestamp now has a `since`: when the UI first saw it. That time resets when the UI restarts. Findings from status keep the controller's `lastTransitionTime`. When an `id` changes, its `since` starts again.
- The findings page now pages on the server. Its filters, sort and cursor live in the URL. Old `?sev=` links are ignored.
- New endpoints:
  - `GET /api/v1/installs/{id}/findings`: one page of findings. Filters `severity`, `code`, `kind`, `namespace`, `group` and `q`; sort `rank`, `since` or `code` with `.asc` or `.desc`; `limit` up to 200; `cursor`. A cursor works only for the install and sort it came from, and only until the UI restarts; a rejected cursor returns 400.
  - `GET /api/v1/installs/{id}/findings/{findingId}`: one finding with 50 affected objects per page (`?affectedCursor=`). An unknown id returns 404.
  - `GET /api/v1/installs/{id}/refs?ref=group/Kind/namespace/name`: up to 6 refs, each returned as a link or `none`. The breadcrumbs use it, so a crumb never shows a name you may not see.

### Check contract (sub-phase 3.1)

For downstream builds:

- A check is now `Name()`, `Inputs()` and `Run(ctx health.RunContext)`. `Run` no longer takes a `health.Input`. `RunContext` embeds `Input`, so `ctx.Now` and `ctx.Install` still work.
- `Inputs()` returns a `health.InputSet` naming what `Run` reads. Declare everything `Run` reads. `health.AllInputs()` declares everything except collector keys and earlier checks' output.
- The engine reruns a check only when a declared input changes or its wake time passes, so an undeclared read can serve a stale result.
- Collector values moved out of `health.Input.Collected`. Declare the key in `Inputs().Collected` and read it with `ctx.Collected("<collector>/<key>")`. It returns false for a key you did not declare.
- Keep state between recomputes in `ctx.State`, a per-check `*health.Store`. Replace package-level variables with it. The engine owns the Store, and it is empty after a restart.
- Call `ctx.WakeAt(t)` to be recomputed at a later time. Call `ctx.Output(name)` to read an earlier check's output, and declare that check in `Inputs().Checks`.
- `app.Build` fails when a check declares a check that runs later or not at all, or when two checks share a name.
- Keep passing the finding-code prefix to `WithChecks`, as in 3.0: `WithChecks("ACME-", acmeWidgetCheck{})`.

```go
func (acmeWidgetCheck) Inputs() health.InputSet {
	// Declare exactly what Run reads
	return health.InputSet{Collected: []string{"acme-widgets/widgets"}}
}

func (c acmeWidgetCheck) Run(ctx health.RunContext) ([]health.Finding, health.CheckResult) {
	// Undeclared keys read as missing
	v, ok := ctx.Collected("acme-widgets/widgets")
	// ...
}
```

### Extension seams (sub-phase 3.0)

For downstream builds:

- `extension.WithChecks` now takes a finding-code prefix first: `WithChecks("ACME-", checks...)`. Calls without one no longer compile.
- `app.Build` fails on the reserved `KGWUI-` prefix, a repeated prefix or a malformed one.

### Computed route delegation (sub-phase 2.4)

The UI now computes route delegation, using kgateway 2.3's or 2.4's own rules, and cross-checks the result with the controller's status:

- A parent HTTPRoute has a Delegation tab. Per delegation ref, it lists the routes the ref selects and whether each is attached, dropped or not permitted, and why. Each row opens its paths from a Gateway or ListenerSet.
- A child route shows its effective matches: what it really serves under each parent, after the parent's match is applied.
- The rules list shows selected and attached counts per ref. The routes list flags a child that is selected but not attached.
- KGWUI-011 also fires when the UI computes that a parent drops or does not permit a child, and for a route with no parentRefs that no parent keeps. These are warnings.
- KGWUI-012 now covers delegation: a child's status entry or a parent's `ResolvedRefs` that disagrees with the computation for over 30 seconds.
- When the install's discovery scope is unknown, delegation rows and effective matches read "Unknown" too.
- A child you can see shows its hidden ancestor routes by name and delegated match only.
- Delegation work is capped at 10,000 selected edges per install. Past the cap, parents read "too large to compute". Raise it with `--delegation-edge-cap` or the chart's `delegation.edgeCap`; a higher cap needs more memory.
- The binary sets its Go memory limit to 90 % of the pod's memory limit, unless `GOMEMLIMIT` is set. The chart passes the limit in `SNELLIUS_MEMORY_LIMIT`. The old name `KGWUI_MEMORY_LIMIT` is still read when the new one is unset.

For downstream builds:

- `app.Run` applies the memory limit, so a downstream `main` gets it too.

### Computed listener attachment (sub-phase 2.3)

The UI now computes which routes attach to each listener, using kgateway 2.3's or 2.4's own attachment rules, and cross-checks the result with the controller's status:

- Listener tables and parent cards show a computed route count and each route's effective hostnames, labelled "Computed" and marked whether the controller agrees. The controller's status is always what counts.
- The Gateways list's routes column and sort now use the computed count of the Gateway's own listeners; routes on its ListenerSets are not included.
- The routes list takes `?listener=<name>` with a Gateway or ListenerSet `parent`.
- KGWUI-012 is a new warning: the UI's computation has disagreed with the controller for over 30 seconds. It never turns an install red.
- When the install's discovery scope is unknown, computed counts and outcomes read "Unknown" instead of guessing.
- With per-user authorisation, a computed fact about a route reads "Hidden" when it depends on an object the viewer cannot see. Its value is not shown. Listener counts always show. Like the controller's `attachedRoutes`, they include routes the viewer cannot see.

For downstream builds:

- `extension.WithComputedRules` registers the rule set used for a controller version.
- The chart gains `extraArgs`, extra flags appended to the UI binary's arguments.

### Gateway & Route Explorer (sub-phase 2.2)

Counts and colours change on clusters that use route delegation:

- A delegated child route with a controller status entry for its parent HTTPRoute now counts in the HTTPRoute tile and in install health. Its failures can turn the install yellow or red.
- A route that names an HTTPRoute parent but has no status entry for it gets KGWUI-011: info while the controller may still be processing it, a warning after that. On kgateway 2.3, a child ref that omits the namespace or sets `sectionName` gets no status entry, so it always gets this warning.
- After a child edit drops its parent, the controller clears the child's old status entry within a second. For that moment the child may still count and show KGWUI-003.
- The KGWUI-004 detail names the target's kind, so a route targeting a ListenerSet reads "targets ListenerSet `<ns/name>`".

For downstream builds:

- `CorePages` gains optional `GatewaysPage`, `GatewayPage`, `ListenerSetsPage`, `ListenerSetPage`, `RoutesPage` and `RoutePage` slots. A build that leaves them out gets the upstream pages.
- The install paths `gateways`, `listenersets` and `routes`, and everything below them, are reserved. A `routes` contribution at one of them is dropped, with a console warning.

Pages now retry a failed request every 5 seconds, so they recover within one poll after an API outage. A 400 is not retried.

## Compatibility

- **API v1** (`/api/v1/...` paths and JSON) and the **SPA URLs** (`/i/{id}/...`) are stable from the first release. Install IDs are opaque: clients must not split or parse them.
- **The Go extension API** (`pkg/extension`, and the `install.Detector`, `install.Install` and `install.ScopeProvider` types it carries) may change until the extension-seam milestone after Phase 2. When the provider seam arrives, a Kubernetes `Detector` keeps working as that provider's adapter.

## Supported versions

kgateway v2.3.11, v2.4.7 and v2.5.0, tested on kind in CI. On 2.3 and 2.4 a policy whose target is missing gets no status, so the UI computes "Policy target not found" itself. kgateway 2.5 sets `TargetNotFound`, and the UI reads it from status. The withheld-config check (KGWUI-024) reads metrics that only 2.5 has; on 2.3 and 2.4 it is "Not available on this version".

## Contributing

[CONTRIBUTING.md](CONTRIBUTING.md) covers bug reports, building and pull requests; report security issues as [SECURITY.md](SECURITY.md) explains.

The design, implementation plans, decisions and roadmap live in [`docs/`](docs/). Start with [`docs/roadmap.md`](docs/roadmap.md) and [`docs/phase-status.md`](docs/phase-status.md).

CI's `hygiene` job reads its term list from the repository secret `FORBIDDEN_TERMS`. Same-repo PRs and pushes fail without it; fork PRs, which get no secrets, skip the check with a warning.

### Releasing

Push a semver tag (`vX.Y.Z`). The release workflow publishes the image `ghcr.io/duncandoyle/snellius:X.Y.Z` and the chart `oci://ghcr.io/duncandoyle/charts/snellius` version `X.Y.Z`, then creates the GitHub release.

ghcr creates both packages as private on their first push. After the first tag, make them public once: open each package's settings (`snellius` and `charts/snellius`) and set its visibility to public. Then check that an anonymous pull works:

```sh
# Run logged out of ghcr.io
docker logout ghcr.io; helm registry logout ghcr.io
docker pull ghcr.io/duncandoyle/snellius:X.Y.Z
helm show chart oci://ghcr.io/duncandoyle/charts/snellius --version X.Y.Z
```

## Licence

Apache-2.0.
