# Phase status

## Phase
Phase: 8 — Policies & Filters
Base: b01b677
Docs: docs/phases/phase-8/
Status: design approved (user, 2026-11-23); D-121 … D-129 accepted; runtime view in Phase 12. 8.1 closed 2026-11-23 (D-130 … D-134). 8.2 closed 2026-11-25 (D-135 … D-137; phases/phase-8/phase-8.2-complete.md). 8.3 closed 2026-11-25 (docs; phases/phase-8/phase-8.3-complete.md). Phase review done 2026-11-25 (phases/phase-8/reviews/phase-codex.md): 2 P1 and 5 P2 accepted and fixed in stabilization sub-phase 8.4, closed 2026-11-25 (phases/phase-8/phase-8.4-complete.md). Phase closed 2026-11-25 (phases/phase-8/phase-8-complete.md). Release steps await the user: merge #65, tag `v0.9.0`, then file the four 8.3 notices and update the program file (approved 2026-11-25). Then reset this file to Phase 9. Phase start: program read, scope endpoint filed as kgateway#15070, kgateway#14815 still open.
Author: claude (Opus 5.5)

## Sub-phase
none

## Program
Program file: https://github.com/DuncanDoyle/kgateway-tooling/blob/main/program.md (read 2026-11-22 at `bd1f17d`; merged on main at `090fd7e`, which adds M-4 and U12: P-008, P-009, M-1, M-2, E7, E13, E14).
Waiting on: nothing for 8.1 and 8.2. Phase 12 needs xdsview `v0.5.0` (E13), tagged 2026-11-22; no impact before Phase 12. Program file latest: kgateway-tooling `56c8868` (U12 → kgateway#15070).
Provides: E7 to kgwctl Phase 6, the declared-policy model `pkg/policy` (D-123). At phase close: update E7, M-1 (rule-1 entry, rule-2 status, D-128) and M-2 in the program file; notify kgwctl.
Consumers: E2, E3, E8 delivered through `v0.8.0` (kgwctl#3 … #8).
Open `cross-repo` issues here: #15, #21, #63 (xdsview E4/E5, for Phase 12; unchanged). The xdsview session will ask which of KGWUI-001, 006, 008, 025, 026 and the withheld-config signal answer the convergence questions (P-009 q6): answer it directly.

## Carry-over (deferred to later phases)
- (P4) Phase 8 and 9 (phase review PH6): extensions receive the raw publication beside per-object visibility helpers; policy relations (Phase 8, no inherited policy, D-121) and winning-route predictions must use viewer-scoped computations so a hidden input cannot change a readable result (D-085) — src: phases/phase-7/reviews/phase-codex.md
- (P3) After `v0.8.0` (user, 2026-11-18, D-118): compare how other Kubernetes UIs and platforms authorise per user, whether they hit the same check-rate limits, and whether Snellius's per-object visibility (D-085) causes them; what could be learned from their architecture. Include (user, 2026-11-18) an implicit namespace scope: derive the namespaces a viewer can read (cached SAR list answers, or SelfSubjectRulesReview as a hint only) and scope pages to them by default, SAR still deciding every object, shown in the UI with a way to widen; OpenShift's Projects API is the precedent
- (P3) After `v0.8.0` (user, 2026-11-18): poll every 10 s instead of 5 s, pause while the tab is hidden and refresh on return (saves server work; check load follows answer lifetimes, not polls); a quiet "checking what you can see" hint after a few seconds of waiting; fairness between viewers in admission. No session cap for restricted viewers: idle sessions cost little, and D-116 already bounds concurrent work
- (P3) Mirror M-4 (`pkg/install` discovery scope, incl. `Scope.Excluded` for kgwctl) retires with [kgateway#15070](https://github.com/kgateway-dev/kgateway/issues/15070) (program U12, filed 2026-11-22; H9, D-009 §7). At each phase start, check its state; when it lands, read the endpoint, keep the env parsing only as a fallback for older controllers, and switch KGWUI-007 to the enabled kinds (D-017 §4). Stale status after deselection was confirmed on v2.5.1 (2026-11-22).
- (P4) Guideline (user, 2026-11-21): keep `pkg/auth/oidc` and `pkg/auth/session` free of new imports from other Snellius packages, so a downstream can later extract them cheaply (program edge E12, deferred). Today they import only `pkg/audit` and `pkg/obs` (and `session` imports `oidc`); where it fits, take those through small interfaces instead
- (P3) Scope verification for experimental Gateway API kinds: switch KGWUI-007 to the controller's enabled kinds once the upstream scope endpoint exists (D-017 §4) — src: phases/phase-1/reviews/phase-codex.md#5
- (P3) Watch CRDs added or removed after startup without a restart — https://github.com/DuncanDoyle/kgateway-ui/issues/1
- (P3) Per-user AuthN/AuthZ behind `api.Authorizer` (D-013) — now Phase 7 scope (D-084) — src: phases/phase-1/phase-1.4-complete.md
- (P3) kgateway#14998 (upstream): once the controller reports a selector that matches nothing, the 3.5 S7 computed rule can go — src: phases/phase-3/phase-3.5-complete.md
- (P3) R59: proxy Pod ownership by UID needs a ReplicaSet watch; the name check (R50) stays until then — src: phases/phase-3/phase-3.2-complete.md
- (P3) D-058 item 1: 023 proxy membership evidence needs a port-aware reverse mapping (xdsview `origin`, Phase 12) — src: phases/phase-3/phase-3.4-complete.md
- (P3) D-058 item 6: DirectResponse status is not judged — Phase 8.1 (design §6.5) — src: phases/phase-3/phase-3.4-complete.md
- (P3) Dense memory: peak live memory at the default edge cap is logged, not gated (D-039). Retained heap is +39.3 of 48 MiB (D-057); Phase 5 must keep the shared cache in the core — src: phases/phase-3/reviews/3.0-gate-codex.md#2
- (P3) A live `warm_withheld` capture for KGWUI-024's error; on kind the gauge never outlasts a scrape — src: phases/phase-3/phase-3.4-complete.md
- (P3) README Install: say that installing the chart needs rights to create a ClusterRole and ClusterRoleBinding (usually cluster-admin), since Kubernetes only lets you bind permissions you hold. Next release (2026-11-09)
- (P4) Watch upstream kgateway#14815 (open, checked 2026-11-10 at Phase 6 start): GatewayExtension gains an `Accepted` condition with observedGeneration. No phase waits on it. At each phase start, check whether it merged; if so, read the condition when present (an error finding for an in-use extension, as KGWUI-002 does for Backends), and show "not available" when absent
- (P4) Neutral `Install` and `Detector` (design §14.4 refactors) — Phase 5 G1 (D-061)
- (P4) kgateway policy ownership, ListenerPolicy CA refs and Backend judgement stay in core packages, inert without kgateway kinds; move behind pack hooks in Phases 6 and 7 — src: phases/phase-5/design.md §4.1, reviews/design-codex.md#4
- (P4) Optional `kind-main` e2e job: run on manual dispatch with `kgw_main_version`; chart source unverified. Its 016-from-status expectation (3.5 S9) is unverified too — src: phases/phase-1/phase-1.4-complete.md, phases/phase-3/phase-3.5-complete.md
- (P4) 2.5 controller log captures; 2.5 TLSRoute SNI conflict handling in view A — src: phases/phase-3/phase-3.4-complete.md
- Smaller Phase 2 and Phase 3 items (parked tasks, test gaps, layout at 720 px, P4 polish) — src: phases/phase-2/phase-2-complete.md and phases/phase-3/phase-3-complete.md, Deferred / open

## Carry-over added 2026-11-25 (8.2)
- (P1) moved to the 8.4 queue (Q8.4-1)
- (P2, phase review) Base authorization runs before the downstream authoriser outside the policy paths; prefetch of downstream-denied refs pays needless checks (cost and order; D-137 covers the outage leak) — src: phases/phase-8/phase-8.2-complete.md
- (P3) Measure the `/findings` cost of policy links and strict bases in a bench world with policy findings — src: phases/phase-8/phase-8.2-complete.md
- (P4) A downstream ListenerSet kind is covered in `pkg/policy` only, not in the API rule world — src: phases/phase-8/phase-8.2-complete.md
- [x] (P3, flake) KGWUI-026 e2e timing: test bug (proxies kept the disconnect onset from earlier controller restarts); fixed in PR #69, merged 2026-11-25
- The 8.3 upgrade notes say that a downstream shell older than v0.9.0 shows policy refs without a link, so upgrade the shell with the backend

## Carry-over added 2026-11-23 (8.1)
- (P4) Route parent KGWUI-002s keep a shared basis and D-040 ranks (D-131 item 1); revisit at the phase review — src: phases/phase-8/phase-8.1-complete.md
- (P4) Exact duplicate status entries: ID follows status order among the duplicates (D-132) — src: phases/phase-8/phase-8.1-complete.md
- At phase close, the E7 notice to kgwctl names D-130, D-131 and D-133 (Guard, collection per namespace, list before get)

## Carry-over added 2026-11-22
- (P3) M-1 rule 2: compare the delegated child's matcher with kgateway's cited translator goldens per tag (D-128) — program order "next"
- (P4) Move engine's rule-set fallbacks and the Gateway CRD table behind the pack (D-128, program open question 3)

## Notes
- 2026-11-25: xdsview Phase 9 (E15, snellius#70): Snellius review posted (1 P1, 2 P2, 3 P3); xdsview accepted all six. For Phase 12: `Dependencies(block).Objects` is `[]Dependent{Object, Kind}`; new gaps `policyRecord`, dropped Compose claims and `gatewaySource` (a consumer may close it with its captured Gateway; ListenerSet listeners show the gap); ambiguous candidates complete; `kgateway.Names()` names its checked minors; an empty `Gaps` means every expected input is attributed. Lands in xdsview `v0.7.0`.
- 2026-11-25: snellius#68 (from xdsview, E14): make "Config accepted" also require no KGWUI-025 or 026, or reword it; optional proxy-only version differences. User ruling 2026-11-25: keep the "Config accepted" label, show it only when 001, 024, 025 and 026 are all absent, no tooltip; document in the docs and site that Snellius does not compare config versions, so a connected proxy on old config still reads as accepted. Version differences wait for Phase 12. After the `v0.9.0` tag. Program read at kgateway-tooling `70fda55`.
- 2026-11-25: KGWUI-026 e2e flake fixed in its own PR #69 (test bug: proxies kept the disconnect onset from earlier controller restarts).
- 2026-11-22: `Scope.Excluded` (#66, PR #67) is for kgwctl Phase 4 task 4.2.2, which it does not block. kgwctl expects it in `v0.9.0` (owner's decision, 2026-11-23); send kgwctl the tag (`cross-repo` issue naming E2), or tell kgwctl if `v0.9.0` ships without it.
- 2026-11-23: Phase 12 targets the xdsview Phase 9 tag (P-010, E15). xdsview will ask for a review of its Phase 9 design (`cross-repo` issue, program open question 7): check it against roadmap Phase 12.
- 2026-11-14: `v0.7.1` released (walkthrough fixes, #42 and #43, D-079 … D-083; phases/phase-6/phase-6-complete.md, Patch release v0.7.1). API v1 changes are additive. The snellius.dev sync now covers `v0.7.0` and `v0.7.1`.
- 2026-11-12: Phase 6 closed (phases/phase-6/phase-6-complete.md), merged in #36 and released as `v0.7.0`. Its snellius.dev gate is still open: sync the site for `v0.7.0`. Open items from the record's Deferred / open: the KGWUI-023 title, the map's reserved rule column, export scope, the D-078 review. The user decides when Phase 7 starts; read the program file and set Base then.
- 2026-11-13: axe scans of the main pages, light and dark, run in every e2e row (#37, `@axe-core/playwright`). Only serious and critical violations fail.
- [x] Q 5.7 (planned, before Phase 6): checks report what they are waiting on (E9, #33, D-068) — closed 2026-11-10, merged in #34, pre-release `v0.7.0-alpha.1`, phases/phase-5/phase-5.7-complete.md. snellius.dev syncs at `v0.7.0`
- 2026-11-09: Phase 5 closed (phases/phase-5/phase-5-complete.md), merged in #31 and released as `v0.6.0`. Phase 6 is next; read the program file and set Base when it starts.
- [x] Q 4.3 (Phase 4 stabilization, v0.5.1): data age reads the server's clock, so cluster clock skew no longer fakes or hides staleness — closed 2026-11-08, phases/phase-4/phase-4.3-complete.md
- 2026-11-07: Phase 3 closed (phases/phase-3/phase-3-complete.md). D-061 adds Phase 4 (Snellius rebrand) and Phase 5 (Generic Gateway API core); the old Phases 4–11 are now 6–13. Phase 4 is pre-approved; set Base when it starts, then write its design.
- The first release tag freezes API v1, install IDs and SPA URLs; make the ghcr packages public first (phase-1-complete.md).
- The repo is public: per the public-repo hygiene rules, vendor and commercial edition names must not appear in code, file names, commit messages or PR text. Refer to them as "downstream distributions".

## 8.4 queue (stabilization, from reviews/phase-codex.md; started 2026-11-25)
- [x] Q8.4-1 (P1 correctness) 2.5 rules: an `Attached` older than the policy raises KGWUI-016 — 3e2b00e: `statusTargetFinding` read Attached without its generation; now skips a stale one
- [x] Q8.4-2 (P1 correctness) a policy referenced by a route in another install gets a policy link whose detail returns 404 — 668ea2c, 21e5bda, 2b80fae: links and detail used different membership; policy link only for members with named get, else YAML with named get
- [x] Q8.4-3 (P2 architecture) `/policies` ignores the namespace scope (D-100 item 8) — 1baf8c8: /policies takes the scope, intersected with its namespace filter
- [x] Q8.4-4 (P2 architecture) `PartiallyValid` and `Overridden` read in core for every policy kind, not via the interpreter (design §8) — 3e2b00e, b5867bf, 827a61d, 4e9e8c5, 479bf06, 379ecad, 01f6ce2: readings and wording from the pack; kgateway checks carry their interpreter; one resolution point; the reading decides Has problems
- [x] Q8.4-5 (P2 architecture) list-only ancestor rule lives in the API, not in `pkg/policy.Relations` (D-123, D-129) — 6dfb034: `policy.Inputs.ListOnlyKinds` (D-138)
- [x] Q8.4-6 (P2 correctness) listener-specific policy view (design §§2, 6.5): build or defer (user) — 77fb09c, df23ed8: option B, policies in each expanded listener row
- [x] Q8.4-7 (P2 correctness) upgrade and site notes overstate finding rules — 5161e2d, a6b79fd
- Phase 10 design: an empty declared-policy group is not evidence that a route lacks auth — src: #carry-overs

## Program rules added 2026-11-25 (kgateway-tooling `dbf7140`)
- P-011 (identity): at the Phase 9 start, audit the remaining D-040 rank-based IDs against P-011 rule 2 (D-102 and D-132 comply)
- P-012 (version bumps): any move to a later kgateway, Gateway API or Envoy version diffs the CRDs, checks kind lists (Helm RBAC test, kind registry), version-keyed tables and mirrors in the same change
- Protocol step 7: every review prompt links the program's "Architecture guidelines" (from the 8.4 reviews on; the Phase 8 phase review predates it)
