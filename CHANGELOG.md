# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

While the project is pre-1.0, breaking changes are possible across any release.
Pinning to an exact version is recommended.

## [Unreleased]

### Added

- Initial kube-ha-surge operator: make-before-break HA for opted-in
  single-replica Deployments during node drains.
  - **Eviction admission webhook** on the `pods/eviction` subresource
    (`eviction.ha-surge.lhns.de`). It returns HTTP 429 to hold an eviction
    while it creates a temporary cloned "surge" pod, and allows the eviction
    once that surge pod is Ready. `failurePolicy: Ignore` (fail-open) so a
    broken operator never wedges a drain; a `--max-surge-seconds` timeout
    bounds the hold.
  - **Surge controller** that garbage-collects surge pods once the evicted pod
    is gone and the ReplicaSet's own replacement is Ready.
  - **Opt-in contract**: namespace label `ha-surge.lhns.de/managed=true` (gates
    the webhook) plus Deployment annotation `ha-surge.lhns.de/enabled=true`
    (per-workload). Safety refusals for RWO/single-writer volumes,
    non-Deployment owners, and already-multi-replica workloads.
  - **Helm chart** (`charts/kube-ha-surge`) with cert-manager (default) or
    self-signed Helm-generated webhook serving certs, RBAC, leader election, and
    a pre-delete hook that clears leftover surge pods on uninstall.
  - **kustomize** manifests under `config/` for `make deploy` / release.yaml.
  - Docs: README, getting-started, reference, limitations, and ADRs 0001–0007.
