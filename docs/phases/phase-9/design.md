# Phase 9 — Search & Route Tester: Design

**Status:** Draft for the Codex design review and the user's approval. No code before approval.
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
| 3 | The kgateway strategy is a registered mirror (P-008 point 3): it extends M-1 | (our call; program mirror rules) | §4.4 |
| 4 | Alignment with the controller is proven by request-level cases in a new public repo, `kgateway-route-conformance` | (user, 2026-11-28; name: user) | §9 |
| 5 | A prediction always carries its source; "Can't predict" replaces any guess | (our call; Codex, 2026-11-28) | §5.3 |
| 6 | In secure mode a prediction needs the full read; others get one uniform "Prediction unavailable for your access" | (our call; D-114, D-085) | §8 |
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
| kgateway, unknown or unverified minor | none for kgateway-only features; Gateway API only when no candidate uses one (§5.3) | "Predicted · Gateway API" or "Can't predict" |
| other implementation (downstream pack without a strategy) | Gateway API | "Predicted · Gateway API" |

The nearest-version fallback of the rule sets (`pkg/computed/rulesets/rulesets.go:21`, D-058) is too loose for predictions (Codex, 2026-11-28): an unverified minor never gets the kgateway strategy (D-142).

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
| 2. Conformance against real controller output per minor | `kgateway-route-conformance` cases run on kind per minor, and the same cases run against the strategy in Snellius's unit tests (§9, §11). This also closes D-128 item 2 for view C |
| 3. Runtime check where controller output exists | Status checks stay (view A, B). The test request shows prediction and response together (§7.5). The Envoy route table check is Phase 12. Rule 3 is partly met until then, and the design says so in the UI note |
| 4. Labelled as computed | "Predicted · kgateway 2.x" (§5.3) |
| 5. Upstream ask, retires when it lands | U2 (`route_source` metadata on by default, kgateway#15067, #15068) plus U10 (delegation chain provenance): with both, Phase 12's observed table answers exactly and view D retires |

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

1. **Listener.** On the port, pick the listener (Gateway or ListenerSet, view A) whose hostname matches the host most specifically, exact before wildcard. No match: "No listener for this host on port N" and stop.
2. **Table.** The strategy builds the listener's ordered table (§3.1), memoised per views generation.
3. **Match.** Walk the table in order; the first entry whose path, method, headers and query match wins. No fall-through to another listener.
4. **Losers.** Every other entry of the listener's virtual host that could be relevant gets its first decisive reason: did not match (which clause), matched but ordered later (which sort step decided), or dropped (delegation reason from view B, or controller-reported `RouteRuleDropped`).
5. **Replacement.** If the controller reports the winning rule replaced (`RouteRuleReplaced`, kgateway `Programmed=False` on 2.4+, `Accepted=False` on 2.3; rules §2), the outcome is "Replaced by the controller", with the status message and a link to its KGWUI-015 finding, not a precedence loss (Codex P3). The reason is controller-reported, not computed.

### 5.3 Outcome and labels

| Outcome | Shown as |
|---|---|
| A winner | "Predicted · Gateway API" or "Predicted · kgateway 2.5", plus the views' snapshot time |
| No match | "No rule matches" (Envoy answers 404), same label |
| Can't predict | the first reason: unverified kgateway minor; unknown setting that matters; a candidate whose view A, B or C result is unknown (rules §3.3); bounds truncated (rules §5.7); stale status on a deciding route; a kgateway-only feature without the kgateway strategy |
| Unavailable | "Prediction unavailable for your access" (§8) |

The Gateway API strategy may answer on a kgateway install without a verified rule set only when no candidate in the virtual host uses delegation, route weights or regex paths (Codex P2: "spec-only fallback when standard behaviour is established for the candidate set").

### 5.4 API (additive to v1)

- `POST /api/v1/installs/{id}/predict` with the request (§5.1); returns the outcome, the source label, the winner (listener, route, rule index and name, effective match, backends with weights) and up to 50 losers by default (our call), each with its reason.
- `POST /api/v1/installs/{id}/test-requests` (§7).
- Both are audit-registered. The prediction is POST because headers can be long; it changes nothing.

### 5.5 IDs

Rule identity is route group/kind/namespace/name plus the rule's `name` when set, else its index. A rule index is spec data the viewer reads (they read the route), not a rank across siblings, so P-011 rule 2 holds. The prediction's table positions are snapshot-local (P-011 rule 6) and never leave the response as IDs.

## 6. Search

**Question it answers:** "Which routes serve `api.example.com/orders`?" across hundreds of hostnames and thousands of paths.

- One index per install and listener context, built from the effective matches (view C) and hostnames, memoised per views generation.
- Query by host (exact or wildcard match, as a listener would), by path prefix or substring, or both. Results are rules, grouped by Gateway and hostname, with their effective match and backends, cursor-paged like `/paths` (`pkg/paths/cursor.go`).
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

### 7.4 Copy as curl

Always available to a viewer who can read the Gateway (it reveals nothing they could not read):

```sh
kubectl -n <proxy-ns> port-forward svc/<proxy-svc> 8080:<listener-port>
curl -v -H 'Host: api.example.com' 'http://localhost:8080/orders/42'
```

HTTPS uses `--resolve api.example.com:8443:127.0.0.1 -k` so SNI is set.

### 7.5 Prediction next to the response

The response panel shows status, timing and Envoy's response headers (such as `x-envoy-upstream-service-time`). A mismatch is a hint, not a verdict: a predicted rule with a 404 links to the route's status; a 503 to a backend with no ready endpoints links to KGWUI-023 (roadmap). The response is a separate observation and never changes the prediction (Codex P1).

## 8. Visibility (D-085, D-114)

- **Prediction.** A prediction reads every route that could compete in the listener's virtual host, including ones in namespaces the viewer cannot read. A hidden route can change the winner, the losers or whether an answer exists. So, like delegation analysis (D-114), a prediction is derived: in secure mode a viewer without the full read gets one uniform answer, "Prediction unavailable for your access", decided by permissions alone. Full readers and local mode see everything (our call; the carry-over item "winning-route predictions must use viewer-scoped computations" is met by withholding, the same way D-114 met it). A per-viewer prediction needs a proof that the viewer reads the whole candidate scope; it is left for later.
- **Search.** Rows follow the D-077 rule: only routes the viewer can read. Counts and facets are over visible rows. A restricted viewer's search never reveals a hidden route's existence through ordering or "shadowed by" notes: search shows matches, not order.
- **Test requests.** Allowed per §7.3 even without a prediction. The response is what the viewer could fetch with port-forward.
- **Copy as curl.** Needs only the Gateway read.

**Hidden-change tests (rule of D-085 point 2):** adding, changing or removing a route in a namespace the viewer cannot read leaves the restricted `predict` and `route-search` responses byte-identical, including whether a block appears and its notes (xdsview D-023 rule 2 pattern).

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
      rule: {index: 0}                  # or name
      chain: [infra/root, team-a/orders] # delegation chain, optional
      backend: orders-v1                # distinct echo backend per rule
  - request: {port: 8080, host: api.example.com, path: /admin}
    expect: {outcome: no-match}
```

Outcomes: `match`, `no-match`, `replaced` (controller direct response), `no-listener`.

### 9.3 Reference runner

- kind cluster per minor, exact patch versions pinned (2.3.11, 2.4.7, 2.5.1 today), CRDs and settings pinned.
- `EnableRouteSourceMetadata` on, where the minor has it (checked 2026-11-28: in `api/settings/settings.go` at v2.4.7, not at v2.3.11). The runner joins the access log's route name and the route's `dev.kgateway.route_source` metadata with each request, so `expect.rule` is proven, not only the backend. Every rule gets its own echo backend that returns its ID. Where the metadata is missing (2.3.11), the runner matches generated route names against the case's rules and fails on ambiguity (Codex P1).
- Waits for reconciliation (status) and an active xDS version before sending.
- Paired requests: for each losing rule the case names, one request that makes it win, so losing reasons are tested too (Codex, 2026-11-28).

### 9.4 Rules for the corpus

- A change to an expected result needs a reason and the runner's evidence (the run, the minor). Expectations are never edited to make a consumer pass.
- First cases come from kgateway's own tests, so maintainers recognise them: the delegation e2e testdata (`test/e2e/features/route_delegation/testdata`: `basic`, `matcher_inheritance`, `header_query_match`, `multiple_parents`, `recursive`, `cyclic`, `invalid_child`, `unresolved_child`, `route_weight`) and the translator inputs (`pkg/kgateway/translator/gateway/testutils/inputs/delegation`: `relative_paths`, `nested_absolute_relative`, `label_based`, `multiple_delegates_per_rule`, `discard_invalid_child_matches`). Plus Gateway API precedence cases: exact vs prefix, longest prefix, method, header and query counts, creation-time ties, a same-listener delegated versus standalone tie, and replacement versus fall-through.
- Apache-2.0 (our call, matching xdsview).
- Codeowner: the user; Snellius's session maintains it until another owner is named.

### 9.5 How Snellius uses it

- A test in `pkg/kgateway` loads the cases at a pinned tag and runs the kgateway strategy over each case's resources: the predicted winner must equal `expect`, per minor. The same loader feeds the Gateway API strategy the cases tagged `gatewayAPI: true`.
- Snellius's kind e2e does not re-run the runner; the repo's CI is the proof per minor (rule 2). P-012: a kgateway bump in Snellius needs the corpus green on that minor first.

## 10. Program and upstream

- **New edge (proposed, not committed):** E16, provider `kgateway-route-conformance`, consumers Snellius Phase 9 (M-1 rule 2, view D), xdsview (route provenance tests), kgwctl (later). Proposed with the user's approval in a program commit after design approval.
- **Mirror register:** M-1 gains view D (§4.4).
- **Upstream asks:** no new ask is filed. U2 and U10 retire view D (§4.4). An upstream adoption of the corpus is the user's call, after the first release of the repo.
- **Phase-start checks (2026-11-28):** program file read at kgateway-tooling `4986792`; no edge Phase 9 needs is open. kgateway#15070 (M-4) and #14815 are still open; no change for Snellius. Open `cross-repo` issues here are all for Phase 12 (#15, #21, #63, #70, #71).
- **P-011 audit (D-040 ranks):** the remaining rank-based IDs are listed in `phase-status.md` with a proposed handling (D-147); Phase 9 adds none.

## 11. Testing

| Boundary | What |
|---|---|
| Strategy unit tests | the conformance cases (§9.5) per minor; spec cases for the Gateway API strategy; each "Can't predict" reason |
| Order | for each sort step (§4.2), a pair that differs only in that step, per minor; 2.5's hostname step absent on 2.3 and 2.4 |
| API | `predict`, `route-search`, `test-requests`: shapes, limits, audit lines |
| Visibility | restricted viewer: uniform "unavailable"; hidden-route add, change and remove leave `predict` and `route-search` byte-identical; send denied without `pods/portforward` |
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
4. **E16 and the M-1 view D entry (§10).** Approve proposing both in the program file once the design is approved?
