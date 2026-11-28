# Detail page layout (0.9.2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every detail page's Overview shows its sections as cards in one fixed, grouped order, with a sticky left section nav (counts, current-section highlight, deep links), at every window width.

**Architecture:** A new `DetailSections` component renders a nav and section cards from one declarative list (`DetailSection[]`). `DetailFrame` gains a `sections` prop and appends contributed `sideSections` to that list; pages move from hand-written `<Section>` children plus `side` to a `sections` list. The Finding page, which has its own frame, uses `DetailSections` directly.

**Tech Stack:** React 18, TypeScript, emotion (`styled`), Vitest + Testing Library, the shell package's theme tokens.

**Spec:** `docs/phases/ui-0.9.2/design.md` (D-148). Read it first; §5 (order), §6 (layout) and §10 (settled calls) are binding.

## Global Constraints

- Group order and labels, verbatim: "Where it sits", "Status", "What it does", "Runtime", "What acts on it", "Related".
- No tabs per section and no collapsing; every section is open (design §4).
- The order never depends on window width; no right-hand column on core pages (design §6).
- No layout shift: counts that are still loading render a fixed-width placeholder in nav and card header (design §10.2).
- `SideSectionContribution` and every export in `web/packages/shell/src/index.ts` keep their shape; `DetailColumns` stays exported and working (design §10.4).
- Section ids keep today's heading ids (`route-rules`, `gw-listeners`, …); anchors scroll below the sticky header (`HEADER_H` 52 + `OBJECT_HEADER_H` 64 from `shell/constants.ts`, `shell/ObjectHeader.tsx`).
- No server or HTTP API change.
- New UI strings go in `web/packages/shell/src/copy/*.ts` with the next free `// S<n>` ids (highest today: S853).
- Hover and pressed styles on every nav entry; neutral borders; AA contrast in every theme; no open animations.
- Comments 1–2 lines; public repo: no vendor names beyond kgateway and Gateway API.
- Web gates per task: `npm run typecheck`, `npm run lint`, `npx vitest run packages/shell` from `web/`.

## Review Focus

1. A deep link with `#route-rules` on a page whose data is still loading: the page lands on Rules once data arrives, not at the top.
2. A route with 200 rules: the nav stays visible (sticky) and highlights "Rules" while scrolling through them, then "Policies" after.
3. A contributed side section that throws: its card shows the error boundary, its nav entry still works, and the other sections render.
4. A narrow window (below the nav breakpoint): the "Sections" control lists the same entries in the same order and jumps to them.
5. An object with no policies: the "Policies" card stays with count 0 and its empty text; it is not removed.

---

### Task 1: `DetailSections` — nav and section cards from one list

**Files:**
- Create: `web/packages/shell/src/pages/explore/DetailSections.tsx`
- Modify: `web/packages/shell/src/copy/explorer.ts` (group labels, "Sections" control label)
- Test: `web/packages/shell/src/pages/explore/DetailSections.test.tsx`

**Interfaces:**
- Produces:
  ```ts
  export type SectionGroup = "where" | "status" | "does" | "runtime" | "actsOn" | "related";
  export interface DetailSection {
    id: string;                 // element id and URL hash, e.g. "route-rules"
    group: SectionGroup;
    title: string;
    count?: number | "loading"; // absent: no badge
    muted?: boolean;            // applies but empty (count 0)
    render: () => ReactNode;    // card body
  }
  export function DetailSections(props: { sections: DetailSection[]; label: string }): JSX.Element;
  export const SECTION_GROUPS: readonly SectionGroup[]; // in the Global Constraints order
  ```

