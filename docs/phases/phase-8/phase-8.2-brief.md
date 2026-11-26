# Sub-phase 8.2 — Subject relations and pages

Kind: planned
Base: d1abced
Issues: none | Plan steps: implementation-plan.md, 8.2 steps 1–3; carry-over (P3) from phases/phase-7/reviews/phase-codex.md

## Problem / goal
8.1 serves policies through the API, but no page shows them, and a Gateway, ListenerSet, route or backend page does not say which policies name it. 8.2 adds relations to those detail responses, a Policies list and a policy detail page, subject sections with source labels, and the Phase 7 test the phase review left for Phase 8.

## Expected behaviour

| Input / action | Expected |
|---|---|
| Gateway, ListenerSet, HTTPRoute (and other route kinds served), backend detail response | A new field with relations in three groups: targeting this object (incl. a listener's section), targeting a parent (design §5.3, with the path walked), reported attached here (Gateway, ListenerSet, backend only; current `Attached=True` ancestors). Each relation names its source |
| A policy, subject, parent hop or ancestor the viewer cannot read | No relation; no note specific to it; bytes equal with and without it (restricted viewer, permissions fixed) |
| targetSelector naming the subject's kind | Not listed; the section says "Policies that select by label are not listed here" and links to the Policies list filtered to that kind and namespace |
| `/installs/:id/policies` | Table: kind, namespace/name, targets as written (selector as `HTTPRoute labels app=cart`), accepted (inferred), reported attached on, features. Chips: kind, feature, namespace, has problems. A note says the list is sorted by namespace and name, not by precedence |
| `/installs/:id/policies/:group/:kind/:ns/:name` | Targets with checks and links; status per ancestor verbatim, stale / other controller / not owned / unreadable marked; the Merged/Overridden note worded by granularity (design §6.3 item 2); configuration by feature in the YAML component; annotations with "kgateway uses this to order policies. Snellius does not evaluate it."; YAML tab |
| Policy links anywhere in the app (incl. 8.1's YAML-path links) | Go to the policy detail page and carry the group |
| Subject page sections | "Targeting this object", "Targeting a parent" (with "These policies name a parent of this route. kgateway decides whether and how they apply."), "Reported attached here" (with the granularity note); every item shows its source label |
| Any Snellius-written label | Never "effective", "applied" or "active" about a policy (copy test); controller messages and YAML shown verbatim |
| Route signals, overview | Unchanged (D-127, D-088) |
| Full reader without Events or CRD access (Phase 7 P3) | Each grant denied separately; backend list, backend detail and route detail covered; byte baseline for the fully granted reader |

## Root cause (if known)
Planned work.

## Acceptance tests
- API: relation tests per group (sectioned, unsectioned, port-only, ListenerSet two-hop) on each subject detail; hidden policy, parent, hop and ancestor byte tests; existing goldens additive only.
- Web: Playwright on the e2e matrix for the list, detail and subject sections; axe light and dark; a copy test over Snellius-generated labels.
- The Phase 7 P3 test above.

## Scope
In: `pkg/api` (subject detail responses, policy links), `web/packages/shell` (pages, sections, routes, copy), `web/e2e`, the P3 test.
Out: docs, snellius.dev, snellius-demo, program file (8.3); route signals (D-127); the runtime view (Phase 12); route parent finding basis (deferred to the phase review).

## Plan / design impact
Changing 8.1's YAML-path policy links to detail links changes `link` values in existing v1 responses where policies appear; record it in `decisions.md` if any existing response changes. UI copy beyond the design's fixed strings is our call, recorded in the completion record.

## Definition of done
- [ ] acceptance tests fail before, pass after (report both)
- [ ] go test, race on `pkg/api`, vet, `make lint`, web typecheck, biome, vitest and build pass
- [ ] Playwright on the e2e matrix and axe, light and dark
- [ ] phase-8.2-complete.md written; phase-status.md updated
