# 0006 — Webhook serving cert: cert-manager or Helm-generated

Status: Accepted

Date: 2026-07-29

## Context

The eviction webhook ([ADR 0001](0001-eviction-admission-webhook-trigger.md)) is
served over HTTPS, and the apiserver validates the webhook server's certificate
against the `caBundle` in the `ValidatingWebhookConfiguration`. So we need:

- a serving cert + key mounted into the manager pod (at
  `--webhook-cert-dir=/tmp/k8s-webhook-server/serving-certs`), with SANs for the
  webhook Service DNS name;
- the matching CA in the webhook config's `caBundle`;
- a way to keep the two in sync across install and upgrade, without rotating the
  cert on every reconcile (which would churn the webhook and risk a window where
  the mounted cert and the published caBundle disagree).

Clusters differ: many run **cert-manager** already and want the operator to fit
that; others don't and want zero extra dependencies.

## Decision

Support **two cert sources**, selected by `webhook.certSource` in the chart:

### `cert-manager` (default)

Render a selfSigned `Issuer` (`ha-surge-selfsigned`) and a `Certificate`
(`ha-surge-webhook`, secret `ha-surge-webhook-tls`, dnsNames
`ha-surge-webhook.ha-surge-system.svc` and `.svc.cluster.local`).
Annotate the webhook config with
`cert-manager.io/inject-ca-from: ha-surge-system/ha-surge-webhook` and
leave `caBundle` empty — cert-manager's **cainjector** fills it. cert-manager
owns rotation.

### `helm` (dependency-free fallback)

Generate a self-signed CA + serving cert with Helm's **`genCA`/`genSignedCert`**
at template time, store it in the `ha-surge-webhook-tls` Secret, and inject
the CA into the webhook config's `caBundle` (base64) directly. On `helm upgrade`
the existing Secret is **reused via `lookup`** so the cert does not rotate every
reconcile — regeneration happens only if the Secret is missing.

Because `genCA`/`genSignedCert` are non-deterministic, the material is generated
**exactly once per render** and cached on `.Values` so both the Secret template
and the webhook-config template read the same CA within a single `helm template`
pass (order-independent). Without that, the Secret and the `caBundle` could
disagree.

## Consequences

- **Fits both worlds.** cert-manager users get managed rotation and the standard
  cainjector flow; everyone else gets a working webhook with no extra operator to
  install.
- **No rotation churn** on the Helm path — `lookup` reuse keeps the cert stable
  across upgrades, so the webhook isn't disrupted on every `helm upgrade`.
- **The Helm path's cert is long-lived and self-signed** — acceptable for an
  internal webhook (the trust anchor is the caBundle we publish, not a public
  CA), but it won't auto-rotate; a very long-lived install should rotate by
  deleting the Secret and upgrading, or switch to cert-manager.
- The non-Helm `config/` (kustomize / `make deploy`) path ships a **placeholder
  caBundle** and expects the cert Secret to be supplied out of band — it's a dev
  convenience, not a production install path.
- A third implicit option (`external`: user supplies the Secret, chart only
  renders the webhook config) is easy to add later but not wired for v1.

## Alternatives considered

- **cert-manager only.** Rejected: forces a cluster-wide dependency on every
  adopter, including small clusters that just want the operator.
- **Helm-generated only.** Rejected: cert-manager shops expect the managed
  `Certificate` + cainjector flow and rotation; hard-coding self-signed certs
  would be a poor fit and a rotation liability there.
- **A cert-generating init/hook Job** (e.g. `kube-webhook-certgen`). Rejected for
  v1: adds a job, RBAC to patch the webhook config, and ordering complexity. The
  `genSignedCert` + `lookup` approach is simpler and covers the dependency-free
  case without an extra pod.
- **Rotate the Helm cert every render.** Rejected: churns the webhook and opens
  a Secret-vs-caBundle mismatch window. `lookup` reuse is the whole point.