- [ ] **Step 1: Write the failing tests**
  - `renders cards in group order, then list order`: given sections out of group order, `getAllByRole("region")` titles follow `SECTION_GROUPS` order, stable within a group.
  - `nav lists every section with its group label and count`: `getByRole("navigation", {name: label})` contains the group labels that have sections (and none for empty groups) and one link per section with `href="#<id>"` and the count text.
  - `loading count renders a placeholder in nav and header`: `count: "loading"` → `getAllByTestId("count-placeholder")` has length 2.
  - `zero count is muted but present`: `count: 0, muted: true` → card exists, nav entry has `aria-describedby` or class marking muted (assert `data-muted="true"`).
  - `clicking a nav entry scrolls to the card and sets the hash`: mock `scrollIntoView`; click → called on `#<id>` element; `window.location.hash === "#<id>"`.
  - `highlights the section in view`: mock `IntersectionObserver`; trigger intersection for `route-policies` → that nav link has `aria-current="location"`.
  - `narrow window shows the Sections control with the same entries`: mock `matchMedia` narrow → a button named by the "Sections" copy opens a list with the same links in the same order.

- [ ] **Step 2: Run them to verify they fail**
  Run: `npx vitest run packages/shell/src/pages/explore/DetailSections.test.tsx` (from `web/`). Expected: FAIL, module not found.

- [ ] **Step 3: Implement `DetailSections` in `DetailSections.tsx`**
  Cards reuse `Section` semantics (`<section aria-labelledby>` with an `h2` in a header bar). Nav is `position: sticky; top: HEADER_H + OBJECT_HEADER_H`, ~150 px; cards get `scroll-margin-top` of the same value. Scroll-spy with one `IntersectionObserver`. The narrow breakpoint and exact spacing are the builder's call (design §9).

- [ ] **Step 4: Run the tests to verify they pass**, then the web gates.

- [ ] **Step 5: Commit**
  `git add web/packages/shell/src/pages/explore/DetailSections.tsx web/packages/shell/src/pages/explore/DetailSections.test.tsx web/packages/shell/src/copy/explorer.ts` and commit "web: DetailSections renders a section nav and cards from one list (D-148)".

### Task 2: `DetailFrame` takes a `sections` list and appends contributions

**Files:**
- Modify: `web/packages/shell/src/pages/explore/detailParts.tsx` (`DetailFrameProps`, `DetailFrame`, skeleton)
- Test: `web/packages/shell/src/shell/contributions.test.tsx`, `web/packages/shell/src/shell/layout.test.tsx`

**Interfaces:**
- Consumes: `DetailSection`, `DetailSections` (Task 1).
- Produces: `DetailFrameProps.sections?: DetailSection[]`. When set, the Overview renders the problem strip, then `DetailSections` with `sections` plus one `{group: "related", id: <slot id>, title: s.title, render}` per `useSideSectionContributions` entry, each still wrapped in `Slot`. `children` and `side` remain for pages not yet migrated and are removed in Task 7.

- [ ] **Step 1: Write the failing tests**
  - `contributed side sections appear in the nav under Related`: with a `sideSections` contribution, the nav has its title under "Related" and the card renders its content.
  - `a throwing contribution keeps its nav entry and card`: contribution `render` throws → error boundary text inside its card; other cards present.
  - `skeleton reserves one column`: while loading with `sections` set, `getByTestId("detail-skeleton")` has no side-column placeholder.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement** in `detailParts.tsx`; the `sections` path never calls `DetailColumns`.
- [ ] **Step 4: Run tests and gates.**
- [ ] **Step 5: Commit** "web: DetailFrame renders a sections list with contributed sections (D-148)".

### Task 3: Route page in the new order

**Files:**
- Modify: `web/packages/shell/src/pages/explore/RoutePage.tsx`, `pages/explore/route/EffectiveMatches.tsx`, `pages/explore/route/ChildRoutes.tsx`, `pages/policies/PolicyRelationsSection.tsx` (export a body without its own `Section`/`h2`)
- Test: `web/packages/shell/src/pages/explore/route.test.tsx`

**Interfaces:**
- Consumes: `DetailFrameProps.sections` (Task 2).
- Produces: `PolicyRelationsBody` (the relations content without its section wrapper) and `policyRelationsCount(relations): number` in `PolicyRelationsSection.tsx`, reused by Tasks 4–5.

Order (design §5): Reached through (`route-serves`, where) · Parents (`route-parents`, where) · Summary (`route-summary`, status) · Rules (`route-rules`, does) · Effective matches (`route-effective`, does, delegation only) · Policies (`policy-relations`, actsOn) · Child routes (`child-routes`, related, delegation only).

