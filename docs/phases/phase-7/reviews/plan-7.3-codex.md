Found **2 P1, 2 P2, 1 P3 and 1 P4** issues. These are plan findings; the reviewed diff changes documentation only.

1. **P1 — Full-read evaluation can return an expired allow.**  
   [implementation-plan.md:280](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:280)

   **Evidence:** Full-read uses cached constituent checks, then caches the aggregate until the earliest constituent expiry. Only individual SAR completion is checked for expiry (line 263); aggregate completion is not.

   **Why:** A cached allow expiring at t=30 can be read at t=29, followed by another check completing at t=31. Every kind returned allowed, but the resulting full-read decision is already expired. It can still take the unbuffered allow-all path, violating D-099.

   **Fix:** Carry each constituent’s actual expiry through evaluation and validate the minimum expiry immediately before returning the aggregate, including merged evaluations. An expired aggregate must be re-evaluated or return an error. Test this with an almost-expired cache hit followed by a delayed check.

2. **P1 — Unbuffered full-read responses bypass the promised error handling.**  
   [implementation-plan.md:294](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:294)

   **Evidence:** Full-read success runs the handler unbuffered. Task 20 gives extensions the original authoriser, while Task 17’s `Authorize` can call `ReportAuthzError`. The plan explicitly supports an extension writing before reporting an authorisation error (line 300).

   **Why:** Full-read success does not prevent an extension from making another check through `Deps.Authorizer`. If it writes first and that check fails, the wrapper cannot replace the committed response with `503 authz-unavailable`. A recorded error can therefore coexist with a successful or partial response.

   **Fix:** Keep response commitment conditional on the absence of errors on the full-read path too. Alternatively, specify and enforce a request-scoped authoriser that resolves all supported checks from the validated full-read decision. Add the write-then-error extension test with `FullRead` returning true.

3. **P2 — Removing the browser settle logic reintroduces the notice-eviction race.**  
   [implementation-plan.md:245](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:245)

   **Evidence:** Line 243 permits notice eviction, after which the same cookie gets `sign-in-required`. Line 245 claims sticky `ended` state covers eviction mid-burst, while line 249 only tests an ended response winning before navigation occurs.

   **Why:** Request A can obtain `session-ended` but have its response delayed. The notice is evicted; request B obtains `sign-in-required` and arrives first. Immediate navigation starts sign-in before A can establish sticky `ended`. Persistent notices reduce this race but do not eliminate it.

   **Fix:** Define client behaviour for a previously authenticated browser losing its session: require explicit sign-in even when the response says `sign-in-required`. Add a deterministic reversed-response test across notice eviction. Do not remove the existing coordination based on the claimed server consistency alone.

4. **P2 — The web task omits the components that currently classify 503 responses.**  
   [implementation-plan.md:290](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:290)

   **Evidence:** The listed files are the API client, `PageState`, and copy. Existing consumers classify all 503s as connecting: `explore/ListFrame.tsx:131`, `states/PageNotice.tsx:38`, `shell/RootRedirect.tsx:15`, and several detail pages. `PageState` receives a caller-supplied title; it does not classify API errors.

   **Why:** Changing the client and presentation component alone leaves these callers displaying the connecting state or raw `authz-unavailable`, rather than design §11’s permission-check state.

   **Fix:** Specify a shared error classification/rendering helper and migrate all relevant callers. Test a rendered list and detail page, including an outage after a successful poll and recovery on a later poll.

5. **P3 — The session acceptance test avoids requests already admitted when the session ends.**  
   [implementation-plan.md:248](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:248)

   **Evidence:** The test expires the session *before* launching concurrent requests. The brief promises every request already in flight when a session ends answers `401 session-ended`. Current middleware checks once, then passes a copied identity downstream (`pkg/auth/session/middleware.go:46–50`).

   **Why:** Persistent notices cannot affect a request that already passed middleware. A request blocked in a SAR or handler can continue after refresh failure or expiry, while the proposed test still passes.

   **Fix:** Add a barrier test that admits requests, ends the session, then releases downstream work. Either implement the promised cancellation/commit-time check or explicitly narrow the brief to requests whose session lookup happens after termination.

6. **P4 — The authoriser interface cannot express 7.4’s list-only permission checks.**  
   [implementation-plan.md:262](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:262)

   **Evidence:** The public check accepts an object ref and performs list-then-get; an empty namespace for a namespaced kind is denied. Design §7.6 requires namespace-level `list EndpointSlices`, and §8.1 requires list checks for kinds needed to assess a path.

   **Why:** Readability of individual objects does not establish permission to inspect a population. Named `get` grants must not satisfy those completeness checks.

   **Fix:** Define an additive request-scoped list-permission interface, sharing identity keys, TTLs, check limits and error recording. Test that named-get grants permit object reads but leave population-dependent assessments incomplete.

No files were edited. Tests were not run. The proposed external `sar_test`/`api_test` package split avoids an obvious import cycle.

## Triage
| # | Finding | Decision | Change |
|---|---|---|---|
| 1 | Full read can return an expired allow | accept | Task 18: constituents carry expiry; aggregate checked against the clock before return, waiters too; one re-evaluation, then `ErrExpired` (503); near-expiry test |
| 2 | Unbuffered full read bypasses error handling | accept | Task 19: every secure response buffered, full readers too; write-then-error test with `FullRead` true |
| 3 | Removing settle logic reopens the eviction race | accept | Task 16: coordination kept; a tab that had a session never redirects by itself; reversed-answer eviction test |
| 4 | 503 classifiers not migrated | accept | Task 19: `ApiError.code`, shared `apiErrorState`; nine callers migrated; list and detail page tests with recovery |
| 5 | In-flight requests escape session end | accept | Task 19: commit check `StillSignedIn` answers `session-ended`; barrier test |
| 6 | No list-only permission check | accept | Task 17: `ListAuthorizer.CanList`, shared cache and cap; named-get test |
## Re-review (Codex, scoped, 9e38468..7d03c03)

Findings 1–4 and 6 addressed. Finding 5 partial: a full-read error returned 503 before the session commit check. New P2: logout cleared the "had a session" flag, so a late `sign-in-required` could restart sign-in.

| Item | Decision | Change |
|---|---|---|
| 5 (partial) | accept | Wrapper: a full-read error is recorded and the commit check runs first, so an ended session answers 401 before any 503; barrier test with a blocked full-read SAR |
| New P2 logout | accept | "Sign out" sets a signed-out state that suppresses automatic redirects until "Sign in again"; deterministic test of a delayed `sign-in-required` after logout |
