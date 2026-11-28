# Review: design + plan (UI round 0.9.2)

Reviewer: Codex (`codex exec --sandbox read-only`, default model)
Author: Claude (Opus 5.5)
Cross-model: yes
Scope: `docs/phases/ui-0.9.2/design.md` and `implementation-plan.md` at `2b61c39`
Date: 2026-11-28

1. **P2 · Architecture — The proposed offsets don’t match the page layout.** The plan uses 116 px for both sticky positioning and anchor clearance. The shell header and object header scroll with the page, the tab strip adds 40 px, and Finding has its own frame without tabs. That offset wastes viewport space today and would undercount the stack if the object header and tabs became sticky. Define the offset at the frame that owns the scroll layout and verify it in a browser. [implementation-plan.md:73](../../../../docs/phases/ui-0.9.2/implementation-plan.md:73)

2. **P2 · Architecture — Contributed anchors need their own namespace.** The plan uses the slot ID as the anchor. `sideSections` deduplicates contributions against each other, and existing heading IDs are prefixed with `useId`. A valid contribution named `gw-listeners` could now collide with the core card. Use stable, namespaced contribution anchors and test a collision, following the precedent of reserving core `detailTabs` IDs. [implementation-plan.md:88](../../../../docs/phases/ui-0.9.2/implementation-plan.md:88) · [contributions.tsx:147](../../../../web/packages/shell/src/shell/contributions.tsx:147)

3. **P2 · Correctness — Task 2 cannot deliver its contribution fallback as written.** `Slot` renders `null` on failure, and the plan omits the design’s empty-output behavior. Specify a side-section-only fallback inside the card, preserve the object-change retry, and update the existing test that expects a failed side section to disappear. A shared default fallback would also change contributed tabs and finding actions. [contributions.tsx:123](../../../../web/packages/shell/src/shell/contributions.tsx:123) · [contributions.test.tsx:274](../../../../web/packages/shell/src/shell/contributions.test.tsx:274)

4. **P2 · Correctness — The section inventory leaves existing content without a home.** Route’s `UpChain` is separate from `route-serves`. Finding’s Suggested fix is already a section with `#finding-fix`. Backend’s `ownProblem` suppresses the shared strip, but its status line currently comes through the children Task 7 removes. Assign each an explicit destination and test the existing anchors. [RoutePage.tsx:101](../../../../web/packages/shell/src/pages/explore/RoutePage.tsx:101) · [FindingPage.tsx:185](../../../../web/packages/shell/src/pages/findings/FindingPage.tsx:185) · [BackendPage.tsx:61](../../../../web/packages/shell/src/pages/backends/BackendPage.tsx:61)

5. **P2 · Correctness — Wrapper-only migration leaves evidence hidden.** Child Routes starts closed and fetches only when expanded. RuleTable also mounts rule details only when expanded. The design promises Ctrl+F access to all Overview evidence. Decide explicitly whether that promise includes row and child-list contents, then test the agreed scope. [design.md:34](../../../../docs/phases/ui-0.9.2/design.md:34) · [ChildRoutes.tsx:44](../../../../web/packages/shell/src/pages/explore/route/ChildRoutes.tsx:44) · [RuleTable.tsx:315](../../../../web/packages/shell/src/pages/explore/route/RuleTable.tsx:315)

6. **P2 · Architecture — Loading counts cross the query ownership boundary.** Task 4 asks pages to supply loading counts, but `SubList` owns the query, cursor, and lag state. That invites duplicate subscriptions or callbacks when detail responses already supply several totals. Define which totals are authoritative, including listener-filtered and unavailable counts. Reserve the nav footprint in the initial skeleton, since a fixed-width badge alone cannot prevent the cards shifting sideways. [implementation-plan.md:132](../../../../docs/phases/ui-0.9.2/implementation-plan.md:132) · [detailParts.tsx:365](../../../../web/packages/shell/src/pages/explore/detailParts.tsx:365)

7. **P2 · Correctness — The task-by-task gates miss tests that must migrate.** Task 4 still has Gateway and ListenerSet tests requiring `detail-side`. Task 7 removes `children`, which direct `DetailFrame` fixtures still pass. Update those tests in the corresponding tasks. Keep breakpoint coverage for the independently exported `DetailColumns` primitive. [details.test.tsx:1268](../../../../web/packages/shell/src/pages/explore/details.test.tsx:1268) · [contributions.test.tsx:453](../../../../web/packages/shell/src/shell/contributions.test.tsx:453) · [index.ts:80](../../../../web/packages/shell/src/index.ts:80)

8. **P3 · Correctness — Single-target mocks miss navigation failures.** Test a tall card, the last short card, and Back/Forward. Also test a URL containing both `?rule=` and a section hash. Rule scrolling already runs after data mounts, and query updates currently omit the hash, so the two navigation paths need defined precedence and history behavior. [implementation-plan.md:65](../../../../docs/phases/ui-0.9.2/implementation-plan.md:65) · [RuleList.tsx:394](../../../../web/packages/shell/src/pages/explore/route/RuleList.tsx:394) · [urlParams.ts:46](../../../../web/packages/shell/src/shell/urlParams.ts:46)

9. **P4 · Architecture — Runtime is a core placement, not an extension placement.** Core descriptors can add Phase 12 route evidence under Runtime. `sideSections` always lands under Related, and `detailTabs` renders outside Overview. Record that boundary now so a future pack-owned xdsview integration can make an explicit placement decision without widening this release’s public API. [design.md:48](../../../../docs/phases/ui-0.9.2/design.md:48) · [roadmap.md:183](../../../../docs/roadmap.md:183)

Static review only. No files edited.
## Triage

| # | P | Dimension | Decision | Where |
|---|---|---|---|---|
| 1 | P2 | architecture | accept: one sticky offset owned by the frame, browser-checked | plan revision R1 |
| 2 | P2 | architecture | accept: contributed anchors `ext-<id>`, collision test | design §10.9, plan R2 |
| 3 | P2 | correctness | accept: side-section-only fallback, retry kept, test updated | plan R3 |
| 4 | P2 | correctness | accept: UpChain, Suggested fix and the Backend status line get explicit places | plan R4 |
| 5 | P2 | correctness | accept: sections never hidden; rule row details stay expandable; child routes open and load with the page | design §10.8, plan R4 |
| 6 | P2 | architecture | accept: counts only from detail-response totals; skeleton reserves the nav | plan R6 |
| 7 | P2 | correctness | accept: migrate affected tests in the task that breaks them | plan R7 |
| 8 | P3 | correctness | accept: tall/last card, Back/Forward, `?rule=` precedence tests | plan R1, R4 |
| 9 | P4 | architecture | accept: Runtime is core-only; extension placement is a later API decision | design §10.10 |
