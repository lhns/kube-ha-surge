# 0008 — Detach the doomed pod from its ReplicaSet

Status: Accepted

Date: 2026-07-29

Supersedes [0002](0002-surge-via-cloned-owned-pod.md).

## Context

The eviction webhook ([ADR 0001](0001-eviction-admission-webhook-trigger.md))
needs to provide make-before-break for a doomed single-replica pod: keep
something serving the workload's traffic until a fresh replacement is Ready,
*then* let the eviction through.

[ADR 0002](0002-surge-via-cloned-owned-pod.md) did this by **cloning** the
doomed pod into a temporary "surge" pod owned by the Deployment, with
`pod-template-hash` stripped so the ReplicaSet ignored it while the Service
routed to it. It worked, but it carried real costs:

- The clone is a *newly created* pod carrying labels the operator injected. Some
  clusters guard specific label keys with admission policies that reject a
  user/operator-supplied value — which would block the clone's creation
  outright. ADR 0002's answer was a `--strip-label-prefixes` / `stripLabelPrefixes`
  knob to drop such labels before create. That is per-cluster config that has to
  be discovered and maintained, and it couples the surge mechanism to each
  cluster's admission setup.
- Two cold starts happen for one drain: the clone starts *and* the ReplicaSet's
  own replacement starts. The clone is pure throwaway — something the GC
  controller must always chase and delete.
- The clone is genuinely a second, operator-authored object whose lifecycle we
  own end to end.

The key realization is that we don't need to *create* anything to get a covering
pod: the doomed pod is already running, already Ready, already selected by the
Service. The only reason it "has to go" is that the ReplicaSet's selector claims
it. So change *that*, not the pod's existence.

## Decision

On an eviction for an opted-in single-replica Deployment, the webhook
**detaches the doomed pod from its ReplicaSet** by removing its
`pod-template-hash` label (a `patch`, not a create/clone). It then holds the
eviction with HTTP 429 until the replacement is Ready, and a controller deletes
the detached pod afterwards.

This rests on a **two-selector insight**: a Deployment's ReplicaSet selects pods
with `pod-template-hash` *included*, whereas the workload's Service selects on
the app's own labels and does **not** include `pod-template-hash`. So removing
that one label:

- makes the ReplicaSet stop matching the pod — the RS now sees itself one pod
  short of `replicas` and **creates its own real replacement** (no clone, no
  scaling);
- leaves the pod fully matched by the Service — it **keeps serving** the whole
  time.

The detached pod is marked with `ha-surge.lhns.de/detached=true`,
`ha-surge.lhns.de/for=<deployment>` (its RS ownerRef is gone, so we remember the
Deployment), and `ha-surge.lhns.de/detached-at=<RFC3339>`. When the replacement
is Ready the webhook allows the eviction; the detach controller then deletes the
detached cover pod.

**Readiness is judged robustly**, not from `dep.Status`: the webhook counts
Ready pods matching the Deployment's selector that are **not** the detached pod.
This is deliberate — the moment we detach, the Deployment's own
`readyReplicas` accounting is briefly wrong (see Consequences), so trusting
`dep.Status.ReadyReplicas` would either allow too early or hang. Counting real
Ready non-detached pods against the selector is the source of truth.

**Guard:** before detaching, the webhook checks that no Service selecting the pod
selects on `pod-template-hash`. If one does, removing the label would also drop
the pod out of that Service and break coverage, so the webhook **passes the
eviction through un-covered** instead. For a normal Deployment this never
happens (Services select on app labels), but the check makes the mechanism safe
by construction rather than by assumption.

## Consequences

- **No cloned pod.** Nothing throwaway is created, so there is nothing
  operator-authored for admission policies to reject — the
  `--strip-label-prefixes` / `stripLabelPrefixes` knob is **gone**, and with it a
  whole class of per-cluster configuration and coupling.
- **One real startup, not two.** The only pod that cold-starts is the
  ReplicaSet's genuine replacement — the same pod that would have started
  anyway, just brought up *before* the old one leaves. There is no second
  transient copy to schedule, pull, and reconcile.
- **Nothing throwaway to garbage-collect except the pod we detached** — which is
  the real workload pod that was going to be evicted regardless. The controller
  deletes it once the replacement is Ready; if the operator dies mid-flight, the
  detached pod is still a normal Ready pod serving traffic, and the
  `--max-surge-seconds` fail-open bound still applies.
- **No GitOps drift.** `spec.replicas` is never touched, so Flux/Argo never see
  the Deployment diverge from the declared `replicas: 1` and never fight us.
- **No scale-down race.** Scaling to 2 and back to 1 (the alternative below)
  risks the ReplicaSet deleting the *new healthy* pod on the way down, and
  briefly shows a "third pod" to anything counting replicas. Detach keeps
  `replicas` at 1 throughout: the RS naturally targets exactly one pod, and the
  one it deletes is bounded by the eviction we still control.
- **RWO/single-writer volumes are still refused.** During the hold there are
  briefly two running copies — the detached pod (still holding the volume) and
  the RS's replacement. A `ReadWriteOnce` volume can't be co-mounted by the
  replacement (Multi-Attach), so those evictions pass straight through, exactly
  as before ([ADR 0004](0004-opt-in-and-safety-refusals.md),
  [`../limitations.md`](../limitations.md)).
- **Honest caveat: the Deployment's `readyReplicas` briefly dips to 0.** Once
  detached, the pod no longer counts toward the Deployment, and the replacement
  is not yet Ready — so `Deployment.status.readyReplicas` reads 0 for a few
  seconds. The **Service** never dips: the detached pod keeps its app labels and
  keeps serving, and the eviction is only allowed once the replacement is Ready.
  No request is dropped. Alerts that page on `readyReplicas == 0` for a
  single-replica Deployment may fire briefly during a covered drain; this is
  documented in limitations.

## Alternatives considered

- **Clone a surge pod (ADR 0002).** Superseded by this ADR. It works but
  requires creating an operator-labelled pod (admission-policy risk and the
  `stripLabelPrefixes` coupling), incurs a second cold start, and leaves a
  purely transient object to GC. Detach achieves the same make-before-break with
  none of those.
- **Scale the Deployment to 2, then back to 1.** Rejected: (1) it fights GitOps —
  Flux/Argo continuously reconcile `replicas: 1` and would scale us back down or
  flap; (2) the scale-down races — the ReplicaSet may delete the *new* healthy
  pod rather than the old one, since it has no notion of "keep the one covering
  the drain," and it momentarily exposes a third pod. Detach never changes
  `replicas`, so neither problem exists.
- **Trust `dep.Status.ReadyReplicas` for the readiness gate.** Rejected: detach
  transiently skews the Deployment's own replica accounting, so its status is an
  unreliable gate. Counting Ready pods that match the Deployment selector and are
  not the detached pod is robust against that skew.
