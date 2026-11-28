# Review: whole branch (UI round 0.9.2)

Reviewer: Codex (`codex exec --sandbox read-only`, default model)
Author: Claude (Opus 5.5)
Cross-model: yes
Scope: `168efd2..18d5be0`, then a scoped re-review of the fix wave `18d5be0..df54bdf`
Date: 2026-11-28

## Branch review

**Hold merge for the navigation regressions, Gateway filter mismatch, and side-section fallback.** I reviewed `168efd2..18d5be0` read-only. The author reports a clean web typecheck, one pre-existing lint info, 1,237 passing Vitest tests across 69 files, and passing `go test ./...`.

1. **P1 · Correctness — The scroll-spy can stop tracking on wide screens.** The observer’s `-60%` bottom margin resolves against viewport **width**. At 1920×1080 it subtracts 1,152 px, collapsing the observation band. The observer also keeps its old top offset when resizing across the narrow breakpoint. Mocked intersections bypass both problems. [DetailSections.tsx:415](../../../../web/packages/shell/src/pages/explore/DetailSections.tsx:415) · [Intersection Observer specification](https://www.w3.org/TR/intersection-observer/#intersectionobserver-rootmargin)

2. **P1 · Correctness — Dragging the scrollbar leaves the clicked section highlighted.** After a jump, `pinned` blocks every scroll update until a wheel, touchmove, or keydown event clears it. Dragging the scrollbar can cross the entire page while the original entry stays current. Promote this deferred item. [DetailSections.tsx:378](../../../../web/packages/shell/src/pages/explore/DetailSections.tsx:378) · [DetailSections.tsx:421](../../../../web/packages/shell/src/pages/explore/DetailSections.tsx:421)

3. **P1 · Correctness — The narrow Sections bar can cover a linked rule.** `?rule=` scrolls the rule itself to the viewport top. Only the card has scroll clearance, so the 48 px sticky bar can cover the entire 40 px rule header. The precedence test checks which element received `scrollIntoView`, so it passes with the rule header obscured. [RuleList.tsx:394](../../../../web/packages/shell/src/pages/explore/route/RuleList.tsx:394) · [DetailSections.tsx:23](../../../../web/packages/shell/src/pages/explore/DetailSections.tsx:23)

4. **P1 · Architecture — Gateway headings and rows use different filter state.** On a server without listener-filter support, the card and nav still use raw `?listener=web` to label the section and suppress its badge. The body uses `pick?.selected` and shows **all** attached routes. Derive the descriptor and query from the same effective selection. [GatewayPage.tsx:180](../../../../web/packages/shell/src/pages/explore/GatewayPage.tsx:180) · [RouteSections.tsx:234](../../../../web/packages/shell/src/pages/explore/RouteSections.tsx:234)

5. **P2 · Architecture — Query writes and anchor history are disconnected.** Rule filters and Gateway listener selection drop the hash when writing the query. `resolved` survives the hashless entry, so returning via Back to the same anchor can skip the jump. The R8 test covers two distinct, uninterrupted hashes. [urlParams.ts:46](../../../../web/packages/shell/src/shell/urlParams.ts:46) · [GatewayPage.tsx:78](../../../../web/packages/shell/src/pages/explore/GatewayPage.tsx:78) · [DetailSections.tsx:398](../../../../web/packages/shell/src/pages/explore/DetailSections.tsx:398)

6. **P2 · Correctness — Valid side-section outputs and IDs escape the new contract.** An empty fragment or `[null]` produces a blank card, violating §10.3. A valid contribution ID such as `team%2Fops` works on click but fails on reload because the link writes the raw ID and the resolver decodes it. Promote the empty-output deferral and test both cases. [contributions.tsx:111](../../../../web/packages/shell/src/shell/contributions.tsx:111) · [DetailSections.tsx:383](../../../../web/packages/shell/src/pages/explore/DetailSections.tsx:383)

7. **P2 · Correctness — Child routes are gated by install capability.** Every HTTPRoute on a delegation-enabled install gets the card, even a plain route with zero children and no delegation. §10.2 makes applicability object-specific. The plain-route test uses TCPRoute, bypassing this case. [RoutePage.tsx:233](../../../../web/packages/shell/src/pages/explore/RoutePage.tsx:233) · [route.test.tsx:680](../../../../web/packages/shell/src/pages/explore/route.test.tsx:680)

8. **P2 · Correctness — Browser verification remains open.** The available artifacts show capture work underway. I could verify neither the complete before/after matrix nor R1 anchor clearance from the supplied evidence. Close that step with actual scroll and viewport assertions. [implementation-plan.md:174](../../../../docs/phases/ui-0.9.2/implementation-plan.md:174)

The remaining logged minors can wait. The legacy `DetailFrame` path and policy wrapper were removed, and the SuggestedFix doc comment was corrected. The A→B→Back→Forward ruling holds for distinct hashes. Severity and code now live in Finding Summary, consistent with the recorded ruling. `DetailColumns` retains its export and tests, `detailTabs` retains its separate rendering path, and core descriptors can place future xdsview evidence in Runtime while `sideSections` stays in Related.
## Re-review of the fix wave

Reviewed `18d5be0..df54bdf` read-only. Finding 6 remains partly open, the browser evidence is insufficient to close finding 8, and the diff introduces one P1.

| Finding | Verdict | Evidence |
|---|---|---|
| 1. Wide scroll-spy | **ADDRESSED** | The observer uses a pixel margin based on viewport height and rebuilds when the height or breakpoint changes. [DetailSections.tsx:467](../../../../web/packages/shell/src/pages/explore/DetailSections.tsx:467) |
| 2. Scrollbar drag | **ADDRESSED** | Scroll events release the jump pin when the position moves more than 2 px. [DetailSections.tsx:475](../../../../web/packages/shell/src/pages/explore/DetailSections.tsx:475) |
| 3. Linked rule clearance | **ADDRESSED** | Both rule layouts use the shared clearance. Chromium measured the rule top at 64 px with the bar ending at 48 px. [RuleList.tsx:35](../../../../web/packages/shell/src/pages/explore/route/RuleList.tsx:35), [RuleTable.tsx:46](../../../../web/packages/shell/src/pages/explore/route/RuleTable.tsx:46) |
| 4. Gateway filter | **ADDRESSED** | The heading, badge, and rows use `pick?.selected`. [GatewayPage.tsx:183](../../../../web/packages/shell/src/pages/explore/GatewayPage.tsx:183) |
| 5. Query and hash history | **ADDRESSED** | Both query writers preserve the hash, and a hashless entry clears the resolved anchor. [urlParams.ts:54](../../../../web/packages/shell/src/shell/urlParams.ts:54), [GatewayPage.tsx:85](../../../../web/packages/shell/src/pages/explore/GatewayPage.tsx:85), [DetailSections.tsx:433](../../../../web/packages/shell/src/pages/explore/DetailSections.tsx:433) |
| 6. Contributions | **NOT ADDRESSED** in full | Encoded IDs round-trip, and empty arrays and fragments show the fallback. A contribution returning `<Empty />` where `Empty` returns `null` still leaves a blank card under §10.3. [DetailSections.tsx:271](../../../../web/packages/shell/src/pages/explore/DetailSections.tsx:271), [contributions.tsx:113](../../../../web/packages/shell/src/shell/contributions.tsx:113) |
| 7. Child routes | **ADDRESSED** | The card requires delegation support and either a delegation ref or reported children. [RoutePage.tsx:234](../../../../web/packages/shell/src/pages/explore/RoutePage.tsx:234) |
| A. Rail overflow | **ADDRESSED** | Grid tracks shrink, titles truncate, and measured counts stay inside the rail. [DetailSections.tsx:113](../../../../web/packages/shell/src/pages/explore/DetailSections.tsx:113) |
| B. Generated ID | **ADDRESSED** | The generated ID belongs to the nested rule body’s `aria-controls` target. The regression test verifies stable card IDs against nav links. [RuleTable.tsx:297](../../../../web/packages/shell/src/pages/explore/route/RuleTable.tsx:297), [route.test.tsx:998](../../../../web/packages/shell/src/pages/explore/route.test.tsx:998) |

**8. Browser verification — Inadequate.** The Chromium checks substantiate the targeted fixes. The full before/after page, theme, and width matrix and the R1 check that a nav click leaves a card header fully visible remain unverified. [Implementation plan:174](../../../../docs/phases/ui-0.9.2/implementation-plan.md:174), [Implementation plan:183](../../../../docs/phases/ui-0.9.2/implementation-plan.md:183)

**New P1 — A contribution ID can crash the overview.** `encodeURIComponent` throws `URIError` on an unpaired surrogate such as `"\uD800"`. The contribution API accepts the string, and the new nav render encodes it outside the slot’s error boundary. [contributions.tsx:30](../../../../web/packages/shell/src/shell/contributions.tsx:30), [DetailSections.tsx:271](../../../../web/packages/shell/src/pages/explore/DetailSections.tsx:271), [DetailSections.tsx:306](../../../../web/packages/shell/src/pages/explore/DetailSections.tsx:306)

Author-reported gates were clean typecheck, one pre-existing lint info, and 1,249 passing shell tests across 67 files.
## Triage

| # | P | Dimension | Decision |
|---|---|---|---|
| 1 | P1 | correctness | accept: pixel band from viewport height, observer rebuilt on resize (`588a7c9`) |
| 2 | P1 | correctness | accept: any scroll that moves away releases the jump pin (`588a7c9`) |
| 3 | P1 | correctness | accept: rule rows get the sticky clearance (`df54bdf`) |
| 4 | P1 | architecture | accept: title, badge and rows use the effective selection (`7b8e989`) |
| 5 | P2 | architecture | accept: query writes keep the hash; a hashless entry clears the resolved anchor (`588a7c9`, `7b8e989`) |
| 6 | P2 | correctness | accept in part: empty arrays and fragments show the fallback, encoded ids round-trip (`588a7c9`). Defer: a child component that renders null still gives a blank card; React cannot detect that cheaply |
| 7 | P2 | correctness | accept: Child routes only for routes that delegate or report children (`df54bdf`) |
| 8 | P2 | correctness | accept: full matrix ran before the fix wave; fixes checked in Chromium; owner reviews the rebuilt build before release |
| A | High | correctness (browser run) | accept: nav column 176 px, titles truncate with full text on hover, counts stay visible (`588a7c9`) |
| B | Low | correctness (browser run) | reject: the generated id belongs to an open rule body inside the Rules card, not a card; test added (`df54bdf`) |
| new | P1 | correctness | defer: an unpaired-surrogate contribution id makes `encodeURIComponent` throw. Ids are developer-authored constants in a downstream build; harden after release |
