# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

While the project is pre-1.0, breaking changes are possible across any release.
Pinning to an exact version is recommended.

## [Unreleased]

## [0.1.1] — 2026-10-22

### Security

- **Rebuilt on current base images.** The image uses
  `gcr.io/distroless/static:nonroot` (Debian 13), whose CA bundle drops the
  emSign roots compromised in the October 2026 eMudhra intermediate CA key
  compromise, and a Go 1.26 toolchain of 1.26.10 or later, which also rejects
  the eight compromised keys in `crypto/x509`.
- **Dependencies:** `golang.org/x/net` v0.60.0 (CVE-2026-78669) and
  `golang.org/x/text` v0.42.0 (CVE-2026-56851), both rated HIGH by trivy.

### Changed

- Three Kubernetes module updates from the Dependabot `kubernetes` group (#5).

### Documentation

- README restructured for discoverability; the surge budget and the behaviour
  with unschedulable replacements are now documented.

## [0.1.0] — 2026-07-29

First release. Published as image `ghcr.io/lhns/ha-surge:0.1.0` (+ `latest`) and
OCI Helm chart `oci://ghcr.io/lhns/charts/ha-surge:0.1.0`. The **product and all
artifacts are named `ha-surge`**; the **GitHub repository and Go module path stay
`github.com/lhns/kube-ha-surge`**.

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
  - **Opt-in cascade** on a single key, `ha-surge.lhns.de/enabled`, resolved
    most-specific-wins: Deployment annotation → Namespace label → chart
    `defaultEnabled` (default `false`). The webhook's `namespaceSelector` is
    rendered from `defaultEnabled` so the apiserver gates scope before the
    operator runs. Safety refusals (which only ever skip) for `strategy: Recreate`,
    RWO/single-writer volumes, non-Deployment owners, already-multi-replica
    workloads, and Services that select on `pod-template-hash`. See ADR 0009
    (supersedes 0004).
  - **Operator runs HA by default**: `replicaCount: 2` with a PodDisruptionBudget
    and cross-node topology spread, because the operator is on the eviction path
    and must stay available during drains. Both replicas serve admission; leader
    election keeps the GC controller a singleton. The operator is stateless (all
    in-flight state lives on pod labels/annotations in etcd), so failover mid-
    eviction is safe. See ADR 0010.
  - **Helm chart** (`charts/ha-surge`) with cert-manager (default) or
    self-signed Helm-generated webhook serving certs, RBAC, leader election, and
    a pre-delete hook that clears leftover detached cover pods on uninstall.
  - **kustomize** manifests under `config/` for `make deploy` / release.yaml.
  - Docs: README, getting-started, reference, limitations, and ADRs 0001–0010.

[Unreleased]: https://github.com/lhns/kube-ha-surge/compare/v0.1.1...HEAD
[0.1.1]: https://github.com/lhns/kube-ha-surge/releases/tag/v0.1.1
[0.1.0]: https://github.com/lhns/kube-ha-surge/releases/tag/v0.1.0
