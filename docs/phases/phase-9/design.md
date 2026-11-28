# Phase 9 — Search & Route Tester: Design

**Status:** Revised after the Codex design review (`reviews/design-codex.md`, 14 findings, all accepted; "design review #n" below). Waiting for the user's approval. No code before approval.
**Date:** 2026-11-28
**Base:** `31c5094` (main after `v0.9.1`, merged into `duncandoyle/phase9Design`; the design adds no code)
**Roadmap:** `docs/roadmap.md`, Phase 9; principles 4, 9 and 10
**Decisions:** D-020, D-024, D-058, D-064, D-085, D-114, D-126, D-128 in `docs/decisions.md`. This design adds D-139 … D-147 (§13).
**Code refs:** Snellius `file:line` at `31c5094`; kgateway at the tag named, or main `34e314f527` (2026-11-24).
**Ships as:** `v0.10.0` (our call).

**One home per contract.** Strategies: §3. The kgateway strategy and its mirror: §4. Prediction model and API: §5. Search: §6. Test requests: §7. Visibility: §8. The conformance repo: §9. Program and upstream: §10. Testing: §11.

## Decisions for this phase

| # | Decision | Marker | Where |
|---|---|---|---|
| 1 | Route prediction uses strategies: a Gateway API strategy and a kgateway strategy now; the proxy's Envoy route table (xdsview) in Phase 12 | (user, 2026-11-28) | §3 |
| 2 | The kgateway strategy covers delegation, matcher inheritance and kgateway's route order, including route weights | (user, 2026-11-28) | §4 |
| 3 | The kgateway strategy is a registered mirror (P-008 point 3): it extends M-1, with mirror rule 3 deferred to Phase 12 by an explicit exception | (our call; program mirror rules; design review #5) | §4.4 |
| 4 | Alignment with the controller is proven by request-level cases in a new public repo, `kgateway-route-conformance` | (user, 2026-11-28; name: user) | §9 |
| 5 | A prediction always carries its source; "Can't predict" replaces any guess | (our call; Codex, 2026-11-28) | §5.3 |
| 6 | In secure mode a prediction needs the full read; others get one uniform "Prediction unavailable for your access" and search declared matches only | (our call; D-114, D-085; design review #4) | §6, §8 |
| 7 | Test requests: GET, HEAD and OPTIONS only; off by default (D-064) | (user, 2026-11-28) | §7 |
| 8 | Sending needs `create` on `pods/portforward` in the proxy's namespace, checked with the viewer's identity | (user, 2026-11-28: b1) | §7.3 |
| 9 | HTTPRoute only. GRPCRoute, TLSRoute and TCPRoute prediction are out | (our call) | §2 |
| 10 | #26 (access-log line) and #27 (certificate per SNI) stay out of Phase 9 | (our call) | §2 |

## 1. Intent and why

An operator with a failing or surprising request asks: "which listener, route and rule handle this request, and why not the one I expected?" Today they read HTTPRoutes by hand, follow delegation chains, and guess kgateway's ordering. Then they run `kubectl port-forward` and `curl` to see what really happens.

Phase 9 answers that question in the UI. The user gives a host, path, method and headers. Snellius shows the winning listener, route and rule, the backends, and the rules that lost, each with the first reason it lost. Optionally, Snellius sends the request and shows the prediction next to the real response.

**Why strategies (user, 2026-11-28).** HTTPRoute delegation is used by a large share of kgateway users. A spec-only prediction would say "can't predict" for most of them. Snellius also aims to stay Gateway API compliant and usable with other implementations. So the Gateway API rules are the base strategy, and kgateway's rules are a second strategy owned by the kgateway pack. The exact source, the proxy's own route table, comes in Phase 12. The user's view: implementing the documented rules that compose the final matchers is not copying controller logic, in the same way that implementing the spec's precedence rules is not. The program still treats it as a mirror (P-008 point 3), so it follows the mirror rules (§4.4).

## 2. Goals, non-goals and anti-goals

**Goals**
1. "Why does this request go here?": for host, path, method, headers and query, the winning Gateway listener, route and rule, and its backends (roadmap).
2. The rules that lost, and shadowed or conflicting rules, each with its first decisive reason (roadmap).
3. Search across hundreds of hostnames and thousands of paths (roadmap).
4. Opt-in: send the request to the Gateway listener and show prediction and response side by side (roadmap; D-064).
5. Copy as curl: a `kubectl port-forward` plus `curl` pair that reproduces the request (roadmap).
6. A conformance corpus that proves the kgateway strategy matches the controller per supported minor, and that meets M-1's open rule 2 (D-128 item 2).

**Non-goals**
- Policy effects on a request: policy inheritance or merge through delegation, filters that reroute (rewrite, redirect, ext-proc), auth outcomes. Policies stay as Phase 8 shows them (D-121).
- Which weighted backend serves one request. The prediction lists the backendRefs with their weights.
- GRPCRoute, TLSRoute and TCPRoute prediction (our call). GRPCRoute is the natural next step; the strategy interface leaves room for it.
- Reading the proxy's Envoy config (Phase 12).
- #26, the access-log line for a test request: it needs `pods/log` and a known access-log format. It fits the Phase 11 metrics and logs work (our call).
- #27, the certificate presented per SNI: it belongs with Phase 10, Security Posture (our call). Phase 9 sends to HTTPS listeners with SNI set (§7.2), without a certificate view.
- Saved test suites, re-runs or load tests (roadmap).

**Anti-goals** (failure even if it works)
- A prediction shown without its source, or a kgateway-specific answer labelled as Gateway API.
- A guess where an input is unknown, a version is unverified, or a feature is unsupported.
- A prediction, losing reason, search result or "unavailable" notice that changes when an object the viewer cannot read changes (D-085).
- A test request the viewer could not have sent with their own RBAC.
- Falling through to another listener after a route miss: Envoy does not, so Snellius does not.

## 3. Strategies

### 3.1 The interface

A strategy predicts, for one Gateway listener context and one request, an ordered rule table and the first match. It is pack-owned like the rule sets (`extension.Pack.RulesFor`, `pkg/extension/pack.go:39`) and the policy interpreter (`policy.Interpreter`, `pkg/policy/policy.go:171`): the core passes explicit inputs, the pack returns results.

```go
// Names are left to the builder; the shape is the contract.
type Predictor interface {
    // Source is the label: "Gateway API" or "kgateway 2.5".
    Source() Source
    // Table builds the ordered route table for one listener context from the
    // computed views (A, B, C) and the install's settings. It is memoised per
    // views Generation (computed.Views.Generation).
    Table(in TableInput) (Table, Unpredictable)
    // Match evaluates one request against a table: winner, losers with reasons.
    Match(t Table, r Request) Prediction
}
```

- The **Gateway API strategy** lives in the core (`pkg/predict`, our call). It reads only spec semantics and Gateway API objects.
- The **kgateway strategy** lives in the kgateway pack, per minor, next to the rule sets `kgw23`–`kgw25`. It reuses views B and C for delegation and adds kgateway's order (§4).
- A downstream pack may register its own strategy. Without a pack strategy, the Gateway API strategy applies.

### 3.2 Which strategy answers

| Install | Strategy | Label |
|---|---|---|
| kgateway, minor with a verified rule set (2.3, 2.4, 2.5) | kgateway | "Predicted · kgateway 2.5" |
| kgateway, unknown or unverified minor | none | "Can't predict: rules not verified for <minor>" |
| other implementation (downstream pack without a strategy) | Gateway API | "Predicted · Gateway API" |

The nearest-version fallback of the rule sets (`pkg/computed/rulesets/rulesets.go:21`, D-058) is too loose for predictions: minors order even plain HTTPRoutes differently (§4.2 step 2), so an unverified minor gets no answer (D-142; design review #3).

### 3.3 Phase 12

Phase 12 adds an observed source: the proxy's loaded route table through xdsview, labelled "Observed · proxy, config version, capture time" (Codex P4). It is a cross-check of the prediction, and later the preferred answer where its provenance and the viewer's access are complete. Phase 9 keeps the `Source` type open for it.

## 4. The kgateway strategy

### 4.1 What it composes

| Step | kgateway source | Snellius today |
|---|---|---|
| Route → listener attachment | `query/route.go` per tag | view A (rules §4) |
| Delegation child selection, permission | `query/httproute.go` `getDelegatedChildren`; `delegation_helpers.go` `filterDelegatedChildren` | view B (rules §5) |
| Effective matcher, default fit and inheritance | `utils/delegation` `IsDelegatedRouteMatch`, `mergeParentChildRouteMatch` | view C (rules §6) |
| One entry per match; per virtual host by hostname | `translator/listener/gateway_listener_translator.go` (main :1187) | new |
| Route order | `translator/routeutils/sortable_route.go` | new |

### 4.2 kgateway's order (from `sortable_route.go`)

Within one virtual host, the controller sorts route entries, one per match, in this order:

1. **Precedence weight**, higher first: the route's `kgateway.dev/route-weight` annotation, used only with `KGW_WEIGHTED_ROUTE_PRECEDENCE=true` (EP-11466; `api/settings/settings.go:326`; `api/annotations/routes.go:8`). Each entry takes the weight of the HTTPRoute that holds the rule, so a delegated child uses its own annotation, not its parent's; an invalid value counts as 0 (`pkg/krtcollections/policy.go:1508`, `translator/httproute/gateway_http_route_translator.go:128`). The setting exists at v2.3.11, v2.4.7 and v2.5.1.
2. **Route hostname specificity** (2.5 only: `CompareHostnameSpecificity`; absent at v2.3.11 and v2.4.7).
3. **Path**: Exact before RegularExpression before PathPrefix; between two prefixes, or two exact paths, the longer first; two regexes are not ordered by length. This differs from the spec, which leaves regex order to the implementation.
4. A **method** match before none; then more **headers**; then more **query params**.
5. **Ties**: the older root route (`creationTimestamp`), then the root's namespace/name; for delegated entries, then the older originating child, then its namespace/name; then the entry's position.

Codex's summary (2026-11-28) adds that delegation gives no blanket priority: delegated and standalone entries share one sort.

The file differs per minor (checksums at v2.3.11, v2.4.7 and v2.5.1 differ; 2.5 adds step 2). Each rule set cites its tag's lines, as the rule sets do today (rules §1 principle 1).

### 4.3 Settings it reads

`install.Settings` (`pkg/install/settings.go:46`) gains `WeightedRoutePrecedence`, read like the other `KGW_*` settings (D-015).

- Unknown setting and no candidate route carries `kgateway.dev/route-weight`: the setting cannot change the order, so the prediction stands.
- Unknown setting and a candidate carries the annotation: "Can't predict: route weights may apply".

### 4.4 Mirror rules (P-008 point 3)

| Rule | How Phase 9 meets it |
|---|---|
| 1. Registered | Extend M-1 with view D, "route order and request matching", naming `sortable_route.go` and the listener translator per tag, kgateway 2.3–2.5 (proposed program commit, §10) |
| 2. Conformance against real controller output per minor | `kgateway-route-conformance` cases run on kind per minor, and the same cases run against the strategy in Snellius's unit tests (§9, §11). For view C, D-128 item 2 also needs the matcher itself compared: a Snellius test compares view C's effective matches with kgateway's cited translator goldens per tag (§9.5), because winner checks alone can pass with a wrong matcher on inputs no case sends (design review #6) |
| 3. Runtime check where controller output exists | Views A and B keep their status cross-checks. For view D, the only controller output is the proxy's route table (Phase 12); a test response carries no route identity. **Phase 9 cannot meet rule 3 for view D.** It needs a program exception, recorded before 9.5 ships: view D ships with rule 3 deferred to Phase 12, where a disagreement between prediction and observed table is a KGWUI-012-style finding and an absent table leaves the prediction labelled "not checked against the proxy" (design review #5; open question 4) |
| 4. Labelled as computed | "Predicted · kgateway 2.x" (§5.3) |
| 5. Upstream ask, retires when it lands | U2 (`route_source` metadata on by default, kgateway#15067, #15068) and U10 (delegation chain provenance). Retirement is by coverage, not by the asks alone: view D retires when the observed table answers for supported installs and viewers, or the user decides to drop config-only prediction then. Until then P-008 point 6 holds: Snellius works without xDS, so prediction stays (design review #14) |

## 5. Prediction model and API

### 5.1 Request

| Field | Notes |
|---|---|
| Gateway | required; a Gateway the viewer can read |
| Port | optional; required when the Gateway has HTTP listeners on several ports |
| Host | the `Host` header, also the SNI for HTTPS listeners |
| Path and query | raw, as sent |
| Method | any method for prediction; sending is limited (§7) |
| Headers | name/value pairs |

### 5.2 Steps

The strategy owns every step from port to winner, because kgateway's grouping differs per minor (design review #1).

1. **Filter chain.** The request's port (and, for HTTPS, SNI) selects the filter chain: all HTTP listeners of the Gateway and its ListenerSets on that port share one route table set. No HTTP listener on the port: "No listener on port N".
2. **Virtual hosts.** As kgateway builds them: every route attached to any listener on that port contributes entries under each of its hostnames intersected with its listener's hostname (`translateHttpFilterChain`, v2.3.11 `gateway_listener_translator.go:686-745`; per minor). A route attached to a wildcard listener can compete in an exact host's virtual host.
3. **Virtual host for the request.** Envoy's domain match on the `Host` header: exact, then the longest suffix wildcard, then `*`. No virtual host: "No virtual host for this host" (Envoy answers 404).
4. **Table.** The strategy orders the virtual host's entries (§4.2), memoised on every input it reads (design review #7): views A–C, the weight setting, `kgateway.dev/route-weight` annotations, creation timestamps and the rule set.
5. **Normalise.** The path is normalised as Envoy does with kgateway's defaults (RFC 3986 normalisation and slash merging, `translator/irtranslator/fc.go:259-269` at main). If a ListenerPolicy on the listener changes those settings, or any other request-handling setting the strategy does not model, the answer is "Can't predict" (design review #8).
6. **Match.** Walk the table in order; the first entry whose path, method, headers and query match wins. No fall-through to another virtual host or listener.
7. **Losers.** Entries of the same virtual host get their first decisive reason on this request: did not match (which clause), matched but ordered later (which sort step decided), or dropped (delegation reason from view B, or controller-reported `RouteRuleDropped`). The response holds at most 50 losers, the total count and `truncated: true` when cut; a "show all" request returns the rest, cursor-paged (design review #10).
8. **Shadowed rules.** A rule is "fully shadowed" when an earlier entry in the same table matches every request it matches. Snellius reports this only when it can prove it: earlier path type and value cover it (Exact equal, or a Prefix that contains it), and the earlier entry has no method, header or query condition the later one lacks. Otherwise it reports nothing; regex paths are never judged (design review #10).
9. **Replacement and controller direct responses.** If the winning entry belongs to a parent report with a current `RouteRuleReplaced` condition, Snellius says "The controller may have replaced this rule" with the status message and its KGWUI-015 link, as a parent-level fact. It names the replaced rule only when the condition's message identifies exactly one entry; otherwise rule attribution is unknown (design review #2). A delegation parent rule emits no route of its own; an unresolved delegation parent may answer 500. Both are cases in the corpus (§9.4).

**Unsupported competitors.** A GRPCRoute in the same virtual host is translated into the same table by kgateway (`krtcollections/grpc_route.go`). Phase 9 does not order GRPCRoute entries, so its presence in the virtual host gives "Can't predict: a GRPCRoute competes on this host" (design review #8).

### 5.3 Outcome and labels

| Outcome | Shown as |
|---|---|
| A winner | "Predicted · Gateway API" or "Predicted · kgateway 2.5", plus the views' snapshot time |
| No match | "No rule matches" (Envoy answers 404), same label |
| Can't predict | the first reason: unknown or unverified kgateway minor; unknown setting that matters; a ListenerPolicy request setting; a GRPCRoute competitor; a candidate whose view A, B or C result is unknown (rules §3.3); bounds truncated (rules §5.7); stale status on a deciding route |
| Unavailable | "Prediction unavailable for your access" (§8) |

On a kgateway install, only the kgateway strategy of a verified minor answers. An unknown or unverified minor is always "Can't predict", even for plain HTTPRoutes: minors order ordinary routes differently (2.5 adds hostname specificity) (design review #3; D-142). The Gateway API strategy answers for installs of other implementations.

### 5.4 API (additive to v1)

- `POST /api/v1/installs/{id}/predict` with the request (§5.1); returns the outcome, the source label, the winner (filter chain, virtual host, listener, route, rule, effective match, backends with weights), the losers (§5.2 step 7) and shadowed rules (step 8).
- `POST /api/v1/installs/{id}/test-requests` (§7).
- Both are audit-registered. The prediction is POST because headers can be long; it changes nothing.

### 5.5 IDs

A **named** rule's ID is the route's group, kind, namespace and name plus the rule's `name`: it is stable across edits and may be linked and joined across tools (P-011 rule 1). An **unnamed** rule is a snapshot-local occurrence (P-011 rule 6): its index is shown as evidence ("rule 2 in the spec") and links go to the route, never to the rule. Inserting a rule before it changes the index; the UI says so. Table positions never leave the response as IDs (design review #11).
## 6. Search

**Question it answers:** "Which routes serve `api.example.com/orders`?" across hundreds of hostnames and thousands of paths.

- Two indexes per install, memoised on their inputs: a **declared** index over each route's own hostnames and matches as written, and an **effective** index over the virtual-host entries (view C effective matches, hostnames intersected with listeners).
- Full readers and local mode search the effective index. In secure mode, a viewer without the full read searches the declared index of the routes they can read: its rows depend only on objects they read, so a hidden parent cannot change them (design review #4; D-114). The result says which index answered.
- Query by host (exact or wildcard match, as a listener would), by path prefix or substring, or both. Results are rules, grouped by Gateway and hostname, with their match and backends, cursor-paged like `/paths` (`pkg/paths/cursor.go`).
- A result row links to the route tester with the fields filled in.
- **Budget (our call):** the index build is memoised per views generation and added to the existing engine bench, which keeps its current regression gate; a query answers in under 50 ms at 1,000 hostnames × 10,000 paths. Measured in 9.4, not estimated.
- `GET /api/v1/installs/{id}/route-search?host=&path=&cursor=` (additive).

## 7. Test requests

### 7.1 Guardrails (D-064, kept)

1. Off by default; chart value `routeTester.send.enabled` (name left to the builder).
2. GET, HEAD and OPTIONS only (user, 2026-11-28). Other methods stay out of Phase 9.
3. Targets: listeners of Gateways the UI watches. No free URL.
4. No redirects followed; connect timeout 2 s and total timeout 10 s; the response body is capped at 64 KiB and shown as text up to 8 KiB (our call).
5. Every request is audit-logged: who, install, Gateway, listener, method, host, path, status, duration. Header values are not logged (our call; they may hold credentials).

### 7.2 Where it goes

From the Snellius pod to the Gateway's proxy Service on the listener port, with `Host` set; HTTPS listeners get SNI = host and no certificate verification, labelled "TLS not verified" (our call; #27 adds the certificate view later). A self-managed Gateway, or one whose Service Snellius cannot find, cannot be sent to: "No proxy Service found for this Gateway" (inferred: the Service is found through the Gateway's deployment, as Phase 6 finds proxies).

### 7.3 Who may send (user, 2026-11-28: b1)

- **Local mode:** anyone who reaches the UI, as for everything else (D-086), with the chart value on.
- **Secure mode:** a SubjectAccessReview for the viewer: `create` on `pods/portforward` in the proxy's namespace. That permission is in the built-in `edit` and `admin` roles, not in `view`. The reasoning: a viewer with it can already send the same request with "Copy as curl". Snellius grants nothing new and adds the audit line.
- The check uses the existing SubjectAccessReview checker and its cache (`pkg/auth/sar`, D-099). A denied viewer sees the send button disabled with the reason "Sending needs permission to port-forward to the proxy in <namespace>".

- **Target resolution is Snellius's, not the viewer's** (design review #9). Snellius finds the proxy Service with its own inventory. The `pods/portforward` check is the agreed gate (user), not a proof that the viewer could find the Service themselves; the response never names the Service or Pod unless the viewer can read it (`get` on that Service).

### 7.4 Copy as curl

Available to a viewer who can read the Gateway. It names the proxy Service only if the viewer can read it; otherwise it shows `<proxy-service>` for them to fill in (design review #9):

```sh
kubectl -n <proxy-ns> port-forward svc/<proxy-service> 8080:<listener-port>
curl -v -X GET -H 'Host: api.example.com' -H 'x-team: a' 'http://localhost:8080/orders/42?debug=1'
```

- It reproduces the request as sent: method, every header, the raw path and query, shell-quoted. HTTPS uses `--resolve <host>:8443:127.0.0.1 -k` so SNI is set.
- **Acceptance:** a unit test parses the generated command and compares method, headers, raw path and query with the request; the e2e runs it against a real proxy and compares the echo backend's view of the request (design review #10).

### 7.5 Prediction next to the response

The response panel shows status, timing and Envoy's response headers (such as `x-envoy-upstream-service-time`). A mismatch is a hint, not a verdict: a predicted rule with a 404 links to the route's status; a 503 to a backend with no ready endpoints links to KGWUI-023 (roadmap). The response is a separate observation and never changes the prediction (Codex P1).

## 8. Visibility (D-085, D-114)

- **Prediction.** A prediction reads every route that could compete in the listener's virtual host, including ones in namespaces the viewer cannot read. A hidden route can change the winner, the losers or whether an answer exists. So, like delegation analysis (D-114), a prediction is derived: in secure mode a viewer without the full read gets one uniform answer, "Prediction unavailable for your access", decided by permissions alone. Full readers and local mode see everything (our call; the carry-over item "winning-route predictions must use viewer-scoped computations" is met by withholding, the same way D-114 met it). A per-viewer prediction needs a proof that the viewer reads the whole candidate scope; it is left for later.
- **Search.** Restricted viewers search the declared index of routes they can read (§6). Counts and facets are over visible rows. No order, "shadowed" note or effective match reaches them.
- **Test requests.** Allowed per §7.3 even without a prediction. **A live response is an observation of real traffic, not something Snellius computes**: a hidden route can change it, as it would change the viewer's own `curl`. D-085 point 2 covers what Snellius derives from objects; the live response is outside it, because the viewer gets the same bytes through port-forward with the same permission (design review #9; D-144). The panel labels it "Live response".
- **Copy as curl.** Needs the Gateway read; the Service name only with the Service read (§7.4).

**Hidden-change tests (rule of D-085 point 2):** adding, changing or removing a hidden route, a hidden delegation parent, a hidden Gateway or a hidden ListenerSet leaves the restricted `predict` and `route-search` responses byte-identical, including whether a block appears and its notes (xdsview D-023 rule 2 pattern; design review #4).

## 9. `kgateway-route-conformance`

A new public repo, https://github.com/DuncanDoyle/kgateway-route-conformance (created by the user 2026-11-28, empty). It holds request-level cases that state kgateway's route composition and order: given these resources, this request reaches this listener, route and rule, or nothing. Precedent: the Gateway API conformance suite. Snellius, xdsview and kgwctl consume it; kgateway may adopt it later.

### 9.1 Contents

```text
schema/   case format, JSON Schema, versioned (v1alpha1)
cases/    one directory per case: Kubernetes YAML plus case.yaml
go/       loader: decode and validate only (no predictor)
runner/   reference runner: applies a case on kind, sends the requests,
          checks the winner; CI per kgateway minor
```

Not in it: any predictor, xDS snapshots, vendor terms.

### 9.2 Case format (sketch; fields final in 9.1)

```yaml
schemaVersion: v1alpha1
name: delegation-matcher-inheritance
source: kgateway test/e2e/features/route_delegation/testdata/matcher_inheritance.yaml  # where the case came from
kgateway: {min: "2.3", max: ""}          # minors it holds for; overrides per minor allowed
settings: {KGW_WEIGHTED_ROUTE_PRECEDENCE: "false"}
resources: [gateway.yaml, routes.yaml, backends.yaml]
createOrder: [routes.yaml#parent, routes.yaml#child-b, routes.yaml#child-a]  # for tie cases
requests:
  - request: {port: 8080, host: api.example.com, method: GET, path: /orders/42, headers: {x-team: a}}
    expect:
      listener: {gateway: infra/gw, name: http}
      route: {kind: HTTPRoute, namespace: team-a, name: orders}
      rule: {index: 0}                  # declared index in the route's spec, or name
      chain: [infra/root, team-a/orders] # delegation chain, optional
      backend: orders-r0                # echo backend unique to this declared rule
  - request: {port: 8080, host: api.example.com, path: /admin}
    expect: {outcome: no-match}
```

Outcomes: `match`, `no-match`, `replaced` (controller direct response), `no-listener`.

### 9.3 Reference runner

- kind cluster per minor, exact patch versions pinned (2.3.11, 2.4.7, 2.5.1 today), CRDs and settings pinned.
- **Declared identity comes from the fixture, not from Envoy names** (design review #6). kgateway compacts delegated rules and matches before numbering them, so an emitted `_rule-0` can be a declared rule 1, and `route_source` metadata omits the delegation chain. So every forwarding rule in a case sends to its own echo backend, whose response names the declared route and rule. A rule that does not forward (redirect, direct response) is identified by its distinct response (status, header, body) and marked "identified by response".
- **Supporting evidence:** `EnableRouteSourceMetadata` on where the minor has it (checked 2026-11-28: in `api/settings/settings.go` at v2.4.7, not at v2.3.11). Any join of Envoy route names or metadata to declared rules goes through xdsview's provenance mapping (P-001), and only as a diagnostic when a case fails.
- **Unprovable cases:** if two candidate rules share a response, the runner reports the request "unprovable" and CI fails unless the case declares it, so no expectation rests on ambiguous evidence.
- Waits for reconciliation (status) and an active xDS version before sending.
- Paired requests, where they tell matcher behaviour apart: a request that makes a named loser win. They do not prove why a rule lost; loser reasons are asserted on the original request in Snellius's tests (§9.5; design review #13).

### 9.4 Rules for the corpus

- A change to an expected result needs a reason and the runner's evidence (the run, the minor). Expectations are never edited to make a consumer pass.
- First cases come from kgateway's own tests, so maintainers recognise them: the delegation e2e testdata (`test/e2e/features/route_delegation/testdata`: `basic`, `matcher_inheritance`, `header_query_match`, `multiple_parents`, `recursive`, `cyclic`, `invalid_child`, `unresolved_child`, `route_weight`) and the translator inputs (`pkg/kgateway/translator/gateway/testutils/inputs/delegation`: `relative_paths`, `nested_absolute_relative`, `label_based`, `multiple_delegates_per_rule`, `discard_invalid_child_matches`). Plus Gateway API precedence cases: exact vs prefix, longest prefix, method, header and query counts, creation-time ties, a same-listener delegated versus standalone tie, and replacement versus fall-through. Plus the cases design review #1–#3 named: same-port listeners where a wildcard listener's route competes in an exact host, a miss in the selected virtual host, 2.5's hostname-specificity step against 2.4, a clean delegation-only parent (no route of its own), an unresolved delegation parent (500), a fully shadowed rule, and a request where several sort steps differ.
- Apache-2.0 (our call, matching xdsview).
- Codeowner: the user; Snellius's session maintains it until another owner is named.

### 9.5 How Snellius uses it

- A test in `pkg/kgateway` loads the cases at a pinned tag and runs the kgateway strategy over each case's resources: the predicted winner must equal `expect`, per minor. The same loader feeds the Gateway API strategy the cases tagged `gatewayAPI: true`.
- **Loser reasons** are asserted in Snellius's own tests on the original request: the first decisive reason per loser, complete shadowing, and cases where several sort steps differ (design review #13).
- **Matcher conformance (D-128 item 2):** a Snellius test reads kgateway's translator goldens for the delegation cases at each pinned tag (`pkg/kgateway/translator/gateway/testutils/outputs/delegation`) and compares view C's effective matches with the emitted route matches, after mapping compacted indexes through the case's backend markers (design review #6).
- Snellius's kind e2e does not re-run the runner; the repo's CI is the proof per minor (rule 2).
- **P-012 bump checklist** (design review #12): moving Snellius to a new kgateway minor needs, in the same change, the corpus green on that minor, a new rule set and strategy reviewed against the tag's `sortable_route.go`, listener translator and delegation helpers, the CRD and kind-list review, and the M-1 register's pinned minors updated.

## 10. Program and upstream

- **New edge (proposed, not committed):** E17, provider `kgateway-route-conformance`, consumers Snellius Phase 9 (M-1 rule 2, view D), xdsview (route provenance tests), kgwctl (later). Proposed with the user's approval in a program commit after design approval.
- **Mirror register:** M-1 gains view D (§4.4), with a recorded exception to mirror rule 3 until Phase 12 (open question 4). The exception is a program change, approved by the user before 9.5 ships.
- **Phase close (coordination protocol):** E17 needs the corpus's first tag and a `cross-repo` notice to each consumer; P-006 needs the site live before the release counts as done; P-007 a snellius-demo scenario for a delegated route.
- **Upstream asks:** no new ask is filed. U2 and U10 support view D's retirement (§4.4). An upstream adoption of the corpus is the user's call, after the first release of the repo.
- **Phase-start checks (2026-11-28):** program file read at kgateway-tooling `4986792`; no edge Phase 9 needs is open. kgateway#15070 (M-4) and #14815 are still open; no change for Snellius. Open `cross-repo` issues here are all for Phase 12 (#15, #21, #63, #70, #71).
- **P-011 audit (D-040 ranks):** the remaining rank-based IDs are listed in `phase-status.md` with a proposed handling (D-147); Phase 9 adds none.

## 11. Testing

| Boundary | What |
|---|---|
| Strategy unit tests | the conformance cases (§9.5) per minor; spec cases for the Gateway API strategy; each "Can't predict" reason (incl. ListenerPolicy normalisation settings, a GRPCRoute competitor, an unverified minor with plain routes) |
| Order | for each sort step (§4.2), a pair that differs only in that step, per minor; 2.5's hostname step absent on 2.3 and 2.4; same-port listener overlap per minor |
| Loser reasons | asserted on the original request; full shadowing; several differing sort steps; truncation at 50 with the total |
| Memoisation | changing only the weight setting, only a `route-weight` annotation, or only a creation order updates an already cached prediction |
| Matcher | view C against kgateway's translator goldens per tag (§9.5) |
| API | `predict`, `route-search`, `test-requests`: shapes, limits, audit lines |
| Visibility | restricted viewer: uniform "unavailable"; declared-index search; hidden route, delegation parent, Gateway and ListenerSet add, change and remove leave `predict` and `route-search` byte-identical; send denied without `pods/portforward`; Service name hidden without the Service read |
| Copy as curl | the parsed command equals the request (method, headers, raw path, query); e2e echo comparison |
| Test requests | method allow-list; no redirect; timeouts; body cap; target limited to watched listeners (an attempt at another host or port is rejected); audit line without header values |
| e2e (kind matrix) | prediction against the real proxy for a few corpus cases through the send path; send disabled by default |
| Web | Playwright: tester form, result, losers, search, copy as curl; axe light and dark |
| Bench | index build and query (§6 budget) |

## 12. Data-plane cost

None. Snellius reads configuration and sends single requests on demand. Each test request is one request through the proxy; no xDS or config change.

## 13. Proposed decisions

- **D-139** Route prediction strategies (decisions 1, 2, 5).
- **D-140** The kgateway strategy is M-1 view D, under the mirror rules (decision 3).
- **D-141** `kgateway-route-conformance` is the alignment proof; contents and rules per §9 (decision 4).
- **D-142** No nearest-version fallback for predictions (§3.2).
- **D-143** Predictions are derived: full read only in secure mode (decision 6, §8).
- **D-144** Test requests: GET/HEAD/OPTIONS, `pods/portforward` check, guard values (decisions 7, 8, §7).
- **D-145** HTTPRoute only; #26 and #27 out (decisions 9, 10).
- **D-146** Route weights: `KGW_WEIGHTED_ROUTE_PRECEDENCE` read and `kgateway.dev/route-weight` evaluated for prediction; D-126 stays for policy annotations (§4.3).
- **D-147** P-011 audit result for D-040 ranks (§10).

## 14. Sub-phases

9.1 corpus and strategies; 9.2 prediction API and visibility; 9.3 test requests and copy as curl; 9.4 search and web; 9.5 docs and program. Details in `implementation-plan.md`.

## 15. Left to the builder

Package and type names; the chart value name; the exact UI layout within the existing route and Gateway pages; how many losers the UI shows before "show more".

## 16. Open questions (for the user)

1. **Secure mode for application teams (§8).** Withholding predictions from viewers without the full read follows D-114, but application teams lose the tester's main feature in secure mode. Accept for Phase 9, with a per-viewer prediction designed later?
2. **#26 and #27 out (§2).** Agree to place #26 in Phase 11 and #27 in Phase 10?
3. **HTTPS without verification (§7.2).** Send to HTTPS listeners with certificate checks off and a "TLS not verified" label, or HTTP listeners only in Phase 9?
4. **E17, M-1 view D and the rule 3 exception (§4.4, §10).** View D cannot be checked against controller output until Phase 12 reads the proxy's route table. Approve proposing E17, view D and an explicit exception to mirror rule 3 until Phase 12 in the program file once the design is approved?
5. **Live responses and D-085 (§8).** A hidden route can change a live test response, as it changes the viewer's own `curl`. Accept that live responses are outside D-085 point 2, because the viewer gets the same bytes through port-forward with the same permission?

- 2026-11-28: The program registered E16 for kgwctl bundle mode (snellius#74, kgateway-tooling `e1d5cc6`; owner: right after Phase 9). The edge proposed here for `kgateway-route-conformance` is renumbered E17 in this design, plan, decisions and status; the Codex review file keeps its original text (E16).
