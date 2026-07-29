# Limitations

kube-ha-surge is deliberately narrow. It refuses to act where it can't help
safely, and it makes one specific window (the drain gap) smaller — it is not a
general availability solution. Know these boundaries before you rely on it.

## ReadWriteOnce / single-writer volumes can't be surged

A surge pod is a *second* running copy of the workload, briefly. If the pod
mounts a `ReadWriteOnce` (or `ReadWriteOncePod`) PersistentVolumeClaim, a second
pod can't co-mount it — the volume is single-writer, and the scheduler/CSI will
reject the second attach (Multi-Attach error). There is no way around this: the
storage semantics forbid two writers.

So kube-ha-surge **detects RWO/single-writer volumes and passes the eviction
straight through** — no surge, immediate allow. The workload takes its normal
drain gap. This is a correctness refusal, not a bug: better an honest gap than a
surge pod stuck `ContainerCreating` on a Multi-Attach error while the drain
waits out `--max-surge-seconds`.

If your single-replica workload owns RWO storage and you need make-before-break,
the answer is storage-level: `ReadWriteMany` volumes, or an app that can hand off
cleanly. kube-ha-surge can't manufacture single-writer concurrency.

## StatefulSets are not supported in v1

kube-ha-surge only surges **Deployments**. StatefulSets have stable network
identity, ordered pod names, and per-replica volumes — cloning `web-0` into a
transient stand-in that the headless Service and the RWO volume both expect to
be *the* `web-0` doesn't work. Supporting StatefulSets safely is a separate
design problem (identity handoff, per-ordinal volumes) and is out of scope for
v1. StatefulSet evictions pass straight through.

## Don't pair it with a blocking PodDisruptionBudget

A PodDisruptionBudget with `minAvailable: 1` (or `maxUnavailable: 0`) on a
single-replica workload makes the eviction API refuse the eviction with **429
before the admission webhook is ever consulted** — the PDB check happens first
in the eviction flow. The result is a drain that blocks forever, and
kube-ha-surge never gets the chance to create a surge pod.

**Let kube-ha-surge own the disruption path for surged workloads.** Remove the
blocking PDB (or set it to something non-blocking). kube-ha-surge already
provides the "don't lose coverage" guarantee a PDB is usually reaching for, and
it provides it by *adding capacity* rather than *refusing disruption* — which is
the thing a single-replica PDB fundamentally cannot do.

## It shortens the gap; it is not replication

kube-ha-surge acts on the **graceful** eviction path — node drains, cordon +
evict, autoscaler consolidation, rolling node upgrades. That's where the gap is
predictable and worth closing.

It does **not** protect against uncontrolled failures. A hard node crash, a
kernel panic, an OOM-kill, or a `kubectl delete pod --force` doesn't go through
the eviction webhook at all — the pod is just gone, and your single replica is
down until the ReplicaSet's replacement is Ready. If you need to survive
*those*, you need real replication (`replicas: 2+`), which is a different tool
for a different failure mode.

The honest framing: kube-ha-surge makes the **drain slower but never lossy**. It
buys you make-before-break for planned disruptions of workloads that genuinely
can't run two replicas — and it gets out of the way (fail-open) the moment
anything about it breaks.

## Fail-open by design

If the operator is down, unreachable, its cert is wrong, or it errors, the
webhook's `failurePolicy: Ignore` means evictions pass straight through. This is
intentional (see [ADR 0004](adr/0004-opt-in-and-safety-refusals.md)): a surge
feature must never be able to wedge a node drain. The cost is that a broken
operator silently reverts you to the plain single-replica gap, with no surge and
no error at the eviction call site — check the operator's health and the
`--max-surge-seconds` timeout if you expected a surge and didn't get one.
