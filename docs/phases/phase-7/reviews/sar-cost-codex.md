# Review: permission-check cost after the 7.3 bench
Reviewer: Codex (cross-model). Input: the proposal and bench report below.

## Proposal and bench

### Permission-check cost: options after the 7.3 bench

#### Bench result (Large fixture: 100 namespaces; restricted viewer granted 10 of them)
Full numbers: the bench report (pasted below the proposal for the reviewer).
- /routes needs ~2,685 SARs per TTL for a viewer whose `list` is denied (2,348 of them named `get`s, one per hidden row), ~2,450 with namespace-level `list` grants. Cap is 500 per request, so /routes answers 503 cold and on ~1/3 of polls after each TTL.
- Steady state with namespace grants: 460–4,899 SARs/user/min on list pages vs design target 50.
- Cache bounds (10,000 decisions × 2,000 identities × 349 B) allow ~6.5 GiB vs D-057's 48 MiB budget.
- Cold page at 2 ms per SAR ≈ 1.1 s (checks are sequential).

Root cause: design §6.1 list-then-get asks a named `get` for every object in a namespace where `list` is denied, to honour `resourceNames` grants. Cost scales with hidden rows, not with what the viewer can see.

#### Options (combinable)

A. **Drop the named-get fallback by default.** A namespaced object is visible iff the viewer may `list` its resource in its namespace (cluster-scoped: `list` cluster-wide). `resourceNames`-only grants then show nothing (fail closed: under-showing, never over-showing). Optional flag to re-enable named `get` for clusters that use `resourceNames` read grants. Cost becomes (kinds × namespaces on the page): /routes ~329 checks per TTL.

B. **Longer TTL for denials than for allows.** A stale deny only hides something newly granted; a stale allow is the security risk (D-099). E.g. allow 30 s, deny 5 min (inverse of the kube webhook authorizer defaults, which cache allow 5 min / deny 30 s — because there a stale allow was judged acceptable). Most checks for a restricted viewer are denials, so steady state drops ~10×.

C. **Namespace-first pruning.** Check `list namespaces`/`get namespace` first; skip all per-kind checks in namespaces the viewer cannot see at all?? (Not valid: RBAC can grant pods in a namespace without granting get on the Namespace object. Listed to be rejected or corrected.)

D. **Parallel checks** within a request (bounded, e.g. 16 in flight) to cut cold latency; keep the per-request cap but raise it to ~2,000 with a global concurrency/rate limit to the API server.

E. **Memory:** a global LRU bound on total decisions (e.g. 100,000 ≈ 35 MiB) across identities instead of per-identity × identity counts; and/or lower per-identity to 2,000.

F. **SelfSubjectRulesReview** with the user's own token per namespace (one call returns all rules for that namespace; `incomplete: true` when a non-RBAC authorizer is involved → fall back to SAR). Kubernetes documents it as not suitable for authorization decisions by external systems; listed for completeness.

G. **Revise the target**: 50 SARs/user/min was an assumption (§6.4), not a requirement; the real constraint is API-server load and the 500 cap.

#### Author's tentative recommendation
A + B + D + E, G (new target stated per page), F rejected. Expected: /routes ~329 checks cold (under the cap), steady state ~(33 allowed × 2/min + 296 denied / 5 min) ≈ 125/min; memory bounded at ~35 MiB.

#### Questions for the reviewer
1. Is dropping the named-get fallback by default acceptable for a least-privilege UI (it can only hide), and is a flag the right escape hatch?
2. Is a longer deny TTL sound? Any case where a stale deny causes a wrong *allow* elsewhere (e.g. list-denied → get-allowed logic, full-read aggregation, path completeness in 7.4/7.5 where "denied" might mean "incomplete" vs "absent")?
3. Option C: is there any correct namespace-level pruning?
4. Better ideas: e.g. one `list` check per (resource, namespace) shared by all pages and identities with the same groups (cache keyed by group set?) — is sharing across identities with identical username-independent rules ever safe? (Probably not: RBAC can bind users directly.)
5. What does D-057 need: a global byte budget for the decision cache?
### Task 21 report: Bench, denied-list workload

