# Phase 7 — Identity & Access (Complete)

**Completed:** 2026-11-19, merged in #50 and released as `v0.8.0`.
**Range:** 81a2c0b..HEAD of the merge

## Summary
Snellius now runs in two modes. Local mode (the default) listens only on the pod's loopback and is reached through `kubectl port-forward`. Secure mode signs users in through the cluster's OIDC provider, checks their identity with TokenReview, and shows each user only what their Kubernetes RBAC allows: every page, count, finding and map follows D-085, so a change to an object a user cannot read never changes what they see. Every route shows two signals, its configuration and its traffic path. Many users opening cold pages at once all get answers (D-116 to D-118). An audit stream records sign-ins, denials, reads and list access.

## Sub-phases (phase record only)
| Sub-phase | Kind | Closed | Description | Record |
|---|---|---|---|---|
| 7.1 | planned | 2026-11-14 | Modes, probe port, audit skeleton | `phase-7.1-complete.md` |
| 7.2 | planned | 2026-11-15 | OIDC sign-in, TokenReview, sessions | `phase-7.2-complete.md` |
| 7.3 | planned | 2026-11-16 | SAR authoriser, its cache and limits | `phase-7.3-complete.md` |
| 7.4 | planned | 2026-11-17 | Visibility everywhere, namespace selector | `phase-7.4-complete.md` |
| 7.5 | planned | 2026-11-18 | Load fix, two signals, the gate opens | `phase-7.5-complete.md` |
| 7.6 | planned | 2026-11-19 | Audit levels, docs, phase review fixes | `phase-7.6-complete.md` |
| 7.7 | — | dropped | Claim-mapping fallback (D-098, #51) | — |

## Reviews
Cross-model (Codex) throughout: the design and plan review, one review file per sub-phase or topic under `reviews/`, and `reviews/phase-codex.md` (whole phase, two stabilization rounds, final check). Every finding is triaged in its file.

## Waivers
- D-115 (7.4): the D-044 timing-regression gate is waived for PR #50 only; D-115's own target was met in 7.5.

## Decisions
D-084 to D-120.

## Deferred / open
- **(P3)** A route readable by name only shows an empty trace (7.5).
- **(P3)** Map nodes past 20 delegating parents or 200 routes per query fall back to path-row state (7.5).
- **(P3)** A stalled upload still holds its connection (7.5).
- **(P3, Phase 8)** Full-read-without-inputs test coverage (7.6).
- **(P4, Phases 8–9)** Viewer-scoped computations for extensions (phase review PH6).
- **After `v0.8.0`:** compare other platforms' per-user authorisation and the implicit namespace scope (D-118); 10-second polling, the waiting hint and admission fairness.
- **Separate PR:** web vitest flakes under load.
- #51 (managed clusters through claim mapping), #52 (delegation analysis for restricted viewers), #53 (diagnostic bundle).
