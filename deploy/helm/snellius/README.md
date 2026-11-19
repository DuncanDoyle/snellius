# Snellius Helm chart

Installs Snellius, a read-only UI for Kubernetes Gateway API, built for kgateway. It installs a Deployment, a ClusterIP Service, a ServiceAccount, a ClusterRole and its binding, and optionally an HTTPRoute.

## Security and trust model

Snellius runs in one of two modes, set with `auth.mode` (default `local`).

- **Local mode.** There is no sign-in. The UI and API listen on `127.0.0.1:8080` in the pod, so only `kubectl port-forward` and other containers in the pod reach them. Sidecars and `extraContainers` share the pod network and can call the API, so add only containers you trust with everything Snellius can read. Whoever can port-forward to Snellius sees everything its ServiceAccount can read, which can be more than their own Kubernetes RBAC allows. A request with an unknown Host gets 421, a cross-origin request 403. Add Host names with `auth.allowedHosts`. The chart refuses `httpRoute.enabled` in local mode.
- **Secure mode.** Users sign in with OIDC. Each signed-in user sees only what their Kubernetes RBAC allows. Without a session, `/api/` answers `401`. Setup steps are in the [README](https://github.com/DuncanDoyle/snellius#secure-mode).
  - The issuer and the redirect URL must be https. A redirect on `localhost` or `127.0.0.1` can use http, with `auth.session.cookieSecure: false`.
  - The API server must accept the client ID as an audience, either because the cluster already trusts that client ID or through `audiences` in its structured authentication configuration.
  - Sessions live in memory. The chart runs one replica, and a restart signs everyone out.
  - The secure-mode ClusterRole gains `create` on `tokenreviews` and `subjectaccessreviews`.
  - An allowed permission check is reused for `auth.authz.cacheTTL` (default `60s`), a denied one for 4 to 5 minutes. Lists show only objects a user may `list`. The defaults are the secure choice. The [README](https://github.com/DuncanDoyle/snellius#secure-mode) explains how to tighten or relax them. Its [Capacity and tuning](https://github.com/DuncanDoyle/snellius#capacity-and-tuning) section says when to raise `auth.authz.maxInFlight`, `rate` and `burst`.
  - The chart fails to render for `httpRoute.enabled` in local mode, and for secure mode without `auth.oidc.issuerURL`, `clientID` or `redirectURL`.
  - Audit records go to stdout as JSON lines. See [Audit stream](https://github.com/DuncanDoyle/snellius#audit-stream).
  - Secrets and tokens never reach the browser or the logs.
- The ClusterRole never grants Secrets. That is the UI's whole secret-handling guarantee: no other field is redacted, and Deployments and other objects are shown as stored.

## Install

```sh
# Install a released chart; --version picks the chart and the matching image
helm install ui oci://ghcr.io/duncandoyle/charts/snellius --version 0.8.2 \
  -n snellius --create-namespace
kubectl -n snellius port-forward svc/ui-snellius 8080:80
```

To install from a checkout, set the image tag to a released version. The in-repo `appVersion` is not bumped on release, so without it the templates from your checkout run an older image.

```sh
helm install ui ./deploy/helm/snellius -n snellius --create-namespace \
  --set image.tag=<version>
```

## Values

| Key | Default | Description |
|---|---|---|
| `image.repository` | `ghcr.io/duncandoyle/snellius` | Image repository. |
| `image.tag` | `""` | Image tag; defaults to the chart `appVersion`. Required for a local-path install. |
| `image.pullPolicy` | `IfNotPresent` | Image pull policy. |
| `install.pin` | `""` | Pin the UI to one controller install: an install ID (e.g. `kgw.kgateway`) or `namespace/name`. Empty shows all installs. |
| `installs.classOnly` | `false` | Also show one install per GatewayClass `controllerName` that no detector recognises, from Gateway API status alone. |
| `delegation.edgeCap` | `0` | Selected delegation edges computed per install, passed as `--delegation-edge-cap`; `0` keeps the binary's default (10000). A higher cap needs a higher `resources.limits.memory`. |
| `diagnostics.events` | `true` | Watch Warning Events for controller and proxy Pods. Off drops the `events` rule; checks that need it report "not available". |
| `diagnostics.crds` | `true` | Watch Gateway API CRD metadata (bundle version and channel). Off drops the `customresourcedefinitions` rule. |
| `diagnostics.proxyStats` | `true` | Read Envoy stats from each proxy Pod on port 9091. Needs no RBAC; the UI Pod must reach proxy Pods on 9091. Off skips the reads. |
| `diagnostics.endpoints` | `true` | Grants `services` and `endpointslices`, which the endpoint counts and KGWUI-023 read. Off drops both rules. |
| `diagnostics.controllerLogs` | `false` | Read controller logs for derived signals. Adds a `pods/log` `get` Role in each namespace below. The UI keeps fixed fields and never stores or shows log text. |
| `diagnostics.controllerLogsNamespaces` | `[kgateway-system]` | Namespaces that get the `pods/log` Role. Only used when `diagnostics.controllerLogs` is on. |
| `auth.mode` | `local` | `local` or `secure`. Local mode has no sign-in and listens on loopback only. Secure mode signs users in and serves each one what their RBAC allows. `httpRoute.enabled` needs it. |
| `auth.allowedHosts` | `[]` | Host names accepted in local mode besides `localhost`, `127.0.0.1` and `[::1]`. Passed as `--allowed-hosts`. |
| `auth.oidc.issuerURL` | `""` | Secure mode: OIDC issuer URL, https. Required. Passed as `--oidc-issuer-url`. |
| `auth.oidc.clientID` | `""` | Secure mode: OIDC client ID. Required. The API server must accept it as an audience. |
| `auth.oidc.redirectURL` | `""` | Secure mode: `https://<host>/auth/callback`. Required. Register it, and `https://<host>/` as the post-logout redirect URI, at the IdP. |
| `auth.oidc.scopes` | `[]` | Secure mode: OIDC scopes. Empty uses `openid`, `profile`, `email`, `groups` and `offline_access`. |
| `auth.oidc.clientSecret.existingSecret` | `""` | Secret holding the client secret, mounted under `/etc/snellius/oidc/secret/`. Empty is a public client. |
| `auth.oidc.clientSecret.key` | `client-secret` | Key inside that Secret. |
| `auth.oidc.ca.existingConfigMap` | `""` | ConfigMap with a PEM bundle for an IdP behind a private CA, mounted under `/etc/snellius/oidc/ca/`. |
| `auth.oidc.ca.key` | `ca.pem` | Key inside that ConfigMap. |
| `auth.session.idle` | `""` | A session ends after this long without a request. Empty uses `30m`. |
| `auth.session.absolute` | `""` | A session ends this long after sign-in. Empty uses `8h`. |
| `auth.session.max` | `""` | Most sessions held at once; past it the one unused longest ends. Empty uses `1000`. Passed as `--session-max`. |
| `auth.session.cookieSecure` | `true` | Mark the session cookie Secure. Set `false` only for plain-HTTP testing. |
| `auth.authz.cacheTTL` | `""` | How long an allowed permission check is reused, `30s` to `15m`. Empty uses `60s`. It is how long a revoked permission stays visible in Snellius; a longer TTL means fewer checks. A denied check is reused for 4 to 5 minutes. Passed as `--authz-cache-ttl`. |
| `auth.authz.namedGetScan` | `false` | Also show in lists, counts and facets the objects a viewer may only `get` by name (`resourceNames` grants). Off by default: it costs one check per hidden object. Passed as `--authz-named-get-scan`, secure mode only. |
| `auth.authz.maxInFlight` | `""` | Most permission checks Snellius sends to the API server at once, across all viewers. Empty uses `32`. Passed as `--authz-max-in-flight`, secure mode only. |
| `auth.authz.rate` | `""` | Most permission checks Snellius sends to the API server per second, across all viewers, `1` to `10000`. Empty uses `100`. Raise it when many restricted viewers use a large cluster. Passed as `--authz-rate`, secure mode only. |
| `auth.authz.burst` | `""` | Permission checks Snellius may send above `rate` before the rate applies. Empty uses `500`, so one cold page does not wait on the rate. Passed as `--authz-burst`, secure mode only. |
| `auth.trustedProxyCIDRs` | `[]` | Secure mode: CIDRs of proxies whose `X-Request-Id` and `X-Forwarded-For` Snellius trusts. It sets the audit `source` and the request ID only, never who can sign in. Empty makes the Gateway's address the audit source. Passed as `--trusted-proxy-cidrs`. |
| `hostAliases` | `[]` | Pod `hostAliases`, for an IdP name that is not in DNS. |
| `audit.levels` | `[security, agent, reads, summaries]` | Audit levels written to stdout as JSON lines, passed as `--audit-levels`. Also available: `disclosure` and `requests`, both off by default. |
| `extraArgs` | `[]` | More flags for the UI binary, appended after the chart's own args, e.g. `--set extraArgs={--poll-interval=10m}`. |
| `httpRoute.enabled` | `false` | Create an HTTPRoute on an existing Gateway. Needs `auth.mode: secure`. Read [Security and trust model](#security-and-trust-model) first. |
| `httpRoute.parentRefs` | `[]` | Gateway parentRefs for the route. See [Security and trust model](#security-and-trust-model). |
| `httpRoute.hostnames` | `[]` | Hostnames for the route. See [Security and trust model](#security-and-trust-model). |
| `serviceAccount.create` | `true` | Create the ServiceAccount. |
| `serviceAccount.name` | `""` | ServiceAccount name; defaults to `<release>-snellius`. |
| `rbac.extraRules` | `[]` | PolicyRules appended to the ClusterRole. The operator's choice: they can widen RBAC beyond the read-only default. |
| `extraEnv` | `[]` | Extra env vars for the UI container, after the chart's own. |
| `extraVolumes` | `[]` | Extra pod volumes. |
| `extraVolumeMounts` | `[]` | Extra volume mounts for the UI container. |
| `extraContainers` | `[]` | Extra containers (sidecars) in the pod. They share the pod network, so in local mode they can reach the UI and API. Bind add-ons to the UI's identity through `serviceAccount.name`. |
| `memoryLimit` | `""` | Opt-in Go soft memory limit, set as `GOMEMLIMIT` (for example `300MiB`). Empty sets none, so the binary uses 90 % of the pod's memory limit. |
| `resources` | 50m CPU / 128Mi requested, 512Mi memory limit | Container resources. The chart sets `GOMEMLIMIT` only from `memoryLimit`: the binary uses 90 % of the pod's memory limit unless `GOMEMLIMIT` is set. |
| `podSecurityContext` | `runAsNonRoot`, `RuntimeDefault` seccomp | Pod security context. |
| `securityContext` | no privilege escalation, read-only root FS, all capabilities dropped | Container security context. |

## Probes and metrics

The container has a second port, `probe` (8081), served by `--probe-listen`. Liveness uses `/healthz` and readiness uses `/readyz`; neither lists install IDs. `/metrics` serves Prometheus text. The [README](https://github.com/DuncanDoyle/snellius#probe-port-and-metrics) lists the series. If a NetworkPolicy limits traffic to the pod, allow your scraper to reach port 8081. The main port also answers `/healthz` and `/readyz` in local mode behind the Host guard. The chart's probes use the `probe` port. `/metrics` is on the probe listener only.

## Recompute

The UI recomputes its model when a watched object, an access state, a metrics scrape or the served kinds change, debounced to at most about one recompute per second. The binary's `--poll-interval` (default 30s; the chart sets it only through `extraArgs`) is only the safety timer: the longest time between recomputes. Setting it to `2s` restores the earlier fixed 2 s cadence as a floor.

## RBAC

The ClusterRole grants only `get`, `list` and `watch`:

- `gateway.networking.k8s.io`: gatewayclasses, gateways, httproutes, grpcroutes, tcproutes, tlsroutes, backendtlspolicies, listenersets.
- `gateway.networking.x-k8s.io`: xlistenersets.
- `gateway.kgateway.dev`: all resources.
- Core: namespaces, pods. `apps`: deployments.
- Core `events`, `apiextensions.k8s.io` `customresourcedefinitions` (metadata only) and core `services` with `discovery.k8s.io` `endpointslices`, each under its `diagnostics` toggle.

No Secrets, no ConfigMaps, no `*/proxy` and no `pods/portforward` access. `pods/log` is granted only with `diagnostics.controllerLogs`, in the listed namespaces. Pods and Deployments are only listed and watched through the controller detectors' label selectors. RBAC cannot express a label restriction, so the grant itself is cluster-wide.

Kinds added downstream through `extension.WithKinds` need matching rules in this ClusterRole.

## NetworkPolicy

In-cluster, the UI scrapes controller metrics directly from the controller pods on port 9092. If a NetworkPolicy blocks that traffic, the UI reports the signal as unavailable (KGWUI-008) instead of showing it green. Allow ingress from the UI pod to the controller pods on 9092 to get metrics-based checks. Proxy stats need the UI Pod to reach port 9091 on proxy Pods.
