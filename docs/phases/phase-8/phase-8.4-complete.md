# Phase 8.4 — Phase review fixes (Complete)

**Implemented:** 2026-11-25. **Closed:** 2026-11-25 on PR #65 (`duncandoyle/phase8Design`)
**Range:** 2635548..01f6ce2 (records in the close commit)
**Close gate:** `make test` (52 packages) and `make lint` (0 issues) at `01f6ce2`; web typecheck, biome and 1204 unit tests at `5161e2d` (web unchanged since); CI on PR #65: see `CI_RESULT` below.

CI_RESULT: all green on 8e34c5e (code as of 01f6ce2): ci (go test, lint, web, helm, image, hygiene), the kind e2e matrix (kgateway v2.3.11, v2.4.7, v2.5.0), kind-secure and bench; kind-main skipped by design

## Summary
Fixes the seven findings of the Phase 8 phase review. KGWUI-016 ignores an outdated `Attached`. A policy link opens a detail that answers, or falls back to YAML with named get. `/policies` takes the namespace scope. kgateway's reason readings and wording live in the kgateway pack, and the kgateway checks carry that interpreter, so `/findings` and "Has problems" use the same one. `policy.Relations` applies the list-only rule (D-138). Each expanded listener row lists the policies that name its section. The docs match HEAD.

## Sub-phases (phase record only)
n/a

## Reviews
- `reviews/8.4-codex.md`: 4 of 7 resolved, 3 partly; 2 new P1 and 2 new P2, all accepted.
- `reviews/8.4-fix-codex.md`: 3 resolved; the name-matching interpreter default gave 1 P1 and 1 P2, accepted with a design ruling (second attempt at the same behaviour).
- `reviews/8.4-fix2-codex.md`: both resolved, no new P1/P2.

## Waivers
None.

## Deliverables
- **Findings:** stale `Attached` raises no 016 (`3e2b00e`). Reason readings and wording from the pack (`b5867bf`); a downstream kind without an interpreter gets the generic error as a fallback after freshness and specific findings.
- **Interpreter resolution:** kgateway check values bring kgateway's interpreter by type (`379ecad`); the engine resolves it once and publishes it on the install model, and the API uses it unless `WithPolicyInterpreters` returns one (`01f6ce2`). New package `pkg/kgateway/kgwpolicy` holds the interpreter (import cycle).
- **Links:** a policy detail link only for install members with named get; another install's policy gets a YAML link with named get (`668ea2c`, `21e5bda`, `2b80fae`). D-136 has a consequence line.
- **Scope:** `/policies` takes `namespaces`, advertised in `/meta`; it bounds the population, and the page's namespace filter narrows inside it (`1baf8c8`).
- **Relations:** `policy.Inputs.ListOnlyKinds` (D-138, `6dfb034`).
- **UI:** listener rows list the policies that name their section; the toggle reads "Show details for listener …", the heading "Policies that name this listener" (`77fb09c`, `df23ed8`).
- **Docs:** README upgrade notes, findings.md, documentation-notes.md (`5161e2d`, `a6b79fd`); design §6.2 amended: the pack's reading decides "Has problems".

## Rulings
- The pack's reading decides "Has problems" as well as the finding; §6.2's "current False" applies when the pack gives no reading.
- No name matching for the kgateway default; one interpreter resolution point shared by engine and API.
- Scope and the Policies page's namespace filter intersect (D-100 item 8).
- An embedder serving the default kgateway engine without `WithPolicyInterpreters` now gets kgateway readings and `interpreted: true` on the policy endpoints. Accepted: it matches what the findings show.

## Notes for later phases
- A new kgateway-owned check type adds the one-line `policyInterpreter()` method to bring the interpreter.
- Links to non-policy objects in `/refs` use the population check while their YAML needs named get. This is the Phase 7 design, not changed here; revisit if a review finds a viewer-visible gap.
- The cross-install link tests run in local mode; no two-install secure harness exists.
- Commit `827a61d` alone fails `TestCoreImportsNoPackPackage`; `4e9e8c5` fixes it.

## Deferred / open
- Carry-over (b) authorization order (P2, cost) and (c) shared route-parent basis (P4) stay in phase-status.md.
- Phase 10 design: an empty declared-policy group is not evidence that a route lacks auth.
