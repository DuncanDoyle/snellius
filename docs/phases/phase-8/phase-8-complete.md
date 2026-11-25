# Phase 8 — Policies & Filters (Complete)

**Completed:** 2026-11-25 on PR #65; released as `v0.9.0` at merge (tag pending user approval).
**Range:** b01b677..HEAD of the merge

## Summary
Snellius now shows declared policies and what kgateway reports about them, and never decides which policy applies (D-121). A Policies list and a policy detail page show targets as written, kgateway's status per ancestor in its own words, missing targets and the configuration per feature. Gateway, ListenerSet, route and backend pages list the policies that name them or a parent they declare, each tagged with its source. Each expanded listener row lists the policies that name its section. Rejected policies now raise KGWUI-002 instead of reading "not yet processed" (D-135), and DirectResponse status is judged (D-129). A downstream pack adds policy kinds and its own readings through the `PolicyInterpreter` hook. kgwctl imports the declared-policy model, `pkg/policy` (E7). The runtime Envoy view is Phase 12 (D-124).

## Sub-phases (phase record only)
| Sub-phase | Kind | Closed | Description | Record |
|---|---|---|---|---|
| 8.1 | planned | 2026-11-23 | Policy model, API, findings, pack hook | `phase-8.1-complete.md` |
| 8.2 | planned | 2026-11-25 | Subject relations, Policies pages, D-135 freshness | `phase-8.2-complete.md` |
| 8.3 | planned | 2026-11-25 | Docs and outward notice drafts | `phase-8.3-complete.md` |
| 8.4 | stabilization | 2026-11-25 | Phase review fixes (2 P1, 5 P2) | `phase-8.4-complete.md` |

## Reviews
Cross-model (Codex) throughout: the design and plan review, sub-phase reviews and fix rounds under `reviews/`, `reviews/phase-codex.md` (whole phase: 7 findings accepted, 3 carry-overs triaged), and the 8.4 review with two fix rounds (final round: no new P1/P2). Every finding is triaged in its file. From 8.4 on, review prompts link the program's "Architecture guidelines" (protocol step 7).

## Waivers
None.

## Decisions
D-121 to D-138.

## Deliverables
- `pkg/policy`: declared-policy model, relations (incl. the list-only rule, D-138) and the `Interpreter` hook; kgateway's interpreter in `pkg/kgateway/kgwpolicy`.
- API: `/policies`, policy detail, `policyRelations` on subject details, namespace scope on `/policies`, policy links (D-136).
- Shell: Policies list and detail, subject policy sections, listener-row policies.
- Findings: policy-ancestor KGWUI-002 IDs (D-132) and strict basis (D-134), freshness on `Accepted` (D-135), DirectResponse (D-129), readings from the pack.
- Docs: README Policies section, "Policy interpreters", v0.9.0 upgrade notes; `docs/findings.md`; `docs/documentation-notes.md`.

## Notes for later phases
- Phase 9: audit the remaining D-040 rank-based IDs against program P-011 rule 2.
- Phase 10: an empty declared-policy group is not evidence that a route lacks authentication or authorization.
- Phase 12: xdsview Phase 9 (E15, `v0.7.0`) gives `Dependencies(block)` with typed objects and gaps; an empty `Gaps` means every expected input is attributed (snellius#70).
- Version bumps follow program P-012.

## Deferred / open
- **(P2)** Base authorization runs before the downstream authoriser outside the policy paths (cost only; D-137 covers the outage path).
- **(P3)** Measure the `/findings` cost of policy links and strict bases in a bench world with policy findings.
- **(P4)** Route-parent KGWUI-002s keep a shared basis (D-131 item 1); Phase 9 keeps prediction inputs off it.
- **(P4)** A downstream ListenerSet kind is covered in `pkg/policy` only, not in the API rule world.
- snellius#68 ("Config accepted" also requires no KGWUI-025 or 026; limit documented) after `v0.9.0`.
- Outward notices (approved 2026-11-25): snellius.dev, snellius-demo, kgwctl E7, and the program-file update, at the tag.
