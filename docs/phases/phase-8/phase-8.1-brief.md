# Sub-phase 8.1 — Policy model, pack hook and API

Kind: planned
Base: c837869
Issues: none | Plan steps: implementation-plan.md, 8.1 steps 1–7

## Problem / goal
Snellius has no view of a policy as an object: its targets, what kgateway reports about it per ancestor, and the features it configures. kgwctl needs the same read as a Go package (E7). 8.1 adds `pkg/policy` (declared model, target checks, parent joins), the `PolicyInterpreter` pack hook, the install-scoped list and detail API with the Phase 7 visibility rules, and DirectResponse ancestor findings. No web pages (8.2). No policy resolution anywhere (D-121).

## Expected behaviour

| Input / action | Expected |
|---|---|
| `policy.Read` on a TrafficPolicy, ListenerPolicy, BackendConfigPolicy, DirectResponse or BackendTLSPolicy from kgateway 2.3, 2.4 or 2.5 | Targets in spec order (targetRefs, then targetSelectors), `declared`; every `status.ancestors` entry with conditions verbatim, unknown reasons and ancestor kinds kept, `controller-reported` |
| Ancestor whose controller is not one of the install's, whose Gateway/ListenerSet the install does not own, or whose conditions are stale | Kept in the detail, `Current=false`; excluded from list summaries and from `attached-on-ancestor` |
| kgateway's synthetic missing-target ancestor | Marked `Summary` by the kgateway pack; shown as written |
| No pack interpreter for the policy's kind | Features are the raw spec keys; no annotations, no summary marker, no notes |
| Same reason string (`Merged`, `PartiallyValid`) on a core policy and on a downstream policy | No kgateway interpretation on either (design §8) |
| Target check, found / section not found | `inferred`, from the rule set's `PolicyTargeter`, only with named `get` on the target |
| Target check, not found | `inferred`, only with `list` on the kind in the target's namespace |
| Target check with no targeter or targeter unknown (2.5 explicit refs), kind not served, watch denied, pending or scoped away, install scope unknown, undecoded section form, or a viewer lacking the access above | `unknown` |
| Downstream authoriser allows the list and denies the named target (D-119) | `unknown`, never "found" |
| Route parentRef `G` section `L` | Relates policies naming `G` unsectioned and `G`/`L` (`targets-parent`, `Via=[G…]`) |
| Route parentRef `G` with no section, or port only | Relates only policies naming `G` unsectioned |
| Route parentRef naming ListenerSet `LS` whose `spec.parentRef` is `G` | Relates `LS`'s policies and `G`'s unsectioned policies; `Via` records both hops; no further hops |
| Any hop, policy or subject unreadable | No relation, no note specific to it |
| Current `Attached=True` ancestor equal to the subject | `attached-on-ancestor`, `controller-reported` |
| targetSelector | Shown as written; never matched to objects |
| `GET /api/v1/installs/{id}/policies` | Readable policies only, sorted by namespace and name; filters kind, feature, namespace, has problems; paging; facets; totals recounted over readable policies; accepted summary `inferred` over current ancestors |
| `GET /api/v1/installs/{id}/policies/{group}/{kind}/{ns}/{name}` | Targets with checks, every ancestor (stale / other controller marked), features as written, annotations; hidden and absent give the same response |
| Restricted viewer; a hidden policy, target or ancestor is created, changed or deleted | List and detail bytes unchanged (rows, totals, facets, links, health, has-problems filter) |
| Two policy kinds with the same name and namespace in different groups | Distinct rows and details |
| List and detail requests | Audit records: list access and a deliberate read |
| Rejected DirectResponse with a current ancestor | Same ancestor findings as a TrafficPolicy |
| DirectResponse with no status or no collected watch | No findings; route signals unchanged; still list-only |
| Snellius-written labels in the API | Never "effective", "applied" or "active" about a policy |

## Root cause (if known)
Planned work.

## Acceptance tests
- `pkg/policy` table tests over real 2.3, 2.4, 2.5 objects, target checks and parent joins (design §11).
- Pack boundary: the reason-string test; `examples/downstream` builds.
- External-package test: model built from `cache.ListOnce`, no `pkg/api` import; boundary test that `pkg/policy` imports neither `pkg/api` nor `pkg/health`.
- API handler tests (design §11 API boundary): full reader, restricted viewer, narrowing authoriser, hidden-change byte tests, group collision, denied = absent, audit.
- Engine tests for DirectResponse findings.

## Scope
In: `pkg/policy` (new), `pkg/extension` (hook), `pkg/kgateway` (interpreter), `pkg/api` (two endpoints, audit), `pkg/health/own` (DirectResponse ancestors), `internal/boundary`, `docs/findings.md`.
Out: relations in subject detail responses and web pages (8.2); route signals (D-127); selector resolution (D-125); runtime view (Phase 12); rule-set fallbacks (D-128).

## Plan / design impact
none expected. Names are the builder's (design §14); any deviation gets a `decisions.md` entry.

## Definition of done
- [ ] acceptance tests fail before, pass after (report both)
- [ ] go test, vet, `make lint` and the web build pass
- [ ] phase-8.1-complete.md written; phase-status.md updated
