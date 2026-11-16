Cross-model: YES (author: Claude Opus 5.5 subagent, reviewer: Codex, command: codex exec --sandbox read-only)
Scope: sub-phase 7.4 plan (Tasks 31–38)
Diff: a3a2c6e..c000af9
Timing: on time

The plan has several D-085 gaps. Task 33’s per-viewer route health and reason calculation belongs in 7.4; it does not need to wait for the new 7.5 signals. The problems below concern inputs and response fields that remain unprojected.

1. **P1 — Hiding install details still exposes the controller’s identity.** [implementation-plan.md:549](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:549) explicitly preserves `ID`. [install.go:51](/Users/ddoyle/Development/claude/snellius-phase7/pkg/install/install.go:51) constructs that ID as `<namespace>.<name>`, and [upstream.go:54](/Users/ddoyle/Development/claude/snellius-phase7/pkg/install/upstream.go:54) takes both values from the controller Deployment. A viewer admitted through one route therefore learns the hidden Deployment’s namespace and name despite the neutral display name. Calling the ID opaque does not redact it. The plan needs an explicit resolution of this conflict with §7.4 and D-085.

2. **P1 — The declared finding bases omit actual deciding inputs.** [implementation-plan.md:494](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:494) gives KGWUI-012 only resource and evidence, but [computed.go:76](/Users/ddoyle/Development/claude/snellius-phase7/pkg/health/computed.go:76) deliberately removes contributing routes from that evidence. A hidden ancestor can therefore decide a shown disagreement. Similarly, the KGWUI-023 basis at plan line 498 omits its route population: [endpoints.go:105](/Users/ddoyle/Development/claude/snellius-phase7/pkg/health/endpoints.go:105) creates findings only for Services referenced by eligible owned routes. Creating or deleting a hidden consumer can change a readable Service’s finding and hold. Both need their complete deciding basis, including population dependencies.

3. **P1 — Check projection covers holds but leaves object-derived text exposed.** [implementation-plan.md:480](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:480) projects pending items and preserves non-pending failures; it supplies no basis rule for ordinary `Detail` or object-derived non-pending failures. [withheld.go:160](/Users/ddoyle/Development/claude/snellius-phase7/pkg/health/withheld.go:160) puts controller version disagreement into an **OK** result’s `Detail`. [checks.go:154](/Users/ddoyle/Development/claude/snellius-phase7/pkg/health/checks.go:154) exposes scope configuration errors and source details. These still reach `/health` and `/diagnostics` when their controller sources are hidden. Collection failures may remain visible, but arbitrary check text cannot inherit that exemption.

4. **P1 — `/health` can retain a group ID decided by a hidden finding.** [implementation-plan.md:509](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:509) projects the report, while line 510 rebuilds grouping separately through `v.diagnosis`. The `/health` step uses only `v.report`. Existing findings already carry serialized `Group` values: [finding.go:86](/Users/ddoyle/Development/claude/snellius-phase7/pkg/health/finding.go:86). The `statusNotProgressing` family binds visible KGWUI-003 warnings to KGWUI-018’s controller cause. Filtering out a hidden 018 without rebuilding those findings’ groups leaves its influence in `/health`. Every emitter of `Finding.Group` must use the viewer’s grouping.

5. **P1 — Visible listener counts still carry hidden-population cross-check state.** [implementation-plan.md:529](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:529) replaces numeric counts, but does not project the associated computed checks. [explain.go:242](/Users/ddoyle/Development/claude/snellius-phase7/pkg/api/explain.go:242) emits `lf.Check`, and [explain.go:219](/Users/ddoyle/Development/claude/snellius-phase7/pkg/api/explain.go:219) propagates it into a visible route’s listener membership. [check.go:255](/Users/ddoyle/Development/claude/snellius-phase7/pkg/computed/check.go:255) compares the full computed population against `attachedRoutes` and explicitly gives this check no dependencies. A hidden route can change `checked`/`pending`/`disagrees` or coverage even after the displayed count is fixed.

6. **P1 — Recomputing path problems during API rendering is too late for queries.** [implementation-plan.md:580](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:580) specifies filtering signals and recomputing row/backend `Problem.State` in the API, without requiring the viewer projection before query execution. [paths.go:137](/Users/ddoyle/Development/claude/snellius-phase7/pkg/api/paths.go:137) calls `paths.Run` before rendering. [query.go:163](/Users/ddoyle/Development/claude/snellius-phase7/pkg/paths/query.go:163) uses the original projection for state filters, ordering, summaries, facets, cursors and digest. Hidden findings or EndpointSlices can still change those outputs. Project before filtering and paging, including route-detail traces that share these problems.

7. **P1 — YAML admission’s dependence on hidden findings has no implementation step.** Design §7.2 explicitly requires admission to ignore findings decided by hidden objects. Tasks 31–38 do not change admission or `linker`. [admit.go:81](/Users/ddoyle/Development/claude/snellius-phase7/pkg/explore/admit.go:81) admits resources from the complete findings set, and [link.go:100](/Users/ddoyle/Development/claude/snellius-phase7/pkg/api/link.go:100) accepts `ReasonFinding` unconditionally. A readable object can consequently switch between unavailable and YAML/detail access when an unreadable basis creates or removes its finding. Filtering finding responses does not fix that admission path.

8. **P2 — Keeping computed `state: "restricted"` contradicts the brief’s blanket contract.** [implementation-plan.md:532](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:532) explicitly retains it; [phase-7.4-brief.md:16](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/phase-7.4-brief.md:16) promises no restricted placeholders in any secure list or detail. A permission-only assessment can satisfy D-085, but D-077 does not establish this exception—it rejects placeholders for hidden dependencies. Moreover, the current implementation is not uniformly permission-only: [rows.go:1059](/Users/ddoyle/Development/claude/snellius-phase7/pkg/explore/rows.go:1059) builds selection scopes from existing routes. The plan needs either removal or an explicit approved exception with permission-derived behaviour, including empty populations.

9. **P2 — Reachability has no bounded denied-all path.** [implementation-plan.md:547](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:547) scans occupied kind/namespace buckets until an allow. For a viewer allowed nothing, it must exhaust them. Batches of 64 limit concurrency, not total checks; a sufficiently wide install exceeds the 1,000-check cap and produces 503 instead of an absent install. Task 38’s report-only **Large** benchmark does not resolve this, and the existing Extreme fixture has 500 namespaces. Add a bounded strategy and an acceptance condition for denied-all requests; hidden population growth must not turn an empty response into a cap failure.

10. **P2 — Global reachability checks conflict with scoped requests’ check contract.** [implementation-plan.md:548](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:548) adds reachability to every install-scoped `find`, before Task 37’s scoped prefetch. Yet [implementation-plan.md:619](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:619) promises no root checks outside scope. A viewer reaching the install through namespace B while requesting empty namespace A exposes the conflict: global reachability checks B; scope-limited reachability incorrectly risks 404. Specify how these requirements interact and test that case.

11. **P3 — Several required surfaces and namespace cases lack rule coverage.** The rule-case lists omit `/meta`, YAML and an extension response, despite design §7.2 and the brief requiring every surface. Comparing `/meta` between viewers at plan line 571 is not a hidden-object mutation test. Task 37’s HTTP coverage at [implementation-plan.md:626](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:626) names only four of the seven scoped lists and does not explicitly test findings with no resource or a cluster-scoped resource. The seven-endpoint scope boundary matches the amended design, and excluding those findings is explicitly planned; both need direct acceptance tests.

