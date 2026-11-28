# Release 0.9.2 — Wording

12 new strings for the detail-page section nav (`web/packages/shell/src/copy/explorer.ts`: `sectionsCopy`, `listenerSetCopy`, `findingsCopy`). Ids run from S854, after S853 in `copy/policies.ts`. Each is a single plain label, so there are no alternative options to choose from.

| # | id | File:line | Where | Placeholders | Text |
|---|---|---|---|---|---|
| 1 | S854 | `copy/explorer.ts:717` | Nav group heading: where the object sits (parents) | none | Where it sits |
| 2 | S855 | `copy/explorer.ts:718` | Nav group heading: summary and conditions | none | Status |
| 3 | S856 | `copy/explorer.ts:719` | Nav group heading: listeners, rules, routes, details | none | What it does |
| 4 | S857 | `copy/explorer.ts:720` | Nav group heading: data plane and runtime facts | none | Runtime |
| 5 | S858 | `copy/explorer.ts:721` | Nav group heading: policies and other objects that act on this one | none | What acts on it |
| 6 | S859 | `copy/explorer.ts:722` | Nav group heading: related objects and extension side sections (`#ext-<id>`) | none | Related |
| 7 | S860 | `copy/explorer.ts:724` | Label of the nav toggle button at narrow widths | none | Sections |
| 8 | S861 | `copy/explorer.ts:726` | Accessible name of the section nav | kind | {kind} sections |
| 9 | S862 | `copy/explorer.ts:728` | Line inside an extension side-section card that failed or rendered nothing | none | Nothing to show here. |
| 10 | S863 | `copy/explorer.ts:207` | ListenerSet "Reached through" card title (the route-parent chain and the objects that serve it) | none | Reached through |
| 11 | S864 | `copy/explorer.ts:451` | Finding page Summary card and nav entry | none | Summary |
| 12 | S865 | `copy/explorer.ts:452` | Finding page: kind name that builds the nav's accessible name (S861) | none | Finding |
