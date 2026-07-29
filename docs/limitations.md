# Limitations

ha-surge is deliberately narrow. It refuses to act where it can't help safely,
and it makes one specific window (the drain gap) smaller — it is not a general
availability solution. Know these boundaries before you rely on it.

## ReadWriteOnce / single-writer volumes can't be covered

While ha-surge holds an eviction, the detached pod keeps running *and* the
ReplicaSet brings up its own replacement — so there are briefly two running
copies of the workload. If the pod mounts a `ReadWriteOnce` (or
`ReadWriteOncePod`) PersistentVolumeClaim, the replacement can't co-mount the
volume the detached pod still holds — the volume is single-writer, and the
scheduler/CSI will reject the second attach (Multi-Attach error). There is no
way around this: the storage semantics forbid two writers.

So ha-surge **detects RWO/single-writer volumes and passes the eviction
straight through** — no detach, immediate allow. The workload takes its normal
drain gap. This is a correctness refusal, not a bug: better an honest gap than a
ReplicaSet replacement stuck `ContainerCreating` on a Multi-Attach error while
the drain waits out `--max-surge-seconds`.

If your single-replica workload owns RWO storage and you need make-before-break,
the answer is storage-level: `ReadWriteMany` volumes, or an app that can hand off
cleanly. ha-surge can't manufacture single-writer concurrency.

## StatefulSets are not supported in v1

ha-surge only covers **Deployments**. StatefulSets have stable network
identity, ordered pod names, and per-replica volumes — detaching `web-0` and
letting a controller spin up another `web-0` that the headless Service and the
RWO volume both expect to be *the* `web-0` doesn't work. Supporting StatefulSets
safely is a separate design problem (identity handoff, per-ordinal volumes) and
is out of scope for v1. StatefulSet evictions pass straight through.

## `Recreate`-strategy Deployments are passed through

A Deployment with `spec.strategy.type: Recreate` has declared that it must
**never run two copies at once** — that is precisely what `Recreate` means (tear
the old pod down before starting the new one). Surging would briefly run two,
violating that contract, so ha-surge detects `Recreate` and passes the eviction
straight through. `RollingUpdate` (the default) already tolerates that overlap
during its own rollouts, so it is covered normally. If you want coverage for a
workload currently on `Recreate` and it *can* tolerate a momentary overlap,
switch it to `RollingUpdate`.

## Per-Deployment opt-in needs the namespace in scope (`defaultEnabled: false`)

With the default `defaultEnabled: false`, the webhook's `namespaceSelector` only
routes evictions from namespaces labelled `ha-surge.lhns.de/enabled=true` to the
operator (the apiserver gates on namespace **labels** before the operator runs).
So annotating a single Deployment `ha-surge.lhns.de/enabled=true` in an
otherwise-unlabelled namespace has **no effect** — the eviction never reaches the
operator to read the annotation. To protect one Deployment in such a namespace,
either label the namespace `enabled=true` (and annotate the *other* Deployments
`enabled=false`), or raise `defaultEnabled`. The Deployment annotation is for
*overriding* within an in-scope namespace, not for pulling a namespace into
scope. See [ADR 0009](adr/0009-enabled-cascade-and-strategy-refusal.md).

## Don't pair it with a blocking PodDisruptionBudget

A PodDisruptionBudget with `minAvailable: 1` (or `maxUnavailable: 0`) on a
single-replica workload makes the eviction API refuse the eviction with **429
before the admission webhook is ever consulted** — the PDB check happens first
in the eviction flow. The result is a drain that blocks forever, and ha-surge
never gets the chance to detach the pod and cover the drain.

**Let ha-surge own the disruption path for covered workloads.** Remove the
blocking PDB (or set it to something non-blocking). ha-surge already provides
the "don't lose coverage" guarantee a PDB is usually reaching for, and it
provides it by *adding capacity* rather than *refusing disruption* — which is
the thing a single-replica PDB fundamentally cannot do.

## The Deployment's `readyReplicas` briefly reads 0

During the handoff the detached pod no longer counts toward the Deployment (it
lost its `pod-template-hash`), and the ReplicaSet's fresh replacement is not yet
Ready — so the **Deployment's** `status.readyReplicas` dips to `0` for a few
seconds. This is cosmetic: the **Service** never loses an endpoint, because the
detached pod keeps its app labels and keeps serving the whole time, and the
eviction is only allowed once the replacement is actually Ready. No request is
dropped. Dashboards or alerts that page on `readyReplicas == 0` for a
single-replica Deployment may fire briefly during a covered drain; gate them on
a short duration, or watch the Service's endpoints instead.

## Workloads whose Service selects on `pod-template-hash` are passed through

Detaching works because a normal Deployment's Service selects on the app's own
labels, **not** on `pod-template-hash` — so removing that label hides the pod
from the ReplicaSet without hiding it from the Service. If a workload's Service
*does* select on `pod-template-hash` (essentially never for a normal Deployment,
but possible if someone hand-wrote such a selector), detaching would also drop
the pod out of the Service and break coverage. ha-surge detects this and
**passes the eviction straight through un-covered** rather than cause an outage.

## It shortens the gap; it is not replication

ha-surge acts on the **graceful** eviction path — node drains, cordon + evict,
autoscaler consolidation, rolling node upgrades. That's where the gap is
predictable and worth closing.

It does **not** protect against uncontrolled failures. A hard node crash, a
kernel panic, an OOM-kill, or a `kubectl delete pod --force` doesn't go through
the eviction webhook at all — the pod is just gone, and your single replica is
down until the ReplicaSet's replacement is Ready. If you need to survive
*those*, you need real replication (`replicas: 2+`), which is a different tool
for a different failure mode.

The honest framing: ha-surge makes the **drain slower but never lossy**. It buys
you make-before-break for planned disruptions of workloads that genuinely can't
run two replicas — and it gets out of the way (fail-open) the moment anything
about it breaks.

## Fail-open by design

If the operator is down, unreachable, its cert is wrong, or it errors, the
webhook's `failurePolicy: Ignore` means evictions pass straight through. This is
intentional (see [ADR 0004](adr/0004-opt-in-and-safety-refusals.md)): a coverage
feature must never be able to wedge a node drain. The cost is that a broken
operator silently reverts you to the plain single-replica gap, with no cover and
no error at the eviction call site — check the operator's health and the
`--max-surge-seconds` timeout if you expected coverage and didn't get it.
