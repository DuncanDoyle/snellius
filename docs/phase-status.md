# Phase status

## Phase
Phase: 9 — Search & Route Tester
Base: 31c50949c97ab3c0144e856e9d4aeab25b00d92e (main after `v0.9.1` merged into `duncandoyle/phase9Design`)
Docs: docs/phases/phase-9/
Status: design and plan revised after the Codex design review (`reviews/design-codex.md`: 14 findings, 4 P1, 8 P2, 1 P3, 1 P4, all accepted); D-139 … D-147 proposed. Waiting for the user's approval and answers to design §16 (5 open questions). No code before approval.
Author: claude

## Sub-phase
none

## Program
Program file: https://github.com/DuncanDoyle/kgateway-tooling/blob/main/program.md (last read 2026-11-28 at `4986792`).
Waiting on: nothing for Phase 9. Phase 12 targets xdsview `v0.7.0` or later (E15; xdsview is at `v0.7.2`, kgwctl#18).
Provides: E7 and E2 (`v0.9.0`), E14 wording (`v0.9.1`). Phase 9 proposes E16 (`kgateway-route-conformance`) and M-1 view D (D-140, D-141), committed to the program file only after design approval.
Consumers: kgwctl (E2, E3, E7, E8); E16 consumers to be: xdsview, kgwctl.
Open `cross-repo` issues here: #15, #21, #63, #70, #71 (all xdsview/krp, for Phase 12). In providers: none in xdsview; kgwctl's are its own (#2, #8, #13, #18).
Upstream at phase start: kgateway#15070 (M-4) open; #14815 open; #15067 and #15068 (U2) open.
P-011 audit: remaining D-040 ranks listed in D-147; handling for the user to decide.

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
- (P2, phase review) Base authorization runs before the downstream authoriser outside the policy paths; prefetch of downstream-denied refs pays needless checks (cost and order; D-137 covers the outage leak) — src: phases/phase-8/phase-8.2-complete.md
- (P3) Measure the `/findings` cost of policy links and strict bases in a bench world with policy findings — src: phases/phase-8/phase-8.2-complete.md
- (P4) A downstream ListenerSet kind is covered in `pkg/policy` only, not in the API rule world — src: phases/phase-8/phase-8.2-complete.md
- [x] (P3, flake) KGWUI-026 e2e timing: test bug (proxies kept the disconnect onset from earlier controller restarts); fixed in PR #69, merged 2026-11-25
- The 8.3 upgrade notes say that a downstream shell older than v0.9.0 shows policy refs without a link, so upgrade the shell with the backend

## Carry-over added 2026-11-23 (8.1)
- (P4) Route parent KGWUI-002s keep a shared basis and D-040 ranks (D-131 item 1); revisit at the phase review — src: phases/phase-8/phase-8.1-complete.md
- (P4) Exact duplicate status entries: ID follows status order among the duplicates (D-132) — src: phases/phase-8/phase-8.1-complete.md

## Carry-over added 2026-11-22
- (P3) M-1 rule 2: compare the delegated child's matcher with kgateway's cited translator goldens per tag (D-128) — program order "next"
- (P4) Move engine's rule-set fallbacks and the Gateway CRD table behind the pack (D-128, program open question 3)

## Notes
- 2026-11-28: Phase 9 pre-design answers (user). Test requests: GET, HEAD and OPTIONS only in Phase 9; other methods later (D-064 point 2). Secure mode: a user may send a test request only with `create` on `pods/portforward` in the Gateway proxy's namespace (an existing permission; in the built-in `edit` and `admin` roles, not `view`), matching "Copy as curl". Route prediction (user): a strategy approach, with a Gateway API strategy and a kgateway strategy (delegation) in Phase 9 and the exact Envoy route table (xdsview) in Phase 12. The kgateway strategy is checked by route-matching cases (resources + requests + expected winner) in a new public repo `kgateway-route-conformance` (name: user; created by the user 2026-11-28 as https://github.com/DuncanDoyle/kgateway-route-conformance, public, empty; its content follows the Phase 9 design), reusable by xdsview and kgwctl and proposed upstream to kgateway later. Codex second opinion (`.agents-tmp/p9/formalize-codex.md`, local) agrees: request-level cases plus a kind reference runner; `router_check_tool` and formal methods only as secondary checks. For the design: kgateway's opt-in `KGW_WEIGHTED_ROUTE_PRECEDENCE` (EP-11466) sorts by route weight before Gateway API order, so the kgateway strategy reads that setting; the runner proves rule identity with the opt-in `dev.kgateway.route_source` route metadata (`EnableRouteSourceMetadata`) plus distinct echo backends.
- 2026-11-28: `v0.9.1` released from main `168efd2`: snellius#68 (E14 wording) in #72. Site: snellius.dev#6 (Explore note and the finding codes synced from `v0.9.1`, also item 5 of snellius.dev#5). Program file: E14 commit `b649309`, push waits for the owner. Next: Phase 9, when the user answers the design questions.
- 2026-11-27: xdsview D-023 (on xdsview main at [`7094201`](https://github.com/DuncanDoyle/xdsview/commit/7094201234774a91cc7b358166d48261dc70c89c); no API change, so no new tag, and `v0.7.0` has the same code) sets two Phase 12 rules. The consumer rules are in the doc comments of `provenance.Claim` and `provenance.Module`. Keep `Module` unchanged, because a link finds its record by `Module` and `Ref`. 1) Downstream builds may swap the kgateway module for one that wraps it under another name: pass the module set to `provenance.Compose` as a seam a downstream build can change, and never select or hide claims by `Claim.Module` or `kgateway.ModuleName`; use assertion, evidence and certainty. Claim IDs only hold within one `Compose` result. 2) Secure mode (D-085), from a Codex P1: test that changing a policy the viewer can't read leaves the restricted response identical, including whether a block appears and its warnings. Checking only the objects in the current `Dependencies` list does not prove this.
- 2026-11-26: `v0.9.0` released from main `c60a707` (Phase 8 in #65, `Scope.Excluded` in #67). Notices filed: snellius.dev#5 (P-006, the release is not done until the site is live), snellius-demo#3 (P-007), kgwctl#16 (E7, E2). Next: snellius#68, then Phase 9 when the user says so.
- 2026-11-26: xdsview sent a draft of route-visibility strategies for D-085 (`require-dump-access`, `require-route-metadata`, `require-all-route-candidates`, `trust-current-route-inventory`). Snellius owns the decision. Copy it into `docs/phases/phase-12/` at the Phase 12 design, without its local paths, and answer its §8 there. Leaning: default `require-route-metadata` in secure mode; don't offer `trust-current-route-inventory` (it breaks D-085 rule 2).
- 2026-11-25: xdsview Phase 9 (E15, snellius#70): Snellius review posted (1 P1, 2 P2, 3 P3); xdsview accepted all six. For Phase 12: `Dependencies(block).Objects` is `[]Dependent{Object, Kind}`; new gaps `policyRecord`, dropped Compose claims and `gatewaySource` (a consumer may close it with its captured Gateway; ListenerSet listeners show the gap); ambiguous candidates complete; `kgateway.Names()` names its checked minors; an empty `Gaps` means every expected input is attributed. Lands in xdsview `v0.7.0`. Update 2026-11-25: `Dependencies` is fail-closed. On the kgateway OSS corpus these always give gaps: routes whose names read several ways without `route_source` metadata, listeners without Gateway metadata (close with the captured Gateway), static bootstrap listeners, non-HCM network filters, mirror clusters. Expect Phase 12 to withhold more than it shows until kgateway emits route source metadata. Gap gains `Slot`, `Level`, `Cluster` (xdsview design §5, branch `duncandoyle/phase9`). Confirmed at xdsview `a348b52` (2026-11-26, snellius#70; one P2: direct-response route action needs an input). Phase 12 rules from that review: add the captured Gateway to every block (bootstrap inputs add no object); closing `gatewaySource` also requires every ListenerSet on that Gateway; never render raw `Metadata`, index warnings or any part of a withheld block; never pass hidden HTTPRoutes to `Query.Known`; review result changes at each xdsview bump. Phase 8 design §9 still names `origin.Policies` and `v0.5.0`: update at the Phase 12 design.
- 2026-11-25: snellius#68 (from xdsview, E14): make "Config accepted" also require no KGWUI-025 or 026, or reword it; optional proxy-only version differences. User ruling 2026-11-25: keep the "Config accepted" label, show it only when 001, 024, 025 and 026 are all absent, no tooltip; document in the docs and site that Snellius does not compare config versions, so a connected proxy on old config still reads as accepted. Version differences wait for Phase 12. After the `v0.9.0` tag. Program read at kgateway-tooling `70fda55`.
- 2026-11-25: KGWUI-026 e2e flake fixed in its own PR #69 (test bug: proxies kept the disconnect onset from earlier controller restarts).
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

## Carry-over added 2026-11-25 (phase review)
- Phase 10 design: an empty declared-policy group is not evidence that a route lacks auth — src: phases/phase-8/phase-8-complete.md

## Program rules added 2026-11-25 (kgateway-tooling `dbf7140`)
- P-011 (identity): at the Phase 9 start, audit the remaining D-040 rank-based IDs against P-011 rule 2 (D-102 and D-132 comply)
- P-012 (version bumps): any move to a later kgateway, Gateway API or Envoy version diffs the CRDs, checks kind lists (Helm RBAC test, kind registry), version-keyed tables and mirrors in the same change
- Protocol step 7: every review prompt links the program's "Architecture guidelines" (from the 8.4 reviews on; the Phase 8 phase review predates it)
