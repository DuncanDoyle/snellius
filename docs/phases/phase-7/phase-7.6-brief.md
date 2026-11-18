# Sub-phase 7.6 — Audit, docs, release

Kind: planned
Base: 1e9f443616d93f699eb55162a4fbfa210c9c2a30
Issues: none | Plan steps: implementation-plan.md, Sub-phase 7.6, items 1–4; carries from phase-7.5-complete.md (Notes for later phases)

## Problem / goal
Phase 7 ships as `v0.8.0`. Secure mode serves data since 7.5, but the audit stream only records security events (7.1), and the README still describes the trust model and setup only in outline. 7.6 completes the audit levels of design §9, writes the secure-mode documentation, runs the phase review and releases.

## Expected behaviour

| Input / action | Expected |
|---|---|
| Secure mode, default levels | `security`, `reads` and `summaries` records on; `disclosure` and `requests` off (design §9.2); `agent` level defined, nothing emits it |
| A detail, YAML or export read | One `reads` record with `refs` (design §9.3 fields) |
| List and overview access | One `summaries` record per session × endpoint × filter set per 5 minutes, with a count; flushed every 5 minutes and on shutdown; at most 10,000 open keys, the oldest flushed early |
| `disclosure` on | The object references each response returned |
| `requests` on | Every API request |
| The same denial repeated within a window | One record with a count |
| Rows left out of a list | No denial record (filtering) |
| Any record | Never tokens, cookies, raw query strings or response bodies; `source` from `X-Forwarded-For` only behind a configured trusted proxy CIDR |
| `/metrics` | Counts written and dropped audit records |
| Local mode | Only the mode-at-startup record |
| Levels | Set by a server flag and chart value (inferred: names follow the existing `auth.*` and `audit` naming) |
| README, chart README, upgrade notes | Trust model; secure-mode setup with both audience options and `kubectl auth whoami`; the capacity note (D-118); a downstream authoriser only narrows RBAC (D-119); clusters whose API server cannot accept Snellius's token stay on local mode (D-098) |

## Root cause (if known)
n/a (planned work).

## Acceptance tests
- Audit: unit tests per level and field rule, the summary window, flush on shutdown, the key cap, denial collapsing, and the never-logged list; an API-level test per record kind; local mode writes only the startup record.
- e2e (kind-secure): a read and a list leave the expected records.
- Docs: the README examples compile or run as the repo's doc tests require.

## Scope
In: `pkg/audit`, `pkg/api` and `pkg/app` where records are emitted, the chart (values, README), README.md, `docs/documentation-notes.md`, test/e2e.
Out: OTLP export (later); MCP agent records (later phases); snellius.dev (follows with the release, program P-006).

## Plan / design impact
None expected. Flag and value names go in the plan's 7.6 section.

## Definition of done
- [ ] acceptance tests fail before, pass after (report both)
- [ ] full test suite, `-race` under `TZ=UTC`, golangci-lint, web and bench gates pass
- [ ] phase cross-model review (Codex) + triage; stabilization if needed
- [ ] phase-7.6-complete.md and phase-7-complete.md written; roadmap and phase-status.md updated
- [ ] `v0.8.0` released with the user's approval; consumer notices (kgwctl: `path: no-path` for callers without computed views)