12. **P3 — The proposed “mutation check” does not test regression sensitivity.** [implementation-plan.md:467](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:467) checks that the full reader’s response changes. That validates fixture reachability, but does not establish that bypassing the restricted projection makes the invariant test fail, as the brief requires. Likewise, [implementation-plan.md:590](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:590) emphasizes `FullRead == false` and per-object SARs when EndpointSlices are denied. Add observable endpoint-count, finding and state assertions at the affected HTTP surfaces.

Reviewed `a3a2c6e..c000af9` and the cited code. No files were edited and no tests were run; there are no test results.
## Triage
| # | Finding | Source | Decision | Note |
|---|---|---|---|---|
| 1 | Install ID exposes the controller Deployment's namespace and name | reviewer | accept, resolved by D-102 | User kept the name-based ID (stable across reinstalls, frozen in API v1). Recorded as a known disclosure; a test checks no other Deployment field reaches a restricted viewer |
| 2 | KGWUI-012 and 023 bases omit their deciding populations | reviewer | accept | Bases include contributing and consumer routes; every analyzer audited (011, 017, 004, 009, 022, 024 extended) |
| 3 | Object-derived check text (Detail, OK results) not projected | reviewer | accept | Every result carries a basis; only collection and access failures exempt, with object-free text |
| 4 | `/health` keeps group IDs decided by a hidden finding | reviewer | accept | Every `Finding.Group` emitter uses the viewer's grouping; 003/018 test |
| 5 | Listener cross-check state carries the hidden population | reviewer | accept | Shown only when the listener's route population is readable |
| 6 | Path problems recomputed after the query ran | reviewer | accept | Viewer projection before `paths.Run`, the backends query and traces |
| 7 | YAML/detail admission uses hidden findings | reviewer | accept | `admitWith` counts only findings the viewer is shown |
| 8 | Computed `state: "restricted"` kept | reviewer | accept | Dropped in secure mode; UI renders "not checked" |
| 9 | Reachability unbounded for a denied-all viewer | reviewer | accept | At most 256 uncached checks per request; yes/no/undecided; denied-all on Extreme gets 200 under the cap |
| 10 | Reachability vs namespace scope | reviewer | accept | Reachability is install-wide and ignores the scope; tested |
| 11 | Rule coverage missing for `/meta`, YAML, extensions and scoped lists | reviewer | accept | Cases added; all seven scoped lists tested |
| 12 | Mutation check does not prove sensitivity | reviewer | accept | Test-only projection bypass makes the invariant test fail; EndpointSlice tests assert HTTP outputs |

Fixes: `7637504` (2–12), D-102 `ff46cd0` and `29878e1` (1).

## Re-review (c000af9..29878e1)

The revision resolves **8 findings and partly resolves 4**. It also introduces new correctness problems in reachability and check projection.

Reviewed only `c000af9..29878e1`, using existing code to verify consequences. No files edited; no tests run.

| Original finding | Status | Evidence and assessment |
|---|---|---|
| **1. Install ID disclosure** | **resolved** | [D-102, decisions.md:1530](/Users/ddoyle/Development/claude/snellius-phase7/docs/decisions.md:1530) records the user-approved exception. [Plan:564](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:564) preserves `<namespace>.<name>`; [plan:595](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:595) tests that other controller metadata stays hidden. |
| **2. Incomplete finding bases** | **partly** | [Plan:507](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:507) adds 012 contributors; [plan:512](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:512) adds 023’s consumer population. However, the latter enumerates namespaces **holding an owned route**. Adding the first hidden route in another namespace—even one consuming an unrelated Service—adds an unreadable basis bucket and can suppress an existing visible 023. The population’s permission boundary must not depend on hidden occupancy. |
| **3. Object-derived check text** | **partly** | [Plan:484](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:484) now covers OK results and ordinary result text, and [plan:485](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:485) limits the collection exemption. But the projection rules introduce the result-state problems detailed below. |
| **4. Hidden influence on finding groups** | **resolved** | [Plan:525](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:525) requires viewer grouping at every emitter, including `/health` and group filters; [plan:532](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:532) adds the 003/018 regression case. |
| **5. Listener cross-check population** | **resolved** | [Plan:548](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:548) gates state, coverage, notes and member-state contribution on population permissions derived from `allowedRoutes`; [plan:554](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:554) tests hidden-route mutations. |
| **6. Path projection after query execution** | **resolved** | [Plan:605](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:605) explicitly projects before queries, including backend queries and traces. [Plan:615](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:615) covers filters, sorting, summaries, facets and revision equality. |
| **7. Admission through hidden findings** | **resolved** | [Plan:549](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:549) conditions `ReasonFinding` admission on shown findings for links, details and YAML; [plan:554](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:554) supplies the HTTP acceptance case. |
| **8. Computed `restricted` placeholders** | **resolved** | [Plan:547](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:547) removes them, including selection-derived delegation rows. [Plan:550](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:550) specifies absent-fact rendering. |
| **9. Unbounded denied-all reachability** | **partly** | [Plan:571](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:571) adds the 256-uncached-check bound; [plan:596](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:596) adds Extreme acceptance coverage. This addresses the original exhaustive-scan cap failure, but undecided handling leaks hidden state and convergence is not guaranteed; see below. |
| **10. Reachability versus namespace scope** | **partly** | [Plan:567](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:567) makes reachability install-wide, and [plan:596](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:596) tests reachability through B while requesting A. However, [plan:652](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:652) still requires **no** root-kind `list` SAR outside scope. That assertion needs an explicit reachability exception; it contradicts the intended cold-request behavior. |
| **11. Missing surfaces and scope cases** | **resolved** | [Plan:471](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:471) adds `/meta`, YAML and extensions; [plan:652](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:652) covers all seven lists and unnamespaced/cluster-scoped findings. |
| **12. Regression sensitivity and endpoint assertions** | **resolved** | [Plan:470](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:470) adds a projection-bypass sensitivity check. [Plan:615](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:615) and [plan:616](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:616) now require observable endpoint counts, findings and states at HTTP surfaces. |

New problems introduced by the revision, ranked:

1. **P1 — `ReachUndecided` exposes hidden install existence and population growth.**  
   [Plan:570](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:570) scans occupied buckets; [plan:572](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:572) returns 404 for `ReachNo` but serves projected responses for `ReachUndecided`.

   For the same denied-all viewer and cold permission cache, a small hidden install answers 404; adding enough hidden buckets makes the same URL answer 200. A nonexistent ID still answers 404 ([handlers.go:297](/Users/ddoyle/Development/claude/snellius-phase7/pkg/api/handlers.go:297)). Empty projected rows do not hide this status distinction. Similarly, hidden buckets sorted before the only readable bucket can make an otherwise visible install disappear from the first `/installs` response. D-102 permits the ID disclosure **after visibility through a readable object**; it does not authorize this existence oracle.

2. **P1 — `notAssessed` can disclose hidden grace activity and suppress readable holds.**  
   [Plan:488](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:488) exposes whether the result basis is readable **before** filtering individual holds. Yet [plan:500](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:500) defines the resource-status result basis as “the resources held in grace.”

   With no held resources, that basis is empty. When a hidden route enters grace, it becomes unreadable and adds `notAssessed: true` to the response. Hidden activity therefore changes serialized output. With both readable and hidden held resources, the whole-result rejection also prevents step 3 from retaining the readable hold, contrary to [plan:514](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:514)’s three-items/one-readable acceptance case. Aggregate hold projection needs separate treatment from an ordinary result’s stable basis.

