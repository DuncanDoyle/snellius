# Phase 8 — Policies & Filters: Design

**Status:** Revised after the Codex design review (`reviews/design-codex.md`, 12 findings, all accepted). Rescoped on 2026-11-22 from the user's Phase 8 brief and the program handoff (program P-008, P-009; kgateway-tooling `bd1f17d`, now on main). Approved by the user on 2026-11-23 ("Approved"; runtime view: "Agree, Phase 12"). Calls marked (our call) are open to change.
**Date:** 2026-11-22
**Base:** `b01b677` (main after #64; the design adds no code)
**Roadmap:** `docs/roadmap.md`, Phase 8, principles 4, 9 and 10
**Decisions:** D-005, D-006, D-020, D-030, D-058, D-085, D-087, D-107, D-109, D-119 in `docs/decisions.md`. This design adds D-121 … D-129 (§12), accepted 2026-11-23.
**Code refs:** `file:line` at `b01b677`.
**Ships as:** `v0.9.0` (our call).

**One home per contract.** Layers and sources: §3. What the controller reports: §4. The policy model (E7): §5. Views and API: §6. Visibility: §7. Extension boundary: §8. Runtime view: §9. Provenance gap: §10. Testing: §11.

## Decisions for this phase

| # | Decision | Marker | Where |
|---|---|---|---|
| 1 | Snellius never resolves policy: no attachment inference, inheritance, precedence, merge, override or conflict logic | (user, 2026-11-22; program P-008) | §3 |
| 2 | "Effective policy" is not used. Every policy value carries its source | (user; P-008 point 4) | §3.2 |
| 3 | Phase 8 ships declared policy visibility (view A). The runtime view (view B) is built in Phase 12; its visibility rule is fixed here | (our call; Codex #3) | §6, §9 |
| 4 | `pkg/policy` is the exported contract for kgwctl (E7) | (program handoff) | §5 |
| 5 | Label selectors are shown as written, never resolved (M-2) | (P-008, M-2) | §5.4 |
| 6 | "Targeting a parent" joins on `spec.parentRefs` only, section-exact | (our call; Codex #4) | §5.3 |
| 7 | Phase 8 adds no policy input to the route signals | (our call) | §6.5 |
| 8 | Precedence annotations are shown as written, never evaluated | (our call) | §5.5 |
| 9 | Visibility follows D-085; target checks need named access | (Phase 7; Codex #1) | §7 |
| 10 | DirectResponse gains status findings only; it stays list-only | (our call; Codex #9) | §6.6 |

## 1. Intent and why

An operator looking at a route or Gateway asks: "which policies are aimed at this, did kgateway accept them, and is something wrong with them?" A platform owner asks the inverse: "where is this TrafficPolicy aimed, and what does kgateway say about it?"

The roadmap promised more: "the direct, inherited and effective policy for a route, with provenance; merge and override conflicts". Research on 2026-11-22 (kgateway v2.5.1 and main `8a71677`) shows the controller does not publish what that needs (§4). Building it in Snellius means copying kgateway's merge semantics, which are configurable, version-dependent and have open bugs. A copy shows what kgateway should do, not what it does. The user ruled this out (2026-11-22): "Snellius must NEVER become a second implementation of kgateway's policy resolution and translation logic."

So Phase 8 shows what is declared and what the controller reports, labelled as such, and states the gap upstream (§10).

## 2. Goals, non-goals and anti-goals

**Goals**
1. A Policies list and a policy detail page: kind, targets as written, controller status per ancestor in the controller's own words, missing targets, and the feature sections the policy configures.
2. On Gateway, listener, ListenerSet, route and backend pages: the policies whose targetRefs name the object, and the policies whose targetRefs name a parent the object declares, each labelled with its source.
3. Per feature (CORS, JWT, ext-auth, rate limiting, transformations, …): which policies declare it, and their configuration as written.
4. A declared-policy model in `pkg/` that kgwctl can import (E7, Snellius's part).
5. A visibility rule for the runtime view that Phase 12 inherits.

**Non-goals**
- Which policy wins, which fields apply to a route, merge or override outcomes. No source reports them per route today.
- The runtime filter chain and xDS access (Phase 12, §9).
- New policy findings, except DirectResponse status (§6.6).

**Anti-goals** (failure even if it works)
- A policy labelled "effective", "applied" or "active" by Snellius because it is attached or targets something.
- A list ordered as if it were precedence.
- A label-selector match presented as attachment.
- A page, count, filter or note that changes when a policy, ancestor, parent or target the viewer cannot read changes.

## 3. Layers and sources

### 3.1 Three layers (P-008 point 1)

| Layer | Contents | Who computes it | Snellius in Phase 8 |
|---|---|---|---|
| Declared | policy specs, targetRefs, targetSelectors, annotations; route parentRefs | users | reads and shows |
| Resolved | attachment, precedence, inheritance, merge, override, translation | kgateway controller only | shows its status verbatim (§4) |
| Runtime | xDS Envoy serves | Envoy, from the controller | Phase 12 (§9) |

### 3.2 Sources (D-122)

Every policy value in the model, API and UI carries exactly one source:

| Source | Meaning | Example |
|---|---|---|
| `declared` | written in a spec | targetRef names HTTPRoute `shop/cart` |
| `controller-reported` | a status condition as the controller wrote it, any type, status or reason | ancestor Gateway `infra/gw`: `Accepted=False Invalid`; `Attached=True Merged` |
| `controller-reported-contribution` | a policy ID in a `merge.*` xDS entry at one level | Phase 12 only (§9) |
| `inferred` | Snellius computed it from declared state or from reported status | target `shop/cart` not found; "worst accepted" on the list |
| `unknown` | no source can tell, or the viewer may not read the input | target in a namespace the viewer cannot read |

The program's "controller-reported attached" (P-008 glossary) is a relation of kind `attached-on-ancestor` with source `controller-reported`, made only from a current `Attached=True` condition. Raw `Accepted`, `Attached=False`, `TargetNotFound` and unknown reasons are `controller-reported` too; they make no positive attachment claim. Summaries over reported status (worst accepted, has problems) are `inferred` and say so.

UI copy that Snellius writes never says "effective", "applied" or "active" about a policy. Controller messages and YAML are shown verbatim.

## 4. What the controller reports (kgateway v2.5.1, main `8a71677`)

Details: program "Policy provenance".

### 4.1 Policy status

- **Ancestor granularity:** TrafficPolicy, ListenerPolicy and DirectResponse report on the Gateway or ListenerSet, with no sectionName, even when the policy targets a route or listener. BackendConfigPolicy reports per backend, with the port as sectionName. BackendTLSPolicy uses Gateway API's own vocabulary.
- **Types and reasons:** `Accepted` (Valid, PartiallyValid, Invalid, Pending, TargetNotFound), `Attached` (Attached, Merged, Overridden, Pending, TargetNotFound). Messages are fixed strings and never name another policy, except BackendConfigPolicy's TLS override, which names the BackendTLSPolicy. The list is not exhaustive: readers keep what they do not recognise.
- **Meaning:** `Attached` is OR-ed over every route and listener under the ancestor. `Overridden` means fully overridden in at least one place, not "has no effect" (kgateway#14861, fix #14870 open). `Merged` means some field lost somewhere.
- **Missing targets:** `TargetNotFound` on a synthetic `StatusSummary` ancestor, for explicit targetRefs only. A selector that matches nothing is silent (kgateway#14998).
- **Known wrong:** a listener-targeted ListenerPolicy can report `Attached` while never applied (kgateway#14919). GatewayExtension has no status (kgateway#14815, open on 2026-11-22).
- **Route side:** an HTTPRoute can report `Accepted=False` with RouteRuleReplaced, ListenerReplaced or GatewayReplaced naming the policy. Snellius raises KGWUI-015 for RouteRuleReplaced and RouteRuleDropped (`pkg/health/own/own.go:651`, `rules.go:30`); the other two show only as the route's raw conditions.

### 4.2 What it supports

| Question | Answer from status |
|---|---|
| Did kgateway accept the policy? | yes, per ancestor |
| Which ancestors report it attached? | yes; false positives are known (kgateway#14919) |
| Is an explicit target missing? | yes (TargetNotFound) |
| Was it merged or overridden somewhere under an ancestor? | yes, worst case per ancestor |
| Does it apply to route R? | no |
| Which policy overrode it, on which field? | no (one BackendConfigPolicy case) |
| Does a selector match anything? | no |

### 4.3 Snellius's existing target diagnostics

KGWUI-016 reports missing targets per rule set (`pkg/health/targets.go:90`): kgateway 2.3 and 2.4 compute it for explicit refs, sections and selectors (`pkg/computed/kgw24/targets.go:14`); 2.5 reads explicit refs from status and computes only the selector case (`pkg/computed/kgw25/targets.go:19`). The verifier can raise KGWUI-012 on a disagreement. Phase 8 keeps this as it is. The selector branch is M-2.

### 4.4 xDS metadata

`filter_metadata["merge.<Kind>.<group>"]` maps each merged field key to the policy IDs that contributed, on the listener, RouteConfiguration, virtual host and route. It is level-local: it does not say which level governs a request. It is missing on the HCM, backendRef policies, clusters and DirectResponse, and its keys are internal (U1). It belongs to view B (§9).

## 5. The policy model (`pkg/policy`, E7)

### 5.1 Shape (our call; names left to the builder)

```go
// Subject is the object side of a relation: a ref plus the section or port it names, if any.
type Subject struct {
	Ref         objects.Ref
	SectionName string // empty when the relation names the whole object
	Port        int32  // backend ports (BackendConfigPolicy)
}

// Policy is one policy object as declared and as its status reports it.
type Policy struct {
	Ref         objects.Ref
	Targets     []Target     // spec order; targetRefs, then targetSelectors (declared)
	Ancestors   []Ancestor   // status.ancestors as written (controller-reported)
	Annotations []Annotation // precedence annotations, as written (§5.5)
	Features    []Feature    // spec sections, labelled by the pack (§8)
}

// Ancestor is one status.ancestors entry. Conditions are copied verbatim.
type Ancestor struct {
	Ref        objects.ParentRef
	Controller string
	Conditions []metav1.Condition
	Current    bool // controller is this install's and every condition is fresh (§6.4)
	Summary    bool // the pack marks the controller's synthetic missing-target ancestor
}

// Relation links a policy to a subject, with its source and the declared path that justified it.
type Relation struct {
	Policy  objects.Ref
	Subject Subject
	Kind    RelationKind // targets, targets-parent, attached-on-ancestor
	Source  Source
	Via     []Subject    // targets-parent only: the parentRefs walked, in order (§5.3)
}
```

**Inputs (the E7 contract).** `Read(u, interp)` is pure: one object and the pack's interpreter (§8) in, no lookups. `Relations(subject, in Inputs)` takes the objects it may use and a `Lookup` for target checks. The caller decides what goes in: `pkg/api` passes only what the viewer may read and a lookup that applies named access (§7); kgwctl passes its one-shot snapshot (`cache.ListOnce`) and the user's kubeconfig answers. Neither function knows about viewers. The package imports `pkg/objects` and `pkg/computed` types only.

Runtime evidence (§9) is not part of this type. Phase 12 adds a separate result, from xdsview, joined on the policy ref.

### 5.2 Targets

- **targetRefs** (`declared`) link to the named object in the policy's namespace (GEP-713).
- **Target check** (`inferred` or `unknown`): the rule set's existing `PolicyTargeter` answers found, not found or section not found (`pkg/computed/targets.go:67`), the same answer KGWUI-016 uses. No second checker. The check is `unknown` when: the rule set has no targeter or returns unknown (2.5 explicit refs); the kind is not served; the kind's watch is denied, pending or scoped away from the namespace (`pkg/health/ownrun.go:191`); the install's scope is unknown (D-024); the section form is one the targeter does not decode; or the viewer may not read it (§7).
- **Controller-reported missing targets** come from the summary ancestor, shown as written.

### 5.3 Parents (D-129 item 1)

"Targeting a parent" comes from the subject's own `spec.parentRefs`, never from status, computed attachment (D-020) or an up-chain walk:

- A route's parentRef naming Gateway `G` with section `L` relates policies whose targetRefs name `G` without a section, or `G` section `L`.
- A parentRef naming `G` with no section relates only policies that name `G` without a section. A policy naming one listener of `G` is not related: the route does not declare that listener. A port-only parentRef is treated as unsectioned.
- A parentRef naming a ListenerSet relates its policies, and through the ListenerSet's own `spec.parentRef` its Gateway's unsectioned policies. That is the only two-hop join. `Via` records both hops.
- Every object on the path must be readable (§7). If one is not, the relation is not made.

### 5.4 Selectors (D-125)

targetSelectors show their group, kind, labels and section as written. Snellius never lists the objects a selector matches. Subject pages say "Policies that select by label are not listed here" and link to the Policies list filtered to that kind and namespace. KGWUI-016's zero-candidate diagnostic stays (M-2) and is never shown as attachment.

### 5.5 Precedence annotations (D-126)

The pack names the annotation keys (`kgateway.dev/inherited-policy-priority`, `kgateway.dev/policy-weight`). They appear as written, with "kgateway uses this to order policies. Snellius does not evaluate it." Lists sort by namespace and name and say so. This answers the Phase 2 concern that "a list without precedence can mislead" (user, 2026-10-23) by not presenting any order as precedence.

## 6. Views and API (view A)

### 6.1 API (additive to v1)

- `GET /api/v1/installs/{id}/policies`: list, server-side filters (kind, feature, namespace, has problems), paging, facets.
- `GET /api/v1/installs/{id}/policies/{group}/{kind}/{ns}/{name}`: detail. A hidden and an absent policy give the same response, as other details do.
- Relations in the existing Gateway, ListenerSet, route and backend detail responses, as a new field.
- Both endpoints register in the audit table (`pkg/api/audit.go:70`): the list as list access, the detail as a deliberate read.
- The YAML link uses the existing install-scoped YAML path.

### 6.2 Policies list (`/installs/:id/policies`)

Columns: kind, namespace/name, targets (as written; a selector shown as `HTTPRoute labels app=cart`), accepted (worst current `Accepted` over this install's ancestors, `inferred`), reported attached on (current `Attached=True` ancestors), features. Filter chips: kind, feature, namespace, has problems (`inferred`: a current condition False, PartiallyValid, or a target check "not found"). Readable policies only (§7).

### 6.3 Policy detail (`/installs/:id/policies/:group/:kind/:ns/:name`)

1. **Targets:** each targetRef with its check and link; selectors as written.
2. **Status per ancestor:** ancestor, `Accepted` and `Attached` with reason and message, verbatim. Stale or other-controller entries are shown, marked "stale" or "other controller", and excluded from list summaries. A note under `Merged` or `Overridden`, worded by granularity: for a Gateway or ListenerSet ancestor, "kgateway reports this for the whole Gateway. It does not say which routes or fields are affected."; for a backend ancestor, "kgateway reports this for this backend port."
3. **Configuration by feature:** each spec section as written, in the existing YAML component.
4. **Annotations** (§5.5) and the YAML tab.

### 6.4 Current status

Summaries and `attached-on-ancestor` relations use only ancestors whose controller is one of the install's controller names, whose Gateway or ListenerSet ancestor the install owns, and whose conditions are fresh. These are the guards the existing policy findings use (`pkg/health/own/status.go:120`, `BlockFreshness`). The detail keeps every entry.

### 6.5 On subject pages; signals (D-127)

| Group | Contents | Source |
|---|---|---|
| Targeting this object | policies whose targetRefs name this object, or this listener's section | declared |
| Targeting a parent | §5.3 | declared |
| Reported attached here | Gateway, ListenerSet and backend pages: current `Attached=True` ancestors that are this object | controller-reported |

Copy under "Targeting a parent": "These policies name a parent of this route. kgateway decides whether and how they apply." Under "Reported attached here", per granularity as in §6.3.

Phase 8 adds no input to the route configuration or traffic path signals. A declared target is not evidence that a policy affects the route. D-087's "inherited policy problems" consequence is withdrawn; revisit when status reports per route (U4). Overview footprint (D-088): none beyond the existing policy counts and findings.

### 6.6 DirectResponse status (D-129 item 2)

DirectResponse stays list-only (D-058; `pkg/install/assign.go:271`): no ownership, freshness or route attribution changes. Its ancestors feed only the ancestor findings path (`policyAncestors`, `pkg/health/own/status.go:122`), which today skips list-only kinds (`:62`). Tests: a rejected DirectResponse raises the same findings as a TrafficPolicy; one with no status or no collected watch raises none; route signals do not change. Amends D-058 item 6. `docs/findings.md`, snellius.dev (P-006) and the affected snellius-demo scenarios (P-007) follow.

## 7. Visibility (Phase 7 rules)

The Phase 7 contract (design §7, D-085, D-107, D-109, D-110, D-119) applies unchanged. Here:

1. **Policies:** in a list, count, facet, filter or relation only when the viewer may see them as members (`can`, `pkg/api/visible.go:270`); a detail needs named `get`.
2. **Spec and status of a readable policy** are shown as Kubernetes shows them, ancestor and target names included. `kubectl get -o yaml` reveals the same.
3. **Linked objects:** an ancestor, target or parent is a link with health only when the viewer may `get` it; otherwise plain text with no health.
4. **Target checks:** "found" and "section not found" need named `get` on the target; "not found" needs `list` on its kind in its namespace. Anything less is `unknown`. A downstream authoriser that allows the list and denies the name (D-119) gets `unknown`, never "found". The same viewer-scoped answer drives rows, the has-problems filter, facets and totals.
5. **Relations** need the subject, the policy and every object on `Via` readable. Missing ones drop the relation, with no note specific to it; the viewer's one permissions note (Phase 7 §7.5) covers it.
6. **Counts** recount over readable policies (`recount`, `pkg/api/visible.go:467`).
7. **Derived findings** on policies keep Phase 7's basis rule (§7.3): shown only when the object and its whole basis are readable.

The byte tests allow only the changes Phase 7 permits (timestamps per D-109, install membership per D-107).

## 8. Extension boundary

- **Core (Gateway API, GEP-713):** decoding targetRefs, targetSelectors, parentRefs, `status.ancestors` and conditions; relations; visibility; BackendTLSPolicy. The core attaches no meaning to a reason string.
- **Pack interpreter (8.1):** a `PolicyInterpreter` hook supplies, for its own kinds: feature labels per spec section (`cors` → "CORS"), the precedence annotation keys, the summary-ancestor marker, PartiallyValid as a warning, and the `Merged`/`Overridden` notes. Without a pack, features are the raw spec keys and nothing is interpreted. A test gives the same reason string on a core policy and on a downstream policy and checks no kgateway interpretation appears.
- Target checks use the rule set's `PolicyTargeter`, already behind the pack (`Hooks.RulesFor`).

## 9. View B: runtime (built in Phase 12, D-124)

Phase 12 already owns the runtime filter chain (E4, E5, E13). It decides:
1. **Access.** Envoy admin and kgateway's 9095 bind to localhost. Reading them needs `pods/portforward` or `pods/proxy`, which the chart forbids (`deploy/helm/snellius/README.md:109`, `chart_test.go:122`), a kgateway install change, or an uploaded config dump.
2. **Transport and redaction of raw `typed_config`.**
3. **xdsview `v0.5.0`** (E13): a policy contribution is at most `inferred`; a field→filter link only when exactly one filter matches.

Fixed now, as requirements Phase 12 inherits:
- **Whole-block visibility.** A runtime block (a route's filter chain, a virtual host's or listener's config) is shown only when the viewer may read the full evidence scope behind it: the Gateway, the routes and every policy the controller reports for it. Otherwise the whole block is withheld, and a withheld block looks the same whether hidden configuration exists or not. Dropping single contributions is not enough: filter presence, order and counts would still leak.
- Contributions come only from xdsview's `origin.Policies` (no Snellius decoder, P-001), name their level, carry xdsview's certainty, and are never presented as governing the request.
- An HTTPRoute → Envoy route link shows xdsview's certainty (`exact` only with `route_source`, U2).
- Snellius works without it (P-008 point 6).

## 10. Provenance gap and upstream asks

A tool could show which policies apply to a route, without a resolver, only if the controller reported it. The asks, in the program's numbering (none filed; the owner decides):

| Ask | Gives | Program |
|---|---|---|
| Document `merge.*` as a contract; stamp it on the HCM, backendRef and cluster policies and DirectResponse | complete runtime contributions | U1 |
| `route_source` on by default | exact HTTPRoute → Envoy route links | U2 (kgateway#15067, #15068) |
| `Overridden` only when overridden everywhere | honest status | U3 (#14861, #14870) |
| Status names the winning policy and keeps sectionName | "overridden by P" | U4 (#14919) |
| Selector matches nothing | retires M-2 | U6 (#14998) |
| A per-target condition (GEP-713's `*Affected`, Experimental) or a per-route resolved-policy record | "which policies affect route R", from the controller | proposed as part of U4 or a new ask; owner decides |

## 11. Testing

- `pkg/policy`: table tests from real policy objects of kgateway 2.3, 2.4 and 2.5 (unknown reasons and ancestor kinds kept; summary ancestor; BackendConfigPolicy with port; stale and other-controller ancestors); parent joins (sectioned, unsectioned, port-only, ListenerSet two-hop).
- **External consumer:** a test in an external package builds the model from a `cache.ListOnce` snapshot, as kgwctl's one-shot flow does, with no `pkg/api` import.
- **API boundary, from 8.1:** full reader and restricted viewer; with permissions fixed, a hidden policy, hidden target and hidden ancestor each change nothing in rows, totals, facets, links, health and the has-problems filter. A downstream authoriser that allows the list and denies the named target. Two groups with the same kind, namespace and name. Denied and absent details are equal. YAML links agree. Audit records for list and detail.
- **Pack boundary:** the reason-string test (§8).
- **Web:** Playwright on the e2e matrix for list, detail and subject sections; axe light and dark. A copy test over Snellius-generated labels (not controller messages or YAML) for "effective", "applied" and "active".
- **e2e:** a snellius-demo scenario with a TrafficPolicy on a route, a ListenerPolicy on a Gateway and a rejected DirectResponse, on the kind matrix.

## 12. Proposed decisions

- **D-121** Phase 8 is declared policy visibility; Snellius never resolves policy; "effective policy" is not used. Supersedes the roadmap's Phase 8 text and D-005's consequence "effective policy must build on ancestor status".
- **D-122** Every policy value carries one of five sources (§3.2).
- **D-123** `pkg/policy` is Snellius's part of E7 (§5.1 inputs); additive changes only within a minor. Route-level xDS evidence for E7 comes from xdsview.
- **D-124** The runtime view moves to Phase 12 with its access, transport and redaction decisions, and inherits the whole-block visibility rule (§9). Supersedes the Phase 12 clause of principle 4 and D-006 (P-009) and roadmap Phase 12 bullet 2.
- **D-125** Selectors are never resolved; M-2 stays the zero-candidate diagnostic only.
- **D-126** Precedence annotations are shown as written; lists say they are not ordered by precedence.
- **D-127** Phase 8 adds no policy input to route signals; withdraws D-087's consequence for Phase 8.
- **D-128** M-1's register entry, conformance status and rule-set selection (program open questions 1 and 3):
  - *Rule 1:* mirrors kgateway `pkg/kgateway/utils/delegation` `MergeParentChildRouteMatch` (view C), route→listener attachment (view A) and delegation child selection (view B); rule sets `kgw23`–`kgw25`, kgateway 2.3–2.5. View C retires with U10.
  - *Question 1 (rule 2 not met):* the matcher goldens (`pkg/computed/golden_test.go:97`, `testdata/kgateway-2.{3,4,5}`) are hand-transcribed. Delegation cases cite kgateway's translator goldens per tag (`TestDelegationGoldensCiteKgateway`, `:120-139`); nothing loads or compares them. The e2e matrix checks delegation against status only (`test/e2e/api_test.go:711-785`). Real xDS was captured once by hand for 2.3 and 2.4 path prefixes (D-021 item 7). A conformance test against the cited upstream goldens per tag is tracked, outside Phase 8.
  - *Question 3:* selection already goes through the pack (`Hooks.RulesFor`, `pkg/extension/extension.go:51`; `pkg/kgateway/pack.go:31`). The core keeps `pkg/engine/engine.go`'s fallbacks (`:218` `rulesets.Select`, `:505` `IsVersion`, `:546` `ExpectedGatewayAPI`) and `pkg/api/details.go:438`'s Gateway CRD table. Moving them is a separate refactor, outside Phase 8.
- **D-129** Parent joins and DirectResponse: (1) "targeting a parent" joins on `spec.parentRefs` only, section-exact, at most two hops through a ListenerSet, every hop readable (§5.3); (2) DirectResponse gains ancestor status findings and stays list-only (§6.6), amending D-058 item 6.

## 13. Sub-phases

| # | Kind | Scope |
|---|---|---|
| 8.1 | planned | `pkg/policy` model, parent joins and tests; `PolicyInterpreter` pack hook; install-scoped list and detail API with audit; DirectResponse status findings; the §11 API-boundary visibility tests. The smallest increment |
| 8.2 | planned | relations in the subject detail responses; web pages and sections; the Phase 7 P3 carry-over (full read without Events or CRDs, per grant; backend list, backend detail, route detail; byte baseline) |
| 8.3 | planned | docs, `docs/findings.md`, snellius.dev (P-006), snellius-demo (P-007), program file (Snellius's part of E7, M-1, M-2), kgwctl notice |

## 14. Left to the builder

Type and function names; chip order; API field names within API v1's additive rule; how the YAML component shows one spec section.

## 15. Open questions

1. **For the user:** is moving view B to Phase 12 right, or should Phase 8 include an opt-in runtime view with a new RBAC grant? Recommended: Phase 12.
2. **For the user:** which upstream asks to raise first. The per-target condition is the one that unlocks "policies affecting this route".
