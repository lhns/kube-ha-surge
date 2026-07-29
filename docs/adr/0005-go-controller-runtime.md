# 0005 — Go + controller-runtime

Status: Accepted

Date: 2026-07-29

## Context

kube-ha-surge is an admission webhook plus a garbage-collection controller. It
needs: an HTTPS webhook server with serving-cert handling, a typed Kubernetes
client with caching informers (to read Deployments, ReplicaSets, Pods, PVCs,
EndpointSlices cheaply), leader election, health/readiness probes, and metrics.
It should also match the conventions of the other lhns operators
(`kube-vnet`, `kube-pv-reaper`) so contributors move between them without
relearning project shape.

## Decision

Build it in **Go** on **`sigs.k8s.io/controller-runtime`**, following the
kubebuilder-style layout (`cmd/`, `internal/controller/`,
`internal/webhook/...`, `config/`) without requiring the kubebuilder CLI —
`controller-gen` alone generates the RBAC from `+kubebuilder:rbac` markers.

controller-runtime gives us, out of the box, exactly the pieces above:
`Manager`, the webhook server (`webhook.NewServer` + `admission.Handler`),
cached and direct clients, `LeaderElection`, `AddHealthzCheck`/`AddReadyzCheck`,
and a metrics server. The webhook handler is a plain `admission.Handler`
returning `admission.Response` — including the 429-carrying deny
([ADR 0001](0001-eviction-admission-webhook-trigger.md)).

## Consequences

- **Same library every Kubernetes operator uses**, and the same shape as the
  sibling lhns operators — upstream fixes and familiar ergonomics.
- The webhook reads Deployments/ReplicaSets/Pods/PVCs directly (uncached) for
  the admission decision — eviction QPS is tiny and a direct read avoids a lazy
  informer warmup racing the first admission call; the GC controller uses the
  cached client. controller-runtime supports both trivially.
- **Version coupling:** controller-runtime tracks Kubernetes versions closely
  (e.g. `v0.19.x` ↔ k8s 1.31). Dependency bumps must move in step with the
  cluster baseline; Dependabot groups `k8s.io/*` + `sigs.k8s.io/*` so they move
  together.
- Go's static distroless binary fits the security posture (non-root uid 65532,
  read-only rootfs, no shell) with no runtime deps.

## Alternatives considered

- **kubebuilder-scaffolded project.** Same result, but the CLI isn't required —
  files are authored directly and `controller-gen` covers codegen. No functional
  difference; we skip the scaffolding step.
- **`operator-sdk`.** A wrapper over controller-runtime; adds Helm/Ansible
  operator types we don't need. No benefit here.
- **Raw `client-go` + a hand-rolled webhook server.** Rejected: we'd
  reimplement caching, leader election, cert wiring, and admission plumbing that
  controller-runtime already provides and battle-tests.
- **A non-Go language (e.g. a shell/kubectl loop like kube-pv-reaper).**
  Rejected: kube-pv-reaper's finalizer-watch loop is simple enough for a shell
  script, but an admission webhook needs a real HTTPS server, TLS, and typed
  admission decoding — controller-runtime is the right weight for that.