3. **P1 — Readable non-pending failures are converted to OK.**  
   [Plan:489](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:489) says that after recomputing holds, `Pending` and its `Error` remain only while a readable item is held; otherwise “the result is `OK`.”

   A readable controller Deployment with unknown scope reaches this branch: its result is object-derived, has a readable basis, and has no hold ([plan:485](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:485); [checks.go:157](/Users/ddoyle/Development/claude/snellius-phase7/pkg/health/checks.go:157)). The proposed rule erases a legitimate required failure. Preserve ordinary readable failures; normalize only failures caused by filtered pending items.

4. **P1 — Cached-prefix scanning does not guarantee convergence across requests.**  
   [Plan:571](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:571) claims cached answers make subsequent sorted scans converge. Denials expire after 4–5 minutes ([authorizer.go:60](/Users/ddoyle/Development/claude/snellius-phase7/pkg/auth/sar/authorizer.go:60)), and the shared cache evicts entries under pressure ([cache.go:313](/Users/ddoyle/Development/claude/snellius-phase7/pkg/auth/sar/cache.go:313)).

   At the specified five-second polling interval, a request can add at most 256 decisions. A sufficiently wide install cannot accumulate all denials within their lifetime; expired or evicted prefix entries consume the next request’s budget again. Thus repeated requests need never reach `ReachNo`. [Plan:661](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:661)’s report-only measurement does not establish a supported bound or recovery strategy. The convergence acceptance test needs advancing time and cache pressure, alongside a defined size/rate assumption or a different strategy.

5. **P2 — The new `notAssessed` outcome has no web integration step.**  
   [Plan:488](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:488) emits `ok: true, notAssessed: true`, but the revision does not update the check-result type, shape or check renderer for this outcome. The existing type lacks the field ([types.ts:48](/Users/ddoyle/Development/claude/snellius-phase7/web/packages/shell/src/api/types.ts:48)); the renderer maps `ok: true` directly to a healthy icon and label ([CheckedList.tsx:88](/Users/ddoyle/Development/claude/snellius-phase7/web/packages/shell/src/pages/install/CheckedList.tsx:88)).

   Following the plan therefore presents an unassessed check as healthy in “What was checked.” Add the field and an explicit unassessed rendering state, with a component assertion. It can remain neutral for rollup purposes without appearing to have passed.
## Re-review triage
| # | Finding | Source | Decision | Note |
|---|---|---|---|---|
| 2 (partly) | 023's population depends on hidden occupancy | reviewer | accept | `AnyOf` for "exists any member" populations; no basis bucket depends on hidden occupancy (018, 022, 011 adjusted). 012 stays: its contributors decide the outcome, so hiding it is right |
| 9, 10 (partly) | Reachability leaks and conflicts with the scope | reviewer | accept, resolved by D-103 | Reachability removed |
| N1 | `ReachUndecided` is an existence oracle | reviewer | accept, resolved by D-103 | Every install listed for every signed-in viewer (user, 2026-11-16) |
| N2 | `notAssessed` reveals hidden grace activity | reviewer | accept | Stable basis separate from held items |
| N3 | Readable failures become OK | reviewer | accept | Only fully filtered pending results normalise |
| N4 | Scan need not converge | reviewer | accept, resolved by D-103 | No scan |
| N5 | `notAssessed` renders as healthy | reviewer | accept | "Not checked" state in the web |
| A1 | Denied-all viewer on Extreme may exceed the 1,000-check cap on install pages | author | defer: D-100 cap, unchanged | Pre-existing, not from reachability; the namespace scope (Task 37) is the mitigation; Task 38 bench reports it |

Fixes: `cc286c8`, D-103 `cd464ec`; brief rows 26–27 follow D-103.

## Pass 3 (29878e1..76a8df3)

Reviewed only `29878e1..76a8df3`, consulting existing code for consequences. **Four new P1 problems remain.** No files edited; no tests run.

| Re-review point | Status | Evidence |
|---|---|---|
| **Partial #2 — incomplete bases** | **partly** | [Plan:480](docs/phases/phase-7/implementation-plan.md:480) prohibits occupancy-derived buckets; [plan:503](docs/phases/phase-7/implementation-plan.md:503), [512](docs/phases/phase-7/implementation-plan.md:512) and [514](docs/phases/phase-7/implementation-plan.md:514) replace the problematic namespace enumeration. [Plan:515](docs/phases/phase-7/implementation-plan.md:515) gives 023 readable-consumer `AnyOf` semantics, but its shared grace history remains unprojected; see finding 3. |
| **#9 — denied-all reachability bound** | **resolved** | [Plan:572](docs/phases/phase-7/implementation-plan.md:572) removes reachability entirely under approved D-103. |
| **#10 — reachability versus scope** | **resolved** | [Plan:645](docs/phases/phase-7/implementation-plan.md:645) removes the global reachability checks while retaining dependency checks. |
| **N1 — install existence oracle** | **resolved** | Every signed-in viewer sees every install, explicitly authorized by [D-103:1544](docs/decisions.md:1544). |
| **N2 — hidden grace changes `notAssessed` / suppresses readable holds** | **partly** | [Plan:486–491](docs/phases/phase-7/implementation-plan.md:486) separates stable bases from held items; resource status has no stable basis at [503](docs/phases/phase-7/implementation-plan.md:503). This fixes the original `notAssessed` transition, but retained pending text still leaks mixed populations; see finding 1. |
| **N3 — readable failures become OK** | **resolved** | [Plan:492](docs/phases/phase-7/implementation-plan.md:492) explicitly preserves readable non-pending failures and normalizes only filtered pending results. |
| **N4 — scan convergence** | **resolved** | No scan remains: [plan:572](docs/phases/phase-7/implementation-plan.md:572). |
| **N5 — unassessed renders healthy** | **resolved** | [Plan:532](docs/phases/phase-7/implementation-plan.md:532) adds the type and neutral “Not checked” rendering; [538](docs/phases/phase-7/implementation-plan.md:538) requires component assertions. |

New problems introduced by this revision, ranked:

1. **P1 — Keeping the original pending `Error` leaks hidden counts.**  
   [Plan:491](docs/phases/phase-7/implementation-plan.md:491) recomputes visible holds, but [492](docs/phases/phase-7/implementation-plan.md:492) preserves their original `Error` whenever one readable hold remains. Resource-status errors contain the **global** grace count ([checks.go:552](pkg/health/checks.go:552)); proxy errors similarly contain the global missing-proxy count ([checks.go:776](pkg/health/checks.go:776)). With one readable item held, adding a hidden held item changes “1 resource” to “2 resources” despite projected `Hold.Count` remaining 1. Re-render pending text from projected items.

2. **P1 — The narrowed skew result basis exposes hidden Pod changes.**  
   [Plan:512](docs/phases/phase-7/implementation-plan.md:512) changes the stable result basis to only the controller Deployment. However, [skew.go:116](pkg/health/skew.go:116) aggregates unknown versions across **all proxy sets**, and [124](pkg/health/skew.go:124) adds `skewUnknownDetail` to the result. A viewer allowed the controller Deployment but denied a proxy namespace sees its check text change when a hidden Pod’s image becomes unversioned. The stable basis must cover these inputs, or the text must be recomputed from readable inputs.

