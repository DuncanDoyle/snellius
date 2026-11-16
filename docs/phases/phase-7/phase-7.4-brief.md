# Sub-phase 7.4 — Visibility everywhere

Kind: planned
Base: 99510dd2
Issues: none | Plan steps: implementation-plan.md, Sub-phase 7.4, items 1–10; carries from phase-7.3-complete.md (Deferred / open)

## Problem / goal
7.3 checks every object a viewer sees, but several surfaces still leak hidden objects through what they show around the rows: `deniedInInstall` counts, `hidden` flags and restricted placeholders, member and per-listener counts, ancestors, findings decided by hidden objects, health aggregates, install metadata and `/meta`. D-085 requires that a change to an object a viewer cannot read never changes what they see. 7.4 makes every surface follow that rule, adds the per-viewer permissions note, adds EndpointSlices and Pods to backend dependencies, and adds the namespace scope selector (D-100 item 8). The data gate stays closed in production; everything is tested below it with `WithSecureMode(true)`, as in 7.3.

## Expected behaviour

| Input / action | Expected |
|---|---|
| Restricted viewer; an object they cannot read is created, deleted, made faulty or made pending | Every surface in design §7.2 answers byte-equal before and after, masking only the time-of-read fields each test names (design §13) |
| Hidden objects push an aggregate over a threshold (freshness, stuck objects) or a hidden Gateway rejects | Unchanged for the restricted viewer |
| Secure mode, any list or detail response | No restricted placeholders, no `deniedInInstall`, no `hidden` flags; hidden objects are absent |
| Local mode, or a full-read viewer | Every existing field unchanged; golden-v1 byte-equal |
| Gateway or ListenerSet with hidden member routes | Member and per-listener counts count visible routes only |
| Route whose ancestor the viewer cannot read | The ancestor is omitted |
| Own-object finding (from an analyzer behind the typed boundary, D-106) whose resource and every object it named the viewer can read | Shown |
| Own-object finding with an unreadable resource or named object; any other finding (derived, extensions included) | Not shown to a viewer without the full read, and raises no state; a full-read viewer sees it |
| Own-object analyzer that reaches anything beyond its resource, the objects it names and the install context | A test fails (the boundary test) |
| Controller settings change (scope, ownership, version, rule set) | May change what every viewer sees: install context, a stated exception (D-106 item 1); the rule test names exactly what may change |
| Health verdict, header tint, finding counts, groups, search | Computed over the findings the viewer is shown |
| Check failed for collection or access (watch denied, cache not synced) | Shown to every viewer, as code and state |
| Any other check result (grace hold, pending count, deadline, text) | The original for a full-read viewer; "Not checked" for everyone else (D-106). Install-wide unknown is raised for a restricted viewer only by collection or access failures and shown error findings |
| Any install, for every signed-in viewer (D-103) | Listed with its ID (D-102); GatewayClass names, controller readiness and version, and scope details only when the viewer can read their source objects; otherwise a neutral name and "Details not visible to you" (design §7.4) |
| Install with no object the viewer can read | Its pages answer 200 with no rows, never 404; no permission checks decide whether it is listed |
| `/meta` in secure mode | Capabilities that describe cluster objects are per viewer or dropped (decided per capability in the plan tasks); local mode unchanged |
| Extension handler | Its `Deps` carry the viewer and a helper that applies the rule; the extension docs make using it a requirement |
| Backend endpoint counts for a Service (not findings) | Need `list` on EndpointSlices in the Service's namespace; without it, unknown, never derived from hidden slices |
| KGWUI-023 | Derived: full-read viewers only (D-106) |
| Full-read viewer denied EndpointSlices | Takes the per-object path, not the fast path |
| Viewer who cannot `list` every served kind in every namespace | One note in the header area: "You see what your Kubernetes permissions allow." Decided by checks of the viewer's own rights; the presence of hidden objects never changes it |
| Full-read viewer, or local mode | No note |
| Route path for a restricted viewer (computed here, exposed in 7.5) | Incomplete when a ref written in the route or its readable chain names an object the viewer may not `get`, or a kind the path needs may not be `list`ed there; never from the presence, absence or findings of a hidden object (design §8.1, test matrix: absent, unreadable, unattached, zero-row) |
| List request scoped to namespaces A and B (our call: `namespaces=A,B` query parameter on the list and summary endpoints) | Rows, counts and facets come only from A and B; no permission check is asked for root buckets outside them; cross-namespace dependencies keep their own checks, and restricted ones stay unknown or incomplete |
| Scope naming a namespace with no visible objects | An empty result, not an error; the answer is the same whether hidden objects exist there or not |
| No scope parameter | All namespaces, as today |
| Web namespace selector (our call: in the header, in both modes) | Choices come from the namespaces of objects the viewer can see, plus typed entry; the choice survives a reload (per browser) and applies to every list page; the default is all namespaces |
| `/routes` on Large, viewer allowed 10 of 100 namespaces, scoped to 1 namespace | Fewer cold checks than unscoped; the bench reports both numbers |
| Gate | Still closed in production |

## Root cause (if known)
Planned work. The leaks are listed in design §7.2 with their current file:line refs.

## Acceptance tests
- The D-085 rule test per surface in design §7.2: two viewers over one model, a fixed clock and publication sequence, named masks, and the change list of design §13; each mutation check fails when the surface reads a hidden object.
- The own-object boundary test (no own-object analyzer reaches beyond its resource, named objects and install context) and the classification test over every finding code (D-106).
- Handler tests for counts, ancestors, installs, `/meta`, the permissions note, the EndpointSlice rule and the namespace scope, at the HTTP boundary.
- Path incompleteness at the `pkg/explore` boundary, over the four cases in design §8.1.
- Web tests for the selector (persists, applies to list pages, default all) and the permissions note.
- golden-v1 byte-equal for local mode.
- Bench: `/routes` cold checks, scoped and unscoped.

## Scope
In: `pkg/health`, `pkg/api`, `pkg/explore`, `pkg/app` (kinds and full-read set), the extension `Deps`, `web/packages/shell` (selector, note, removal of the hidden-path summaries in secure mode), the bench, README and extension docs.
Out: the two route signals in the API and UI and opening the gate (7.5); audit levels (7.6); RBAC watch (D-099); the snellius.dev sync (7.6).

## Plan / design impact
The plan lists 7.4 as items, not tasks: the tasks are written into implementation-plan.md and reviewed by Codex before implementation, as in 7.3. The namespace scope's parameter name and the selector's place in the header are our calls, recorded in design §11 when the tasks are written. Any other deviation gets a `decisions.md` entry.

## Definition of done
- [ ] acceptance tests fail before, pass after (report both)
- [ ] full test suite passes (go, race on `pkg/auth/...` and `pkg/api/...`, web, helm)
- [ ] phase-7.4-complete.md written; phase-status.md updated
