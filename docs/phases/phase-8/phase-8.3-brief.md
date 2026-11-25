# Sub-phase 8.3 — Docs and program

Kind: planned
Base: e6f1cb1
Issues: none | Plan steps: implementation-plan.md, 8.3; carry-over from phase-8.1-complete.md and phase-8.2-complete.md

## Problem / goal
8.1 and 8.2 ship policy pages, endpoints, a pack hook and a finding change, but the repo's user and extender docs do not mention them, and the program, the site, the demo and kgwctl have not been told. 8.3 brings the docs in line with the code and prepares every outward notice.

## Expected behaviour

| Input / action | Expected |
|---|---|
| README, what Snellius shows | A Policies section (beside "Traffic and backends"): the Policies list and detail, policy sections on Gateway, ListenerSet, route and backend pages, the four source tags (declared, reported, inferred, unknown), "sorted by namespace and name, not by precedence", and that Snellius never decides whether a policy applies (D-121) |
| README, Extending | The `PolicyInterpreter` pack hook: what a pack supplies, what the core does without one (D-130 neutral behaviour), with a short code example in the style of the existing ones |
| README, Upgrade notes | A Phase 8 entry: the two policies endpoints and the `policyRelations` field (additive, v1); rejected policies now raise KGWUI-002 on 2.3, 2.4 and 2.5 instead of reading "not yet processed" (D-135); DirectResponse ancestor findings; policy links are the policy endpoint path and need named `get` (D-136); a downstream shell older than 8.2 shows policy refs without a link, so upgrade the shell with the backend; for secure mode, a policy-ancestor KGWUI-002 needs list and named access to every object it rests on (D-134), and an authorization outage may answer 503 (D-137) |
| `docs/findings.md` | KGWUI-002 and 003 text matches D-132 (policy-ancestor IDs), D-134 (strict basis) and D-135 (freshness on `Accepted` alone; an older `Attached` ignored) |
| `docs/documentation-notes.md` | An entry for policy visibility for the site: sources, what "not by precedence" means, what Snellius will never show (effective policy), the runtime view coming in Phase 12 |
| Outward notices (drafts only, in `.agents-tmp/phase8.3/`, filed after user approval) | snellius.dev `cross-repo` issue listing what users can see change (P-006); snellius-demo `cross-repo` issue for a policy scenario and the D-135 finding change in existing scenarios (P-007); kgwctl `cross-repo` issue naming E7 and D-130, D-131, D-133; program-file diff: E7 Snellius part delivered with `pkg/policy` at the release tag, M-1 rule-1 entry and rule-2 status (D-128), M-2 unchanged |

## Root cause (if known)
Planned work.

## Acceptance tests
Docs only. Each claim in the new text is checked against the code at HEAD (endpoint paths, field names, hook signature, finding conditions); the README code example compiles in `examples/` or matches a compiling one. Codex review of the diff checks the docs against code and decisions.

## Scope
In: `README.md`, `docs/findings.md`, `docs/documentation-notes.md`, drafts under `.agents-tmp/phase8.3/`.
Out: code changes; filing issues or pushing to other repos (user approval first); snellius#68 ("Config accepted" wording, E14), which is not Phase 8 scope; the 026 flake (own PR).

## Plan / design impact
None. The release version in the upgrade notes follows kgwctl's expectation (`v0.9.0`) and is confirmed at the release.

## Definition of done
- [ ] every new claim checked against HEAD
- [ ] `make lint` and the hygiene check pass (no vendor names, no local paths)
- [ ] Codex review of `git diff e6f1cb1..HEAD` + triage
- [ ] drafts of the four notices ready for user approval
- [ ] phase-8.3-complete.md written; phase-status.md updated