3. **P1 — `AnyOf` filters consumers but retains hidden-consumer grace history.**  
   [Plan:485](docs/phases/phase-7/implementation-plan.md:485) replaces only `Facts` and `Detail`; [515](docs/phases/phase-7/implementation-plan.md:515) now permits 023 when any consumer is readable. Existing grace state is keyed solely by Service port ([endpoints.go:138](pkg/health/endpoints.go:138)), and supplies both the deadline and finding `Since` ([144](pkg/health/endpoints.go:144), [148](pkg/health/endpoints.go:148)). A hidden consumer can start that timer before a readable route qualifies. When the readable route qualifies, the viewer can receive an immediate warning or older `Since` instead of a fresh hold. Member filtering does not remove this hidden influence; timing needs readable-witness semantics too.

4. **P1 — Status-progress holds lose the threshold population’s basis.**  
   [Plan:514](docs/phases/phase-7/implementation-plan.md:514) protects the **finding** with cluster-wide population checks, but reduces its result basis to the controller Deployment and held-item bases to stuck objects plus that Deployment. Yet the hold exists only after the global threshold passes ([freshness.go:187](pkg/health/freshness.go:187), [202](pkg/health/freshness.go:202)). With readable stuck objects and controller unchanged, adding hidden stuck routes can cross the threshold and create a visible hold. The hold’s deciding basis must also include the threshold population, or the threshold must be evaluated over readable inputs.
## Pass 4 (76a8df3..6a8f81a)

Reviewed only `76a8df3..6a8f81a`, consulting existing code to check consequences. **All four third-pass P1s are resolved in the plan, but rules (a) and (b) are not yet applied consistently.** D-102 and D-103 are accepted exceptions. No files edited; no tests run.

| Third-pass P1 | Status | Evidence |
|---|---|---|
| Original pending `Error` leaks hidden counts | **resolved** | [Plan:490](docs/phases/phase-7/implementation-plan.md:490) introduces `PendingText`; [plan:496](docs/phases/phase-7/implementation-plan.md:496) renders it from the projected hold count. |
| Skew unknown-version text includes hidden proxies | **resolved** | [Plan:516](docs/phases/phase-7/implementation-plan.md:516) replaces the aggregate note with per-source notes carrying their own bases. |
| 023 retains hidden-consumer grace history | **resolved** | [Plan:519](docs/phases/phase-7/implementation-plan.md:519) requires cluster-wide route-kind permissions for both the finding and hold, covering the shared Service-port timer. |
| 018 holds omit the threshold population | **resolved** | [Plan:518](docs/phases/phase-7/implementation-plan.md:518) includes the whole threshold population in each held item’s basis. |

Remaining findings:

1. **P1 — Proxy-stat result counts and outcome branches remain unprojected.**  
   [Plan:517](docs/phases/phase-7/implementation-plan.md:517) gives these results no stable basis and specifies no projection of their gap aggregate. [proxystats.go:144](pkg/health/proxystats.go:144) computes text from all Gateways and failed proxies; [proxystats.go:145](pkg/health/proxystats.go:145) switches to `NotAvailable` when every Gateway is a gap. Adding a hidden healthy Gateway can change a readable “all unavailable” result into an OK result; adding hidden failures changes counts. `Notes` filtering alone cannot fix that branch or its denominator. Additionally, the planned hold basis is Gateway plus controller Deployment, while its deadline and existence depend on proxy Pods ([proxystats.go:118](pkg/health/proxystats.go:118), [proxystats.go:125](pkg/health/proxystats.go:125)). Include those deciding populations and recompute the result from projected inputs.

2. **P1 — xDS result text and restart holds still expose hidden populations.**  
   [Plan:509](docs/phases/phase-7/implementation-plan.md:509) protects the result with only the controller Deployment, and holds with Gateway plus that Deployment. Yet [xds.go:143](pkg/health/xds.go:143) branches on the total owned-Gateway count and embeds that count in an error. A hidden Gateway can change both the error and whether this failure exists. Separately, controller Pod changes set `changedAt` ([xds.go:151](pkg/health/xds.go:151)), which creates the visible restart hold and deadline ([xds.go:182](pkg/health/xds.go:182)). A viewer allowed the Deployment but denied its Pods can therefore observe a hidden Pod restart. Neither population is covered by the revised bases.

3. **P1 — Resource-status holds discard the parent that decides their deadline.**  
   [Plan:486](docs/phases/phase-7/implementation-plan.md:486) and [plan:507](docs/phases/phase-7/implementation-plan.md:507) assign these holds only the resource in grace. However, declared delegation grace starts at the later first sight of the child and parent route ([delegation.go:30](pkg/health/delegation.go:30)); that value enters the resource hold at [checks.go:477](pkg/health/checks.go:477). Changing a hidden parent’s generation can create or extend a readable child’s hold, changing `Pending`, text and `Hold.Until`, even though the 011 finding itself is filtered. The held item needs the deciding parent basis too.

4. **P1 — Skew’s proxy findings still omit controller Pods used to choose the comparison version.**  
   [Plan:516](docs/phases/phase-7/implementation-plan.md:516) includes proxy-namespace Pods and the controller Deployment, but not controller-namespace Pods for proxy skew. When no reported version exists, [skew.go:174](pkg/health/skew.go:174) reads controller Pod versions and [skew.go:188](pkg/health/skew.go:188) uses their shared version. That value determines whether a proxy finding exists and its text ([skew.go:254](pkg/health/skew.go:254), [skew.go:261](pkg/health/skew.go:261)). With the Deployment and proxy inputs readable, changing an unreadable controller Pod’s image can create, remove or rewrite the finding. The per-source unknown-version-note fix does not cover this comparison input.

5. **P1 — The Events-cap note is incorrectly exempted as `Collection`.**  
   [Plan:515](docs/phases/phase-7/implementation-plan.md:515) explicitly marks this note collection-only. It appears when `Snapshot.EventsCapped` changes ([process.go:373](pkg/health/process.go:373)); that flag is set by the global qualifying-Event count crossing 10,000 ([events.go:297](pkg/cache/events.go:297)). Hidden Events can therefore add visible “More than 10,000 Events” text while readable objects remain unchanged. Overflow also evicts Event histories ([events.go:369](pkg/cache/events.go:369)), potentially changing a readable Pod’s probe count and threshold result ([process.go:381](pkg/health/process.go:381)). The Events population needs protection; an object-free sentence is still object-derived.

6. **P1 — Undeclared extension checks fail closed only for findings, not results.**  
   [Plan:485](docs/phases/phase-7/implementation-plan.md:485) makes `BasisDeclarer` optional and requires `Evaluate` to mark only the check’s *findings* unknown. An existing extension can return an object-derived non-pending `Error` without declaring a result basis. Under [plan:496](docs/phases/phase-7/implementation-plan.md:496), that failure retains its original text, with no specified unknown basis to suppress it. Hidden-object text and failure state remain observable. Undeclared results must also fail closed.

7. **P2 — Policy-target holds are missing from the `HoldOn` migration.**  
   [Plan:486](docs/phases/phase-7/implementation-plan.md:486) lists the core migrations but omits the callback passed at [checks.go:489](pkg/health/checks.go:489). It emits target and ancestor holds at [targets.go:132](pkg/health/targets.go:132) and [targets.go:137](pkg/health/targets.go:137). Following the plan leaves these on legacy `Hold`, whose items receive unknown bases and are filtered—even when the viewer can read every relevant input. Specify their target, selector-population and ancestor bases and migrate the callback.

