# Phase 7.1 — Modes and groundwork (Complete)

**Completed:** 2026-11-14 (local branch `duncandoyle/phase7Design`, not yet pushed)
**Range:** 1900727..99d92f9

## Summary
Snellius now has two modes. Local mode, the default, serves the UI and API on 127.0.0.1 inside the pod, so only `kubectl port-forward` reaches it. It refuses foreign `Host` headers (421) and cross-origin requests (403), refuses non-loopback `--listen` and host networking, warns at startup, and shows a banner on every page from the first paint. Secure mode answers every `/api/` request with `503 secure-mode-incomplete` until sub-phase 7.5. A probe listener on :8081 serves `/healthz`, `/readyz` (no install IDs) and a dependency-free `/metrics`. An audit stream writes JSON lines on stdout; in 7.1 only the startup record. The chart renders both modes, refuses `httpRoute.enabled` in local mode, and upgrades with `--reuse-values` from 0.7.1.

## Sub-phases (phase record only)
n/a

## Reviews
- Claude task reviews per task (8 tasks): fix rounds on Tasks 3, 5, 6, 7 (twice) and 8 (twice); all closed.
- Codex: `reviews/7.1-codex.md`: 7 findings (4 P2, 1 P3, 2 P4), all accepted and fixed in one wave; re-review: all addressed, no new P1/P2.

## Waivers
None.

## Deliverables
- Commits `d3bce58` … `99d92f9` (19).
- Flags: `--auth-mode`, `--allowed-hosts`, `--probe-listen`, `--audit-levels`. Chart values: `auth.mode`, `auth.allowedHosts`, `audit.levels`; container port `probe` 8081; env `POD_IP`, `HOST_IP`.
- API, additive: `/meta` `authMode`; `index.html` carries `data-auth-mode`; `X-Request-Id` on the UI and API listener.
- Gates on `99d92f9`: `go test ./...` all pass; `make lint` 0 issues; chart tests pass; web 1004/1004, lint, typecheck (at `a7f9904`; the fix wave touched no web code).
- Cluster checks on `kgw-25-snellius-demo` (7.1 build, local mode): `helm upgrade --reuse-values` from 0.7.1 works; Service and pod port-forward 200; Host rebinding 421; cross-origin 403; pod :8080 and Service :80 refused from another pod; probe `/readyz` 200, `/api` 404; audit startup record in the pod log; banner in both themes with no layout shift (`<main>` at 89 px before and after `/meta`).
- e2e: three new local-mode tests pass against that cluster with `-race`. The kind-based `make e2e` did not run locally (colima resources); CI runs it on the PR.

## Notes for later phases
- 7.2 builds sign-in on the request-ID middleware and the audit stream; the audit writer now outlives draining requests and counts late records as dropped.
- The secure gate (`api.WithSecureMode(false)`) opens in 7.5.

## Deferred / open
Minor review items, none blocking (from the ledger):
- `--allowed-hosts` entries with a port or `[v6]` are not normalised, so they never match.
- `CounterVec.Add` accepts negative values; the series key separator `\xff` could collide in theory.
- `audit.WithBuffer(n<=0)` gives an unbuffered channel; the audit `route` field includes the method.
- The chart does not validate `auth.mode` (the binary refuses unknown modes); `extraEnv` can duplicate `POD_IP`/`HOST_IP`.
- The `index.html` stamp matches the literal `<html ` only; the Vite dev server and a CDN-served `index.html` keep the banner shift.
- e2e: listener close-then-reuse port race in `forward()`; curl exit 7 also covers "no route to host".
