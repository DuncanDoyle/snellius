Cross-model: YES (author: Claude Opus 5.5, reviewer: Codex, command: codex exec --sandbox read-only)
Scope: design (Phase 7, before the implementation plan)
Diff: n/a (design review of the uncommitted draft at bd5e106)
Timing: on time

I would request changes before implementation. The main blockers are remaining D-085 leaks and incomplete authentication/session contracts. These are design findings, not claims about vulnerabilities in an implemented Phase 7.

**MAJOR product-owner decision:** the proposed removal of claim-mapping fallback from `v0.8.0` changes the roadmap’s supported-cluster scope. Keeping GatewayClass access as a prerequisite for otherwise-readable resources would also require a product decision. The other findings can be settled by the author within the accepted decisions.

1. **P1 — §7.6: check state and install-wide unknown still leak hidden resources.**  
   **Evidence:** The design retains check state and `requiredNotOK`, calling them Snellius’s own state. However, [checks.go:549](pkg/health/checks.go:549) changes `OK`/`Pending` when resources enter grace; [finding.go:139](pkg/health/finding.go:139) exposes hold counts and deadlines; [explore.go:481](pkg/explore/explore.go:481) turns a failed required check into install-wide unknown.  
   **Why:** Creating a hidden pending route can change a visible route’s state and check output without changing any readable object. Removing detail strings does not fix this.  
   **Fix:** Separate collection/access failures from object-derived check results. Project object-derived state, counts, holds and deadlines through complete visibility dependencies. **MAJOR: No.**

2. **P1 — §7.3: named evidence is not a complete authorization dependency set.**  
   **Evidence:** [finding.go:95](pkg/health/finding.go:95) defines `Evidence` as objects whose data the detail/facts quote. It does not promise all inputs deciding existence or severity. For example, [freshness.go:183](pkg/health/freshness.go:183) derives a finding on the controller Deployment from the number of stuck objects, but [freshness.go:207](pkg/health/freshness.go:207) supplies no dependencies for those objects.  
   **Why:** A viewer allowed to read the Deployment can learn hidden route changes even after the proposed evidence check. Aggregate thresholds also require dependencies on the population counted, not just displayed examples.  
   **Fix:** Introduce a distinct authorization provenance contract covering positive inputs, absence checks and aggregate populations. Audit every analyzer and derived projection against it; deny unknown provenance in secure mode. **MAJOR: No.**

3. **P1 — §§7.3, 8.1: incompleteness can become an existence oracle.**  
   **Evidence:** §7.3 says the path becomes incomplete “where such a finding would have applied”; §8.1 detects unreadability from path rows. Existing path rows are filtered by their actual dependencies at [query.go:171](pkg/paths/query.go:171).  
   **Why:** If incompleteness is discovered only through an existing hidden finding or path row, adding/removing that hidden object changes the visible signal. D-087 explicitly requires the same note whether the hidden object exists or has findings.  
   **Fix:** Determine required evidence and permission gaps from readable references and required collection permissions before examining hidden findings or topology. Specify absent, unreadable, unattached and zero-row cases. **MAJOR: No.**

4. **P1 — §3.1: loopback binding alone does not protect local mode from browser DNS rebinding.**  
   **Evidence:** The proposed boundary checks bind addresses only. [app.go:364](pkg/app/app.go:364) creates a TCP listener, and [handlers.go:57](pkg/api/handlers.go:57) installs routes without a Host allowlist.  
   **Why:** A hostile website can use a hostname that subsequently resolves to loopback and read an unauthenticated local/port-forwarded API under that hostname. TCP loopback enforcement does not establish a browser-origin boundary.  
   **Fix:** Require an explicit local Host allowlist and reject unexpected origins where applicable, before serving API data. Test rebinding-style Host values alongside direct pod-access refusal. **MAJOR: No.**

