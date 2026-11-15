# Review: D-099 proposal (RBAC watch invalidation)
Reviewer: Codex (cross-model). Input: the proposal below, design §4.4 and §6, D-091, plan 7.3.

## Proposal

### Proposal D-099 (amends D-091): invalidate the permission cache on RBAC changes

#### Problem with D-091 as designed (design §6)
- SAR results cached 30 s per identity, never extended.
- The full-read fast path (allow-all projection) is decided at sign-in and at identity refresh, which happens at most every 15 min. So a full reader who loses cluster-wide read keeps the allow-all view up to 15 min, not 30 s.
- Rechecking the fast path every 30 s costs ~25 SARs (one per served kind) per full reader per 30 s: ~40 SAR/s at 50 signed-in full readers. Each SAR is a full API request (authn, APF), mostly answering an unchanged question.

#### Proposal
1. Watch (informers) Roles, ClusterRoles, RoleBindings, ClusterRoleBindings. On any add/update/delete event, clear every identity's cached SAR results and fast-path decision (whole-cache generation bump; no attempt to compute which identities are affected). Snellius never evaluates RBAC rules itself; the API server still answers every SAR.
2. TTL becomes a backstop for what a watch cannot see (non-RBAC authorizers such as webhooks, ABAC; Node authorizer is irrelevant). Default 5 min with the watch, 30 s without. Fast-path decision also expires at the TTL and on every invalidation.
3. Opt-out: chart `auth.authz.watchRBAC: true` (default). false: no RBAC list/watch in the ClusterRole, TTL default 30 s, fast path rechecked every TTL.
4. `auth.authz.cacheTTL`: empty = default above; range 30 s–15 min.
5. Self-protection: if the watch is not synced, or errors persistently (permission removed, watch failing), Snellius falls back to the 30 s TTL automatically, logs a warning, and exposes a metric; returns to the long TTL once synced again.
6. ClusterRole gains list/watch on the four RBAC kinds only in secure mode with watchRBAC true.
7. Group membership from the IdP still refreshes at most every 15 min (identity refresh, design §4.4); unchanged.

#### Precedent
- kube-apiserver RBAC authorizer runs on informers of the four kinds.
- openshift-apiserver pkg/project/auth/cache.go: per-subject project cache invalidated on RBAC resourceVersion changes, with a max lifespan backstop added after a missed-invalidation bug (OCPBUGS-57474).
- rancher/steve pkg/accesscontrol: evaluates RBAC itself from informers, 24 h cache keyed by a hash including RBAC state.

#### Questions for the reviewer
- Is a whole-cache clear on any RBAC event safe and sufficient? Ordering: can a SAR answered just before the event be cached after the clear (stale result surviving the invalidation)? How to guard (generation counter captured before the SAR, discard on mismatch)?
- Is 5 min a reasonable backstop given webhook authorizers and aggregated ClusterRoles (aggregation controller updates the ClusterRole; does that arrive as an event in time)?
- Informer lag: API server applies RBAC from its own informer; Snellius's informer may lag. Is a window where kubectl refuses but Snellius still serves acceptable, and how long is it realistically?
- Memory/cardinality of watching all RBAC objects on large clusters (tens of thousands of RoleBindings); churn from controllers that rewrite RBAC often (invalidation storms → SAR storms). Should invalidations be debounced or rate-limited, and does that weaken revocation?
- Anything in design §6 (bounds, 500 uncached SAR cap per request, ErrAuthorizer → 503, merged concurrent checks) that this breaks.

## Review

The proposal identifies a real bug: the full-read fast path can retain access until identity refresh, bypassing the 30-second SAR expiry. Whole-cache invalidation is a reasonable optimization, but **it is not a revocation barrier**, and the proposed five-minute TTL weakens the existing freshness policy.

References below use `proposal:<line>` for the supplied the Proposal section above. These are findings against the proposal, not claims about implemented code.

1. **P1 — A generation check must protect publication, consumers, and merged checks.**

   **Evidence:** proposal:9,23; [design §6.4](../design.md:172) requires merged concurrent checks and uses an allow-all projection.

   A SAR can begin in generation G, return after invalidation, and repopulate the cache. Capturing G and discarding on mismatch addresses that race only if the comparison and insertion are atomic with invalidation. Otherwise invalidation can occur between them.

   Preventing insertion alone is insufficient: the caller and existing single-flight waiters might still consume the obsolete allow, and a new request might join an old flight. A session-held allow-all flag or request-local memo can also survive clearing the shared cache.

   **Fix:** generation-tag every decision and full-read proof; include generation in flight keys; compare and publish under the same synchronization as invalidation. Reject obsolete results for both caching and consumption. Scope request-local decisions to that generation, and check the generation before committing the buffered response. Retry with bounded work or return `503 authz-unavailable`.

   A full-read proof must cover one generation and expire no later than its **earliest constituent SAR expiry**, rather than receiving a fresh TTL after the last check finishes.