Status: DONE_WITH_CONCERNS
Commit: b7c838e9f8d6098f32adacff2936b9b29b8e21c4 (branch duncandoyle/phase7Design, on 29586ee, not pushed)

#### Changes

| File | Change |
|---|---|
| pkg/api/export_bench_test.go (new, `//go:build bench`, package `api`) | `LargeHandlers(tb)`: computes the Large tier once (snapshot sorted as the cache sorts it) and returns a builder `func(Authorizer, ...HandlerOption) http.Handler` with a fresh path memo, the bounded synthetic scoped reader and the kgateway backend consumers, as `backendHandler`; `DeepestChild` exports `deepestChild` |
| pkg/api/sar_bench_test.go (new, `//go:build bench`, package `api_test`) | `TestBudgetSARDeniedList`, report-only. Real `sar.Authorizer` (default 30 s TTL, fake clock), handler behind `WithSecureMode(true)`, fake `Checker` counting list/get with an injected sleep. `benchKinds` mirrors `pkg/app` `sarKinds`: `All` = served upstream kinds + Namespace, Pod, Deployment, Service (not EndpointSlice/Event/CRD, per the Task 20 fix round); `Lookup` maps all seven core kinds |

Makefile unchanged: `make bench` runs `-run 'Budget'` over `./pkg/api`, which matches.

#### Method

- Viewers. Granted namespaces are one in ten (`ns-000`, `ns-010`, … `ns-090` of 100). **Denied-list**: every `list` denied; `get` allowed on objects in granted namespaces (and on the granted Namespaces themselves), so `resourceNames`-style grants only. **Namespace grants**: `list` allowed in granted namespaces, everything else denied. **Full read**: everything allowed.
- Pages: `/routes`, `/gateways`, `/backends`, `/paths` (default page size), the deepest route detail visible to the restricted viewers (`ns-010/r-01859`, depth 3), and a visible Service backend detail (`ns-000/r-02294`; visible Services have one use each at Large).
- Cold = a new identity's first request; warm = the same identity's next request inside the TTL. "Uncapped demand" runs the same viewer through a handler without the commit wrapper (no 500 cap) after an explicit `FullRead`.
- Latency: denied-list viewer; cold = a fresh identity per request (50 requests at 0 ms, 12 at 2 ms per page); warm = one identity primed until 200, then 200 requests. Fake latency is a `time.Sleep` per SAR.
- Per minute: one namespace-grants user polls one page every 5 s (fake clock) for 2 minutes; the second minute is counted.
- Memory: `HeapAlloc` after two GCs around filling one identity with exactly 10,000 decisions (100 lists + 9,900 named gets); identity overhead from 2,000 identities × 1 decision.
- Buffered response = the response body the commit wrapper holds (`bytes.Buffer` may hold up to ~2× while growing).
- Iterations reduced, not tiers: 12 cold requests at 2 ms (p95 of 12 ≈ the max). Run time 78 s.

#### Numbers (Large, run 2; run 1 within a few SARs and ±0.2 ms)

### Denied-list viewer: SARs and the 500 cap

| Page | Cold SARs → status | 500 cap hit | Warm SARs → status | Polls to 200 (SARs) | Uncapped demand (full-read / list / get) |
|---|---|---|---|---|---|
| /routes | 500 → 503 | yes | 500 → 503 | 6 (2,682) | 2,685 (8 / 329 / 2,348) |
| /gateways | 261 → 200 | no | 0 → 200 | 1 (261) | 261 (2 / 124 / 135) |
| /backends | 500 → 503 | yes | 500 → 503 | 3 (1,049) | 1,049 (2 / 312 / 735) |
| /paths | 500 → 503 | yes | 336 → 200 | 2 (836) | 836 (2 / 313 / 521) |
| route detail | 14 → 200 | no | 0 → 200 | 1 (14) | 14 (2 / 5 / 7) |
| backend detail | 500 → 503 | yes | 340 → 200 | 2 (840) | 839 (1 / 315 / 523) |

