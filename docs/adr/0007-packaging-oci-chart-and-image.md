# 0007 — Packaging: OCI image + Helm chart to GHCR

Status: Accepted

Date: 2026-07-29

## Context

kube-ha-surge ships two artifacts: a container image (the operator) and a Helm
chart (how you install it). We need a release process that's reproducible,
verifiable, and consistent with the sibling lhns operators, so users can pin and
verify what they run.

## Decision

- **Distribution:** publish the operator **image** and the **Helm chart as an
  OCI artifact** to **GitHub Container Registry (GHCR)**:
  - image `ghcr.io/lhns/kube-ha-surge`
  - chart `oci://ghcr.io/lhns/charts/kube-ha-surge`
- **Trigger:** releases are cut from **git tags** (`vX.Y.Z`). The release
  workflow (already present as `.github/workflows/release.yaml`) stamps the
  version/commit/date into the binary, builds a multi-arch image, and pushes both
  artifacts. CI (`ci.yaml`) does not release — it only builds, tests, scans, and
  lints on push/PR to `main`.
- **Image:** multi-stage `Dockerfile` on `gcr.io/distroless/static:nonroot`,
  non-root uid 65532, static `CGO_ENABLED=0` binary, no shell — matching the
  security posture in [ADR 0005](0005-go-controller-runtime.md).
- **Supply chain:** released images and charts are signed with **cosign**
  (keyless, GitHub OIDC) and ship **SPDX SBOMs** as cosign attestations / release
  assets.
- **Versioning:** chart `version`/`appVersion` track the release; the chart's
  `image.tag` defaults to `appVersion`. Pre-1.0, pin an exact version.

## Consequences

- **One registry, standard tooling.** `helm install ... oci://ghcr.io/...` and
  `docker pull` need no extra index or repo config; GHCR hosts both.
- **Tag-driven releases are auditable** — every release corresponds to a tag, and
  the binary self-reports its version (`--version`).
- **Verifiable provenance.** cosign signatures + SBOMs let adopters verify what
  they run and scan its dependencies; this backs the claims in
  [`../../SECURITY.md`](../../SECURITY.md).
- **CI/release split** keeps `main` fast (no publishing on every push) while
  guaranteeing releases are built the same way each time.

## Alternatives considered

- **A classic Helm HTTP repo (`index.yaml` on GitHub Pages).** Rejected: OCI is
  the current Helm-recommended distribution, keeps the chart next to the image in
  one registry, and needs no separate Pages pipeline.
- **Docker Hub / other registry.** Rejected: GHCR is co-located with the source,
  integrates with GitHub OIDC for keyless signing, and matches the other lhns
  operators.
- **Branch/commit-driven "latest" releases.** Rejected for released artifacts:
  unpinnable and unauditable. `main` builds stay in CI; only tags publish
  versioned artifacts.
- **No signing / no SBOM.** Rejected: supply-chain verification is a stated
  security property, cheap to add via keyless cosign in CI.
