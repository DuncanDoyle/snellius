Cross-model: YES (author: Claude Opus 5.5, reviewer: Codex, command: codex exec --sandbox read-only)
Scope: plan (Phase 7, 7.1 in detail)
Diff: n/a (plan review of the uncommitted draft at 1001b39)
Timing: on time

Request changes before executing 7.1. Findings below concern the plan; no implemented Phase 7 vulnerability is claimed.

1. **P2 — 7.1 Task 5: local Service port-forwarding can break.**  
   **Evidence:** Plan line 92 declares `http:8080` for secure mode only. The Service uses `targetPort: http` ([service.yaml](/Users/ddoyle/Development/claude/snellius-phase7/deploy/helm/snellius/templates/service.yaml:10)); `setup.sh:131` forwards through that Service.  
   **Why:** Service port-forwarding must resolve the named target port from the selected pod, even when the actual listener binds loopback. Removing the local container port declaration breaks design §3.1 and Task 7.  
   **Fix:** Explicitly retain named container port `http:8080` and Service port `80` in both modes; restrict exposure through binding.

2. **P2 — 7.1 Task 4: secure startup and nil-authorizer refusal are not reconciled.**  
   **Evidence:** Plan line 81 requires a construction error for nil authorization; `app.go:303` passes `reg.Authorizer`, currently commonly nil. The SAR implementation arrives in 7.3. `NewHandler` currently returns only `http.Handler`.  
   **Why:** Following this literally prevents incomplete secure builds from starting their probes and returning the promised gated response. Changing the constructor signature also affects existing callers without a migration step.  
   **Fix:** Define the construction-error contract and wire an explicit deny-only authorizer for incomplete secure builds, while keeping the outer gate authoritative.

3. **P2 — 7.1 Tasks 2/5 and 7.2: secure configuration/readiness scope is missing.**  
   **Evidence:** Design §§3.2/12 require missing secure configuration to produce not-ready runtime behavior and Helm rendering failures. Task 5 omits required issuer/client/redirect validation; Task 2’s `ProbeHandler` accepts only model source and metrics. Current `readyz` checks cache synchronization and rejected installs (`handlers.go:158–169`).  
   **Why:** A synchronized model can report ready despite unavailable authentication. Secure NOTES also reference OIDC values not introduced until 7.2.  
   **Fix:** Allocate configuration validation, startup retries, sign-in configuration state, and shared readiness state explicitly; introduce the necessary chart values before using them.

4. **P2 — 7.1 Task 5: undeclared install-surface additions and incomplete wiring.**  
   **Evidence:** The plan uses `service.type`, `hostNetwork`, `replicas`, and overridable pod metadata. None exists in current `values.yaml`; Service type and Deployment replicas are hardcoded. It introduces `auth.allowedHosts` and `audit.levels` but lists no corresponding rendered arguments.  
   **Why:** The builder must invent public values/defaults/override semantics, or implement checks against settings the chart otherwise ignores. Configured Host exceptions and audit levels may have no effect.  
   **Fix:** Explicitly declare and document each new supported value—or state that unsupported settings are rejected—and specify values-to-flags wiring and precedence.

5. **P2 — 7.1 Tasks 2/5/6: prescribed changes leave existing checks failing.**  
   **Evidence:** `TestInvalidInstallIDsAreRejected` explicitly decodes JSON and requires rejected IDs (`api_test.go:1307–1312`). Chart tests at lines 219, 374, 403 and 417 require the old listen argument. Task 6 names `explore_types.go`, but `MetaResponse` lives in `pkg/api/types.go:55`; the frontend’s exhaustive `metaShape` is in `types.golden.test.ts:597`.  
   **Why:** Adding the listed tests and regenerating a meta golden does not complete the existing test/type migration.  
   **Fix:** Include these updates explicitly, preserving rejection evidence in operational logs while asserting generic readiness responses.

6. **P2 — 7.1 Task 3: trusted request-ID handling lacks a configuration path.**  
   **Evidence:** `audit.RequestID(next)` promises to retain incoming IDs only from trusted CIDRs, but accepts no trust configuration. Trusted CIDRs arrive under 7.2 chart work.  
   **Why:** The stated interface cannot implement configurable trust without an unspecified global dependency.  
   **Fix:** Pass validated proxy configuration into middleware construction; generate IDs unconditionally until that configuration exists.

7. **P2 — 7.4/7.5 ordering: the gate opens before secure summary behavior is complete.**  
   **Evidence:** 7.4 opens the gate; 7.5 introduces restricted-viewer legacy `health` behavior and `path-incomplete` reasons. Design §8.3 requires unknown health over an otherwise healthy incomplete path.  
   **Why:** Path incompleteness alone does not establish correct existing API summaries.  
   **Fix:** Move the minimum secure summary/reason projection into 7.4, or retain the gate through 7.5.

8. **P2 — 7.6/7.7 scope: release precedes an unresolved supported-cluster decision.**  
   **Evidence:** 7.6 releases `v0.8.0`; optional fallback follows in 7.7. Committed review finding 11 remains deferred to the user.  
   **Why:** Dependency approval alone does not decide whether fallback must ship in this release.  
   **Fix:** Record that scope decision before finalizing release order.

9. **P3 — 7.1 Task 2: two-listener lifecycle coverage is incomplete.**  
   **Evidence:** Planned tests cover successful startup only; existing lifecycle tests cover one listener (`app_test.go:575–644`).  
   **Why:** Second-bind failure or one server failing can leak the first listener or leave the process partially serving.  
   **Fix:** Test either bind failing, runtime server failure, cleanup, and draining both listeners through observable HTTP behavior.

No files were edited; tests were not run.
## Triage
| # | Finding | Source | Decision | Note |
|---|---|---|---|---|
| 1 | Service port-forward needs the `http` port in local mode | reviewer | accept | Task 5: `http` 8080 and Service 80 in both modes |
| 2 | Nil-authoriser refusal vs secure startup | reviewer | accept | Task 4: `DenyAll` until 7.3; outer gate authoritative; `NewHandler` signature unchanged |
| 3 | Secure configuration and readiness scope | reviewer | accept | Task 2: shared readiness func; 7.2 tasks 4–5 add config validation and chart values |
| 4 | Undeclared install-surface changes | reviewer | accept | Task 5: install surface listed; no refusals for values the chart lacks |
| 5 | Existing tests and types left failing | reviewer | accept | Tasks 2, 5, 6 name the existing tests, `types.go:55` and `metaShape` |
| 6 | Trusted request IDs without configuration | reviewer | accept | Task 3: always generate in 7.1; trust in 7.2 |
| 7 | Gate opens before the restricted summary | reviewer | accept | Gate opens in 7.5; design §3.2 and §15 updated |
| 8 | Release before the fallback scope decision | reviewer | accept | 7.6 step 4: release waits for the user's decision |
| 9 | Two-listener lifecycle tests | reviewer | accept | Task 2: second-bind, one-server-fails, drain-both tests |
