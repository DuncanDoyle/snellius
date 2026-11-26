# Phase 8 — Policies & Filters: Implementation plan

**Design:** `design.md` (this directory), revised after `reviews/design-codex.md`
**Base:** `b01b677`
**Sub-phases:** 8.1, 8.2, 8.3, all planned. Each is one reviewable PR into the phase branch.

## Global constraints

- No policy resolution: no code decides whether a policy applies to, wins on, or merges into an object. A reviewer rejects any function that orders policies, combines their fields, or derives attachment from targets or computed attachment (design §3, D-121).
- Every policy value carries a source (D-122). No Snellius-written label says "effective", "applied" or "active" about a policy.
- Visibility per design §7; every new endpoint gets hidden-change byte tests in the same sub-phase.
- API v1 changes are additive only and install-scoped. No new dependencies. No new chart RBAC.

## 8.1 — Policy model, pack hook and API (smallest increment)

| Step | Outcome | Test boundary |
|---|---|---|
| 1 | `pkg/policy.Read(u, interp)`: targets as written, ancestors verbatim with `Current` (install controllers, owned ancestor, fresh), precedence annotations and features from the interpreter | table tests over real 2.3, 2.4, 2.5 objects; stale and other-controller ancestors |
| 2 | `PolicyInterpreter` pack hook; kgateway pack implementation; neutral behaviour without a pack | extension test; same reason string on core and downstream policies gets no kgateway interpretation; `examples/downstream` builds |
| 3 | Target checks through the rule set's `PolicyTargeter` and a caller-supplied `Lookup`; `unknown` for the design §5.2 cases | table tests incl. denied, pending and scoped watches, 2.5 explicit refs |
| 4 | Parent joins (design §5.3) and `Relations(subject, Inputs)` | table tests: sectioned, unsectioned, port-only, ListenerSet two-hop, hidden hop |
| 5 | `GET /api/v1/installs/{id}/policies` and `.../policies/{group}/{kind}/{ns}/{name}`, audit-registered | API tests per design §11 API boundary |
| 6 | DirectResponse ancestor findings, list-only kept | engine tests per design §6.6; `docs/findings.md` |
| 7 | External-package test builds the model from `cache.ListOnce`; boundary test: `pkg/policy` imports neither `pkg/api` nor `pkg/health` | `internal/boundary/imports_test.go`; external test |

**Done when:** go test, vet, golangci-lint and the web build pass; the endpoints return design §6.2–6.3 data; the §11 visibility tests pass for a full reader, a restricted viewer and a narrowing downstream authoriser.

## 8.2 — Subject relations and pages

| Step | Outcome | Test boundary |
|---|---|---|
| 1 | Relations field on Gateway, ListenerSet, route and backend detail responses | API tests; hidden policy, parent, hop and ancestor byte tests |
| 2 | Web: Policies list, policy detail, subject sections with source labels and the fixed notes; links carry group | Playwright on the e2e matrix; axe light and dark; copy test over Snellius labels |
| 3 | Phase 7 P3 carry-over: full read without Events or CRDs denies each grant separately; backend list, backend detail, route detail; byte baseline for the full reader | API tests |

## 8.3 — Docs and program

- `docs/findings.md`, README, `docs/documentation-notes.md`.
- snellius.dev `cross-repo` issue for the site session (P-006); snellius-demo scenario (P-007).
- Program file: Snellius's part of E7 delivered (`pkg/policy` at the release tag), M-1 rule-1 entry and rule-2 status (D-128), M-2 unchanged. Notify kgwctl with a `cross-repo` issue naming E7.

## Out of the phase (tracked in phase-status)

- View B, runtime contributions (Phase 12, D-124).
- M-1 matcher conformance against kgateway's translator goldens (D-128).
- Rule-set fallbacks and the CRD table behind the pack (D-128).