Full-read SARs vary 1–8 between runs: up to 8 workers start before the first denial stops the evaluation.

### Denied-list viewer: latency (ServeHTTP)

| Page | Cold p50 / p95 at 0 ms | Cold p50 / p95 at 2 ms | Warm p50 / p95 at 0 ms | Warm p50 / p95 at 2 ms |
|---|---|---|---|---|
| /routes (cold 503) | 1.32 / 2.69 ms | 1,102 / 1,109 ms | 4.89 / 5.57 ms | 4.92 / 5.47 ms |
| /gateways | 0.38 / 0.90 ms | 579 / 585 ms | 0.21 / 0.78 ms | 0.29 / 0.88 ms |
| /backends (cold 503) | 1.48 / 2.36 ms | 1,103 / 1,133 ms | 1.69 / 3.19 ms | 1.77 / 3.11 ms |
| /paths (cold 503) | 0.94 / 2.43 ms | 1,102 / 1,134 ms | 1.08 / 2.58 ms | 1.12 / 2.51 ms |
| route detail | 0.12 / 0.17 ms | 28.9 / 60.5 ms | 0.10 / 0.13 ms | 0.12 / 0.15 ms |
| backend detail (cold 503) | 0.93 / 2.23 ms | 1,101 / 1,126 ms | 0.97 / 2.20 ms | 1.04 / 2.51 ms |

Per-object checks run one after another, so a cold request costs about SARs × SAR latency (500 × 2.2 ms ≈ 1.1 s to reach the cap). Warm requests make no SARs, so their latency does not depend on SAR latency.

### Namespace grants: SARs per user per minute at a 5 s poll (target 50, design §6.4)

| Page | First poll | Steady-state SARs/min | Polls answered 200 (of 12) | vs 50 |
|---|---|---|---|---|
| /routes | 500 → 503 | 4,899 | 8 (run 1: 12) | ~98× |
| /gateways | 236 → 200 | 460 | 12 | ~9× |
| /backends | 500 → 503 | 1,520 | 12 | ~30× |
| /paths | 500 → 503 | 1,423 | 12 | ~28× |
| route detail | 10 → 200 | 18 | 12 | within |
| backend detail | 500 → 503 | 1,428 | 12 | ~29× |

Every list page misses the target. With `list` denied outside the viewer's namespaces, §6.1 asks a named `get` per hidden object, so a cluster-wide page costs about one SAR per hidden row every TTL. On /routes the demand per TTL (~2,450) exceeds the cap, so after each expiry the page answers 503 until enough polls have refilled the cache.

### Memory

| Measure | Value |
|---|---|
| Heap per cached decision (one identity, 10,000 entries) | 349 B (3.33 MiB for the identity) |
| Per identity besides its decisions | 886 B |
| Projection: 2,000 identities × 10,000 decisions (the cache bounds) | 6,660 MiB |
| Projection: 2,000 identities × 2,000 decisions (≈ /routes demand) | 1,333 MiB |

Against D-057 (48 MiB retained heap over the 2.1 baseline, +46.6 MiB at the last measured phase close (decisions.md)) the bounds allow over 100× the whole budget. One identity at the 10,000 bound (3.3 MiB) already exceeds the remaining ~1.4 MiB of headroom.

### Largest buffered response per page (one per in-flight request)

| Page | Full read, default page | Full read, `?limit=200` | Denied-list viewer |
|---|---|---|---|
| /routes | 78.5 KB | 319 KB | 36.0 KB |
| /gateways | 13.1 KB | 25.9 KB | 2.7 KB |
| /backends | 48.1 KB | 129 KB | 12.4 KB |
| /paths | 69.9 KB | 271 KB | 40.9 KB |
| route detail | 4.3 KB | — | 2.7 KB |
| backend detail | 2.2 KB | — | 0.8 KB |

