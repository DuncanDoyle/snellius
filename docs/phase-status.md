# Phase status

## Phase
Phase: 8 — Policies & Filters
Base: b01b677
Docs: docs/phases/phase-8/
Status: design (2026-11-22). design.md and implementation-plan.md written; Codex design review done (12 findings, all accepted, design revised); waiting for user approval before code. Scope reworked (D-121 … D-129, proposed): declared policy visibility, no resolver; runtime view to Phase 12. Phase start: program read, scope-endpoint reminder given, kgateway#14815 still open.
Author: claude (Opus 5.5)

## Sub-phase
Sub-phase: none

## Program
Program file: https://github.com/DuncanDoyle/kgateway-tooling/blob/main/program.md (read 2026-11-22 at `429b2a2` on `duncandoyle/controllerSemantics`, pending the owner's merge: P-008, P-009, M-1, M-2, E7, E13, E14).
Waiting on: nothing for 8.1 and 8.2. Phase 12 needs xdsview `v0.5.0` (E13).
Provides: E7 to kgwctl Phase 6, the declared-policy model `pkg/policy` (D-123). At phase close: update E7, M-1 (rule-1 entry, rule-2 status, D-128) and M-2 in the program file; notify kgwctl.
Consumers: E2, E3, E8 delivered through `v0.8.0` (kgwctl#3 … #8).
Open `cross-repo` issues here: #15, #21, #63 (xdsview E4/E5, for Phase 12; unchanged). The xdsview session will ask which of KGWUI-001, 006, 008, 025, 026 and the withheld-config signal answer the convergence questions (P-009 q6): answer it directly.

## Carry-over (deferred to later phases)
- (P3) Phase 8 (phase review, final check): extend the full-read-without-Events-or-CRDs test to deny each grant separately and to cover backend list, backend detail and route detail, with a byte baseline for the fully granted reader — src: phases/phase-7/reviews/phase-codex.md
- (P4) Phase 8 and 9 (phase review PH6): extensions receive the raw publication beside per-object visibility helpers; policy relations (Phase 8, no inherited policy, D-121) and winning-route predictions must use viewer-scoped computations so a hidden input cannot change a readable result (D-085) — src: phases/phase-7/reviews/phase-codex.md
- (P3) After `v0.8.0` (user, 2026-11-18, D-118): compare how other Kubernetes UIs and platforms authorise per user, whether they hit the same check-rate limits, and whether Snellius's per-object visibility (D-085) causes them; what could be learned from their architecture. Include (user, 2026-11-18) an implicit namespace scope: derive the namespaces a viewer can read (cached SAR list answers, or SelfSubjectRulesReview as a hint only) and scope pages to them by default, SAR still deciding every object, shown in the UI with a way to widen; OpenShift's Projects API is the precedent
- (P3) After `v0.8.0` (user, 2026-11-18): poll every 10 s instead of 5 s, pause while the tab is hidden and refresh on return (saves server work; check load follows answer lifetimes, not polls); a quiet "checking what you can see" hint after a few seconds of waiting; fairness between viewers in admission. No session cap for restricted viewers: idle sessions cost little, and D-116 already bounds concurrent work
- (P3) REMIND USER at the start of each phase: file the upstream kgateway issue "Expose the controller's effective discovery scope through a read-only endpoint" (H9, D-009 §7). User chose to file it in a later phase (2026-10-20). Draft kept outside the repo (public repo). Before filing: confirm on a cluster that status stays after a namespace is deselected, and re-verify the file:line refs against kgateway main.
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

## Carry-over added 2026-11-22
- (P3) M-1 rule 2: compare the delegated child's matcher with kgateway's cited translator goldens per tag (D-128) — program order "next"
- (P4) Move engine's rule-set fallbacks and the Gateway CRD table behind the pack (D-128, program open question 3)

## Notes
- 2026-11-14: `v0.7.1` released (walkthrough fixes, #42 and #43, D-079 … D-083; phases/phase-6/phase-6-complete.md, Patch release v0.7.1). API v1 changes are additive. The snellius.dev sync now covers `v0.7.0` and `v0.7.1`.
- 2026-11-12: Phase 6 closed (phases/phase-6/phase-6-complete.md), merged in #36 and released as `v0.7.0`. Its snellius.dev gate is still open: sync the site for `v0.7.0`. Open items from the record's Deferred / open: the KGWUI-023 title, the map's reserved rule column, export scope, the D-078 review. The user decides when Phase 7 starts; read the program file and set Base then.
- 2026-11-13: axe scans of the main pages, light and dark, run in every e2e row (#37, `@axe-core/playwright`). Only serious and critical violations fail.
- [x] Q 5.7 (planned, before Phase 6): checks report what they are waiting on (E9, #33, D-068) — closed 2026-11-10, merged in #34, pre-release `v0.7.0-alpha.1`, phases/phase-5/phase-5.7-complete.md. snellius.dev syncs at `v0.7.0`
- 2026-11-09: Phase 5 closed (phases/phase-5/phase-5-complete.md), merged in #31 and released as `v0.6.0`. Phase 6 is next; read the program file and set Base when it starts.
- [x] Q 4.3 (Phase 4 stabilization, v0.5.1): data age reads the server's clock, so cluster clock skew no longer fakes or hides staleness — closed 2026-11-08, phases/phase-4/phase-4.3-complete.md
- 2026-11-07: Phase 3 closed (phases/phase-3/phase-3-complete.md). D-061 adds Phase 4 (Snellius rebrand) and Phase 5 (Generic Gateway API core); the old Phases 4–11 are now 6–13. Phase 4 is pre-approved; set Base when it starts, then write its design.
- The first release tag freezes API v1, install IDs and SPA URLs; make the ghcr packages public first (phase-1-complete.md).
- The repo is public: per the public-repo hygiene rules, vendor and commercial edition names must not appear in code, file names, commit messages or PR text. Refer to them as "downstream distributions".