- [ ] **Step 1: Write the failing tests**
  - `route sections follow the design order`: region names in order: "Reached through", "Parents", "Summary", "Rules", "Policies" for a plain route; with delegation, "Effective matches" after "Rules" and "Child routes" last.
  - `no side column at 1600 px`: with `matchMedia` wide, Parents is inside the main column after "Reached through" (no element with the old side-column test id).
  - `deep link opens Rules after load`: render with `#route-rules` and delayed data → `scrollIntoView` called on `#route-rules` after data resolves.
  - `route with no policies keeps an empty Policies card`: count 0, empty text present.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement** by building the `sections` list in `RoutePage`; drop `side`, `sideWhenWide` and `useTwoColumns` there. Existing section bodies stay; only their wrappers move.
- [ ] **Step 4: Run tests and gates** (all existing route tests must still pass, updated only where they assert the old order).
- [ ] **Step 5: Commit** "web: route page sections in the D-148 order".

### Task 4: Gateway and ListenerSet pages

**Files:**
- Modify: `pages/explore/GatewayPage.tsx`, `pages/explore/gateway/DataPlaneSection.tsx`, `pages/explore/gateway/ProxiesSection.tsx`, `pages/explore/RouteSections.tsx`, `pages/explore/ListenerSetPage.tsx`
- Test: `pages/explore/details.test.tsx`

Gateway order: Summary (status) · Conditions `gw-conditions` (status) · Listeners `gw-listeners` · ListenerSets `gw-listenersets` · Routes `attached-routes`/`failed-routes` (does) · Data plane `gw-dataplane` · Proxies `gw-proxies` (runtime) · Policies (actsOn).
ListenerSet order: Reached through (its Gateway; only if the page shows it today, design §10.6) · Conditions `ls-conditions` · Listeners `ls-listeners` · Routes · Policies.

- [ ] **Step 1: Failing tests** `gateway sections follow the design order` and `listenerset sections follow the design order` (region names in order), and `gateway nav groups Data plane and Proxies under Runtime`.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement** the `sections` lists; async sub-lists (ListenerSets, routes) pass `count: "loading"` until their query resolves.
- [ ] **Step 4: Run tests and gates.**
- [ ] **Step 5: Commit** "web: Gateway and ListenerSet sections in the D-148 order".

### Task 5: Policy and Backend pages

**Files:**
- Modify: `pages/policies/PolicyPage.tsx`, `pages/backends/BackendPage.tsx`, `pages/backends/{EndpointsSection,ExposureSection,Explanations,PortChain}.tsx`
- Test: `pages/policies/policies.test.tsx`, `pages/backends/backends.test.tsx`

Policy order: Summary `policy-summary` · Status `policy-status` (status) · Targets `policy-targets` · Configuration `policy-config` (does) · Annotations `policy-annotations` (related).
Backend order: Summary `backend-facts` · Conditions `backend-conditions` (status) · Ports `backend-ports` · Endpoints `backend-endpoints` · Explanations `backend-explanations` (does) · Policies (actsOn) · Exposure `backend-exposure` · Consumers `backend-consumers` (related).

- [ ] **Step 1: Failing tests** `policy sections follow the design order` and `backend sections follow the design order`.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement** the `sections` lists. Backend keeps its own status line (`ownProblem`).
- [ ] **Step 4: Run tests and gates.**
- [ ] **Step 5: Commit** "web: policy and backend sections in the D-148 order".

### Task 6: Finding page

**Files:**
- Modify: `pages/findings/FindingPage.tsx`
- Test: `pages/findings/findings.test.tsx`

Order: Summary (code, severity, since; status) · Details `finding-details` (does; today an unlabelled section, so it gets this id and the title "Details") · Next steps `finding-steps` (does) · Affected objects `finding-affected` (related).

- [ ] **Step 1: Failing tests** `finding sections follow the design order` and `finding nav lists Next steps only when there are steps`.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement** with `DetailSections` inside the page's own frame; no tabs and no problem banner added.
- [ ] **Step 4: Run tests and gates.**
- [ ] **Step 5: Commit** "web: Finding page sections and nav (D-148)".

### Task 7: Remove the old paths, docs, and UI-loop evidence

