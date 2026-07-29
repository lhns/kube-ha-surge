# 0010 — The operator runs HA by default

Status: Accepted

Date: 2026-07-29

## Context

ha-surge is a validating webhook on `pods/eviction`. Its whole job is to keep a
single-replica workload available *during a node drain*. But the operator itself
is a workload subject to the same drains: when a node hosting the operator's pod
is drained, the operator may be rescheduling at the exact moment some other
workload's eviction needs it.

At `replicaCount: 1` there is a window during every drain where the operator is
unavailable. Because the webhook is fail-open ([0004](0004-opt-in-and-safety-refusals.md)),
that window degrades to *uncovered* evictions — drains proceed, but with the same
availability gap ha-surge exists to remove. And the operator cannot
make-before-break *itself* (it would have to be up to hold its own eviction). So
the component that provides HA should itself be HA.

This raises a correctness question: if a second replica or a leader failover can
take over mid-eviction, is that safe? It is only safe if the operator holds no
important state in memory.

## Decision

Default to **HA**:

- **`replicaCount: 2`** — two operator pods.
- **PodDisruptionBudget `maxUnavailable: 1`** — a drain can never take both at
  once; at least one replica always stays up to serve admission.
- **`topologySpreadConstraints`** across `kubernetes.io/hostname`, `maxSkew: 1`,
  `whenUnsatisfiable: ScheduleAnyway` (soft, so a small or heavily-drained
  cluster still schedules both).

This composes with the existing leader election ([0005](0005-go-controller-runtime.md)):

- **Both replicas serve the webhook.** Admission is stateless (see below), so any
  replica can answer any eviction — the apiserver load-balances across the Service
  endpoints, and availability does not depend on which pod is leader.
- **Only the leader runs the GC controller.** Deleting detached cover pods is a
  singleton reconcile; gating it on the lease prevents two replicas double-acting.

The 2-replica operator is itself `AlreadyHA`, so ha-surge would never surge its
own eviction — no self-referential loop.

### Why HA is safe: the operator is stateless

Every piece of in-flight state lives in the cluster (etcd), not in operator
memory:

- **The detach itself is a pod mutation.** Removing `pod-template-hash` and adding
  the `ha-surge.lhns.de/detached` / `for` labels ([0008](0008-detach-doomed-pod.md))
  is persisted on the pod object. Any replica reading that pod sees the in-progress
  detach.
- **Every webhook call recomputes from live cluster state.** The handler reads the
  pod, its Deployment/ReplicaSet, and the namespace through a **direct, uncached
  reader** on each call — it never relies on a decision cached from a previous
  call or a previous replica.
- **The hold timeout is derived from a persisted timestamp.** The
  `--max-surge-seconds` budget is measured against the `ha-surge.lhns.de/detached-at`
  annotation written on the pod, not a timer in memory. A replica that has never
  seen this eviction before computes the same deadline.
- **The GC controller rebuilds from a watch.** On leader acquisition it lists/
  watches detached pods and resumes cleanup; nothing is lost on failover.

So any replica can serve any request, and a **leader failover mid-eviction loses
nothing** — the retrying `kubectl drain` (5s cadence, HTTP 429) simply hits
whichever replica is up, which recomputes the same answer from etcd. The only
fail-open edge is a crash *between* persisting the detach and returning the 429;
that degrades to a single uncovered, self-healing eviction (the detached pod is
GC'd once the ReplicaSet's replacement is Ready) — never a wedge, never a lost
pod.

## Consequences

- **The webhook stays available during the drains it's meant to cover**, including
  a drain of a node hosting one of its own replicas — the other replica answers.
- **Higher baseline cost:** two operator pods instead of one. The operator is
  small (50m/64Mi requested), so this is cheap relative to the availability it
  buys, and can be turned down to `replicaCount: 1` for dev/single-node clusters.
- **Correctness rests on statelessness**, which is now an explicit invariant: no
  handler may cache a cross-call decision in memory, and all lifecycle state must
  live on the pod object. Future changes must preserve this.
- **A hard cross-node guarantee is opt-in.** The spread is soft by default so tiny
  clusters still schedule; set `whenUnsatisfiable: DoNotSchedule` for a strict
  one-replica-per-node guarantee.

## Alternatives considered

- **`replicaCount: 1` (single replica).** Rejected: leaves an uncovered window on
  every drain of the operator's own node — the exact failure ha-surge exists to
  fix, applied to itself.
- **Active/standby (only the leader serves the webhook).** Rejected: a failover
  would briefly have *no* endpoint serving admission, reintroducing the gap.
  Serving from both replicas keeps admission continuously available; leader
  election is still used, but only to keep the GC controller a singleton.
- **Leader-only GC via in-memory queue shared between replicas.** Rejected as
  unnecessary state: rebuilding GC from a watch on leader-acquire is simpler and
  inherently crash-safe.
- **A hard (`DoNotSchedule`) spread by default.** Rejected as the default: it can
  leave the second replica `Pending` on a 2-node or heavily-drained cluster,
  hurting the very availability it's meant to protect. Left as an opt-in.