8. **P2 — Rule (b)’s result contract contradicts the projection algorithm.**  
   [Plan:480](docs/phases/phase-7/implementation-plan.md:480) requires `notAssessed` when the deciding population is unreadable. But [plan:494](docs/phases/phase-7/implementation-plan.md:494) determines that field solely from the stable result basis. For 018 that basis is only the controller Deployment; for 023 it is empty ([plan:518](docs/phases/phase-7/implementation-plan.md:518), [plan:519](docs/phases/phase-7/implementation-plan.md:519)). Denying the threshold or consumer population therefore removes findings and holds while leaving an ordinary OK result. Reconcile the contracts using a permission-only assessment rule; deriving `notAssessed` from whether hidden holds currently exist would recreate the earlier leak.
## Pass 5 (e876a03..bd1a57b)

Reviewed only `e876a03..bd1a57b`, consulting existing code for evidence. **The fourth-pass fixes are largely resolved, but P1 gaps remain in the declarations and finding bases.** No files edited; no tests run.

| Fourth-pass finding | Status | Evidence in the revised plan |
|---|---|---|
| 1. Proxy-stat aggregates and holds | **Partly** | Whole-result gating addresses aggregate branches at [plan:488](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:488); [plan:543](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:543) adds proxy Pods to finding bases. Deployment-dependent readiness remains uncovered; see finding 5 below. |
| 2. xDS Gateway counts and restart holds | **Resolved** | [Plan:501](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:501) declares Gateway, Deployment and Pod; [plan:534](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:534) includes controller-Pod and cluster-wide Gateway populations in finding bases. |
| 3. Delegation-parent grace deadline | **Resolved** | [Plan:519](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:519) removes hold projection; [plan:532](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:532) adds the deciding parent route to declared-child finding bases. |
| 4. Controller Pods deciding proxy skew | **Resolved** | [Plan:542](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:542) explicitly includes controller-namespace Pods. |
| 5. Events-cap exemption | **Resolved** | [Plan:508](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:508) declares Events for results; [plan:541](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:541) requires cluster-wide Events for probe findings. |
| 6. Undeclared extension results | **Resolved** | [Plan:488](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:488) explicitly makes them “Not checked”; [plan:550](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:550) specifies the regression case. |
| 7. Policy-target `HoldOn` migration | **Resolved** | D-104 eliminates that migration at [plan:519](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:519); [plan:532](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:532) and [plan:539](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:539) cover the target-held findings. |
| 8. Conflicting assessment contracts | **Resolved** | [Plan:487](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:487) makes assessment permission-only; the next line specifies the complete whole-or-“Not checked” contract. |

1. **P1 — The declared-kinds table still omits kinds the checks read.**  
   The table at [plan:495](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:495)–512 cannot safely drive `Assessed` as written:

   | Check / plan line | Missing kinds | Code evidence |
   |---|---|---|
   | controller scope, 496 | **Pod** | `pkg/install/scope.go:158` selects a controller Pod before the Deployment template; its environment and container errors determine `Scope` (`:102`, `:120`, `:171`). `pkg/health/checks.go:157` returns that error. |
   | gateway proxies, 500 | **GatewayClass, GatewayParameters** | `pkg/health/checks.go:745` calls `proxyMode`; `pkg/health/finding.go:265` delegates to the resolver. `pkg/proxymode/proxymode.go:46` reads the class, and `:51`/`:61` resolve parameters. These decide self-managed versus missing-proxy grace. |
   | xDS rejects, 501 | **Route, ListenerSet, Policy categories** | `pkg/health/xds.go:162` calls `changesOn`; `:445` onward reads these owned populations to populate `RecentlyChanged`. |
   | computed views, 506 | **Namespace** | Computed attachment inputs read namespace labels at `pkg/computed/check.go:418`; listener-count fingerprints include them at `:338`. Health reads the resulting checks at `pkg/health/computed.go:43`. |
   | restarts / probes / rollouts, 507–508 | **Gateway** | `pkg/health/process.go:88` consumes per-Gateway subjects. Those are built from owned Gateways at `pkg/engine/engine.go:546`; Gateway presence can change `proxyGap` and its result detail (`pkg/health/process.go:129`). |
   | skew, 509 | **Gateway, GatewayClass, GatewayParameters** | `pkg/health/skew.go:275`, `:284`, `:310` explicitly look up all three. Gateway process sets also determine the gap result. |
   | proxy stats, 510 | **Deployment** | `pkg/health/proxystats.go:105` uses `proxyReady`; `:207` reads process membership selected by the proxy Deployment (`pkg/process/kube.go:51`, `:56`, `:64`). This affects holds and deadlines. |

   `owned` explicitly excludes the Parameters category ([plan:491](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:491)), so it cannot implicitly cover GatewayParameters. Add these declarations and audit derived-input dependencies rather than only checking that implementations reproduce this table.

2. **P1 — Listener-count KGWUI-012 findings still expose a hidden population and its persistence window.**  
   [Plan:539](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:539) requires resource plus `Deps`/`Contributors`. But listener-count checks deliberately have **no `Deps`** (`pkg/computed/check.go:258`). Their expected count and fingerprint depend on candidate routes and namespace labels (`:264`, `:274`, `:307`). A fingerprint change resets the 30-second window and `Since` (`pkg/engine/persist.go:67`–73), after which health emits the finding (`pkg/health/computed.go:45`–51).

   Thus a hidden route can create, remove or retime a finding on a readable Gateway. The population guard at [plan:586](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:586) protects detail-page computed facts, not these health findings. Apply the corresponding route and namespace population permissions to the finding basis too.

3. **P1 — Computed/orphan KGWUI-011 triggers still omit contributing ancestors that decide maturity and `Since`.**  
   [Plan:532](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:532) covers a named parent and its delegation-selection bucket, but does not require the computed trigger’s complete dependencies. `pkg/health/delegation.go:67` gates emission on maturity and `:77` explicitly removes contributing routes from evidence. Orphan checks have dependencies across selecting parents (`pkg/computed/check.go:1248`) and fingerprint those parents’ inputs (`:1258`–1269).

   A hidden ancestor can reset that persistence window while the child and its planned basis remain readable. Require the complete trigger dependencies/contributors and any selection populations, including for orphan findings that have no single named parent.

4. **P1 — The skew basis table omits CRD findings and their deciding population.**  
   [Plan:542](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:542) specifies controller-replica and proxy skew only. The check also emits expected-bundle and mixed-bundle CRD findings (`pkg/health/skew.go:381`, `:390`). Their comparison representative is selected by counts across the CRD population (`:365`–375), and **all** these findings are suppressed by the controller rollout/settle timer (`:101`–108).

   Add an explicit CRD-finding basis containing cluster-wide CRD list permission, the controller Deployment deciding settlement, and the controller-version inputs quoted in the finding. Named CRD evidence alone cannot protect a representative selected by hidden CRDs.

5. **P1 — Proxy-stat finding bases omit the Deployment population deciding the timer.**  
   [Plan:543](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:543) requires Gateway and proxy-namespace Pods only. However, the timer advances only when `proxyReady` succeeds (`pkg/health/proxystats.go:105`–112). That function’s process membership comes from a Deployment lookup and its selector (`pkg/process/kube.go:51`–65). Changing an unreadable Deployment can change which readiness value is used, reset the timer, and remove or retime a readable Gateway’s finding.

   Include the Deployment selection population in the finding basis. Adding Deployment only to `CheckKinds` fixes results, but leaves findings exposed.
## Pass 6 (b2df78b..594cc96; run-reviewer.sh, codex, resolved_model gpt-6.1-sol, status ok)

**The plan still has P1 gaps.** D-105 covers the previously identified derived populations, but several “own-object” findings have additional dependencies, and the kind probe cannot guarantee declaration completeness.

No files edited; no tests run. This is a plan review against the existing analyzers.

