Cross-model: YES (author: Claude Opus 5.5, reviewer: Codex, command: codex exec --sandbox read-only)
Scope: plan (Sub-phase 7.2, Tasks 9-15)
Diff: n/a (plan review at b9c7422)
Timing: on time

Reviewed uncommitted Tasks 9–15 against the specified design sections and HEAD `b9c7422`. No files changed; static review only.

1. **P1 — Task 11: logout fails behind TLS termination.**  
   **Evidence:** [Plan:172](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:172) says to reuse the 7.1 Origin check. `pkg/api/guard.go:36–50` derives the scheme from `r.TLS`; the application serves HTTP behind the Gateway.  
   **Why:** A browser’s legitimate `Origin: https://…` fails comparison against the backend’s HTTP origin, preventing session deletion.  
   **Fix:** Compare logout Origin against the validated, configured redirect URL’s origin. Require Origin explicitly; test HTTPS externally with HTTP upstream.

2. **P2 — Tasks 10–11: contradictory token exposure contract.**  
   **Evidence:** [Plan:159](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:159) prohibits sending ID tokens to the browser; Task 11 returns a logout URL containing `id_token_hint`, which Task 13 navigates to. Design §5 specifies server-held tokens.  
   **Why:** Following the logout instructions exposes the token in browser-visible JSON and a URL.  
   **Fix:** Specify RP logout without a token hint where supported, including required client identification and redirect registration. Otherwise explicitly resolve and document the exception before implementation.

3. **P2 — Tasks 9, 11, 13: sign-in state transport is incomplete.**  
   **Evidence:** [Plan:146](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:146) exposes configuration status only through readiness/logging; Task 13 requires configuration and session-ended pages. Task 11’s 401 contains only `sign-in-required`. HEAD gates `/api/v1/meta`.  
   **Why:** The SPA cannot distinguish missing configuration from an ordinary unauthenticated request or session expiry. Component-only page tests would miss this.  
   **Fix:** Define a public, sanitized auth-status contract and explicit login/session failure codes, then test actual backend responses driving each page.

4. **P2 — Task 14: issuer connectivity and trust are incomplete.**  
   **Evidence:** [Plan:206–207](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:206) installs the generated CA only in the API server and names browser/API-server connectivity.  
   **Why:** Snellius also needs HTTPS discovery/token access from its pod, with CA trust. A published port and matching SAN alone provide neither name resolution nor certificate trust across all participants.  
   **Fix:** Specify the hostname, mappings and routing for browser, API server **and Snellius**, plus CA distribution to each TLS client. State any added chart/flag install surface explicitly.

5. **P2 — Task 14: promised CI coverage has no integration step.**  
   **Evidence:** [Plan:209](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:209) promises CI runs secure kind tests, but omits workflow changes. HEAD `.github/workflows/e2e.yaml` never sets `SECURE=1`.  
   **Why:** Existing jobs install local mode; new sign-in tests either fail there or are skipped without ever running securely.  
   **Fix:** Add a dedicated secure CI row/job, explicit test selection, and the workflow to Task 14’s files.

6. **P2 — Task 11: active-session metric is omitted.**  
   **Evidence:** [Task 11](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:163) lists session lifecycle operations but no metric wiring. Design §3.3 requires sessions active; HEAD `pkg/obs` supplies only registry primitives.  
   **Why:** No later task assigns this requirement.  
   **Fix:** Register and update a session gauge across creation, logout, expiry and eviction; verify its exposed value.

7. **P3 — Task 10: fake Reviewer bypasses the production TokenReview adapter.**  
   **Evidence:** [Plan:161](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:161) tests through a fake `Reviewer`; design §13 requires a fake TokenReview server.  
   **Why:** These tests cannot catch incorrect `spec.audiences` or production response handling.  
   **Fix:** Exercise the real adapter against a fake API server, checking request audiences, authenticated false, missing/wrong response audiences and transport failures.

8. **P3 — Task 13: hidden-tab retry coverage is missing.**  
   **Evidence:** [Plan:195](/Users/ddoyle/Development/claude/snellius-phase7/docs/phases/phase-7/implementation-plan.md:195) pauses polling; HEAD `retryEveryPoll` independently schedules `setTimeout` retries.  
   **Why:** Retries after errors can keep hidden sessions active despite pausing regular polling.  
   **Fix:** Test hiding the tab after an error; gate scheduled retries and visibility-triggered revalidation appropriately.

**MAJOR product decision:** The existing claim-mapping fallback/dependency and release-scope decision remains with the owner. These findings require no additional major product decision if the server-held-token promise is retained.
## Triage
| # | Finding | Source | Decision | Note |
|---|---|---|---|---|
| 1 | Logout Origin fails behind TLS termination | reviewer | accept | Task 11: compare with the configured redirect URL's origin; Origin required |
| 2 | `id_token_hint` exposes the token to the browser | reviewer | accept | Task 11: `client_id` + `post_logout_redirect_uri`, no hint |
| 3 | Sign-in state transport incomplete | reviewer | accept | Task 9 `/auth/status`; Task 10 `/signin-failed?reason=`; Task 11 `session-ended`; Task 13 pages from real responses |
| 4 | Issuer connectivity and CA trust | reviewer | accept | Task 14 connectivity per client; Task 9 `--oidc-ca-file`; Task 12 `auth.oidc.ca`, `hostAliases` |
| 5 | No CI step for secure e2e | reviewer | accept | Task 14: `kind-secure` job |
| 6 | Sessions gauge missing | reviewer | accept | Task 11 |
| 7 | Fake Reviewer skips the production adapter | reviewer | accept | Task 10: adapter against a fake API server |
| 8 | Hidden-tab retries | reviewer | accept | Task 13 |
