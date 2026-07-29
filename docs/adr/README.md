# Architecture Decision Records

Each ADR captures a single decision: the context, what was decided, and the
consequences. ADRs are immutable once accepted — if a decision is reversed, a
new ADR supersedes the old one.

## Index

1. [0001 — Eviction admission webhook as the trigger](0001-eviction-admission-webhook-trigger.md)
2. [0002 — Surge via a cloned Pod owned by the target Deployment](0002-surge-via-cloned-owned-pod.md)
3. [0003 — Gate on real pod readiness, no fixed delays](0003-gate-on-readiness-no-fixed-delays.md)
4. [0004 — Opt-in, safety refusals, and fail-open](0004-opt-in-and-safety-refusals.md)
5. [0005 — Go + controller-runtime](0005-go-controller-runtime.md)
6. [0006 — Webhook serving cert: cert-manager or Helm-generated](0006-webhook-serving-cert-sources.md)
7. [0007 — Packaging: OCI image + Helm chart to GHCR](0007-packaging-oci-chart-and-image.md)
8. [0008 — Detach the doomed pod from its ReplicaSet](0008-detach-doomed-pod.md)

## Format

Each ADR follows MADR-lite:

```
# NNNN — Short title
Status: Accepted | Superseded by NNNN | Deprecated
Date: YYYY-MM-DD

## Context
Why we needed to decide.

## Decision
What we decided.

## Consequences
What follows — both the wins and the costs.

## Alternatives considered
What we rejected, and why.
```