| Fifth-pass P1 | Status | Evidence |
|---|---|---|
| 1. Missing declared kinds | **Partly** | The identified additions appear at [plan:494](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:494)–508. However, transitive dependencies remain missing: resource status excludes controller Pods/Deployments; backend status declares only Backend. See findings 1 and 5 below. |
| 2. Listener-count 012 population/window | **Resolved** | 012 is derived at [plan:535](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:535), with routes and Namespace declared at [plan:504](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:504). The hidden-listener-count mutation is specified at [plan:467](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:467). |
| 3. Computed/orphan 011 ancestors | **Partly** | Computed/orphan triggers are derived at [plan:535](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:535). The existing merge with own-object status triggers remains unspecified; see finding 4. |
| 4. CRD-skew population | **Resolved** | All 022 variants, explicitly including CRD bundles, are derived at [plan:535](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:535); their declaration includes CRDs, Deployment and Pod at [plan:507](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:507). |
| 5. Proxy-stat Deployment timer | **Resolved** | 025/026 are derived at [plan:535](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:535), and Deployment joins their declared kinds at [plan:508](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:508). |

1. **P1 — correctness — Backend-status 002 is consumer-dependent, not own-object.**  
   [Plan:534](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:534) classifies backend-status 002 as own-object; [plan:503](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:503) declares only Backend.

   However, [backend.go:47](/Users/ddoyle/Development/claude/snellius-phase7/pkg/health/backend.go:47) evaluates only `Owned.Backends`. That set is selected from consumer routes’ backendRefs at [install/delegation.go:250](/Users/ddoyle/Development/claude/snellius-phase7/pkg/install/delegation.go:250), with membership conditional on those references at [install/delegation.go:269](/Users/ddoyle/Development/claude/snellius-phase7/pkg/install/delegation.go:269).

   A readable, rejected Backend gains or loses its 002 when its only hidden consumer appears or disappears. The Backend does not name that consumer, so the proposed basis cannot protect the finding. Classify this trigger as derived and include its upstream dependencies, or explicitly redesign evaluation so consumer occupancy cannot decide the shown finding.

2. **P1 — correctness — Listener 013/014 findings scan unnamed ListenerPolicies.**  
   The own-object classification at [plan:534](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:534) covers 013/014 without distinguishing listener triggers.

   [refs.go:288](/Users/ddoyle/Development/claude/snellius-phase7/pkg/health/refs.go:288) calls `listenerPolicyCARefs`, which scans owned policies at [refs.go:209](/Users/ddoyle/Development/claude/snellius-phase7/pkg/health/refs.go:209), including policies targeting the resource through selectors. This changes `RefsEvidence.Attributed` at [refs.go:336](/Users/ddoyle/Development/claude/snellius-phase7/pkg/health/refs.go:336). The existing test explicitly demonstrates the change with and without a ListenerPolicy at [refs_test.go:406](/Users/ddoyle/Development/claude/snellius-phase7/pkg/health/refs_test.go:406).

   These findings read beyond their resource and named objects. A hidden policy can change their typed evidence in `/health`; placing only the certificate/target reference in `Basis.Refs` does not cover it. These listener triggers require derived treatment or a resource-only attribution algorithm.

3. **P1 — correctness — Policy 003/016 triggers retain derived gates.**  
   [Plan:534](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:534) makes 003 and named-target 016 own-object, but:

   - Policy 003 is suppressed by `allMissing`, obtained from computed target results at [checks.go:491](/Users/ddoyle/Development/claude/snellius-phase7/pkg/health/checks.go:491) and consulted at [checks.go:505](/Users/ddoyle/Development/claude/snellius-phase7/pkg/health/checks.go:505). Those results include selectors. A global policy’s selector can match a hidden object in another namespace ([kgw25/targets.go:57](/Users/ddoyle/Development/claude/snellius-phase7/pkg/computed/kgw25/targets.go:57)). Adding that object can replace a hidden derived 016 with a shown own-object 003.
   - Named-target 016 depends on the controller-selected rule set. Version selection comes from controller evidence ([engine.go:518](/Users/ddoyle/Development/claude/snellius-phase7/pkg/engine/engine.go:518), [upstream.go:88](/Users/ddoyle/Development/claude/snellius-phase7/pkg/install/upstream.go:88)); kgateway 2.5 stops computing named-target 016 entirely ([targets_test.go:484](/Users/ddoyle/Development/claude/snellius-phase7/pkg/health/targets_test.go:484)). Computed 016 also prints the selected rule set at [targets.go:341](/Users/ddoyle/Development/claude/snellius-phase7/pkg/health/targets.go:341).

   Thus unchanged readable policy/target objects can produce different own-object findings after hidden selector-population or controller changes. The classification must cover suppression and rule selection, not merely the final finding constructor.

4. **P1 — architecture — Classifying 011 per trigger does not resolve its merged output.**  
   [Plan:524](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:524) classifies 011 per trigger, but [checks.go:483](/Users/ddoyle/Development/claude/snellius-phase7/pkg/health/checks.go:483) merges computed triggers into status triggers before projection. The merge changes detail, `Since`, severity and evidence ([delegation.go:165](/Users/ddoyle/Development/claude/snellius-phase7/pkg/health/delegation.go:165)).

   Keeping the merged finding own-object leaks computed dependencies. Promoting it to derived only when a computed trigger joins makes an existing shown status finding disappear when hidden inputs mature that trigger—also violating D-085.

   The plan needs an explicit representation and projection rule: retain independently projectable trigger contributions and merge only the admitted contributions, or consistently gate the entire trigger family. Merely assigning a class to the final merged finding cannot preserve the required invariance.

5. **P1 — architecture — Output perturbation cannot guarantee complete declarations; controller scope supplies a concrete missed dependency.**  
   [Plan:512](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:512)–514 treats changed output as a read recorder and [plan:480](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:480) substitutes it for declaration review.

   Consider a controller Pod and Deployment template initially containing identical discovery settings. Removing the Pod falls back to the same template ([scope.go:158](/Users/ddoyle/Development/claude/snellius-phase7/pkg/install/scope.go:158)); changing its labels, generation, conditions or image need not change resource-status output. The probe never changes environment variables. Yet changing that hidden Pod’s discovery-selector environment changes scope ([scope.go:106](/Users/ddoyle/Development/claude/snellius-phase7/pkg/install/scope.go:106)), which changes owned resources ([assign.go:121](/Users/ddoyle/Development/claude/snellius-phase7/pkg/install/assign.go:121)) and therefore resource-status findings and pending counts.

   Resource status does not declare Pod or Deployment ([plan:497](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:497)). A viewer allowed every declared kind but denied controller Pods can therefore receive changing output.

   More generally, the probe misses unexercised branches, empty populations, fields it does not mutate, interactions requiring multiple changes, historical reads requiring multiple computations, and collector-derived inputs held constant outside the snapshot. These limitations become a concrete D-085 leak when such a dependency is omitted, as above. The probe is useful regression testing; it is not an enforceable read boundary.

6. **P2 — correctness — Task 35’s positive 023 acceptance case conflicts with D-105.**  
   [Plan:638](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:638) says granting namespace EndpointSlice access restores counts, 023 and states “as today.”

   Under [plan:535](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:535), 023 is derived. Its declaration at [plan:510](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:510) therefore requires cluster-wide Route, Service and EndpointSlice access. Granting namespace EndpointSlice access alone cannot restore 023.

   Specify the intended positive permission set and reconcile this with the brief’s readable-whole-basis contract ([brief:20](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/phase-7.4-brief.md:20)). Otherwise implementation cannot satisfy both acceptance statements.

