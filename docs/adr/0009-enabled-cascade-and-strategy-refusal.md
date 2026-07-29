# 0009 — Enabled cascade + strategy-derived refusal

Status: Accepted

Date: 2026-07-29

Supersedes [0004](0004-opt-in-and-safety-refusals.md) (opt-in model only; the
fail-open posture and the safety-refusal principle from 0004 still hold).

## Context

[0004](0004-opt-in-and-safety-refusals.md) required **two independent markers**
to protect a workload: a namespace label `ha-surge.lhns.de/managed=true` (webhook
scope) *and* a per-Deployment annotation `ha-surge.lhns.de/enabled=true`
(per-workload opt-in). In practice that is tedious: an operator wanting HA for a
namespace has to annotate every single Deployment in it, and keep annotating new
ones. What users actually want is "protect this namespace" — or even "protect the
whole cluster" — with per-workload *exceptions*, not per-workload *enrolment*.

Separately, 0004's opt-in existed partly to avoid surging workloads that must
never run two copies at once. But a Deployment already declares that intent in a
field: `spec.strategy.type: Recreate` means "tear the old pod down before the new
one starts". We were making users restate, via an annotation, something the spec
already encodes.

## Decision

### One key, three levels — the enabled cascade

A single key, **`ha-surge.lhns.de/enabled`** (`"true"` / `"false"`), is read at
three levels and resolved most-specific-wins:

```
effective = Deployment annotation  ha-surge.lhns.de/enabled   // "true"/"false"
         ?? Namespace  label       ha-surge.lhns.de/enabled   // "true"/"false"
         ?? chart .Values.defaultEnabled                       // default: false
```

An absent (or non-boolean) level falls through to the next. This folds the old
`managed` namespace label into the same `enabled` key: the namespace level now
does double duty as both "in scope" and "default for the namespace", and the
chart default `defaultEnabled` is the cluster-wide floor. Common cases collapse to
*zero* per-workload markers:

- protect one namespace → label the namespace `enabled=true`;
- protect the cluster → `defaultEnabled: true`, exempt with `enabled=false`;
- one exception → annotate that one Deployment.

The resolution is a pure function (`surge.ResolveEnabled`) so it is exhaustively
unit-tested across the full Deployment × Namespace × default matrix.

### The load-bearing mechanic: namespaceSelector is rendered from defaultEnabled

The apiserver evaluates the webhook's `namespaceSelector` (which matches on
**namespace labels**) *before* the handler ever runs. The handler can therefore
see the Deployment annotation and the namespace label, but the apiserver decides
whether to *call* the handler at all using only namespace labels — it cannot see
the chart default or any annotation. So the chart renders the selector to match
the intended scope of `defaultEnabled`:

- **`defaultEnabled: false`** (default) → `matchLabels {ha-surge.lhns.de/enabled:
  "true"}`. The webhook is consulted **only** for namespaces explicitly labelled
  enabled — a tiny, deliberate blast radius.
- **`defaultEnabled: true`** → `matchExpressions`: `enabled NotIn ["false"]` **and**
  `kubernetes.io/metadata.name NotIn [kube-system, <release-ns>, <excludeNamespaces…>]`.
  Consulted for every namespace except explicit opt-outs and always-excluded
  system namespaces.

**Inherent limitation** (documented in [`../limitations.md`](../limitations.md)):
with `defaultEnabled: false`, a lone per-Deployment `enabled=true` annotation has
**no effect unless its namespace is at least labelled `enabled=true`** — the
apiserver gates on namespace labels first, so the eviction never reaches the
operator. To protect a single Deployment in an otherwise-default namespace, that
namespace must be enabled (and other Deployments individually opted out) or
`defaultEnabled` raised.

### Strategy-derived refusal: `strategy: Recreate` → skip

Add one refusal to 0004's list: if `spec.strategy.type == Recreate`, allow the
eviction untouched. The owner has declared "never two at once"; surging would
violate exactly that contract. Conversely a `RollingUpdate` Deployment already
tolerates overlap (its own rollouts briefly run old+new together), so surging
introduces no overlap it doesn't already accept. This derives the "don't double"
signal from the field that already encodes it instead of a separate annotation.

Enablement (`enabled`) only decides *may we*; refusals only ever *skip*. So even
`defaultEnabled: true` can never surge a `Recreate`, RWO, already-HA, or
Service-selects-on-hash workload.

## Consequences

- **Zero-marker common case.** Enrol a namespace once (or the whole cluster) and
  every eligible single-replica Deployment is protected, new ones included.
- **`managed` retired.** One key (`enabled`) now governs scope *and* override at
  every level; the old `managed` label is gone. This is a breaking change to the
  enrolment contract (reflected in the chart, `config/`, and docs).
- **`defaultEnabled: true` widens the webhook to nearly all evictions
  cluster-wide.** Fail-open (`failurePolicy: Ignore`, unchanged from 0004) keeps
  that safe, and system namespaces are always excluded — but it stays **false by
  default** and is flipped only deliberately.
- **One extra `Namespace` GET per first eviction.** Cheap (evictions are rare)
  and via the handler's direct reader. A GET error is treated as "label absent"
  so it falls to the default and never wedges.
- **Recreate workloads are transparently skipped** rather than needing a manual
  opt-out — one less footgun for `defaultEnabled: true`.

## Alternatives considered

- **Separate `managed` (scope) + `enabled` (override) keys.** Rejected: two keys
  meaning nearly the same thing at different levels is confusing; one key with
  clear precedence is simpler and still expresses every case.
- **Handler-only enforcement, `namespaceSelector: {}` (match all).** Rejected: it
  would put the webhook in the path for *every* eviction cluster-wide even when
  `defaultEnabled: false`, enlarging blast radius for no benefit. Gating scope at
  the apiserver via the rendered selector is strictly safer.
- **Keep requiring a per-Deployment annotation.** Rejected as the tedium this ADR
  exists to remove.
- **A dedicated `surge=false` annotation instead of reading `strategy`.**
  Rejected: `Recreate` already means exactly "no two at once"; reading it avoids
  redundant configuration and covers workloads whose authors never heard of
  ha-surge.
