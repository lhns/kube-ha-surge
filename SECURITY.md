# Security Policy

## Reporting a vulnerability

**Please do not open a public issue for security vulnerabilities.**

Report privately via GitHub's private vulnerability reporting:

**https://github.com/lhns/kube-ha-surge/security/advisories/new**

Please include:

- what an attacker gains (e.g. "a namespace tenant can force an operator to
  create arbitrary pods in another namespace")
- the kube-ha-surge version and Kubernetes version
- a minimal reproduction — the Deployment/namespace opt-in state and the
  observed vs expected eviction behaviour

You will get an acknowledgement, and we will coordinate a fix and disclosure
timeline with you. If you would like credit in the release notes, say so.

## Supported versions

kube-ha-surge is pre-1.0. Only the **latest released minor** receives security
fixes. Breaking changes are possible in any release — pin an exact version.

## What counts as a vulnerability

kube-ha-surge intercepts `pods/eviction` admission and creates temporary
"surge" pods. Its security boundary is that it only acts on opted-in workloads
and only creates a faithful clone of the pod being evicted. Treat as reportable:

- the operator creating a surge pod for a workload that did **not** opt in
  (namespace not labelled `ha-surge.lhns.de/managed=true`, or Deployment not
  annotated `ha-surge.lhns.de/enabled=true`)
- a surge pod that escalates privilege beyond the pod it clones (extra
  volumes, serviceAccount, securityContext, or host access it should not have)
- a tenant using the eviction webhook to **block** evictions past the
  `--max-surge-seconds` fail-open bound (denial of drain)
- privilege escalation via the operator's or the Helm pre-delete hook's
  ServiceAccount
- supply-chain issues in released images or charts

## Known non-issues (documented, by design)

These are **not** vulnerabilities. Reports of them will be closed with a pointer
here.

- **The webhook fails open.** `failurePolicy: Ignore` is mandatory (see
  [ADR 0004](docs/adr/0004-opt-in-and-safety-refusals.md)). If the operator is
  down, evictions pass straight through. A broken operator must never wedge a
  node drain — availability of the drain path outranks the surge feature.
- **Cluster-admin can defeat kube-ha-surge**, as can anyone who can stop the
  operator, edit its RBAC, or remove the ValidatingWebhookConfiguration.
- **A namespace owner with `patch namespace` can opt in or out** by adding or
  removing the `ha-surge.lhns.de/managed=true` label. This is the intended
  opt-in control, not a boundary.
- **RWO / single-writer workloads are passed through, not helped.** A stand-in
  can't co-mount a `ReadWriteOnce` volume (Multi-Attach), so such evictions are
  allowed immediately with no surge. This is a correctness refusal, not a
  weakness — see [`docs/limitations.md`](docs/limitations.md).
- **Established connections survive a pod going away.** kube-ha-surge shortens
  the *gap* on drain; it does not make a single-replica workload connection-
  lossless in every failure mode (a hard node crash still drops the pod).

## Supply chain

Released container images and Helm charts are signed with
[cosign](https://github.com/sigstore/cosign) (keyless, GitHub OIDC) and ship
SPDX SBOMs as cosign attestations and release assets (see
[ADR 0007](docs/adr/0007-packaging-oci-chart-and-image.md)).