2. **P1 — Even a correctly implemented generation guard can cache a stale allow after the event.**

   **Evidence:** proposal:9–10,23–25 assumes event invalidation followed by SAR revalidation suffices.

   A valid ordering is:

   - Snellius receives the revocation event and increments its generation.
   - A new SAR reaches an API server whose RBAC informer still contains the old grant.
   - That SAR allows; Snellius caches it in the new generation.
   - The API server catches up, but Snellius receives no further relevant event.

   This also applies across API-server replicas. SAR invokes the receiving server’s authorizer; its response supplies no RBAC revision barrier. This scenario follows from the [SAR implementation](https://github.com/kubernetes/kubernetes/blob/v1.36.2/pkg/registry/authorization/subjectaccessreview/rest.go#L78) and client-go’s [eventual-consistency contract](https://github.com/kubernetes/client-go/blob/v0.36.2/tools/cache/shared_informer.go#L41).

   **Fix:** treat watch invalidation as an early-expiry hint and retain a short, non-renewable maximum age for positive decisions, including full-read proofs. Keeping 30 seconds preserves the existing local cache policy. If five minutes is accepted, explicitly accept that this ordering can leave access available for nearly that long after Kubernetes starts denying it.

   An immediate or delayed second clear reduces exposure but does not establish an ordering guarantee.

3. **P1 — Watch degradation and recovery lack safe transition semantics.**

   **Evidence:** proposal:13 promises automatic fallback and recovery “once synced again.”

   `HasSynced()` establishes initial population, not current connection health; informer-store synchronization also does not establish that an individual handler has caught up. See the [client-go interface contract](https://github.com/kubernetes/client-go/blob/v0.36.2/tools/cache/shared_informer.go).

   Two failures follow:

   - A previously synced informer can remain “synced” through a prolonged outage.
   - Changing the TTL used for new entries leaves existing five-minute allows and full-read proofs alive.

   **Fix:** define health for **all four** watches separately from initial synchronization, with bounded failure-detection and recovery criteria. Do not use absence of object events as proof of failure: quiet clusters are valid.

   On degradation, atomically bump the generation and invalidate existing proofs, or enforce `min(original expiry, checked-at + 30s)` at lookup. Do not start another 30 seconds from failure detection. Fence old in-flight work too. On recovery, invalidate again after catch-up, including handler processing. Specify whether explicit `cacheTTL` overrides are capped during degradation; the stated automatic fallback requires that cap.

4. **P2 — Five minutes is a security-policy change, including for ordinary RBAC.**

   **Evidence:** proposal:10–12 versus [design §6.2](../design.md:147) and [D-091](../../../decisions.md:1463): 30 seconds, never extended.

   The backstop covers missed events and informer-ordering races as well as non-RBAC authorizers. Webhook policy changes need not generate any RBAC event. API-server webhook caching can add its own delay; Kubernetes documents an independently configurable authorization cache. See [authorization configuration](https://kubernetes.io/docs/reference/access-authn-authz/authorization/).

   **Fix:** retain 30 seconds by default unless the longer disclosure window is explicitly accepted. Document `cacheTTL` as an authorization freshness tradeoff, including watch-disabled behavior and the possible 15-minute setting.

   Aggregated ClusterRoles **do produce an update** when the aggregation controller changes their effective rules. Watching ClusterRoles therefore covers that update, but neither controller processing nor event delivery has a guaranteed deadline. The [aggregation controller](https://github.com/kubernetes/kubernetes/blob/v1.36.2/pkg/controller/clusterroleaggregation/clusterroleaggregation_controller.go#L77) confirms this asynchronous step.

   There is no defensible universal “realistic maximum” for informer lag. Measure normal and tail latency on supported deployments; do not describe the revocation window as necessarily subsecond.

5. **P2 — Global invalidation needs a global load budget.**

   **Evidence:** proposal:9,26 versus [design §6.4 bounds](../design.md:174).

   One unrelated namespace’s RBAC churn invalidates every active identity. At 50 active full readers and 25 checks each, rebuilding every five seconds is roughly **250 SAR/s**, before restricted-viewer checks. A five-minute TTL offers little benefit under continuous invalidation.

   The 500-distinct-SAR cap per request does not bound aggregate concurrent work, queued work, or repeated attempts after generation changes. Eager rebuilding makes this worse.

   **Fix:** invalidate immediately but rebuild lazily. Bound global SAR concurrency, request rate, queued work, and retries; merge checks within a generation. Exhaustion or repeated invalidation must produce a buffered 503, never stale allows or a partial response.

   Debounce **rebuilding**, not the moment existing proofs become unusable. Debouncing invalidation itself adds an explicit revocation delay. Ignore synthetic resync updates with unchanged resourceVersion. Preserve the 500-distinct-check cap while separately bounding actual attempts, and include fast-path SARs in request budgets when performed for that request.

6. **P2 — RBAC informer memory is outside the existing cache bounds.**

   **Evidence:** proposal:9,14,26; [design §13](../design.md:321) requires memory measurement against D-057.

   The identity LRU limits do not constrain informers retaining every Role, binding, ruleset, subjects list, and potentially large metadata across the cluster. Initial lists and notification backlogs also create transient memory pressure.

   **Fix:** retain only the metadata needed for invalidation, using metadata informers or appropriate transforms; measure initial-list peak, steady retained heap, and churn backlog on a large RBAC fixture. Keep callbacks constant-time.

   A generation bump must not accumulate unbounded generations of decisions or flights: obsolete entries still count toward the existing bounds and must be reclaimed. If watching cannot operate within budget, disable that optimization and use short-TTL revalidation rather than silently watching a subset.

7. **P3 — The existing tests do not establish the new guarantees.**

   **Evidence:** proposal adds no acceptance tests; [design §13](../design.md:316) covers ordinary TTL expiry and revocation, but not these event-ordering and transition cases.

   **Fix:** add deterministic tests with a fake clock, controlled watch delivery, and blocked SAR responses. Verify observable responses for:

   - An old allow completing after invalidation, including existing waiters and a new concurrent request.
   - A full-read proof invalidated during construction or response buffering.
   - An event arriving before the SAR server applies the revocation.
   - Watch failure after initial synchronization, an already-aged long-TTL entry, and recovery catch-up.
   - SAR transport errors, timeouts, and `evaluationError` during fast-path revalidation: 503 without stale fallback.
   - Sustained unrelated RBAC churn: bounded work and memory, with complete responses or 503.

The identity policy in §4.4 can remain unchanged: watching RBAC does not refresh IdP groups, and group freshness remains limited by token contents and identity refresh. New groups must select the corresponding identity hash; an old full-read proof must not carry across that replacement.

D-099 should amend design §§6.2–6.4, §12 and §13, plus [implementation-plan 7.3](../implementation-plan.md:229). It changes cache validity, full-read proof lifetime, watch operation, RBAC permissions, configuration, load control, and testing—not just the invalidation mechanism. List-then-get, error-to-503 behavior, response buffering, and existing bounds should remain explicit requirements.

No files changed.
## Round 2: precedents and invalidation signals

Question from the user: do the platforms that use RBAC watches have the same flaws, or a smarter solution, and is there an API server signal to invalidate cached decisions? The author's position (a)-(d) was put to Codex to test.

**Your diagnosis is largely right; the conclusion in (d) does not follow.** Delayed re-clears can substantially improve normal-case revocation, but they can still turn a briefly stale API-server answer into five minutes of additional access after every API server has caught up.

For Phase 7, I recommend **30 s TTL without watches**, applied to both individual decisions and full-read proofs. Watch plus 30 s is a reasonable later improvement for faster revocation. I would make the long-backstop option an explicitly accepted freshness tradeoff.

Source limitation: Kubernetes references below are pinned to **v1.36.2**, with client-go/API at **v0.36.2**. I could retrieve Steve and OpenShift implementation source on branches, but tagged retrieval failed. I therefore cannot honestly claim to have completed the requested tag-level verification for those two projects.

**(a) Correct for Kubernetes’ RBAC authorizer, with two qualifications.**

The RBAC authorizer evaluates each call against informer-backed listers:

- [`pkg/kubeapiserver/authorizer/config.go:120–126`](https://github.com/kubernetes/kubernetes/blob/v1.36.2/pkg/kubeapiserver/authorizer/config.go#L120) supplies the four RBAC listers.
- [`plugin/pkg/auth/authorizer/rbac/rbac.go:67–72`](https://github.com/kubernetes/kubernetes/blob/v1.36.2/plugin/pkg/auth/authorizer/rbac/rbac.go#L67) visits applicable rules for each authorization call; it has no decision cache there.

Consequently, replicas can disagree while their informers converge. Even one replica’s four RBAC collections are not a single transactional snapshot. Client-go promises eventual consistency and per-object ordering, without ordering across different objects. [`shared_informer.go:41–75`](https://github.com/kubernetes/client-go/blob/v0.36.2/tools/cache/shared_informer.go#L41).

You are also right about SAR: its handler invokes the receiving server’s authorizer directly. Neither the request specification nor status provides a minimum authorization revision or evaluated revision. Setting SAR metadata’s `resourceVersion` would not create such a barrier. [`subjectaccessreview/rest.go:78–85`](https://github.com/kubernetes/kubernetes/blob/v1.36.2/pkg/registry/authorization/subjectaccessreview/rest.go#L78), [`authorization/v1/types.go:177–239`](https://github.com/kubernetes/api/blob/v0.36.2/authorization/v1/types.go#L177).

The qualifications are:

1. Say **“the RBAC authorizer does not cache decisions”**, rather than “kube-apiserver does not.” Authorization webhooks do cache decisions.
2. Treat resourceVersion as an API change-tracking token. Its etcd-revision implementation does not provide an authorization revision spanning informers, replicas and external authorizers.

**There is no supported cluster-wide “authorization state changed and is now applied” notification.** RBAC resource watches provide change notifications, not acknowledgements that all authorization evaluators have incorporated those changes. SAR supplies no missing acknowledgement.

**(b) Broadly correct, but Steve has a more specific safeguard than “one cache.”**

In the inspected Steve implementation, `AccessFor`:

1. Collects the user’s applicable grants from local RBAC caches.
2. Hashes those collected grants.
3. Looks up the resulting access set using that hash.
4. On a miss, constructs the access set from those **same collected grants**.

The cached access set has a 24-hour TTL, but changed grants select a different key. That TTL is therefore not ordinarily a 24-hour revocation window. [`access_store.go`, branch `main`, `AccessFor`, `newAccessSet`, `userGrantsFor`](https://raw.githubusercontent.com/rancher/steve/main/pkg/accesscontrol/access_store.go), [`user_grants.go:10–71`](https://github.com/rancher/steve/blob/main/pkg/accesscontrol/user_grants.go#L10).

**That is the smarter solution:** the cache key and computed result derive from the same captured inputs. An old computation can finish under an old key without becoming the answer for newly observed grants.

Steve still has informer state plus a derived cache; “one cache” is shorthand for **one local source of authorization inputs**, rather than a local invalidation feed coupled to a remote evaluator. The RBAC caches can lag or present mixed collection states. This access-set calculation also does not reproduce an arbitrary Kubernetes authorizer chain. [`policy_rule_index.go`, branch `main`, `getRules` and `getRoleRefs`](https://raw.githubusercontent.com/rancher/steve/main/pkg/accesscontrol/policy_rule_index.go).

That last distinction can affect both directions: local RBAC evaluation can miss an external grant, or allow something an earlier denying webhook would reject. Kubernetes’ union stops at the first decisive Allow or Deny. [`authorization/union/union.go:39–61`](https://github.com/kubernetes/kubernetes/blob/v1.36.2/staging/src/k8s.io/apiserver/pkg/authorization/union/union.go#L39).

OpenShift’s inspected project cache likewise uses local evaluation: its reviewer calls an RBAC `SubjectLocator.AllowedSubjects`, rather than making remote SAR requests. It derives subjects able to access a namespace, then maintains the subject-to-project index. [`reviewer.go:41–60`, branch `master`](https://github.com/openshift/openshift-apiserver/blob/master/pkg/project/auth/reviewer.go#L41).

Its cache source explicitly references **OCPBUGS-57474** and sets the maximum lifespan to **15 seconds**. Expiry forces a rebuild through the synchronization loop; this is not a strict 15-second request-time expiry, because scheduling and rebuild time also matter. That precedent supports a short periodic backstop—not specifically a five-minute one. [`cache.go:28–31, 260–270, 404–440`, branch `master`](https://github.com/openshift/openshift-apiserver/blob/master/pkg/project/auth/cache.go#L28).

So these systems share eventual-consistency limitations, but **they do not demonstrate that local-watch invalidation safely fences a remotely obtained SAR result**.

**(c) The explanation is right; the proposed bound is conditional.**

For identical identity and authorization attributes, an allow returned by a stale replica is what that replica’s authorizer would answer at that instant. Snellius’s additional problem is retaining that answer after the evaluator changes.

However, “Snellius allows what Kubernetes denies” remains an accurate description of the later observable state. It is not a separate root cause.

Your delayed-clear argument works under these assumptions:

- The event reaches Snellius.
- Every replica that might answer subsequent SARs has incorporated the revocation before the final clear.
- The clears invalidate old in-flight work as well as stored decisions.
- The change actually concerns RBAC.

Under those conditions, clearing at +2 s and +30 s prevents pre-convergence answers surviving beyond the final clear.

But consider this valid execution:

| Time after Snellius receives the event | Action |
|---|---|
| +30.0 s | Final delayed clear |
| +30.1 s | Replica B still allows; Snellius caches that answer for 5 min |
| +30.2 s | B catches up; all API servers now deny |
| Until +330.1 s | Snellius continues allowing |

Only **0.2 seconds of lag beyond the assumed threshold** produced almost five minutes of access after Kubernetes converged.

Thus your statement “kubectl on that replica is also wrong” is true **when the stale SAR is obtained**. It need not remain true while Snellius continues serving it.

Conversely, delayed clears can genuinely outperform a plain 30-second TTL in ordinary operation: if convergence takes 100 ms, a +2-second clear can remove an allow that otherwise survives for nearly 30 seconds. The objection is to the claimed guarantee, not the usefulness of the heuristic.

**The additional mechanisms do not supply the missing fence.**

- **Consistent reads from cache in Kubernetes 1.31+:** these establish freshness for storage GET/LIST operations using etcd progress notifications. They do not make each authorization call wait for RBAC informer processing. Reading a fresh RoleBinding and then issuing SAR—even to the same server—does not establish that ordering. [Kubernetes’ explanation](https://kubernetes.io/blog/2024/08/15/consistent-read-from-cache-beta/), together with the informer wiring cited above.
- **Watch bookmarks:** acknowledge progress of the particular watch stream. They do not acknowledge application by another informer or replica. Bookmarks are not guaranteed at a particular interval, or even guaranteed to arrive when requested, so they are not a dependable heartbeat deadline either. [API concepts: bookmarks](https://kubernetes.io/docs/reference/using-api/api-concepts/#watch-bookmarks).
- **Load balancing and stickiness:** revalidation can reach a different replica and return an older decision. Persistent HTTP connections may produce practical affinity; they do not establish authorization freshness. Pinning one replica removes replica selection variability, but preserves that replica’s informer lag. This follows from SAR calling the receiving server’s local authorizer.
- **Webhook caches:** the API server caches webhook results by SAR specification, with independently configured positive and negative lifetimes. The documented defaults are **5 min authorized / 30 s unauthorized**. Snellius receives no remaining lifetime, so its cache can extend an upstream cached allow further. [`webhook.go:211–265`](https://github.com/kubernetes/kubernetes/blob/v1.36.2/staging/src/k8s.io/apiserver/plugin/pkg/authorizer/webhook/webhook.go#L211), [authorization configuration](https://kubernetes.io/docs/reference/access-authn-authz/authorization/#configuring-the-api-server-using-an-authorization-config-file).
- **Aggregated ClusterRoles:** changing a contributing role first requires asynchronous aggregation. The controller writes the aggregate’s effective `.rules`, producing another ClusterRole event. Schedule invalidation against that event too; the original constituent event does not mean effective permissions have changed yet. [`clusterroleaggregation_controller.go:77–129`](https://github.com/kubernetes/kubernetes/blob/v1.36.2/pkg/controller/clusterroleaggregation/clusterroleaggregation_controller.go#L77).
- **Non-RBAC changes:** webhook policy/configuration, ABAC deployment changes and identity changes need not produce an RBAC event. Delayed re-clears offer no assistance for such changes. SAR also evaluates the identity supplied by Snellius; it does not independently refresh the user’s IdP groups.

One further qualification to comparisons with Kubernetes: native watch requests are authorized when established, rather than reauthorized for every delivered object. RBAC revocation does not generally terminate an existing watch immediately. That makes “Kubernetes’ own revocation window” operation-dependent; it does not justify retaining allows for new Snellius requests. [`filters/authorization.go:56–76`](https://github.com/kubernetes/kubernetes/blob/v1.36.2/staging/src/k8s.io/apiserver/pkg/endpoints/filters/authorization.go#L56), [`handlers/get.go:236–275`](https://github.com/kubernetes/kubernetes/blob/v1.36.2/staging/src/k8s.io/apiserver/pkg/endpoints/handlers/get.go#L236).

**(d) Same eventual-consistency category, but a weaker freshness policy.**

Kubernetes gives no universal maximum informer lag. Therefore neither a 30-second TTL nor a five-minute TTL guarantees revocation within that time **of the RBAC write**.

The useful comparison is the extra retention **after all reachable authorization evaluators consistently deny**, holding identity and request attributes fixed:

| Option | Residual risk | Operational consequence |
|---|---|---|
| **30 s TTL, no watch** | A previously obtained allow can survive up to its remaining 30 s. Stale upstream evaluators can keep supplying new allows before convergence. | Simplest; no RBAC-watch permissions or health machinery. |
| **Watch + 30 s** | Events often revoke earlier; a post-clear stale SAR can still survive up to 30 s. Missed events retain the same short backstop. | Same quiet-cluster TTL revalidation load, plus watch memory and event-triggered rebuilds. |
| **Watch + delayed clears + 5 min** | Often fast when convergence precedes a later clear. A stale answer obtained after the last clear can survive nearly 5 min after convergence. Invisible changes also use the long backstop. | Lower quiet-cluster revalidation load; more timers, health handling and invalidation bursts. |

These bounds require expiry anchored conservatively to SAR initiation, no sliding renewal on cache hits, and no independently longer full-read flag. Starting TTL at response completion adds the request’s duration.

**My recommendation is 30 s without watches for Phase 7.** Fix the full-read expiry first. At the proposal’s assumed workload, 50 active users × 25 checks / 30 s is about **42 SAR/s**, versus about **4.2 SAR/s** at five minutes. Measure that cost before accepting a tenfold increase in potential extra retention. Lazy checks and bounded concurrency matter in either design.

If faster ordinary RBAC revocation warrants the watch complexity, choose **watch plus 30 s**. It improves latency without enlarging the backstop. It does not reduce quiet-cluster expiry-driven load.

The long-backstop option can be an acceptable product choice, but describe it accurately: **fast best-effort RBAC invalidation, with up to five minutes of additional cached access when the heuristics miss**. It is not established as equally fresh as Kubernetes by the proposed delays.

Finally, I would qualify my earlier review’s severity language: five minutes is not inherently an implementation defect if explicitly accepted as policy. The publication race and broken expiry remain correctness problems; the longer residual disclosure window is a policy decision. Also, a final generation check is stronger response-fencing behavior than Kubernetes’ ordinary authorize-once request handling—it cannot turn this architecture into a cluster-wide revocation barrier.

No files changed.
## Triage

| # | Finding | Decision | Change |
|---|---|---|---|
| R1-1 | Generation checks must fence publication, consumers and merged checks | n/a for v0.8.0 | No watch, so no generation clears (D-099). Holds if a watch is added later. |
| R1-2 | A stale allow can be cached after the event | accept | No watch; TTL stays the bound (D-099) |
| R1-3 | Watch degradation and recovery lack safe transitions | n/a for v0.8.0 | No watch |
| R1-4 | 5 min is a security-policy change | accept | Default stays 30 s; a longer TTL is the operator's explicit choice, documented |
| R1-5 | Global invalidation needs a global load budget | n/a for v0.8.0 | No watch; 7.3 bench measures SAR load |
| R1-6 | RBAC informer memory outside the bounds | n/a for v0.8.0 | No watch |
| R1-7 | Tests for the new guarantees | accept, reduced | 7.3 tests: fast-path expiry at the TTL from its earliest check, lazy recheck, no extension past expiry |
| R2 | Precedents evaluate RBAC locally; no authorization-applied signal; delayed re-clears do not bound the long TTL | accept | Recommendation adopted: 30 s TTL without a watch for every decision, fast path included (D-099) |
| author | Fast path decided only at sign-in and refresh (15 min window) | accept | Fast path is a TTL-bound cached decision, rechecked lazily (D-099) |
