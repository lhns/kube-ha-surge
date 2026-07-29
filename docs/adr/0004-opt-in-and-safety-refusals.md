# 0004 — Opt-in, safety refusals, and fail-open

Status: Accepted

Date: 2026-07-29

## Context

ha-surge intercepts a cluster-critical path: `pods/eviction`, the API that
node drains depend on. A component sitting there carries real blast-radius risk.
It must never:

- act on workloads that didn't ask for it;
- surge a workload it *can't* safely surge (and thereby stall a drain);
- become a single point of failure that wedges evictions cluster-wide if it
  breaks.

So we need an explicit opt-in model, a set of hard refusals where surging is
impossible or wrong, and a fail-open posture.

## Decision

### Opt-in (two independent gates)

1. **Namespace label `ha-surge.lhns.de/managed=true`** — the webhook's
   `namespaceSelector`. Evictions in unlabelled namespaces never even reach the
   operator. This is the blast-radius limit: enrolling is a deliberate,
   per-namespace act.
2. **Deployment annotation `ha-surge.lhns.de/enabled=true`** — per-workload
   opt-in. Within a managed namespace, only annotated Deployments are surged;
   everything else is passed straight through.

Both are required. Namespace-scoped enrolment keeps the webhook's reach small;
the annotation gives workload owners precise control.

### Safety refusals (pass the eviction straight through)

The webhook refuses to surge — and immediately allows the eviction — when:

- the pod mounts a **`ReadWriteOnce`/single-writer PVC** (a stand-in can't
  co-mount it; Multi-Attach). Surging would only produce a stuck
  `ContainerCreating` pod and waste the whole `--max-surge-seconds` window.
- the pod's owner is **not a Deployment** (StatefulSet, bare pod, Job, etc.) —
  out of scope for v1.
- the Deployment is **already multi-replica** (`replicas >= 2`) — normal
  replication + PDB already handle disruption; surging is unnecessary.

### Fail-open

- The `ValidatingWebhookConfiguration` sets **`failurePolicy: Ignore`**. If the
  operator is down, unreachable, or its cert is wrong, the apiserver ignores the
  webhook and the eviction proceeds.
- The webhook also enforces the **`--max-surge-seconds` timeout**: even when the
  operator is healthy, a stand-in that never becomes Ready stops being waited on
  and the eviction is allowed.

`failurePolicy: Ignore` is mandatory and non-negotiable: **a broken surge
feature must never be able to wedge a node drain.** Availability of the drain
path strictly outranks the surge benefit.

## Consequences

- **Small, deliberate blast radius.** Nothing happens without both an enrolled
  namespace and an annotated workload. A cluster that installs the chart but
  labels nothing is inert.
- **The drain gets slower, never lossy, never wedged.** Refusals fall back to
  the normal (gappy) drain rather than to a stall; fail-open falls back to the
  normal drain rather than to a block.
- **Refusals are silent at the eviction call site.** A user who expected a surge
  and got a plain eviction has to look at the operator's events/logs to learn
  *why* (RWO, not-a-Deployment, already-HA, or operator-down). Documented in
  [`../limitations.md`](../limitations.md).
- **You must not stack a blocking PDB** on a surged workload — the PDB 429s
  *before* the webhook runs, pre-empting the surge. Also in limitations.

## Alternatives considered

- **`failurePolicy: Fail`.** Rejected outright. It would mean a webhook outage
  blocks *all* evictions in enrolled namespaces — including the operator's own
  rescheduling and any drain in progress. For a make-before-break *optimisation*
  that's covered by a fail-open fallback, trading away drain availability is
  never worth it.
- **Opt-out instead of opt-in** (surge everything by default). Rejected: far too
  broad for a component on the eviction path; it would surge workloads that
  can't be surged and surprise every namespace owner.
- **Single gate (annotation only, no namespace label).** Rejected: the namespace
  `namespaceSelector` is what keeps the webhook from being *consulted* for
  unrelated evictions at all. Without it the webhook is in the path for every
  eviction cluster-wide, widening blast radius for no benefit.
- **Enforce refusals by rejecting the eviction** (deny instead of pass-through).
  Rejected: a workload we can't help must not have its eviction *blocked* by us —
  that's the opposite of fail-open. Refusal means "get out of the way", i.e.
  allow.