The largest is 319 KB (/routes at the maximum limit), so up to ~0.6 MiB with `bytes.Buffer` growth. Buffers are transient, not retained heap: 20 concurrent worst-case requests hold about 12 MiB. That is small next to the decision cache.

#### Gates

- `go test -tags bench -run TestBudgetSARDeniedList ./pkg/api`: pass, 78 s (twice).
- `make bench` pkg/api steps (full make bench skipped: the pkg/api budgets alone took 86 min locally). Budgets: all pass except `TestBudgetSpecEditStream/large-contradicting-worst`, process CPU 30.8–31.1 % vs ≤ 25 %. It fails identically on a clean HEAD 29586ee worktree (31.1 %), so it is pre-existing and not caused by this task. Benchmarks (`-bench . -count 1`): pass, 35 s.
- `go test ./...`: pass. `make lint`: 0 issues; golangci-lint with `--build-tags bench` on `./pkg/api`: 0 issues. `go vet -tags bench ./pkg/api`: clean.

#### Concerns

1. The 50 SARs/user/min target is missed on every list page by 9–98×. With namespace-level grants the per-object path still asks a named `get` per hidden row (§6.1), so cost scales with hidden rows, not with the viewer's namespaces. That needs a design decision (e.g. answer hidden rows by a namespace `list` denial alone when no `resourceNames` grant can apply, or accept the cost).
2. Four of six pages hit the 500 cap cold for the denied-list viewer; /routes and /backends still 503 on the second poll, and /routes for namespace-grant users answers 503 on about a third of polls after each TTL expiry.
3. The decision cache's bounds allow ~6.5 GiB (2,000 × 10,000 × 349 B), far past D-057. The per-identity bound or the identity bound likely needs lowering, or a global byte cap.
4. Cold latency at a realistic 2 ms SAR is ~1.1 s per capped request, because per-object checks are sequential.
5. Full-read SAR counts are nondeterministic (1–8) by design of the early stop; reported, not asserted.
6. `TestBudgetSpecEditStream/large-contradicting-worst` fails its CPU budget (31 % vs 25 %) on HEAD 29586ee without this change. It is pre-existing, here and probably in CI.

## Review

Recommend **A + B + E, a narrower D, and G**. Keep the 500 cap initially. The proposed 35 MiB cache does **not** fit D-057.

