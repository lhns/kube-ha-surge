# 0011 — Release signing scope: tagged releases only, image SBOM only

Status: Accepted. Narrows the supply-chain decision in [0007](0007-packaging-oci-chart-and-image.md).
Date: 2026-11-22

## Context

The release workflow moves to the build and release shape shared with the sibling operators. It
used to sign dev builds as well, and to run syft on the chart `.tgz`, which finds nothing inside a
Helm chart. Nothing verifies dev builds, and each signature is a public Rekor entry.

## Decision

- A `vX.Y.Z` tag signs the image digest and the chart digest with cosign keyless (GitHub OIDC) and
  attests an SPDX SBOM of the **image**. The chart gets a signature, no SBOM.
- `id-token: write` is granted to the `release` job only.
- Dev builds (every non-Dependabot branch) are published unsigned; their image SBOM is still
  generated, so that step stays exercised.

## Consequences

- **Pro**: Releases stay verifiable as 0007 and `SECURITY.md` promise, with no empty chart SBOM.
- **Con**: A dev build cannot be verified; install a release where provenance matters.

## Alternatives considered

- **Keep signing dev builds.** Rejected: nothing verifies them, and every push would add public
  Rekor entries and `.sig`/`.att` tags on ghcr.
