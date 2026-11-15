**The plan needs revision before implementation.** I found two P1 gaps, five P2 gaps, and one P3 test issue. These are plan findings against the current code, not claims that the proposed code already exists.

1. **P1 — Separate named-read and population memos explicitly.**  
   **Location:** `docs/phases/phase-7/implementation-plan.md:362`  
   **Evidence:** Task 22 adds memoised `canNamed`, but does not require separate storage from `can`. Today `pkg/api/visible.go:21` stores one boolean per ref. Shared rendering helpers mix linking and population checks: `findingRow` calls `l.linked` before `v.can` (`pkg/api/findings.go:174–176`), and Task 22 includes `link.go` among named-ref changes.  
   **Why:** Reusing the existing memo lets a named-get allow satisfy a later population check, exposing findings or counts without list permission. The reverse order incorrectly suppresses named access. “Unclassified calls only under-show” is therefore not a sufficient safety argument.  
   **Fix:** Require separate population/named memos, including pooled storage and dependency memos. Test both call orders for the same ref with list denied/get allowed, through a real handler.

2. **P1 — Rebuilding must repeat the session check immediately before commit.**  
   **Location:** `docs/phases/phase-7/implementation-plan.md:388`  
   **Evidence:** The specified order checks the session, then detects expiry and rebuilds. Current `pkg/api/commit.go:59` performs one session check after one handler execution.  
   **Why:** A session can end during the second build. Following the listed sequence would then write data after logout, reopening the 7.2 race. “Fresh view and buffer” also leaves request-level full-read and expiry state unspecified.  
   **Fix:** Define a bounded build/validate loop. Each attempt resets full-read, expiry and view state while preserving the 500-check counter. After **each** build, repeat session → error → expiry validation; only then write. Add logout-during-rebuild and expired-full-read-rebuild tests.

3. **P2 — Changing the backend detail’s first check does not enable named-only access.**  
   **Location:** `docs/phases/phase-7/implementation-plan.md:362`  
   **Evidence:** After `pkg/api/backends.go:134`, backend detail calls `paths.Backends` through `backendsInput`, whose `Can` remains `v.can` (`backends.go:36`). `pkg/paths/backends.go:187` rejects the backend again using that population predicate. Backend link membership has the same restriction (`pkg/api/link.go:41`, `pkg/paths/backends.go:141`).  
   **Why:** A Service or Backend granted only by name still gets a 404 or no working detail link after the planned subject check changes. Switching the shared predicate wholesale to named checks would instead enable enumeration.  
   **Fix:** Separate detail-subject admission from backend population membership. Preserve list checks for discovered consumers/routes/backends. Add named-only Service and Backend detail/link tests with `namedGetScan` off.

4. **P2 — Batched list results can disable the compatibility flag.**  
   **Location:** `docs/phases/phase-7/implementation-plan.md:415`  
   **Evidence:** Task 22 requires population checks to fall back to named get when `NamedGetScan` is enabled. Task 26 resolves only `(kind, namespace)` list booleans, then makes population `can` read that table.  
   **Why:** A prefetched list denial cannot distinguish objects whose named gets allow or deny. Treating it as the final population result defeats the advertised flag; treating it as an allow would over-show.  
   **Fix:** Specify that a negative prefetched list result is final only with scanning disabled. With scanning enabled, perform the per-ref fallback through the bounded cache/limiter. Test serial-versus-prefetched HTTP responses with the flag both off and on.

5. **P2 — Prefetching every dependency can make hidden changes determine availability.**  
   **Location:** `docs/phases/phase-7/implementation-plan.md:415`  
   **Evidence:** The batch includes buckets from the install’s rows **and dependency tables**, before rendering, and any batch error discards the response. Topology dependencies include entire route chains and backend targets (`pkg/paths/topology.go:505`).  
   **Why:** A list-denied route can introduce dependency buckets that no readable object requires. Those checks can consume the cap or fail, changing an otherwise identical visible response from 200 to 503. The proposed create/delete test does not require introducing a new dependency bucket.  
   **Fix:** Resolve root populations first, prune denied roots, then resolve dependencies required by admitted objects. Apply the later namespace scope before batching. Test hidden objects introducing new cross-namespace dependencies, including errors and cap exhaustion.