| Option | Verdict | Concrete recommendation |
|---|---|---|
| **A** | **Accept with modification** | Default enumeration to `list` permission; disable scanning hidden rows with named `get`. This safely under-shows, but explicitly amends D-091’s resourceNames support. Offer a server-side compatibility flag, default off, retaining the cap and 503 behavior. |
| **B** | **Accept** | Allow TTL **30 s**, deny TTL **300 s**, both anchored at SAR start. Never cache errors as denials. Amend D-099 explicitly; keep request/check timeouts separate from cache lifetime. |
| **C** | **Reject as written** | Namespace-object permission says nothing about permission on resources inside it. Correct pruning is per **identity + resource + namespace**: with A, denied `list` eliminates enumeration of that resource there. |
| **D** | **Modify** | Start with **8 workers/request**, **32 actual SARs globally**, **100 SAR/s global, burst 32**, configurable to cluster capacity. Keep **500/request** until revised benches justify more. Raising to 2,000 still cannot satisfy the existing 2,685-check workload. |
| **E** | **Accept principle; reject proposed sizing** | Bound total accounted bytes across decisions, identity metadata and full-read entries; separately bound flights/queues. A 2,000-entry identity cap alone still permits ~1.3 GiB. See Q5. |
| **F** | **Reject for authorization** | SSRR would require local rule evaluation and cannot replace authoritative SARs. `incomplete=false` is not sufficient justification. Kubernetes explicitly warns against this external authorization use. [SSRR reference](https://kubernetes.io/docs/reference/kubernetes-api/definitions/self-subject-rules-review-v1-authorization/) |
| **G** | **Accept with modification** | Replace 50/min with measured workload targets, not merely a higher number. Initial Large `/routes` targets: **≤400 cold SARs**, **≤150/min** after warm-up, **all polls 200** without contention, plus a separate global-load/admission test. |

The five questions:

1. **Dropping fallback:** yes, under your stated acceptance of under-showing. Document that resourceNames-only grants no longer populate ordinary lists. A compatibility flag is reasonable, but provides functionality, not a scaling solution.

   **Better hybrid:** enumerate only list-readable populations; retain named checks for explicit detail URLs and names already written in readable objects. This supports useful resourceNames access without discovering candidates by scanning hidden objects. Use consistent authorization for any object actually displayed.

2. **Longer denials:** sound if denial only removes evidence or marks assessment incomplete. In list-denied → get-allowed, the `get` allow must retain its **own 30-second expiry**. A cached `FullRead=false` cannot authorize anything; `FullRead=true` must expire at the earliest positive constituent expiry.

   Current [full-read aggregation](../../../../pkg/auth/sar/fullread.go:181) takes the earliest expiry across all answers. With asymmetric TTLs, incidental allows can therefore shorten a negative aggregate to 30 seconds. Safe, but it weakens the projected saving. Cache false using a denying witness’s expiry; never lengthen true.

3. **Namespace pruning:** no general namespace-wide permission shortcut. A namespace selector can narrow user-requested scope. Resource-specific denials can prune that resource under A. Neither Namespace denial nor an empty visible population proves the namespace contains nothing relevant.

4. **Sharing/better ideas:** cross-page sharing already exists in the authorizer cache. **Do not share by groups alone**: usernames, UID and extras can change authorization. Keep the existing complete-identity hash and exact attributes. Under an explicitly RBAC-only contract, a fresh cluster-wide resource-list allow could cover namespace enumeration; do not infer that universally for arbitrary authorizer chains. First implement the hybrid above and permission-tuple batching.

5. **D-057:** yes, a global byte budget is needed, but it cannot create headroom. The reported **46.6/48 MiB leaves ~1.4 MiB**, before other Phase 7 allocations. Either reclaim memory or explicitly revise the overall budget. A concrete revision candidate is **64 MiB total delta with an 8 MiB auth-cache sub-budget**, subject to an integrated heap/RSS bench including sessions. Account variable string sizes and map capacity; entry counts × average bytes are only estimates.

Arithmetic:

- **2,685 = 8 + 329 + 2,348** is correct. After A, the estimate is **329 list checks plus full-read checks**, not exactly 329 total; rerun because visibility and traversal change.
- **33×2 + 296/5 = 125.2 SAR/min** is correct assuming those allow/deny counts. Against 329×2 = 658/min, that is **5.3×**, not 10×. Full-read work and eviction add cost.
- **100,000×349 B = 34.9 MB = 33.3 MiB**, before additional overhead.
- **20 million×349 B ≈6.50 GiB**; the report’s rounded projection is reasonable.
- **500×2 ms =1.0 s**; observed ~1.1 s is plausible. It measures the capped **503**, not successful uncapped page completion.
- Four failed polls out of twelve supports “one-third” for that measured minute; run-to-run variation means it is not a stable rate.

Risks, ranked:

- **P1 — Full-read bypass omits protected inputs.** [Production kinds](../../../../pkg/app/kinds_sar.go:21) exclude EndpointSlices, and a test explicitly permits that full-read result. Yet §7.6 requires EndpointSlice permission. Since `view.all` bypasses population checks, include every protected dependency population in full-read eligibility before opening 7.5’s gate, or preserve those checks outside the bypass.
- **P1 — Denial interpreted as absence/completeness.** Under A/B, withheld targets must remain unknown/incomplete. Never infer “missing backend”, “no endpoints”, healthy aggregates or complete paths from omitted inputs. Check permissions for names in readable refs even when the target does not exist.
- **P2 — Expiry at response commit is unspecified.** [Commit](../../../../pkg/api/commit.go:58) checks session/errors, not authorization expiry; request memos retain booleans. Slow requests can commit after their allows expire. Define validity at commit and carry the earliest used allow expiry; discard/rebuild or return 503 if expired.
- **P2 — Memory and load estimates omit contention.** Small global caches can evict five-minute denials early; many users can invalidate the 125/min estimate. Bound queues as well as active calls.
- **P3 — Required tests:** asymmetric expiry with mixed list/get grants; negative full-read witnesses; parallel error discarding the entire buffered response; EndpointSlice-denied full readers; hidden-object create/delete preserving responses and completeness; multi-identity cache churn.
- **P4 — Parallelizing `view` directly is unsafe.** Its maps and dependency memos explicitly assume one goroutine. Parallelize SAR resolution into a request-local result table, then render serially.

The webhook TTL defaults quoted are correct; the explanation of *why* those defaults were chosen is unsupported and should be removed. [Kubernetes configuration](https://kubernetes.io/docs/reference/access-authn-authz/authorization/)

No files edited; assessment uses the supplied bench results, not a rerun.
## Round 2: allow TTL and per-page checks

User accepted A, B and the memory-budget direction, then asked: raise the allow TTL to 45-60 s with documented tightening? And can checks follow the page being viewed?

**Q1: Recommend 60 s allow / 5 min deny.** It is defensible for this read-only UI, provided it is documented as an authorization-cache lifetime, not a universal revocation guarantee.

The concrete incident is a compromised app-team session, an offboarded employee, or an incident responder removing a RoleBinding. After Kubernetes starts denying checks for the session’s current identity, a previously cached allow can still expose **newly fetched data** for its remaining lifetime. Moving from 30 to 60 s adds at most 30 s to that window; it cannot retract data already downloaded.

The Kubernetes comparisons support that tradeoff, with qualifications:

- An existing `kubectl watch` is authorized when opened; ordinary authorization does not periodically recheck the stream. New requests/reconnections are checked again. See the [upstream authorization filter](https://github.com/kubernetes/apiserver/blob/master/pkg/endpoints/filters/authorization.go).
- The API server’s **webhook authorizer** defaults to caching allows for **5 min**. That is webhook-specific, not a five-minute cache on ordinary RBAC decisions. If involved in SAR evaluation, its staleness can compound Snellius’s cache. [Kubernetes authorization configuration](https://kubernetes.io/docs/reference/access-authn-authz/authorization/)
- Token lifetime is a separate boundary: an OIDC ID token normally remains authenticatable until expiry despite IdP revocation. [Kubernetes authentication](https://kubernetes.io/docs/reference/access-authn-authz/authentication/)
- Snellius refreshes identity at the earlier of token expiry and **15 min after review** ([store.go](../../../../pkg/auth/session/store.go:207)). Removing an IdP group may therefore take substantially longer than 60 s; removing its Kubernetes binding can take effect while the identity remains unchanged.

Using your **33 allows + 296 denies** assumption:

| Allow TTL | Estimated SARs/user/min | Saving versus 30 s |
|---|---:|---:|
| 30 s | 125.2 | — |
| 45 s | 103.2 | 18% |
| 60 s | 92.2 | 26% |

Thus 60 s halves the **allow-check component**, but saves only 26% overall after A+B. These exclude explicit named checks, FullRead overhead and eviction. I would choose 60 s; 45 s offers little practical advantage as a separate default.

Proposed README wording:

> For faster permission revocation, set `auth.authz.cacheTTL: 30s` and reduce `auth.session.idle` and `auth.session.absolute` to suit your environment. The cache TTL limits reuse of an allowed permission check; identity-provider changes and upstream authorization caches can take longer to become effective. Idle expiry does not stop an actively used or polling session, so use a shorter absolute lifetime where that matters.

**Currently 30 s is the enforced minimum** ([flags.go](../../../../pkg/cli/flags.go:77)); documenting “or lower” requires changing validation. Also retain the earlier requirement to handle allows expiring during response construction before claiming a strict response-level bound.

**Q2: Mostly lazy already—but lazy per endpoint/ref, not per displayed row.**

The current implementation still has list→named-get scanning; A is accepted direction, not yet implemented in this checkout.

- [visible.go](../../../../pkg/api/visible.go:69) checks refs on demand and memoizes each ref within the request. Dependency checks short-circuit.
- The SAR cache already shares exact permission tuples **across requests and pages**, keyed by complete identity plus authorization attributes. It also merges concurrent identical checks. No additional cross-page cache is needed.
- One exception: [commit.go](../../../../pkg/api/commit.go:38) probes **FullRead before every secure API handler**, trying cluster-wide list checks across mapped kinds until denial. Cached results usually eliminate actual SARs, but cold requests can check unrelated kinds.

After A, population cost is approximately **distinct resource × namespace tuples actually encountered**, plus explicit named references and FullRead—not object count and not every installed kind multiplied by every namespace.

| Endpoint | Why it examines more than the displayed rows |
|---|---|
| `/routes` | Visible matching total before pagination; computed filter dependencies; displayed rows’ parents, children and reasons. `deniedInInstall` additionally scans **all routes**, regardless of filters. |
| `/gateways` | Visible total; displayed gateways’ ListenerSets and not-accepted route counts. Also unfiltered `deniedInInstall`. |
| `/backends` | Builds the visible backend set **before query/pagination**: paths, route uses, standalone Backend objects, consumers, problem ordering and facets. |
| `/paths` | Authorizes path dependencies **before filtering**, then computes totals, state summaries, facets, ordering and pagination. Facets deliberately omit their own filter. |

Health/findings aggregation belongs to endpoints that request those aggregates; visiting `/gateways` does not inherently run the whole health handler. However, current `visibleHealth` still retains hidden findings in aggregate state/counts: **7.4 must fix that for D-085**. The map walks `/paths` pages, so it inherits that endpoint’s population work; the delegation tree already fetches child lists **only when expanded**.

My concrete proposal:

1. **Enumerate by present resource/namespace buckets**, applying hard requested scope and inexpensive candidate filters first. Authorize each bucket once; inspect/render only admitted populations. Keep named checks for explicit refs from readable objects, including absent targets.
2. **Remove exact hidden counts** such as `deniedInInstall` in secure mode. Hidden-object creation currently changes that response field, contrary to D-085. Likewise, avoid deriving a “hidden” indicator from hidden population changes.
3. Add a persistent **hard namespace scope selector** governing rows, counts and facets. Existing namespace filters alone do not provide this saving: hidden counts and self-excluding facets reach outside them. Cross-namespace dependencies still need their own checks; restricted dependencies remain unknown/incomplete.
4. Apply root-object authorization before computed filters/dependencies; prioritize cached denials within dependency sets. This can avoid unrelated checks without changing visible results.
5. Make FullRead an optional/lazy optimization, or retain a negative result using its denying witness’s expiry. **Do not precompute every identity in the background**: that pays for unused pages. Navigation prefetch can improve latency, but does not reduce SAR demand.

**Pagination first is unsafe with the current contract:** it produces underfilled pages, incorrect visible totals/facets, and potentially hidden-object-dependent cursors. A safe alternative is a separate cursor-only mode that scans until it has enough visible matches, exposes only visible anchors, and omits exact totals/global facets. Existing problem ordering and aggregate filters still need their relevant visible inputs.

Planning estimates for cold requests, using the supplied Large measurements—not a rerun:

| Endpoint | All 100 namespaces after A | Hard scope to the viewer’s 10 namespaces |
|---|---:|---:|
| `/routes` | ~329 | ~33 |
| `/backends` | ~312 | ~31 |
| `/paths` | ~313 | ~31 |
| `/gateways` | ~124 | ~12 |

These are **list-tuple estimates**, with scoped values approximated as one tenth; add cross-scope dependencies, explicit named-get fallbacks and any retained FullRead probe. Exact scoped figures require remeasurement because the fixture has cross-namespace relationships. Unscoped enumeration cannot simply check only the ten granted namespaces: it must discover permissions in the other ninety. Warm navigation reuses overlapping tuples and can issue **zero new SARs** within their TTLs.

No files edited.