# Reference

Every label, annotation, flag, and Helm value kube-ha-surge understands.

## Opt-in contract

kube-ha-surge acts only when **both** gates are set.

| Gate | Where | Value | Meaning |
| --- | --- | --- | --- |
| `ha-surge.lhns.de/managed` | **Namespace** label | `"true"` | The eviction webhook's `namespaceSelector`. Only evictions in labelled namespaces reach the operator (blast-radius limit). Enforced in the `ValidatingWebhookConfiguration`, not in code. |
| `ha-surge.lhns.de/enabled` | **Deployment** annotation | `"true"` | Per-workload opt-in. Absent/`"false"` → the webhook passes the workload's evictions straight through. |

## Labels & annotations kube-ha-surge sets

These are stamped by the operator on the surge (stand-in) pods it creates. You
don't set them; they're documented so you can select on and reason about them.

| Key | On | Value | Meaning |
| --- | --- | --- | --- |
| `ha-surge.lhns.de/surge` | surge pod label | `"true"` | Marks a pod as an operator-created stand-in. The GC controller and the uninstall hook select on this. |
| `ha-surge.lhns.de/for` | surge pod label | `<deployment>` | Which Deployment the surge pod is standing in for. |
| `ha-surge.lhns.de/replacing` | surge pod annotation | `<pod>` | The doomed pod this stand-in is covering for. The controller only cleans the stand-in up once that pod is actually gone and the ReplicaSet's own replacement is Ready. |

The surge pod deliberately does **not** carry the `pod-template-hash` label the
ReplicaSet injects — so the RS ignores it (never counts it toward `replicas`,
never deletes it), while the Service (which selects on app labels, not the hash)
still routes traffic to it.

## Manager flags

Set on the `manager` container. The Helm chart wires the ones you'd tune; the
rest match the binary defaults.

| Flag | Default | Helm value | Meaning |
| --- | --- | --- | --- |
| `--leader-elect` | off | `leaderElection.enabled` | Enable leader election (lease in the release namespace). |
| `--max-surge-seconds` | `120` | `maxSurgeSeconds` | Max time the webhook holds an eviction waiting for the stand-in to become Ready before failing open and allowing it. |
| `--health-probe-bind-address` | `:8081` | fixed | Health/readiness probe address (`/healthz`, `/readyz`). |
| `--metrics-bind-address` | `:8080` | fixed | Metrics address. |
| `--webhook-port` | `9443` | fixed | Port the webhook server binds to (Service target port). |
| `--webhook-cert-dir` | `/tmp/k8s-webhook-server/serving-certs` | fixed | Directory holding the serving cert (`tls.crt`/`tls.key`); the cert Secret is mounted here read-only. |
| `--version` | — | — | Print version and exit. |

## Helm values

| Value | Default | Meaning |
| --- | --- | --- |
| `image.repository` | `ghcr.io/lhns/kube-ha-surge` | Manager image repo. |
| `image.tag` | `""` | Image tag; defaults to the chart `appVersion` when empty. |
| `image.pullPolicy` | `IfNotPresent` | Image pull policy. |
| `image.pullSecrets` | `[]` | imagePullSecrets for the pod. |
| `replicaCount` | `1` | Operator replicas. Leader election makes 2 safe for operator HA. |
| `maxSurgeSeconds` | `120` | Wired to `--max-surge-seconds`. |
| `serviceAccount.create` | `true` | Create the operator ServiceAccount. |
| `rbac.create` | `true` | Create the ClusterRole + ClusterRoleBinding. |
| `leaderElection.enabled` | `true` | Enable leader election + its namespaced Role. |
| `webhook.certSource` | `cert-manager` | `cert-manager` (Issuer + Certificate + cainjector) or `helm` (self-signed `genCA`/`genSignedCert`, CA injected into `caBundle`). |
| `webhook.failurePolicy` | `Ignore` | Webhook failure policy. **Keep `Ignore`** — fail-open is mandatory (ADR 0004). |
| `managedNamespaceSelector` | `matchLabels: {ha-surge.lhns.de/managed: "true"}` | Used verbatim as the webhook's `namespaceSelector`. |
| `podSecurityContext` | non-root uid 65532, RuntimeDefault seccomp | Pod security context. |
| `securityContext` | no-priv-esc, RO rootfs, drop ALL, uid 65532 | Container security context. |
| `resources` | 50m/64Mi req, 500m/256Mi lim | Manager resources. |
| `livenessProbe` / `readinessProbe` | 15s/20s, 5s/10s | Probe timing. |
| `nodeSelector` / `affinity` / `tolerations` / `priorityClassName` | empty | Scheduling. |
| `podAnnotations` | `{}` | Extra annotations on the operator Pod. |
| `commonLabels` | `{}` | Extra labels on every templated resource. |
| `terminationGracePeriodSeconds` | `10` | Pod termination grace. |
| `cleanup.enabled` | `true` | Render the pre-delete hook that clears leftover surge pods on uninstall. |
| `cleanup.activeDeadlineSeconds` | `120` | Job-level backstop for the hook. |
| `cleanup.image.*` | `registry.k8s.io/kubectl:v1.30.0` | Image the hook runs `kubectl delete` from. |

## Fixed resource names

The chart's fixed-contract names (with a release named `kube-ha-surge` in
namespace `kube-ha-surge-system`):

| Resource | Name |
| --- | --- |
| Namespace | `kube-ha-surge-system` |
| ServiceAccount | `kube-ha-surge` |
| ClusterRole / ClusterRoleBinding | `kube-ha-surge-manager` |
| Leader-election Role / RoleBinding | `kube-ha-surge-leader-election` |
| Manager Deployment | `kube-ha-surge-controller` (container `manager`) |
| Webhook Service | `kube-ha-surge-webhook` (443 → 9443) |
| Serving-cert Secret | `kube-ha-surge-webhook-tls` (`tls.crt`/`tls.key`/`ca.crt`) |
| ValidatingWebhookConfiguration | `kube-ha-surge` (webhook `eviction.ha-surge.lhns.de`, path `/validate-eviction`) |

## RBAC

The `kube-ha-surge-manager` ClusterRole grants:

| API group | Resources | Verbs |
| --- | --- | --- |
| core (`""`) | `pods` | get, list, watch, create, delete, patch |
| core (`""`) | `persistentvolumeclaims` | get, list, watch |
| core (`""`) | `events` | create, patch |
| `apps` | `deployments`, `replicasets` | get, list, watch |
| `discovery.k8s.io` | `endpointslices` | get, list, watch |

Plus a namespaced leader-election Role (`coordination.k8s.io` leases + events)
in the release namespace.
