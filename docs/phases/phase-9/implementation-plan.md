# Phase 9 — Search & Route Tester: Implementation plan

**Design:** `design.md` (this directory)
**Base:** `31c5094`
**Sub-phases:** 9.1 … 9.5, all planned. Each is one reviewable PR into the phase branch, except 9.1's corpus work, which lands in `kgateway-route-conformance`.

## Global constraints

- Every prediction carries its source; nothing that an unknown input could change is shown as an answer (design §5.3, D-139, D-142).
- The kgateway strategy lives behind the pack; the core never imports kgateway order rules (design §3.1).
- Visibility per design §8 (D-143); every new endpoint gets hidden-change byte tests in the same sub-phase.
- Test requests follow D-064 and D-144; sending is off by default and never available without the permission check in secure mode.
- API v1 changes are additive only and install-scoped. No new chart RBAC. A new Go dependency only for the corpus loader (our own module).
- No ranked or positional IDs leave a response (P-011).
- Public repos: no vendor or edition names; `kgateway-route-conformance` holds no predictor.

## 9.1 — Conformance corpus and strategies

| Step | Outcome | Test boundary |
|---|---|---|
| 1 | `kgateway-route-conformance`: schema `v1alpha1`, loader (decode, validate), README, license, CI skeleton | schema and loader unit tests |
| 2 | Reference runner on kind per pinned minor (2.3.11, 2.4.7, 2.5.1): apply, wait for status and active xDS, send, identify the declared rule by its unique echo backend or distinct response; `route_source` through xdsview as diagnostics only; "unprovable" fails unless declared | runner CI green on the first cases |
| 3 | First cases: design §9.4, including same-port listener overlap, 2.5 hostname step, delegation-only and unresolved parents, full shadowing | runner CI per minor |
| 4 | Release `v0.1.0` of the corpus | tag |
| 5 | Snellius: `install.Settings.WeightedRoutePrecedence` | settings table tests |
| 6 | Gateway API strategy in the core: filter chain, virtual hosts, table, normalisation, match, losers with reasons, provable shadowing, "Can't predict" reasons | unit tests incl. spec corpus cases; loser reasons on the original request |
| 7 | kgateway strategy per minor in the pack: grouping and order per design §5.2 and §4.2, views B and C reused, weights per D-146, no fallback per D-142, GRPCRoute competitor and ListenerPolicy settings give "Can't predict" | corpus cases at the pinned tag, per minor; one pair per sort step |
| 8 | Table memoised on every input it reads | changing only the setting, an annotation or creation order updates a cached result |
| 9 | View C matcher against kgateway's translator goldens per tag (D-128 item 2) | golden comparison test |
| 10 | P-012 bump checklist next to the strategy tests (design §9.5) | checklist file; guard test for pinned minors |

**Done when:** go test, vet and golangci-lint pass; every corpus case passes for its minors in the runner CI and in Snellius's strategy tests; the matcher comparison passes per tag.

## 9.2 — Prediction API and visibility

| Step | Outcome | Test boundary |
|---|---|---|
| 1 | Listener selection (design §5.2 step 1), table memoised per views generation | API tests over Gateway and ListenerSet listeners |
| 2 | `POST /api/v1/installs/{id}/predict`, audit-registered; losers capped at 50 with total and cursor; parent-level replacement note with KGWUI-015 link, rule named only when unique; named rule IDs vs snapshot-local unnamed occurrences | API tests |
| 3 | Secure mode: full read or the uniform "unavailable" (D-143) | hidden route, delegation parent, Gateway and ListenerSet add, change and remove leave restricted responses byte-identical |

## 9.3 — Test requests and copy as curl

| Step | Outcome | Test boundary |
|---|---|---|
| 1 | Chart value, off by default; proxy Service lookup; HTTP and HTTPS with SNI | chart test; unit tests |
| 2 | `POST /api/v1/installs/{id}/test-requests`: method allow-list, watched listeners only, no redirects, timeouts, body cap, audit without header values | API tests incl. rejected targets |
| 3 | `pods/portforward` SubjectAccessReview in secure mode; disabled reason; Service named only with the Service read; "Live response" label | API tests: allowed, denied, outage (D-137), Service hidden |
| 4 | Copy as curl: method, all headers, raw path and query, shell-quoted; HTTP and HTTPS | parse-back unit test; e2e echo comparison |
| 5 | e2e: a few corpus cases sent through a real proxy on the kind matrix; prediction and response agree | e2e workflow |

## 9.4 — Search and web

| Step | Outcome | Test boundary |
|---|---|---|
| 1 | Declared and effective search indexes and `GET .../route-search`, cursor-paged; restricted viewers get the declared index only | API tests; hidden-parent change tests |
| 2 | Bench: index build in the engine bench; query under 50 ms at 1,000 hostnames × 10,000 paths | bench gate |
| 3 | Web: route tester page (form, result with source label, losers, response panel, copy as curl), search, entry points from Gateway and route pages | Playwright on the e2e matrix; axe light and dark |

## 9.5 — Docs and program

- README, `docs/documentation-notes.md`, upgrade notes (chart value).
- snellius.dev `cross-repo` issue (P-006). **Gate:** the release is done only when the site update is live.
- snellius-demo scenario for a delegated route, verified on a cluster (P-007).
- Program file, with the user's approval: E17, M-1 view D and the explicit mirror rule 3 exception until Phase 12 (D-140, D-141). **Gate:** the exception is recorded before 9.5 ships.
- E17 at phase close: the corpus tag in the contract table and a `cross-repo` notice to each consumer (xdsview, kgwctl).

## Out of the phase (tracked in phase-status)

- Envoy route table as observed source (Phase 12).
- GRPCRoute prediction; per-viewer prediction in secure mode (D-143).
- #26 (Phase 11), #27 (Phase 10) (D-145).
- D-147's ID fixes, as the user decides.
