# Phase 8.1 — Policy model, pack hook and API (Complete)

**Implemented:** 2026-11-23. **Closed:** 2026-11-23 on PR #65 (`duncandoyle/phase8Design`)
**Range:** c837869..HEAD of the close commit
**Close gate:** met. Locally on the close commit: `go test ./...` (52 packages ok), `go test -race` on `pkg/api`, `pkg/policy`, `pkg/health/...`, `go vet ./...` (also the bench build), golangci-lint v2.15.0 (0 issues), web build (`npm ci` from the lockfile, `npm run build`; 8.1 changes nothing under `web/`). Every acceptance test failed before its change; red runs are kept in the agents' scratch reports.

## Summary
Snellius now reads a policy as an object: its targets as written, kgateway's status per ancestor verbatim, the features it configures, and its precedence annotations, each value labelled with its source. `pkg/policy` is the declared-policy model kgwctl can import (E7); a pack hook supplies kgateway's interpretation; two install-scoped endpoints serve the list and detail under the Phase 7 visibility rules. DirectResponse gains ancestor status findings and stays list-only. Nothing resolves policy (D-121).

## Sub-phases (phase record only)
n/a

## Reviews
- `reviews/8.1-codex.md`: 9 findings (3 P1), all accepted; fix round 1.
- `reviews/8.1-fix-codex.md`: re-review; 3 new P1s, 1 P2, 1 P3, all accepted; fix rounds 2 and 3.
- `reviews/8.1-ids-codex.md`: advisory on finding IDs, asked by the user; option B (namespace/name, no UID), scoped to policy-ancestor 002.
- `reviews/8.1-fix2-codex.md`: re-review of rounds 2–3; 2 accepted (fixed in round 4), 2 P1s rejected as covered by D-107 (D-134, user).
Round 4 (duplicate-entry ordinal, contract text) was not re-reviewed; the phase review covers it.

## Waivers
None.

## Deliverables
- **`pkg/policy`** (E7): `Read(u, interp, guard)` (D-130), target checks through the rule set's `PolicyTargeter` with a caller-supplied `Lookup`, parent joins and `Relations` (design §5.3, D-129), every value with a source (D-122, D-131 item 4). Imports neither `pkg/api` nor `pkg/health` (boundary test); an external test builds the model from a `cache.ListOnce` snapshot with its own guard.
- **`PolicyInterpreter`** pack hook and kgateway's implementation: feature labels, precedence annotation keys, the summary-ancestor marker, PartiallyValid as a warning. Without a pack, nothing is interpreted (reason-string test). `examples/downstream` builds.
- **API:** `GET /api/v1/installs/{id}/policies` (filters kind, feature, namespace, has problems; paging; facets; recounted totals) and `.../policies/{group}/{kind}/{ns}/{name}` (denied equals absent), both in the audit table. Hidden-change byte tests for policy, target and ancestor; narrowing authoriser; group collision; YAML links.
- **DirectResponse:** ancestor findings like a TrafficPolicy's, including TargetNotFound; still list-only (design §6.6).
- **Visibility fixes beyond 8.1:** a policy ancestor finding rests on its own ancestor (predates Phase 8 for TrafficPolicy); collection is checked per namespace, also for KGWUI-013 explanations and KGWUI-003 "parent does not exist" (D-131 item 5).
- **Finding IDs:** a policy-ancestor KGWUI-002 hashes its ancestor ref and condition type (D-132); exact duplicate status entries get an ordinal among themselves.
- **Check budget:** target and ancestor judgments need `list` first (D-133). Cold `/policies?limit=1`, Large plus one policy per route: 105, 209 and 391 checks for the three bench viewers, all 200.

## Rulings
In the review triage tables and the build reports; design-level ones are D-130 … D-134.

## Notes for later phases
- 8.2 adds detail links for policies; until then links use the YAML path (D-131 item 6).
- kgwctl's `Guard` and `Lookup` must follow D-130, D-131 and D-133 (list before get; collection per namespace). Say so in the E7 notice at phase close.
- The targeter contract (no state between calls; `unknown` independent of the object) is documented, not enforced; a factory on the pack hook would enforce the first.

## Deferred / open
- **(P4, phase review)** Route parent KGWUI-002s keep a shared basis and D-040 ranks: a hidden second parent withholds the readable parent's finding (D-131 item 1).
- **(P4)** A duplicate status entry's ID follows status order among exact duplicates; dropping the first renumbers the rest.
- The P3 and P4 Phase 7 carry-overs stay with 8.2.
