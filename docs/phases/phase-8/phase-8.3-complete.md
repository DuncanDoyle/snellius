# Phase 8.3 — Docs and program (Complete)

**Implemented:** 2026-11-25. **Closed:** 2026-11-25 on PR #65 (`duncandoyle/phase8Design`)
**Range:** e6f1cb1..HEAD of the close commit
**Close gate:** met for the repo. Docs only: `make lint` (go vet and golangci-lint v2.15.0) 0 issues at `8b53ae6`; no vendor names, local paths, or "effective", "applied" or "active" in the added lines. The README Go example was compiled and run against HEAD in a scratch module. The four outward notices are drafted and wait for user approval (Deferred / open).

## Summary
The README, `docs/findings.md` and `docs/documentation-notes.md` now describe Phase 8: the Policies pages and sections, the `PolicyInterpreter` pack hook, and the v0.9.0 upgrade notes. Exact finding rules live in `docs/findings.md` only; the README and the site notes state what changed and link there. Three Codex rounds checked every claim against the code.

## Sub-phases (phase record only)
n/a

## Reviews
- `reviews/8.3-codex.md`: 10 findings (2 P1), all accepted; the roadmap goal "which policies apply" reworded (D-121). One code gap deferred (below).
- `reviews/8.3-fix-codex.md`: re-review; partly-resolved items and 6 new wrong claims, all accepted. The approach changed: the README states changes at a high level and defers to `docs/findings.md`.
- `reviews/8.3-fix2-codex.md`: re-review, P1/P2 only; 3 P2 accepted and fixed in `8b53ae6`. Round 3 was not re-reviewed; the phase review covers it.

## Waivers
None.

## Deliverables
- **README:** Policies section; "Policy interpreters" under Extending, with a compiling example; "v0.9.0: policies (Phase 8)" upgrade notes (endpoints and `policyRelations` additive; two wire changes: policy `link` values and policy-ancestor KGWUI-002 IDs; rejected policies; DirectResponse; older shells show policy refs without a link; secure-mode items).
- **`docs/findings.md`:** KGWUI-002 and 003 for policy ancestors (D-132 IDs and duplicate ordinals, D-134 basis, D-135 freshness, 2.5 `TargetNotFound` to 016, DirectResponse).
- **`docs/documentation-notes.md`:** "Policy visibility" entry for the site.
- **Decision log:** D-132's and D-136's consequences now match the code.
- **Notice drafts** (`.agents-tmp/phase8.3/notices.md`, not in git): snellius.dev (P-006), snellius-demo (P-007), kgwctl E7 notice (D-130, D-131, D-133), program-file diff (E7, M-1 rule 1 and rule 2 status, open questions 1 and 3, M-2 unchanged).

## Rulings
- Exact finding rules in one place (`docs/findings.md`); other docs link to it. Prose copies of the rules drifted in every review round.
- The upgrade notes use `v0.9.0`, kgwctl's expected version; confirmed at the release.

## Notes for later phases
- An e2e flake found during 8.2's CI (KGWUI-026 timing) is a test bug: proxies from earlier controller restarts kept their disconnect onset. Fixed in its own PR, #69.

## Deferred / open
- **(user approval)** File the four notices. The site, demo and kgwctl issues go out with the v0.9.0 tag (P-006: the release is done when the site is live); the program-file commit lands in the coordination repo at the tag.
- **(P3, phase review)** On the 2.5 rules, `statusTargetFinding` reads an older `Attached=False TargetNotFound` beside a current `Accepted` (D-135 covers 002 and summaries only) — `reviews/8.3-codex.md` #4.
- snellius#68 (xdsview E14: "Config accepted" wording) is not Phase 8 scope; the user decides when.
