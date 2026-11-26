# Sub-phase 8.4 — Phase review fixes

Kind: stabilization
Base: 2635548
Issues: none | Plan steps: n/a; findings 1–7 of reviews/phase-codex.md (queue Q8.4-1 … Q8.4-7 in phase-status.md)

## Problem / goal
The Phase 8 phase review found two bugs, a design mismatch in the policy pages' scope, two places where kgateway meaning or an API rule sits outside the boundary the design gives it, a missing per-listener view, and docs that overstate finding rules. 8.4 fixes all seven so the phase can close.

## Expected behaviour

| # | Input / action | Expected |
|---|---|---|
| 1 | 2.5 rules; a policy with a current `Accepted=True` and an `Attached=False TargetNotFound` older than the policy's generation | No KGWUI-016 from that `Attached`. The policy's "Has problems" and `/findings` agree. A current `Attached=False TargetNotFound` still raises 016 as today |
| 2 | An HTTPRoute in install A references a readable policy that only install B owns or lists | A's responses carry no policy link to A's policy detail. Any link Snellius returns for a policy opens a detail that answers for the same viewer and install (inferred: no link rather than a link into B) |
| 3 | `/policies` with the namespace scope set (header or query, as the other lists take it) | Rows, counts, facets and permission checks cover only the scoped namespaces. `/meta` lists the scope for `policies`, and the shell's Policies page sends it like the other lists |
| 4 | A downstream policy kind with no interpreter, current `Accepted=True` reason `PartiallyValid` (or `Overridden`) | No KGWUI-002 warning or info from the reason string (design §8: without a pack, nothing is interpreted). kgateway kinds keep today's findings through the kgateway interpreter. "Has problems" and findings agree for both |
| 5 | `pkg/policy.Relations` called the way the README shows, with the DirectResponse fixture (`pkg/policy/testdata/kgateway-2.3.yaml`) | No `attached-on-ancestor` relation from a list-only kind's ancestors; the same result the API returns (D-129). A kgwctl caller needs no extra rule |
| 6 | Gateway page, expanded listener row (and a ListenerSet's listener rows) | The row shows the policies whose targetRef names that listener's section, with the same source labels and links as the page's Policies section. Policies on the whole Gateway stay in the page's Policies section, not repeated per row (inferred). Secure mode: same visibility rules as the section |
| 7 | README upgrade notes, `docs/documentation-notes.md`, `docs/findings.md` | Match HEAD: the KGWUI-002 basis has the full-read exception; only rejections whose `Attached` made the block pending or stale used to read as KGWUI-003; KGWUI-016 from status can be one per eligible current ancestor |

## Root cause (if known)
1. `statusTargetFinding` reads the reason and status of `Attached` without its generation; D-135 made `Accepted` alone decide freshness, which removed the stale block that hid this.
2. Link admission and the policy detail use different membership rules: links accept a referenced object, the detail searches only the install's own and listed policies.
3. `/policies` was not added to the lists that take the 7.4 namespace scope (D-100 item 8).
4. `policySeverity` in `pkg/health/own` reads `PartiallyValid` and `Overridden` for every policy kind.
5. The list-only filter is applied in `pkg/api` before `pkg/policy.Relations`, so the exported contract (D-123) lacks it.
6. 8.2 built the subject sections for the Gateway as a whole only.
7. The 8.3 docs predate the full read-through Codex did at the phase review.

## Acceptance tests
- 1: health test on the 2.5 rules with mixed generations (old `Attached`, current `Accepted`), plus the policy list's "Has problems" for the same object at the API.
- 2: API test, two installs, route in A referencing B's policy: no link in A's route detail and refs; every policy link in the response returns 200 for the same viewer.
- 3: API test (scoped and unscoped, secure viewer: rows, counts, facets, check count), `/meta` golden, shell list test.
- 4: finding-path test with the same reason string on a kgateway kind and on a downstream kind with no interpreter.
- 5: `pkg/policy` test on the DirectResponse fixture, and the API relations tests unchanged.
- 6: shell test for the listener row; secure test that a hidden policy does not appear in it.
- 7: each changed sentence checked against HEAD.
- Gates: `make test`, `make lint`, web typecheck and tests; CI green including the kind matrix and kind-secure.

## Scope
In: `pkg/health`, `pkg/api`, `pkg/policy`, `pkg/kgateway` (interpreter readings), `web/packages/shell` (Policies list scope, listener rows), README, `docs/findings.md`, `docs/documentation-notes.md`, `docs/decisions.md` (D-138).
Out: carry-over (b) authorization order and (c) shared route-parent basis (tracked); new policy features; snellius#68 (after v0.9.0).

## Plan / design impact
D-138: `pkg/policy.Relations` takes the caller's list-only classification as input and applies D-129 itself; the API passes the served kinds (amends D-123's contract, additive for kgwctl). Item 4 brings the code in line with design §8; no design change. Item 6 is the view design §§2 and 6.5 already call for.

## Definition of done
- [ ] all seven rows pass at their boundary
- [ ] `make test`, `make lint`, web gates pass; CI green
- [ ] D-138 in `decisions.md`
- [ ] Codex review of `git diff 2635548..HEAD` + triage (prompt links the program's "Architecture guidelines")
- [ ] phase-8.4-complete.md written; phase-status.md updated