6. **P2 — Limiter admission and cancellation conflict with detached SAR contexts unless ordered explicitly.**  
   **Location:** `docs/phases/phase-7/implementation-plan.md:414`  
   **Evidence:** Waiting must end with the request context, but current `pkg/auth/sar/cache.go:150` detaches cancellation before calling the checker. Full-read evaluation also detaches its request context.  
   **Why:** Placing the limiter around the checker would make queued work outlive canceled requests. Placing it only in batching would leave named checks, serial checks or full-read constituents outside the global limits. Cancellation/error paths can also leak permits or token reservations.  
   **Fix:** Define one admission boundary for every actual SAR, distinguish queued cancellation from already-running merged work, and release capacity on every exit. Test cancellation while queued, overload recovery, checker errors/panics, merged-flight cancellation, and mixed full-read/named/batch traffic.

7. **P2 — Live-entry accounting does not establish the retained-memory bound.**  
   **Location:** `docs/phases/phase-7/implementation-plan.md:398`  
   **Evidence:** Accounting is specified as fixed per-entry costs plus string lengths. Current storage uses maps whose allocated capacity survives deletion (`pkg/auth/sar/cache.go:71`, `pkg/auth/sar/fullread.go:43`). The heap comparison fills 10,000 decisions once; churn checks only the accounting counter.  
   **Why:** Eviction can lower accounted bytes without releasing map capacity. Full-read-only identities and their eviction lifecycle also need explicit treatment. The tests can pass while retained heap exceeds the claimed sub-budget.  
   **Fix:** Account backing allocation/high-water capacity or require bounded compaction/replacement. Include full-read entries in the global eviction policy. Measure retained heap after identity churn, full-read-only churn and eviction, and include representative sessions in the integrated 64 MiB measurement.

8. **P3 — Some proposed tests assert contradictory or incorrect behavior.**  
   **Location:** `docs/phases/phase-7/implementation-plan.md:378`  
   **Evidence:** `TestDetailDoesNotProbeFullRead` demands no cluster-wide list checks for route detail, but route detail computes child/declared populations (`pkg/api/details.go:519`), and Task 23 explicitly activates full read on `recount`. `TestMixedListGetExpiry` says the object disappears after allow expiry, although a successful fresh get should keep returning 200.  
   **Why:** These tests can force endpoint-specific shortcuts or artificial disappearance rather than verify D-100. `TestBatchEightWorkers` alone also does not demonstrate bounded, correct resolution.  
   **Fix:** Use YAML or another purely named operation for the no-population test. At get expiry, test both renewed allowance and revocation. Verify batch results, bounded concurrency, cancellation and discarded partial responses.

The fixed eight workers and shared 500-check rebuild cap are reasonable and match D-100. The 1,024-waiter/4,096-flight bounds with `ErrOverloaded → 503` are fail-closed choices. A hand-written limiter is acceptable only with the lifecycle guarantees above. The true/false full-read expiry rules and deferral of EndpointSlice eligibility to 7.4 are correctly stated; retain them as explicit gates before 7.5 opens secure access.

No files edited; no tests run.

## Triage
| # | Finding | Decision | Change |
|---|---|---|---|
| 1 | Named and population memos shared | accept | Task 22: separate memos, pools and dependency memos; both-order tests through a handler |
| 2 | Rebuild skips the session check | accept | Task 24: two-attempt build/validate loop, state reset per attempt, cap kept; session → error → expiry after each build; logout and full-read rebuild tests |
| 3 | Backend detail still needs population membership | accept | Task 22: `BackendsInput.Subject` admitted by name; links split; named-only Service and Backend tests |
| 4 | Prefetched denial defeats the scan flag | accept | Task 26: denial final only with scanning off; per-ref fallback otherwise; equality tests with the flag off and on |
| 5 | Dependency prefetch lets hidden objects decide availability | accept | Task 26: roots first, then dependencies of admitted roots; 7.4 scope before batching; hidden-root bucket tests with error and cap |
| 6 | Limiter admission vs detached SAR contexts | accept | Task 26: one admission point in the flight leader; queued vs running cancellation; release on every exit; lifecycle tests |
| 7 | Accounting misses map capacity | accept | Task 25: high-water accounting with compaction; full-read entries in the global LRU; retained-heap churn tests; sessions in the 64 MiB run |
| 8 | Contradictory tests | accept | Task 23: YAML for the no-probe test; renewed and revoked get at expiry. Task 26: `TestBatchResolve` checks results, bounds, cancellation, discard |