5. **P2 — §§4.1–4.2: TokenReview does not replace the OIDC client-validation contract.**  
   **Evidence:** The callback explicitly checks state and nonce, then relies on TokenReview for issuer/audience validation. TokenReview validates acceptance by Kubernetes; omitted audiences default to the API server’s audience. It does not establish that the token belongs to this configured OIDC client. [Kubernetes TokenReview reference](https://kubernetes.io/docs/reference/kubernetes-api/definitions/token-review-v1-authentication/).  
   **Why:** The design leaves discovery issuer matching, expected token issuer/client audience, claim validation, transaction integrity and replay handling unspecified. These remain client responsibilities under [OIDC Core](https://openid.net/specs/openid-connect-core-1_0.html#IDTokenValidation).  
   **Fix:** Define the complete validation sequence, PKCE S256 verifier storage, one-use browser-bound transaction, protected cookie attributes, HTTPS requirements and same-origin return-path validation. Retain TokenReview as the Kubernetes identity authority. This need not introduce local signature verification in the default mode. **MAJOR: No.**

6. **P2 — §§4.4, 5: session validity between refreshes is underspecified.**  
   **Evidence:** Identity refresh runs every 15 minutes, while expiry at ID-token expiration is specified only for sessions without refresh tokens.  
   **Why:** A five-minute ID token can expire before the next scheduled refresh. The design also does not require refreshed tokens to retain the original issuer/subject or define concurrent refresh versus logout.  
   **Fix:** State an explicit session-validity policy; schedule refresh before token expiry, fail closed when fresh identity is required, bind refresh to the original subject, serialize refresh and prevent logout from being undone by an in-flight refresh. Document that refresh tokens do not themselves guarantee fresh group claims. **MAJOR: No.**

7. **P2 — §6.3: `ErrAuthorizer` needs a response-commit protocol.**  
   **Evidence:** [visible.go:68](pkg/api/visible.go:68) returns only a boolean. A denial immediately produces a 404 in [backends.go:134](pkg/api/backends.go:134); other handlers serialize directly.  
   **Why:** Merely recording an error in `view` cannot guarantee the promised 503 before a handler commits a 404 or partial response. The design also leaves SAR response-level errors unspecified. Kubernetes can return `evaluationError` in a successful HTTP response. [SAR reference](https://kubernetes.io/docs/reference/kubernetes-api/authorization-resources/subject-access-review-v1/).  
   **Fix:** Define centralized pre-commit error handling or explicit error propagation across all paths. Specify behavior for `allowed`, `denied`, no opinion and `evaluationError`; an unavailable list check must not become an ordinary denial followed by `get`. **MAJOR: No.**

8. **P2 — §7.4: GatewayClass admission adds a permission prerequisite beyond object RBAC.**  
   **Evidence:** The design hides an install unless its GatewayClass is readable. APIs are install-scoped, including [handlers.go:62](pkg/api/handlers.go:62).  
   **Why:** A user may legitimately read their HTTPRoutes without permission to read GatewayClasses. The proposed example ClusterRole changes their required permissions instead of supporting their existing rights.  
   **Fix:** Allow readable resources to remain reachable through a neutral install context, exposing install metadata only when authorized. **MAJOR: Yes if retaining this prerequisite; otherwise an author fix to honor D-085.**

9. **P2 — §§3.1, 12: forbidding the API Service port breaks required Service port-forwarding.**  
   **Evidence:** D-086 requires pod and Service port-forwarding. The existing setup uses `port-forward svc/ui-snellius …:80` at [setup.sh:131](test/e2e/setup.sh:131), mapped by [service.yaml:10](deploy/helm/snellius/templates/service.yaml:10).  
   **Why:** Service port-forwarding needs the Service port even when ordinary Service traffic cannot reach the loopback listener.  
   **Fix:** Retain a ClusterIP Service port for forwarding while enforcing loopback binding; reject external exposure configurations. Test both forwarding forms. **MAJOR: No.**

10. **P2 — §8.3: “today’s value” is incompatible with worst-path aggregation.**  
    **Evidence:** A backend error currently lifts an otherwise healthy route only to warning: [rows.go:551](pkg/explore/rows.go:551), [rows.go:142](pkg/explore/rows.go:142). Path projection retains error severity at [project.go:100](pkg/paths/project.go:100).  
    **Why:** Making legacy `health` the worst of configuration and path can change an unrestricted viewer’s route from warning to error. Existing `reason`, flags, sorting and `state=` behavior are also not fully specified.  
    **Fix:** Provide an explicit compatibility matrix. Either preserve legacy aggregation or document and approve the semantic change; keep legacy reason/filter behavior coherent with the chosen summary. **MAJOR: No unless changing accepted unrestricted behavior.**

11. **P2 — §§4.3, 15, 17: fallback deferral changes the agreed release scope.**  
    **Evidence:** Roadmap Phase 7 includes copied claim mapping and a startup check. The design substitutes a manual comparison and potentially excludes fallback from `v0.8.0`.  
    **Why:** This determines which production clusters can use secure mode. It cannot be settled solely as dependency sequencing.  
    **Fix:** Obtain an explicit scope decision covering supported clusters, dependency approval and what “checked” means. Update the roadmap and decisions consistently. **MAJOR: Yes.**

12. **P2 — §15: secure exposure precedes complete enforcement.**  
    **Evidence:** 7.1 introduces secure-mode chart exposure, 7.2 login, 7.3 SAR and 7.4 visibility; two-signal handling arrives in 7.5. Today, nil authorization becomes `AllowAll` at [handlers.go:43](pkg/api/handlers.go:43).  
    **Why:** Intermediate sub-phases could expose all data or authenticated but insufficiently filtered data. The order lacks an explicit fail-closed invariant.  
    **Fix:** From 7.1, secure mode must deny all data unless every required security component is installed. Keep incomplete secure builds unavailable through 7.5, including extension routes; explicitly prevent nil/AllowAll fallback. **MAJOR: No.**

13. **P3 — §13: differential tests need stronger oracles and adversarial cases.**  
    **Evidence:** Responses include publication timestamps, e.g. [paths.go:122](pkg/api/paths.go:122); checks include collection times and holds. The design proposes byte equality without defining clock/publication control. Audit has no explicit test contract.  
    **Why:** Naive equality either fails routinely or encourages stripping fields that themselves leak hidden changes. Happy-path sign-in also misses the authentication failures above.  
    **Fix:** Control clocks and define permitted temporal changes precisely. Include hidden-object create/delete, aggregate threshold crossings, permission revocation/TTL expiry, cross-user memo reuse, mid-request SAR failure, invalid OIDC tokens, callback replay, logout/refresh races, and audit redaction/counting/drop behavior. **MAJOR: No.**

14. **P4 — §§6.2–6.4: cache bounds and SAR budget do not cover restricted large installations.**  
    **Evidence:** Only per-identity cache bounds are specified. Route queries authorize rows before paging at [lists.go:74](pkg/api/lists.go:74).  
    **Why:** With namespace `list` denied, named `get` checks can scale with thousands of candidates every 30 seconds, exceeding the stated 50 SAR/minute budget. Identity count, concurrent misses and session storage remain globally unbounded.  
    **Fix:** Benchmark denied-list and `resourceNames` workloads; distinguish typical targets from hard limits. Define global cache/session bounds, cleanup, request budgets and duplicate-check suppression. **MAJOR: No.**

The proposed **cluster-scoped `create` permissions on TokenReviews and SubjectAccessReviews are appropriate**; this approach does not need user impersonation permissions. A test-only issuer using standard-library crypto is feasible without a new dependency. Its plan should specify HTTPS trust and a hostname reachable by both the browser and kind API server, and reuse the existing Go-based chart tests.

No files were edited or tests executed; this was a read-only design and source review.
## Triage
| # | Finding | Source | Decision | Note |
|---|---|---|---|---|
| 1 | Check state and install-wide unknown leak hidden objects | reviewer | accept | §7.6: collection failures vs object-derived results; holds carry a basis |
| 2 | Evidence is not a complete dependency set | reviewer | accept | §7.3: a basis per finding (inputs, absence checks, populations); unknown basis is unreadable |
| 3 | Incompleteness as an existence oracle | reviewer | accept | §8.1: decided from readable refs and permissions only; case matrix |
| 4 | DNS rebinding against local mode | reviewer | accept | §3.1: Host allowlist, `421`, no cross-origin |
| 5 | TokenReview does not replace OIDC client validation | reviewer | accept | §4.1: full validation order, one-use transaction, TokenReview `audiences` |
| 6 | Session validity between refreshes | reviewer | accept | §4.4: freshness policy, same `iss`/`sub`, logout wins |
| 7 | `ErrAuthorizer` needs a response-commit protocol | reviewer | accept | §6.3: buffered response, SAR outcome table, no get after a failed list |
| 8 | GatewayClass prerequisite beyond object RBAC | reviewer | accept | §7.4: install reachable through any readable object; no new prerequisite |
| 9 | Service port needed for `port-forward svc/` | reviewer | accept | §3.1: Service keeps its port; refusals cover external exposure only |
| 10 | Legacy `health` changes unrestricted values | reviewer | accept | §8.3: today's lift kept; compatibility matrix |
| 11 | Claim-mapping fallback deferral changes release scope | reviewer | defer: user decision | Queued for the user with the JWT dependency approval; 7.7 waits |
| 12 | Secure exposure before complete enforcement | reviewer | accept | §3.2: `503 secure-mode-incomplete` gate until 7.4; no nil→AllowAll in secure mode |
| 13 | Test oracles and adversarial cases | reviewer | accept | §13: fixed clock and named masks; adversarial list |
| 14 | Cache and SAR bounds for restricted large installs | reviewer | accept | §6.4: full-read fast path, hard limits, global bounds, denied-list bench |