D-104’s whole-result gating is a reasonable conservative implementation of §7.6. D-105’s conservative treatment of genuinely derived findings can also preserve D-085, provided declarations cover the complete pipeline. The current own-object exceptions and probe do not establish that condition.

A simpler design is one finding dependency contract containing named references plus permission requirements for populations and upstream context. Own-object findings are simply the case containing only named references; trigger contributions retain separate dependencies until projection. Keep whole-result gating and use perturbation tests as supplementary evidence. Completeness must come from an explicit dependency boundary—including assignment, scope, rule selection and history—or conservatively gate outputs whose dependencies cannot yet be established.
## Pass 7 (3921539..6cf6f86; run-reviewer.sh, codex, status ok)

The plan still has **one P1 and two P2 findings**. D-106 resolves the identified derived-finding cases, but the proposed boundary does not yet establish the claimed isolation.

No files edited; no tests run. This is a plan review against the existing code.

| Sixth-pass finding | Status | Evidence |
|---|---|---|
| 1. Backend-status 002 depends on consumers | **Resolved** | Explicitly derived at [implementation-plan.md:535](docs/phases/phase-7/implementation-plan.md:535); hidden-consumer mutation at [line 467](docs/phases/phase-7/implementation-plan.md:467). |
| 2. Listener 013/014 scans ListenerPolicies | **Resolved** | Listener triggers explicitly derived at [line 536](docs/phases/phase-7/implementation-plan.md:536). |
| 3. Policy 003/016 has derived gates | **Resolved** | Policy 003 and every 016 trigger are derived at [lines 537–538](docs/phases/phase-7/implementation-plan.md:537). Controller rule selection is now an approved exception. |
| 4. Merged 011 cannot be classified per trigger | **Resolved** | The entire family is gated at [line 539](docs/phases/phase-7/implementation-plan.md:539). |
| 5. Perturbation probe cannot establish complete dependencies | **Partly** | The probe and declarations are removed, and controller settings are explicitly exempted. However, the replacement boundary still admits unrecorded dependencies; findings 1–2 below. |
| 6. Namespace EndpointSlice grant incorrectly restores 023 | **Resolved** | [Task 35:644](docs/phases/phase-7/implementation-plan.md:644) now distinguishes endpoint counts from full-read-only 023. |

1. **P1 — architecture — Ownership and scope callbacks import hidden object state beyond controller settings.**

   **Location:** [implementation-plan.md:493](docs/phases/phase-7/implementation-plan.md:493), [line 515](docs/phases/phase-7/implementation-plan.md:515).

   `Owns` and `InScope` are not functions of controller settings alone:

   - Gateway ownership reads a GatewayClass’s `controllerName`, then the Gateway’s class reference: [assign.go:132](pkg/install/assign.go:132), [line 152](pkg/install/assign.go:152).
   - ListenerSet ownership follows its parent Gateway: [assign.go:178](pkg/install/assign.go:178).
   - Route ownership follows those Gateway/ListenerSet ownership maps: [assign.go:224](pkg/install/assign.go:224).
   - Namespace scope evaluates the Namespace object’s labels: [assign.go:121](pkg/install/assign.go:121), [scope.go:59](pkg/install/scope.go:59).

   Consequently, with controller settings unchanged, changing an unreadable GatewayClass can remove a readable Gateway and its routes from an install. A readable route’s 015 can disappear despite `ownRules` recording no named objects. Changing an unreadable ListenerSet or Namespace labels can likewise change readable rows and findings.

   `Named` cannot protect dependencies evaluated before invocation when selecting `resources`, and calling `ic.Owns(ref)` records no basis. These changes are outside D-106’s **controller-settings** exception. The plan itself recognizes this distinction for Backend consumer occupancy at [line 535](docs/phases/phase-7/implementation-plan.md:535).

   **Why:** Filtering findings after global assignment cannot preserve D-085 when hidden objects determine which readable resources are analyzed or listed. Specify permission-aware assignment and ownership dependencies, or explicitly obtain approval for a broader exception. Add mutations for hidden GatewayClass, ListenerSet and Namespace-label changes with controller settings fixed.

2. **P2 — architecture — The AST test does not enforce D-106’s “nothing else is reachable” boundary.**

   **Location:** [implementation-plan.md:513](docs/phases/phase-7/implementation-plan.md:513), [line 544](docs/phases/phase-7/implementation-plan.md:544).

   The test scans `own_*.go` for selected identifiers and package variables. An analyzer can call a package function in another file whose implementation reads global state; the analyzer contains none of the forbidden references. Function signatures also permit captured state, while `Owns`, `InScope`, `Views` and `Named` execute implementations outside the scanned files.

   The current types therefore constrain the visible API, but do not constrain transitive reads. Holding install context fixed in `TestOwnBoundaryIgnoresOthers` also freezes the callbacks that conceal finding 1’s dependencies.

   Specifically:

   - `Conditions` and `Delegation` are legitimate settings values.
   - The current decoder is object-local: [decode.go:92](pkg/engine/decode.go:92) decodes/cache-looks-up the supplied object. The `objects.Decoder` interface itself does not enforce that property.
   - Current `FirstSeen` looks up only the supplied object’s UID/generation: [engine.go:274](pkg/engine/engine.go:274). However, `Named.FirstSeen` merely comments that its argument must be the resource or a named object; the plan specifies no validation or recording for that method.
   - Ordinary methods on the supplied `Unstructured` read that object; the additional state access comes from the supplied interfaces, callbacks and transitive helpers.

   **Why:** The brief promises that an analyzer exceeding the boundary makes a test fail, and D-106 relies on isolation “by construction.” This guard cannot deliver that guarantee. Define the trusted implementations and enforce the allowed call/dependency surface, including callback construction and history provenance; keep perturbation testing supplementary.

3. **P2 — correctness — The boundary lacks inputs needed to preserve existing findings, and `ownRules` contradicts its evidence invariant.**

   **Location:** [implementation-plan.md:491](docs/phases/phase-7/implementation-plan.md:491), [line 532](docs/phases/phase-7/implementation-plan.md:532), [line 544](docs/phases/phase-7/implementation-plan.md:544).

   The planned full-reader parity test requires today’s 002/003/013/014/015 output, but the interface omits inputs those paths use:

   - Route status selection matches `ControllerNames`: [checks.go:450](pkg/health/checks.go:450). Neither controller names nor a status-controller predicate appears in `InstallContext`.
   - 013/014 explanations use discovery knowledge, including kind availability and suggested groups: [refs.go:314](pkg/health/refs.go:314), [line 337](pkg/health/refs.go:337). The boundary supplies no catalog.
   - 015 parses a policy reference from status and emits it as `Evidence`: [rules.go:80](pkg/health/rules.go:80), [line 86](pkg/health/rules.go:86). Yet `ownRules` lists **no named objects**, while `runOwn` must panic in tests for evidence not recorded by `Named`.

   **Why:** Implementing the specified interface either loses existing full-reader behavior, violates the boundary, or fails the evidence assertion. Add narrowly scoped controller/discovery inputs and specify recording status-named policy references, including absent targets.

The concrete remaining hidden-object response path is **assignment and scope**, affecting both findings and inventory responses before projection. The current `Conditions`, `Delegation`, decoder and per-object first-seen implementations reveal no additional population dependency themselves; the plan still needs enforceable contracts around them.
## Pass 8 (672e69b..4c8ba92; run-reviewer.sh, codex, status ok)

