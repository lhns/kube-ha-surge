# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

While the project is pre-1.0, breaking changes are possible across any release.
Pinning to an exact version is recommended.

## [Unreleased]

### Changed

- Renamed the product and all published/deployed artifacts from `kube-ha-surge`
  to **`ha-surge`** (image `ghcr.io/lhns/ha-surge`, chart
  `oci://ghcr.io/lhns/charts/ha-surge`, namespace `ha-surge-system`, and every
  Kubernetes resource name). The **GitHub repository and Go module path stay
  `github.com/lhns/kube-ha-surge`**.
- Replaced the clone-a-surge-pod mechanism with a **detach** mechanism: on an
  eviction the webhook removes the doomed pod's `pod-template-hash` label,
  detaching it from its ReplicaSet (which then creates its own real replacement)
  while the pod keeps serving via the Service. `spec.replicas` is never touched
  (no GitOps drift), no pod is cloned (no admission-policy/label-strip coupling —
  the `--strip-label-prefixes` flag / `stripLabelPrefixes` value are removed),
  and a controller deletes the detached cover pod once the replacement is Ready.
  See ADR 0008 (supersedes 0002).

### Added

- Initial ha-surge operator: make-before-break HA for opted-in single-replica
  Deployments during node drains.
  - **Eviction admission webhook** on the `pods/eviction` subresource
    (`eviction.ha-surge.lhns.de`). It returns HTTP 429 to hold an eviction while
    it detaches the doomed pod from its ReplicaSet, and allows the eviction once
    the ReplicaSet's own replacement is Ready. `failurePolicy: Ignore`
    (fail-open) so a broken operator never wedges a drain; a
    `--max-surge-seconds` timeout bounds the hold.
  - **Detach controller** that deletes the detached cover pod once the
    ReplicaSet's replacement is Ready.
  - **Opt-in contract**: namespace label `ha-surge.lhns.de/managed=true` (gates
    the webhook) plus Deployment annotation `ha-surge.lhns.de/enabled=true`
    (per-workload). Safety refusals for RWO/single-writer volumes,
    non-Deployment owners, already-multi-replica workloads, and Services that
    select on `pod-template-hash`.
  - **Helm chart** (`charts/ha-surge`) with cert-manager (default) or
    self-signed Helm-generated webhook serving certs, RBAC, leader election, and
    a pre-delete hook that clears leftover detached cover pods on uninstall.
  - **kustomize** manifests under `config/` for `make deploy` / release.yaml.
  - Docs: README, getting-started, reference, limitations, and ADRs 0001–0008.
