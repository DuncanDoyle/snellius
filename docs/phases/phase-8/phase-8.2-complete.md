# Phase 8.2 — Subject relations and pages (Complete)

**Implemented:** 2026-11-23 … 2026-11-24. **Closed:** 2026-11-25 on PR #65 (`duncandoyle/phase8Design`)
**Range:** 2a23c6f..HEAD of the close commit
**Close gate:** met. Locally on `5123eb3`: `go test ./...` (52 packages), `-race` on `pkg/api`, `pkg/policy`, `pkg/health/...`, `go vet` (also `-tags bench`), the bench budget test, golangci-lint v2.15.0 (0 issues), web typecheck, biome, vitest (1200 tests) and build. Playwright policies spec and axe, light and dark, against a live kgateway v2.5.0 cluster: 23 passed. CI on PR #65 at `65f10f7`: every check passed, incl. the kind e2e matrix (2.3.11, 2.4.7, 2.5.0), kind-secure and bench. The run at `5123eb3` failed two policies jobs (list busy bug, fixed in `65f10f7`) and one 026 timing flake (Deferred).

## Summary
The Gateway, ListenerSet, route and backend pages now show the policies aimed at them, in three groups with their source and the path walked. A Policies list and a policy detail page show targets, kgateway's status per ancestor verbatim, configuration and annotations, every value tagged with its source. Policy links open the policy page. On the way, a bug older than Phase 8 surfaced: kgateway writes `Attached=False Pending` on every policy it rejects, so Snellius reported rejected policies as "not yet processed" on 2.3, 2.4 and 2.5 (D-135).

## Sub-phases (phase record only)
n/a

## Reviews
- `reviews/8.2-codex.md`: 10 findings (4 P1), all accepted; fix round 1 (Go and web).
- `reviews/8.2-fix-codex.md`: re-review; outage-only 503 paths rejected under D-137 (user), the rest accepted; fix round 2.
- `reviews/8.2-fix2-codex.md`: re-review; 3 accepted (fix round 3), 1 rejected (header tags are declared by definition). Round 3 was not re-reviewed; the phase review covers it.

## Waivers
None.

## Deliverables
- **Relations** on Gateway, ListenerSet, route and backend detail responses: targeting this object, targeting a parent (parentRefs only, ListenerSet two-hop, `Via`), reported attached here; a selector hint built from readable policies only. Candidates are narrowed on declared data before any access check (cold route detail 8 checks, Gateway ≤139, Service ≤319).
- **Web:** Policies list (chips, facets, precedence note), policy detail (targets, status per ancestor, configuration by feature, annotations, YAML), policy sections on the four subject pages, source tags from API fields, link routing from the link path. Axe clean light and dark.
- **Links (D-136):** every policy link is the policy endpoint path and needs named `get` on the policy.
- **Freshness (D-135):** a policy ancestor is judged on `Accepted` alone; an older `Attached` is ignored; rejected policies raise KGWUI-002 on every supported version.
- **Visibility:** list-only kinds make no attached-on relation; denied policies get no access check; a policy-ancestor KGWUI-002 needs list and named access to every object it rests on; with all checks answering, hidden and downstream-denied policies change nothing on the policy and subject pages (D-137 test).
- **Phase 7 P3 carry-over:** the full read without Events or CRDs denies each grant separately and covers backend list, backend detail and route detail, with a baseline for the full reader.
- **E2e fixture:** TrafficPolicy `shop/tp-good-timeout` on `shop/good`, used by name in the policies spec and axe.
- **List busy fix (found by CI):** every list stayed `aria-busy` when a filter's answer deep-equalled the rows shown, since busy read the SWR cache, which does not re-render. On kgateway 2.3 and 2.4 the only policy kind's filter does exactly that. Busy now reads SWR's `isLoading`; a hook test covers it.

## Rulings
In the review triage tables; design-level ones are D-135 … D-137. UI copy chosen from five options each (list in `.agents-tmp`, summarised): source tags Declared / Reported / Inferred / Unknown; "Waiting for status" for pending; "None." for an empty group; "Open Policies in <namespace>" for the selector link.

## Notes for later phases
- A downstream shell older than 8.2 shows policy refs without a link, since it does not know the new link value; the 8.3 upgrade notes say to upgrade the shell with the backend.
- `/findings` now asks one named check per distinct policy linked, and a strict basis asks `list` per basis object; no bench world has policy findings yet, so this cost is unmeasured.
- On a kind node, a Docker restart resets `fs.inotify.max_user_instances` to 128; kgateway and kube-proxy then crash-loop with "too many open files" (set 512, restart both).

## Deferred / open
- **(P2, phase review)** Base authorization runs before the downstream authoriser on endpoints outside the policy paths; any prefetch of downstream-denied refs pays checks it need not (D-137 covers the outage leak; cost and order are left for the review).
- **(P3)** Measure the `/findings` cost of policy links and strict bases in a bench world with policy findings.
- **(P4)** A downstream ListenerSet kind is covered in `pkg/policy` only; the API rule world cannot add a served downstream ListenerSet kind.
- 8.1's deferred items stay (route parent finding basis; duplicate status entries).
- **(P3, flake, own PR)** `TestDataPlaneDiagnostics/controller_scaled_to_zero` saw KGWUI-026 46 s after the scale-down (wants ≥ 60 s) on one 2.3.11 job; first time in the last 40 e2e runs. The previous subtest restarts the controller, so the proxy's disconnect clock likely starts before the scale-down. Code outside Phase 8.
