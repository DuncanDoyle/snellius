# Sub-phase 7.5 — Two signals (the gate opens)

Kind: planned
Base: 40932fdc626d9a2f5876bfecb9cf59a4fbb9a493
Issues: none | Plan steps: implementation-plan.md, Sub-phase 7.5, items 1–4; carries from phase-7.4-complete.md (Deferred / open): the P2 load cliff, the P3 engine cost (D-115), the P3 `/meta` cost

## Problem / goal
7.5 opens the secure-mode gate (design §2, fail-closed invariant). Two things must hold first.

1. **Load.** Many viewers opening cold pages at once must all get answers. Today they don't. Reproduction: the load bench (`pkg/api/sar_bench_test.go`, `loadTest`, `-tags bench`) run on `/paths` and `/backends` instead of `/routes`, on the base commit:

   | Page | Polls in 6 min | 200 | 503 reasons | Checks |
   |---|---|---|---|---|
   | `/routes` (7.4 record) | many per viewer | 96.8–97.1 % | `expired` | — |
   | `/paths` | 50, one per viewer | 0 | 50 `expired` | 39,800 |
   | `/backends` | 50, one per viewer | 0 | 50 `check-limit` | 50,000 |

2. **Two signals per route** (design §8, D-087, D-093): route configuration and traffic path, in `pkg/explore`, the API and the web, with the legacy `health` as their summary.

Then the gate opens: secure mode serves data after sign-in, with the 7.4 rule tests covering the new fields.

## Expected behaviour

| Input / action | Expected |
|---|---|
| 50 cold namespace-grants viewers poll `/routes`, `/paths` or `/backends` every 5 s at the default limits (Large, 2 ms per check, the load bench) | Every viewer gets a first 200. The last first 200 comes within the cold checks of all viewers ÷ the default rate, plus 60 s (inferred). From each viewer's first 200 on, at least 95 % of polls answer 200 (inferred). No `check-limit` from contention alone (inferred) |
| One viewer, cold, Large, viewer allowed 10 of 100 namespaces | Cold check counts stay within the D-100 targets (`/routes` and `/paths` ≤ 400, `/backends` ≤ 550) |
| Route row and route detail, any viewer | `config` and `path`, each `{state, reason}`; `path.incomplete` (design §8.1–8.2) |
| Route's parentRef or backendRef names an object the viewer may not `get` (it exists or not), or a kind the path needs that they may not `list` there | `path` is `unknown`, `incomplete: true`, reason `path-incomplete`; decided without looking at hidden objects (design §8.1) |
| Readable object on the path shows a fault, and the path is also incomplete | That state, `incomplete: true` |
| Readable ref to an object that does not exist, viewer may read it | The ordinary finding (for example KGWUI-013); `incomplete: false` |
| Route with no readable path rows and no unreadable refs | Its ordinary path state (for example "not attached") |
| `health` for a full-read viewer, and in local mode | Unchanged: `health`, `reason`, flags, sort order and every `state=` value; golden-v1 byte-equal on existing fields |
| `health` for a restricted viewer whose path is incomplete | At least `unknown`; the design §8.3 table |
| `reason` `backend-problem` or `gateway-rejecting` | Only to a viewer who can read that backend or Gateway; otherwise `path-incomplete` |
| Filters | `config=<state>` and `path=<state>` on the route list (states `healthy`, `warning`, `error`, `unknown`; repeated values OR, the two parameters AND), and on Gateway and ListenerSet route tables (inferred: names) |
| Gateway and ListenerSet route tables | Both signals per row |
| Web route list, route detail, map | "Configuration" and "Traffic path" columns; both badges in the route header and a problem strip naming the deciding signal; map route nodes show the summary, so map and route page agree for every viewer (design §8.4) |
| Restricted viewer; a hidden object is created, deleted or made faulty | `config`, `path` and `health` byte-equal before and after (D-085; rule harness) |
| Secure mode, signed-in viewer, any data request | Served; no `503 secure-mode-incomplete` |
| Warm recompute at Large (D-115 follow-up) | Compute/large, large-contradicting, large-contradicting-worst and large-hit within +15 % of `main` before Phase 7 (`39c47c9`) on the reference machine |

## Root cause (if known)
Load: the global limiter (100 checks/s, D-100 item 6) is shared evenly by every waiting request. With 50 cold requests of 400–500 checks each, all of them progress together and none finishes before the whole backlog drains, about 4 minutes. Allowed answers live 60 s, so a request's early answers expire before its last check. It re-asks them, which pushes `/backends` past the 1,000-check cap; `/paths` stays under the cap but fails validity at commit (`expired`). `/routes` (about 350 checks each) drains in about 3 minutes and mostly survives.

## Acceptance tests
- The load bench runs on `/routes`, `/paths` and `/backends` and asserts the first row of the table; it fails on the base commit for `/paths` and `/backends`.
- Explore and API tests for each row of the incompleteness matrix: absent, unreadable, unattached and zero-row routes (design §8.1), for a full-read and a restricted viewer.
- The rule harness: `config` and `path` on every route surface, with hidden-object changes; a rule case per new field (7.4 Notes for later phases).
- golden-v1 byte-equal for a full-read viewer on existing fields.
- Secure-mode e2e (`kind-secure`): a signed-in viewer reads data pages.
- The D-115 A/B benchmarks against `39c47c9`.

## Scope
In: `pkg/sar` (and `pkg/api` where requests wait for checks), `pkg/explore`, `pkg/api`, `pkg/health/own` and `pkg/engine` for D-115, `web/`, `test/e2e`, the chart only if a default changes.
Out: audit levels and release docs (7.6); delegation analysis for restricted viewers (#52); kgwctl changes (design §10); the `/meta` cost (P3) unless it shows in the load bench.

## Plan / design impact
The load fix changes how checks are scheduled or how long answers stay valid at commit: a decisions.md entry and user approval before the code. Filter names go in the plan's 7.5 section. Anything else that departs from design §8 gets a decisions.md entry.

## Definition of done
- [ ] acceptance tests fail before, pass after (report both)
- [ ] full test suite passes, with `-race` under `TZ=UTC`, golangci-lint and the bench job's gates run locally
- [ ] cross-model review of `git diff 40932fd..HEAD` + triage
- [ ] phase-7.5-complete.md written; phase-status.md updated