**Files:**
- Modify: `pages/explore/detailParts.tsx` (drop `children`-as-main and `side`/`sideWhenWide` from `DetailFrameProps` once no core page uses them; keep `DetailColumns` and its export), `shell/layout.test.tsx`, `README.md` (one line in the upgrade notes for 0.9.2), `docs/phases/ui-0.9.2/` (complete record per the SDLC template)
- Test: whole shell suite

- [ ] **Step 1:** `grep -rn "sideWhenWide\|side=" web/packages/shell/src/pages` returns nothing.
- [ ] **Step 2:** Remove the unused props; `DetailColumns` export and `layout.test.tsx` cases for it still pass.
- [ ] **Step 3:** Web gates pass; `go test ./...` untouched but run once.
- [ ] **Step 4:** UI loop: before/after screenshots and a short clip per page kind (route with delegation, Gateway, ListenerSet, policy, Backend, Finding), light and dark, narrow and wide, against the demo cluster (`kgw-25-snellius-demo`) or a local kind install. Evidence stays in `.agents-tmp/`.
- [ ] **Step 5: Commit** "web: drop the two-column detail paths; 0.9.2 upgrade note".

---

## Revisions after the Codex review

These override the tasks above where they differ. Source: `docs/phases/ui-0.9.2/reviews/design-codex.md`.

- **Task 1, offsets (R1).** The frame that owns the scroll layout defines one `--detail-sticky-top` value: the height of whatever is sticky above the nav when it sticks (the object header and tabs scroll away today, so the value may be just `HEADER_H`). Nav `top` and card `scroll-margin-top` both use it. The Finding page sets its own value. Step 4 adds a browser check in the UI loop that a nav click lands the card's header fully visible.
- **Task 1, navigation tests (R8).** Add: `jumps to a tall card and to the last, short card` (the last card's nav entry becomes current even if it cannot reach the top); `Back and Forward move between section hashes`. Section clicks use `history.replaceState`-free `pushState` with the hash only, keeping the query string.
- **Task 2, contribution anchors (R2).** Contributed cards use the id `ext-<contribution id>`. Test `a contribution named like a core section does not collide`: a contribution with id `gw-listeners` gets `#ext-gw-listeners` and the core card keeps `#gw-listeners`.
- **Task 2, contribution fallback (R3).** Add a side-section-only fallback: `Slot` gets an optional `fallback` prop used only by the side-section path, showing a short line inside the card; empty output shows the same line. The object-change retry (`resetKey`) stays. Update `contributions.test.tsx:274`, which expects a failed side section to disappear, to expect the card with the fallback line. Contributed tabs and finding actions keep `null`.
- **Task 2, skeleton (R6).** The skeleton reserves the nav's width and a fixed number of placeholder entries, so cards do not shift sideways when the nav appears.
- **Task 3, inventory (R4, R5, R8).** `UpChain` renders at the top of the "Reached through" card, above the serves list. `ChildRoutes` starts open and fetches with the page (design §10.8). Test `?rule= wins over a section hash`: with both `?rule=2` and `#route-policies`, the page scrolls to rule 2 (the existing rule behaviour), and the nav highlights Rules.
- **Counts (R6).** Counts come only from totals the detail response already carries (for example `rules.length`, `parentRefs.length`, `policyRelations`, `childCount`). A section whose total exists only inside a `SubList` query shows no count badge; pages never add a second subscription for a count. `count: "loading"` is used only while the detail response itself loads.
- **Tasks 4–6, inventory (R4).** Finding: "Suggested fix" (`finding-fix`) is its own card in "What it does", between Details and Next steps; existing anchors `#finding-fix`, `#finding-steps` keep working (test). Backend: its status line (`ownProblem`) renders above the sections, where the shared problem strip goes, not as a card.
- **Tests that must migrate in the same task (R7).** Task 4 updates the Gateway and ListenerSet tests that require `detail-side` (`details.test.tsx:1268` and neighbours). Task 7 updates the direct `DetailFrame` fixtures that pass `children` (`contributions.test.tsx:453`), and keeps `layout.test.tsx` coverage for the exported `DetailColumns`.
