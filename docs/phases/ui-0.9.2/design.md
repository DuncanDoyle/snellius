# UI round 0.9.2: detail page layout

**Status:** draft, awaiting owner approval
**Ships as:** `v0.9.2`, separate from Phase 9 (owner, 2026-11-28)
**Decision:** D-148

## 1. Intent

Detail pages are hard to read. On the HTTPRoute page you cannot tell that "Policies", "Rules" and "Reached through" are different sections with different kinds of information, and the Finding page has the same problem (owner, 2026-11-28). Every section is a plain `h2` at body font size with some space below it (`Section` in `web/packages/shell/src/pages/explore/detailParts.tsx`). The order is also off: policies come before rules, while users look at rules first. On large configs a long rule list pushes later sections out of view, so users need a way to move between sections.

This round gives every detail page the same structure: clearly separate sections, one reading order, and a section nav.

## 2. Goals

1. Each section is visibly its own block, with a header bar that names it and shows a count where one exists.
2. A sticky section nav on the left lists the page's sections, grouped, with counts. It jumps to a section and highlights the current one.
3. One reading order per page kind (§5), the same at every window width.
4. The hierarchy stays on top: on a route, "Reached through" and Parents come first.
5. The same pattern on every detail page kind: HTTPRoute (and the other route kinds), Gateway, ListenerSet, Policy, Backend, Finding.
6. Deep links to a section (`…#route-rules`) open that section, below the sticky header.

## 3. Non-goals

- The filter UI on list pages. The owner finds it heavy, but has no alternative yet; it is a later topic.
- The Processes page, which is not a detail page.
- Phase 9 features (route prediction, search, test requests).
- Changing the tabs: Overview, YAML and the route's Delegation tab stay as they are. This round only changes the Overview tab.
- New data or API changes. The server and the HTTP API do not change.

## 4. Anti-goals

These would make the round a failure even if it works:

- **Hidden evidence.** No tabs per section and no sections that stay collapsed between visits: an admin who searches the page with Ctrl+F must find everything on the Overview tab.
- **Order that depends on width.** A section must never move to another place when the window gets wider or narrower.
- **Layout shift.** Sections and the nav must not jump while data loads; the nav's entries and counts must not push content around.
- **Broken extensions.** Sections contributed through `sideSections` (the downstream build uses this seam) must keep working and appear in the nav.

## 5. Section order and groups

Every page uses the same group labels, in this order. A page shows only the groups and sections it has.

| Group | Meaning |
|---|---|
| Where it sits | The hierarchy: what leads to this object, and its parents |
| Status | Summary facts and conditions |
| What it does | The object's own configuration |
| Runtime | What the data plane does with it (data plane, proxies). Later phases add xdsview information here |
| What acts on it | Policies that name it |
| Related | Child and related objects, and contributed sections |

| Page | Where it sits | Status | What it does | Runtime | What acts on it | Related |
|---|---|---|---|---|---|---|
| HTTPRoute, GRPCRoute, TCPRoute, TLSRoute | Reached through · Parents | Summary | Rules · Effective matches (delegation only) | — | Policies | Child routes (delegation only) |
| Gateway | — | Summary · Conditions | Listeners · ListenerSets · Routes (attached, failed) | Data plane · Proxies | Policies | — |
| ListenerSet | Reached through (its Gateway) (inferred) | Conditions | Listeners · Routes | — | Policies | — |
| Policy (incl. DirectResponse) | — | Summary · Status per ancestor | Targets · Configuration | — | — | Annotations |
| Backend | — | Summary · Conditions | Ports · Endpoints · Explanations | — | Policies | Exposure · Consumers |
| Finding | — | Summary (code, severity, since) | Details (cause, evidence) · Next steps | — | — | Affected objects |

Contributed sections (`sideSections`) go in "Related", after the core sections, in the order the contribution lists them. Each contribution already has an `id` and a `title`, which give its nav entry.

"(inferred)" marks a section the page may not show today; the builder checks it and drops it if the data is not there, rather than adding API.

## 6. Layout

```text
┌ Header: breadcrumb, kind + name, tabs (Overview · YAML · …) ─────────────┐
│ Problem banner (unchanged, above everything)                             │
├──────────────┬───────────────────────────────────────────────────────────┤
│ WHERE IT SITS│ ┌ Reached through   2 ─────────────────────────────────┐ │
│ Reached thr 2│ │ …                                                    │ │
│ Parents     1│ └──────────────────────────────────────────────────────┘ │
│ STATUS       │ ┌ Parents   1 ──────────────────────────────────────────┐ │
│ Summary      │ …                                                         │
│ WHAT IT DOES │                                                           │
│ Rules      12│                                                           │
│ …            │                                                           │
└──────────────┴───────────────────────────────────────────────────────────┘
  sticky nav      one column of section cards
```