**Three P2 findings remain; no P1 identified.** D-107 resolves the membership issue through the approved exception, but the boundary still does not enforce the claimed guarantee.

No files edited or tests run; this was a plan review against the existing code.

| Seventh-pass finding | Status | Evidence |
|---|---|---|
| 1. Hidden membership and scope inputs | **Resolved** | The approved exception is explicit at [implementation-plan.md:481](docs/phases/phase-7/implementation-plan.md:481); membership mutations and named masks are specified at [line 468](docs/phases/phase-7/implementation-plan.md:468). |
| 2. Boundary does not enforce isolation | **Partly** | Package isolation, callback construction and `FirstSeen` validation address several weaknesses at [lines 527–530](docs/phases/phase-7/implementation-plan.md:527). However, arbitrary analyzer closures remain accepted; finding 1 below. |
| 3. Missing inputs and contradictory 015 evidence | **Partly** | `ControllerNames` and `Catalog` are added at [lines 493–499](docs/phases/phase-7/implementation-plan.md:493), and 015 now records its policy at [line 549](docs/phases/phase-7/implementation-plan.md:549). Existing 015 behavior still needs inputs and scheduling absent from the interface; finding 3 below. |

1. **P2 — architecture — `runOwn` can classify a closure with unrestricted captured state as an own-object analyzer.**

   **Location:** [implementation-plan.md:523](docs/phases/phase-7/implementation-plan.md:523), [line 530](docs/phases/phase-7/implementation-plan.md:530).

   `own.Analyzer` is an exported function type, and `runOwn` accepts any value of that type. A caller in `pkg/health` can pass a closure that captures `in.Snapshot`, reads a hidden unrelated object, and returns an `own.Finding` with no evidence. `runOwn` then assigns `ClassOwn`; its recorded basis contains none of that dependency.

   This passes the specified package checks: the closure lives outside `pkg/health/own`, introduces no imports or package variables there, and need not call any trusted callback.

   **Why:** The brief promises that exceeding the boundary makes a test fail, and D-106 relies on isolation by construction. The proposed mechanism does not deliver that guarantee. Remove the arbitrary function input—for example, dispatch inside `own` using a closed analyzer selector—and constrain the runner to that entry point.

2. **P2 — correctness — The transitive import allowlist rejects required existing dependencies.**

   **Location:** [implementation-plan.md:527](docs/phases/phase-7/implementation-plan.md:527).

   The test must walk **transitive** imports and reject third-party packages outside four listed prefixes. However, the required dependency chain already includes:

   `pkg/objects` → `k8s.io/apimachinery/pkg/runtime` → `k8s.io/klog/v2` → `github.com/go-logr/logr`.

   The last package is outside the allowlist. The chain is present in `objects/decoder.go`, apimachinery’s `runtime/converter.go`, and klog’s `contextual.go`.

   **Why:** `TestOwnImportsAllowlisted` cannot pass as specified, even for the intended implementation. Define an audited transitive dependency closure, distinguishing direct analyzer imports from dependencies required by approved libraries. Silently stopping traversal at approved prefixes would weaken the stated guarantee.

3. **P2 — correctness — The boundary still lacks inputs and scheduling needed to preserve KGWUI-015.**

   **Location:** [implementation-plan.md:491](docs/phases/phase-7/implementation-plan.md:491), [line 523](docs/phases/phase-7/implementation-plan.md:523), [line 560](docs/phases/phase-7/implementation-plan.md:560).

   Two existing behaviors cannot be carried through the specified interface:

   - Policy recognition uses both discovery and the configured served-policy descriptors, excluding `ListOnly` kinds: [rules.go:110](pkg/health/rules.go:110), [line 122](pkg/health/rules.go:122). `Catalog` supplies discovery, but does not supply policy category or `ListOnly` information. The boundary has no equivalent of `in.Served`.
   - The delayed replacement hint schedules a wake at its deadline: [rules.go:89](pkg/health/rules.go:89). The analyzer receives `Now`, but has no wake callback or deadline output. Calling `Named.FirstSeen` cannot express this condition-transition-based deadline.

   **Why:** The plan requires unchanged full-reader findings, but implementing this interface either changes policy evidence selection or loses timely hint updates. Add narrowly scoped policy-kind configuration and a deadline mechanism carried into the existing report scheduler.

The package boundary therefore provides useful isolation, **not an enforceable dependency guarantee as written**. The concrete remaining bypass is the captured-state analyzer accepted by `runOwn`.

Outside the approved exceptions, I found no additional demonstrated hidden-object path through the planned list, detail, count, install, `/meta`, YAML, extension, backend, path or namespace-scope projections. That conclusion remains conditional on analyzer isolation: the bypass above can affect findings and every surface that derives state or counts from them. The plan is not implementable unchanged because of findings 2 and 3.
## Passes 3–8 triage
| Pass | # | Finding | Dimension | Decision | Note |
|---|---|---|---|---|---|
| 3 | 1 | Pending `Error` keeps hidden counts | correctness | accept, superseded by D-104 | `6a8f81a`, then whole-result gating |
| 3 | 2 | Skew text from hidden Pods | correctness | accept, superseded by D-104 | |
| 3 | 3 | 023 grace history from hidden consumers | correctness | accept, superseded by D-105/D-106 | 023 is derived |
| 3 | 4 | Status-progress holds lose the threshold population | correctness | accept, superseded by D-104 | |
| 4 | 1–6 | Proxy stats, xDS, delegation grace, skew Pods, Events cap, undeclared extension results | correctness | accept, resolved by D-104 (`e876a03`, `bd1a57b`) | Check results whole or "Not checked" |
| 4 | 7–8 | Policy-target holds; `notAssessed` contract | correctness | accept, resolved by D-104 | |
| 5 | 1 | Declared kinds incomplete | correctness | accept, superseded by D-106 | No kind lists |
| 5 | 2–5 | Listener-count 012, computed 011, CRD skew, proxy-stat timer | correctness | accept, resolved by D-105/D-106 | All derived |
| 6 | 1–3 | Backend 002, listener 013/014, policy 003/016 not own-object | correctness | accept (`6cf6f86`) | Derived |
| 6 | 4 | Merged 011 | architecture | accept (`6cf6f86`) | Whole family derived |
| 6 | 5 | Perturbation probe is not a boundary | architecture | accept, D-106 (`3921539`) | Typed boundary instead |
| 6 | 6 | 023 positive case conflicts | correctness | accept (`6cf6f86`) | Full reader only |
| 7 | 1 | Membership from hidden GatewayClass, ListenerSet, Namespace labels | architecture | accept, resolved by D-107 (user) | Named masks in the rule test |
| 7 | 2 | AST scan does not enforce the boundary | architecture | accept (`4c8ba92`) | Separate package, import allowlist |
| 7 | 3 | Boundary lacks controller names, catalog; 015 evidence | correctness | accept (`4c8ba92`) | |
| 8 | 1 | Closure bypass through `runOwn` | architecture | accept (`e35a66c`) | One closed entry point; sealed findings |
| 8 | 2 | Import allowlist cannot pass | correctness | accept (`e35a66c`) | Audited transitive closure |
| 8 | 3 | 015 needs policy kinds and a wake time | correctness | accept (`e35a66c`) | `PolicyKinds`, `Result.WakeAt` |

Pass 8 found no P1 and no leak outside the approved exceptions, conditional on the closure fix; the controller checked `e35a66c` and stopped the plan loop there. The per-task reviews and the 7.4 Codex review cover the code.
