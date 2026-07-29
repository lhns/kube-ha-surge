# 0002 — Surge via a cloned Pod owned by the target Deployment

Status: Superseded by [0008](0008-detach-doomed-pod.md)

Date: 2026-07-29

## Context

Once the eviction webhook ([ADR 0001](0001-eviction-admission-webhook-trigger.md))
decides to provide make-before-break for a pod, it needs to bring up temporary
serving capacity — a "surge" — that:

1. is a faithful copy of the doomed pod (same image, env, volumes, probes,
   labels the Service selects on), so it actually serves the same traffic;
2. is **routed to by the workload's Service** while it lives;
3. is **ignored by the ReplicaSet** (so the RS doesn't count it toward
   `replicas` and immediately delete it, and doesn't get confused about desired
   state);
4. is cleaned up automatically when it's no longer needed, including if the
   operator crashes mid-flight.

The tension is 2-vs-3: the Service and the ReplicaSet both select pods by
labels, but we want the surge pod visible to one and invisible to the other.

## Decision

The webhook creates a **plain `Pod`** that is a clone of the doomed pod, with:

- an **`ownerReference` to the target Deployment with `controller: true`**, so
  the surge pod is garbage-collected by Kubernetes if the Deployment is deleted,
  and is unambiguously "ours";
- the **`pod-template-hash` label stripped**. The ReplicaSet's selector includes
  `pod-template-hash`, so without it the RS never matches the surge pod — it
  neither counts it nor deletes it. The Service selects on the app's own labels
  (not the hash), so it *does* route to the surge pod. This single omission
  resolves the 2-vs-3 tension.
- surge marker labels/annotations (`ha-surge.lhns.de/surge=true`,
  `ha-surge.lhns.de/for=<deployment>`,
  `ha-surge.lhns.de/replacing=<pod>`) for selection, ownership, and GC bookkeeping.

The pod-as-state approach means the surge pod *is* the operator's record of an
in-flight surge — no separate CRD, no external store. The GC controller
reconstructs everything it needs from the labels/annotations on live pods.

## Consequences

- **Works with GitOps.** We never touch the Deployment's `spec.replicas`, so
  Flux/Argo don't see drift and don't fight us. The Deployment stays exactly as
  the user declared it.
- **Self-cleaning on operator death.** The `controller: true` ownerReference plus
  the GC controller mean a crashed operator doesn't leak surge pods
  indefinitely; the pre-delete hook and controller reconcile clean up the rest.
- **The surge pod is a real second replica for its lifetime** — which is exactly
  why RWO/single-writer workloads can't be surged
  ([ADR 0004](0004-opt-in-and-safety-refusals.md), [`../limitations.md`](../limitations.md)).
- **No CRD to install or version.** Simpler chart, no schema migrations. The
  cost is that surge state is only as durable as the pod object (fine — a lost
  surge pod just means we re-surge on the next eviction retry).
- Stripping `pod-template-hash` is subtle and load-bearing; it's documented in
  the reference so nobody "helpfully" adds it back.

## Alternatives considered

- **Scale the Deployment to 2, then back to 1.** Rejected on two counts: (1) it
  fights GitOps — Flux/Argo continuously reconcile `replicas: 1` and would
  immediately scale us back down, or flap; (2) the scale-down afterward races —
  the ReplicaSet may delete the *new* healthy pod instead of the old one, since
  it has no notion of "keep the one that's covering the drain". Owning a discrete
  pod sidesteps both.
- **A `Surge` CRD as the unit of state.** Deferred. A CRD would give us a
  first-class status object and a cleaner controller, but for v1 the pod itself
  carries all the state we need (labels + annotations + ownerRef), and avoiding a
  CRD keeps install/upgrade trivial. Revisit if surge orchestration grows beyond
  what a single pod's metadata can express.
- **A Deployment/ReplicaSet of surge pods.** Rejected: overkill for a single
  transient replica, and reintroduces the selector-collision problem with the
  workload's own RS.