- **Section cards.** Each section is a card on a different level from the page background, with a header bar: title, count badge, and room for existing header controls. Cards follow the theme tokens in every theme, with neutral borders.
- **Section nav.** Sticky on the left, about 150 px wide. Group labels are small and muted; entries show the title and the same count as the card. The entry of the section in view is highlighted. Clicking an entry scrolls to the section and updates the URL hash. Hover and pressed styles per the team rules.
- **Narrow windows.** Below a width the builder picks, the nav folds into a compact "Sections" control above the cards. The order does not change.
- **No right-hand column.** The column that opens from 1600 px today (`side`, `sideWhenWide` in `DetailFrame`, and `useTwoColumns`) is folded into the ordered sections. On very wide screens the cards keep a readable maximum width or grow; the builder picks.
- **All sections open.** No collapsing in this round.
- **Anchors.** Each section keeps a stable id (the existing heading ids, such as `route-rules`), with a scroll margin so the sticky header does not cover it. A hash in the URL scrolls there once the data has loaded.

## 7. Approaches considered

| | Approach | Trade-off | Outcome |
|---|---|---|---|
| A | Section cards + sticky left nav with scroll-spy | best "where am I" signal and a table of contents with counts; costs ~150 px width | **picked** (owner; Claude agreed) |
| B | Hierarchy and summary on top, one tab per section | long lists never push others away; but Ctrl+F finds only the open tab, and rules and policies are never visible together | rejected |
| C | Full-width section bands, collapsible, with a sticky "jump to" bar | full width for tables; weaker location signal; remembered collapse can hide evidence | Codex's pick; rejected in favour of A |

Wide screens: keeping the 1600 px right-hand column gives three columns and moves the hierarchy away from the top, and the order then changes with window width. Folding it in was picked (owner).

## 8. Constraints

- Reuse the existing shared parts (`DetailFrame`, `Section`, `Facts` in `detailParts.tsx`); the Finding page uses its own frame and gets the same nav and cards.
- The `Contributions` interface (`web/packages/shell/src/shell/contributions.tsx`) keeps its shape. Adding an optional field is allowed; removing or renaming one is not.
- Team UI rules: no layout shift while loading; hover and pressed styles on every interactive element; check every theme for contrast, badges included; secondary text at lower contrast but AA; no open animations.
- Tests assert behaviour at the page level (sections present and in order, nav entries match sections, a hash opens the section), not styles.
- Public repo: no vendor or product names beyond kgateway and Gateway API.

## 9. Left to the builder

- The exact breakpoint and form of the narrow-window "Sections" control.
- Nav width, card spacing and the maximum content width, within the theme's spacing scale.
- How the highlight follows scrolling (for example an `IntersectionObserver`).
- Whether a page's existing sub-headings inside a section become card headers or stay inside the card.

## 10. Settled before planning (Claude's calls, from the builder check)

1. **Where the nav gets its sections.** Each page declares its sections as data: a list of `{id, group, title, count?, render}` passed to the frame, in the order of §5. The frame renders the nav and the cards from that one list, so nav and page can never disagree, and tests can assert the order.
2. **Empty, loading and absent sections.** A section that does not apply to this object (child routes on a route without delegation) is left out of the list. A section that applies but has no items stays, with count 0, a muted nav entry and its existing empty text: "no policies" is evidence. While a section's own data loads, its nav entry and badge show a fixed-width placeholder, so nothing shifts when the count arrives.
3. **Contributed sections.** Each `sideSections` contribution gets a nav entry from its `id` and `title`, with no count, in "Related". If it renders nothing, its card shows a short empty line; if it throws, the existing error boundary shows in its card. The `SideSectionContribution` interface does not change in this round.
4. **The two-column parts.** Core pages stop using `side`, `sideWhenWide` and `useTwoColumns`. `DetailColumns` is exported from the shell package (`web/packages/shell/src/index.ts`), so a downstream build may use it: it stays exported and working. Only what no export reaches may be removed.
5. **Scroll and sticky offsets.** The page scrolls as it does today. The nav sticks below the object header and tabs, using the existing header height constants; section anchors get a scroll margin of the same height.
6. **Where the §5 table and the code differ.** The table describes groups, not new content. The builder maps each section a page has today into its group, keeping the page's existing data, and reports any mapping that needed a guess. A section in the table that a page does not have is dropped, never added.
7. **The Finding page** keeps its own frame (it has no tabs). It gets the same nav and section cards; the problem banner is not added there.

8. **What "everything on the Overview" means for Ctrl+F.** No section is ever hidden or collapsed. Inside a section, row details that expand today (a rule's details in the rule table) stay expandable: opening 200 rules at once would make the page unusable. Child routes, which today start closed and load only when opened, start open and load with the page.
9. **Contributed anchors** are namespaced as `ext-<contribution id>`, so a contribution can never take a core section's id.
10. **Runtime is a core placement.** Core sections, such as Phase 12's route evidence, can go in "Runtime". Contributed `sideSections` always land in "Related"; a placement field for extensions is a later, separate API decision.

## 11. Verification

- The UI loop records each page kind (route with delegation, Gateway, ListenerSet, policy, Backend, Finding) before and after, in light and dark themes, at a narrow and a wide window, against the demo cluster or a local kind install.
- Web gates: type-check, lint and shell unit tests.
- Codex reviews this design before code, and the diff before the release.
