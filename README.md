# kube-ha-surge

Make-before-break high availability for opted-in **single-replica Deployments**
during node drains.

## The problem

A single-replica Deployment has a coverage gap every time its node is drained.
`kubectl drain` (or the cluster-autoscaler, or a Karpenter consolidation, or a
node upgrade) evicts the one pod, and only *after* it is gone does the
ReplicaSet create a replacement — which then has to be scheduled, pulled,
started, and pass its readiness probe. For the tens of seconds in between, the
workload is down.

The usual answers are unsatisfying:

- **"Just run two replicas."** Often you can't: the app is a singleton
  (a leader-elected controller, a license-locked process, a migration runner),
  or two replicas cost real money at scale across hundreds of such services, or
  the workload owns a `ReadWriteOnce` volume that a second replica can't mount.
- **"Add a PodDisruptionBudget with `minAvailable: 1`."** For a single-replica
  Deployment that just makes the eviction fail *forever* — the drain blocks
  instead of the workload being protected. A PDB can refuse disruption; it can't
  create capacity.
- **"Add a fixed pre-stop delay / sleep."** Guesswork. Too short and you still
  drop traffic; too long and every drain crawls. It also does nothing to bring
  up replacement capacity *before* the old pod leaves.

None of these give you the thing you actually want: **new pod Ready first, old
pod gone second.**

## How it works

kube-ha-surge is a small operator with two pieces:

1. **An eviction admission webhook.** It registers a `ValidatingWebhook` on the
   `pods/eviction` subresource. When something tries to evict a pod belonging to
   an opted-in single-replica Deployment, the webhook:
   - creates a temporary cloned **surge pod** — a faithful copy of the doomed
     pod, owned (`controller: true`) by the target Deployment, but stripped of
     the `pod-template-hash` label so the ReplicaSet ignores it while the
     Service still routes to it;
   - **denies the eviction with HTTP 429** ("too many requests"). A
     webhook-supplied 429 surfaces to `kubectl drain` as a retryable
     `TooManyRequests` and is retried automatically — so the drain politely
     waits instead of failing.
   - On the retry, once the surge pod is **Ready**, the webhook **allows** the
     eviction. Old pod leaves only after new capacity is serving:
     make-before-break.

2. **A garbage-collection controller.** Surge pods are temporary. Once the
   evicted pod is gone and the ReplicaSet's own replacement is Ready, the
   controller deletes the surge pod. It also enforces the `--max-surge-seconds`
   fail-open timeout.

If the operator is down, unreachable, or erroring, the webhook **fails open**
(`failurePolicy: Ignore`): evictions pass straight through and drains proceed
exactly as they would without kube-ha-surge. The drain can get *slower*, never
*wedged*, and never *lossy-by-surprise*.

## Quickstart

Install the operator (OCI Helm chart on GHCR):

```sh
helm install kube-ha-surge oci://ghcr.io/lhns/charts/kube-ha-surge \
  -n kube-ha-surge-system --create-namespace
```

This defaults to `webhook.certSource=cert-manager` (needs cert-manager in the
cluster). No cert-manager? Use the self-signed Helm path:

```sh
helm install kube-ha-surge oci://ghcr.io/lhns/charts/kube-ha-surge \
  -n kube-ha-surge-system --create-namespace \
  --set webhook.certSource=helm
```

Opt in a workload in two steps:

```sh
# 1. Enroll the namespace (gates the eviction webhook — blast-radius limit).
kubectl label namespace my-app ha-surge.lhns.de/managed=true

# 2. Opt the specific single-replica Deployment in.
kubectl annotate deployment my-app -n my-app ha-surge.lhns.de/enabled=true
```

Now drain the node hosting that pod and watch a surge pod appear, become Ready,
and the eviction complete — with no coverage gap:

```sh
kubectl drain <node> --ignore-daemonsets --delete-emptydir-data
kubectl get pods -n my-app -l ha-surge.lhns.de/surge=true
```

See [`docs/getting-started.md`](docs/getting-started.md) for a full walkthrough
and [`docs/reference.md`](docs/reference.md) for every label, annotation, flag,
and Helm value.

## Limitations

kube-ha-surge deliberately refuses to act where it can't help safely:

- **`ReadWriteOnce` / single-writer volumes** can't be surged — a stand-in
  can't co-mount the volume (Multi-Attach). Those evictions pass straight
  through.
- **Only Deployments** are supported in v1. StatefulSets (stable identity,
  ordered volumes) are out of scope.
- **Don't add a blocking `minAvailable` PDB** to a surged workload — the PDB
  refuses the eviction *before* the webhook is consulted, so the surge never
  happens. Let kube-ha-surge own the disruption path.
- It shortens the drain gap; it is not a substitute for real replication
  against uncontrolled failures (a hard node crash still drops the single pod).

Full detail in [`docs/limitations.md`](docs/limitations.md).

## Design

Decisions are recorded as ADRs in [`docs/adr/`](docs/adr/README.md).

## License

Apache-2.0 — see [LICENSE](LICENSE) and [NOTICE](NOTICE).
