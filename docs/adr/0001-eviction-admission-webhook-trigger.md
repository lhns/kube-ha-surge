# 0001 — Eviction admission webhook as the trigger

Status: Accepted

Date: 2026-07-29

## Context

ha-surge needs to notice, *synchronously and before it's too late*, that a
single-replica pod is about to be removed by a graceful disruption (node drain,
cordon + evict, autoscaler consolidation, node upgrade), so it can bring up a
stand-in first. "Before it's too late" is the hard part: by the time the pod is
already `Terminating`, the coverage gap has begun and nothing we do can be
make-*before*-break.

The graceful removal path in Kubernetes is the **Eviction API**: a `POST` of an
`Eviction` object to the `pods/eviction` subresource. `kubectl drain`, the
cluster-autoscaler, Karpenter, and node-upgrade controllers all evict rather
than delete. Admission webhooks can intercept that subresource. Critically, the
Eviction API already has retry semantics baked in: a `429 Too Many Requests`
response is interpreted by callers as "not yet, back off and retry" — that's how
PodDisruptionBudgets hold evictions today.

We need a trigger mechanism that (a) fires before the pod is gone, (b) can
create the surge pod, and (c) can *hold* the eviction and have the caller retry
until we're ready.

## Decision

Register a **`ValidatingWebhookConfiguration` on the `pods/eviction`
subresource** (`operations: [CREATE]`, `resources: [pods/eviction]`, one webhook
`eviction.ha-surge.lhns.de`, path `/validate-eviction`). When an eviction
targets an opted-in single-replica Deployment's pod and no Ready stand-in exists
yet, the webhook **creates the surge pod and denies the eviction with HTTP
429**. The caller retries; once the stand-in is Ready, the webhook allows the
eviction.

We use a *validating* (not mutating) webhook: we are not mutating the Eviction
object, we are admitting or rejecting it, with the side effect of creating a
separate pod out-of-band.

**Validated fact:** a webhook-supplied 429 on `pods/eviction` surfaces to
`kubectl drain` as `TooManyRequests` and is retried automatically — confirmed on
a live cluster. This is the linchpin that lets us "hold and retry" without the
drain failing.

## Consequences

- The trigger is exactly the graceful-disruption event we care about, caught at
  the one moment when make-before-break is still possible.
- We inherit the Eviction API's retry loop for free — no polling, no custom
  client behaviour. The drain waits politely.
- The webhook must be fast and robust; a hang would slow every eviction in an
  enrolled namespace up to its `timeoutSeconds`. Combined with fail-open
  (`failurePolicy: Ignore`, [ADR 0004](0004-opt-in-and-safety-refusals.md)) a
  broken webhook degrades to "no surge", never "no drain".
- Uncontrolled removals (`delete pod --force`, node crash, OOM) don't go through
  eviction and are therefore out of scope by construction — see
  [`../limitations.md`](../limitations.md).
- Requires serving-cert lifecycle for the webhook
  ([ADR 0006](0006-webhook-serving-cert-sources.md)).

## Alternatives considered

- **Watch nodes for cordon/`unschedulable` and pre-emptively surge.** Rejected:
  racy and imprecise. Cordoning a node doesn't mean every pod on it is being
  drained (or drained *now*), and there's no back-pressure mechanism — we'd have
  no way to hold the eviction, only to guess and hope the stand-in wins the
  race. It also surges pods that may never actually be evicted.
- **`ValidatingAdmissionPolicy` (CEL) on `pods/eviction`.** Rejected: a VAP can
  only allow/deny based on CEL over the request; it **cannot create the surge
  pod** (no side effects) and **cannot return a retryable 429** with our
  semantics — it returns a plain deny. It can't do the two things that make this
  work.
- **A mutating webhook.** Rejected: there's nothing on the Eviction object worth
  mutating. The work is "create a pod + decide admit/deny", which validating
  admission expresses directly.
- **A finalizer on the pod.** Rejected: finalizers intercept *deletion*, which
  is already past the point of no return for coverage, and they can't add
  capacity — only delay teardown, which is the wrong half of make-before-break.
