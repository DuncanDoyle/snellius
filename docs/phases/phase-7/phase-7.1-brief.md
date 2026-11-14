# Sub-phase 7.1 — Modes and groundwork

Kind: planned
Base: cecadca06a5688962f6c8429c7ec9efe6454919f
Issues: none | Plan steps: implementation-plan.md, Sub-phase 7.1, Tasks 1–8

## Problem / goal
Today Snellius binds `:8080` in-cluster and serves everything its ServiceAccount reads to whoever reaches the Service (D-013). Phase 7 starts by making the default safe without losing the five-minute demo (D-086): local mode serves only on loopback inside the pod, reachable through `kubectl port-forward`, with a banner; secure mode exists but serves no data until 7.5.

## Expected behaviour

| Input / action | Expected |
|---|---|
| Default install, `kubectl port-forward svc/ui-snellius 18182:80`, open the UI | Works as today, with a local-mode banner on every page |
| Same, `port-forward pod/…` | Works |
| From another pod: pod IP :8080, Service ClusterIP :80 | Connection refused |
| From another pod: pod IP :8081 `/healthz`, `/readyz`, `/metrics` | Answer; `/api/` is 404 there |
| Through port-forward with `Host: evil.example` | `421` |
| Request with a cross-origin `Origin` | `403` |
| `--auth-mode=local --listen=:8080` (or any non-loopback host) | Startup error |
| Local mode with `POD_IP == HOST_IP` | Startup error |
| `helm template` with `httpRoute.enabled=true` and default mode | Fails with a message naming `auth.mode: secure` |
| `--auth-mode=secure`, any `/api/` route including extension mounts | `503 secure-mode-incomplete` |
| `/readyz` with a rejected install | `503`, generic body; the IDs only in the log |
| Startup in either mode | One audit record `{"log":"snellius.audit","action":"startup","mode":…}` on stdout |

## Root cause (if known)
n/a (planned work).

## Acceptance tests
The tests named in implementation-plan.md Tasks 1–7, at the HTTP handler, rendered chart, browser and e2e boundaries.

## Scope
In: `pkg/app`, `pkg/cli`, `pkg/api` (guard, probe handler, gate, meta), new `pkg/obs`, new `pkg/audit`, `deploy/helm`, `web/packages/shell` (banner, types), `test/e2e`, README and chart README.
Out: sign-in, sessions, SAR, visibility changes, two signals (7.2–7.5). No change to what local mode shows.

## Plan / design impact
none beyond the reviewed plan.

## Definition of done
- [ ] acceptance tests fail before, pass after (report both)
- [ ] full test suite passes (go, lint, web, helm; e2e on kind)
- [ ] phase-7.1-complete.md written; phase-status.md updated
