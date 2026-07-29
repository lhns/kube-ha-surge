# Reference

Every label, annotation, flag, and Helm value ha-surge understands.

## Opt-in contract — the enabled cascade

A single key, **`ha-surge.lhns.de/enabled`** (`"true"`/`"false"`), is read at
three levels; the most specific one that is set wins (ADR 0009):

```
effective = Deployment annotation  ha-surge.lhns.de/enabled   (overrides all)
         ?? Namespace  label       ha-surge.lhns.de/enabled   (namespace default)
         ?? chart .Values.defaultEnabled                       (cluster floor, default false)
```

| Level | Where | Value | Meaning |
| --- | --- | --- | --- |
| Deployment | annotation `ha-surge.lhns.de/enabled` | `"true"`/`"false"` | Per-workload override. Absent → fall through to the namespace. |
| Namespace | label `ha-surge.lhns.de/enabled` | `"true"`/`"false"` | Namespace-wide default **and** webhook scope (see below). Absent → fall through to `defaultEnabled`. |
| Cluster | value `defaultEnabled` | `true`/`false` (default `false`) | Cluster-wide floor when neither level above is set. |

**Scope is gated at the apiserver, not just in code.** The webhook's
`namespaceSelector` is rendered from `defaultEnabled`, because the apiserver
evaluates it (on namespace **labels**) before the operator runs:

- `defaultEnabled: false` → `matchLabels {ha-surge.lhns.de/enabled: "true"}`: the
  operator is consulted **only** for namespaces labelled enabled.
- `defaultEnabled: true` → `matchExpressions`: every namespace except those
  labelled `enabled=false`, `kube-system`, the release namespace, and any
  `excludeNamespaces`.

Consequence: with `defaultEnabled: false`, a lone Deployment annotation only takes
effect if its **namespace** is at least labelled `enabled=true` (see
[limitations](limitations.md)).

## Safety refusals

Independent of enablement, the operator **skips** (allows the eviction untouched)
when surging is impossible or wrong — enablement only decides *may we*, never
*force on*:

| Refusal | Why |
| --- | --- |
| `spec.strategy.type: Recreate` | The owner declared "never two at once"; surging would violate it. (RollingUpdate already tolerates overlap.) |
| Mounts a `ReadWriteOnce`/single-writer PVC | A cover pod can't co-mount it (Multi-Attach); it would only stall. |
| Already `replicas >= 2` | Normal replication + PDB already cover disruption. |
| Owner is not a Deployment | Out of scope (StatefulSet/bare pod/Job). |
| A Service selects on `pod-template-hash` | Detaching removes that label and would break routing. |

## Labels & annotations ha-surge sets

When it covers an eviction, the webhook **detaches** the doomed pod from its
ReplicaSet — it removes the pod's `pod-template-hash` label so the RS no longer
matches it (and creates its own replacement), while the pod keeps its app labels
so the Service keeps routing to it. It also stamps the following markers. You
don't set them; they're documented so you can select on and reason about them.

| Key | On | Value | Meaning |
| --- | --- | --- | --- |
| `ha-surge.lhns.de/detached` | detached pod label | `"true"` | Marks a pod the operator has detached from its ReplicaSet to cover a drain. The GC controller and the uninstall hook select on this. |
| `ha-surge.lhns.de/for` | detached pod label | `<deployment>` | Which Deployment the detached pod belongs to (its RS ownerRef is gone once detached, so this remembers it). |
| `ha-surge.lhns.de/detached-at` | detached pod annotation | `<RFC3339>` | When the pod was detached, for diagnostics and the fail-open timeout. |

The detached pod deliberately no longer carries the `pod-template-hash` label
the ReplicaSet injects — so the RS ignores it (it drops out of the RS's
replica count, prompting a real replacement, and the RS never deletes it),
while the Service (which selects on app labels, not the hash) still routes
traffic to it until the eviction is finally allowed.

## Manager flags

Set on the `manager` container. The Helm chart wires the ones you'd tune; the
rest match the binary defaults.

| Flag | Default | Helm value | Meaning |
| --- | --- | --- | --- |
| `--leader-elect` | off | `leaderElection.enabled` | Enable leader election (lease in the release namespace). Both replicas serve admission; only the leader runs the GC controller. |
| `--default-enabled` | `false` | `defaultEnabled` | Cluster-wide floor of the enabled cascade (bottom level). Also drives the rendered `namespaceSelector`. |
| `--max-surge-seconds` | `120` | `maxSurgeSeconds` | Max time the webhook holds an eviction waiting for the ReplicaSet's replacement to become Ready before failing open and allowing it. |
| `--health-probe-bind-address` | `:8081` | fixed | Health/readiness probe address (`/healthz`, `/readyz`). |
| `--metrics-bind-address` | `:8080` | fixed | Metrics address. |
| `--webhook-port` | `9443` | fixed | Port the webhook server binds to (Service target port). |
| `--webhook-cert-dir` | `/tmp/k8s-webhook-server/serving-certs` | fixed | Directory holding the serving cert (`tls.crt`/`tls.key`); the cert Secret is mounted here read-only. |
| `--version` | — | — | Print version and exit. |

## Helm values

| Value | Default | Meaning |
| --- | --- | --- |
| `image.repository` | `ghcr.io/lhns/ha-surge` | Manager image repo. |
| `image.tag` | `""` | Image tag; defaults to the chart `appVersion` when empty. |
| `image.pullPolicy` | `IfNotPresent` | Image pull policy. |
| `image.pullSecrets` | `[]` | imagePullSecrets for the pod. |
| `replicaCount` | `2` | Operator replicas. Defaults to 2 for HA (ADR 0010): the operator is on the eviction path, so it must stay up during drains. Both serve admission; only the leader runs GC. |
| `podDisruptionBudget.enabled` | `true` | Render a PDB so a drain can't take both replicas at once. |
| `podDisruptionBudget.maxUnavailable` | `1` | Keeps ≥1 operator replica available during a node drain. |
| `topologySpreadConstraints` | `hostname`, `maxSkew: 1`, `ScheduleAnyway` | Spread the operator replicas across nodes (soft; set `DoNotSchedule` for a hard guarantee). |
| `maxSurgeSeconds` | `120` | Wired to `--max-surge-seconds`. |
| `defaultEnabled` | `false` | Cluster-wide floor of the enabled cascade; also renders the webhook `namespaceSelector` (ADR 0009). Wired to `--default-enabled`. |
| `excludeNamespaces` | `[]` | Extra namespaces never touched when `defaultEnabled: true` (`kube-system` + release namespace always excluded). |
| `serviceAccount.create` | `true` | Create the operator ServiceAccount. |
| `rbac.create` | `true` | Create the ClusterRole + ClusterRoleBinding. |
| `leaderElection.enabled` | `true` | Enable leader election + its namespaced Role. Required for safe multi-replica HA. |
| `webhook.certSource` | `cert-manager` | `cert-manager` (Issuer + Certificate + cainjector) or `helm` (self-signed `genCA`/`genSignedCert`, CA injected into `caBundle`). |
| `webhook.failurePolicy` | `Ignore` | Webhook failure policy. **Keep `Ignore`** — fail-open is mandatory (ADR 0004). |
| `podSecurityContext` | non-root uid 65532, RuntimeDefault seccomp | Pod security context. |
| `securityContext` | no-priv-esc, RO rootfs, drop ALL, uid 65532 | Container security context. |
| `resources` | 50m/64Mi req, 500m/256Mi lim | Manager resources. |
| `livenessProbe` / `readinessProbe` | 15s/20s, 5s/10s | Probe timing. |
| `nodeSelector` / `affinity` / `tolerations` / `priorityClassName` | empty | Scheduling. |
| `podAnnotations` | `{}` | Extra annotations on the operator Pod. |
| `commonLabels` | `{}` | Extra labels on every templated resource. |
| `terminationGracePeriodSeconds` | `10` | Pod termination grace. |
| `cleanup.enabled` | `true` | Render the pre-delete hook that clears leftover detached cover pods on uninstall. |
| `cleanup.activeDeadlineSeconds` | `120` | Job-level backstop for the hook. |
| `cleanup.image.*` | `registry.k8s.io/kubectl:v1.30.0` | Image the hook runs `kubectl delete` from. |

## Fixed resource names

The chart's fixed-contract names (with a release named `ha-surge` in namespace
`ha-surge-system`):

| Resource | Name |
| --- | --- |
| Namespace | `ha-surge-system` |
| ServiceAccount | `ha-surge` |
| ClusterRole / ClusterRoleBinding | `ha-surge-manager` |
| Leader-election Role / RoleBinding | `ha-surge-leader-election` |
| Manager Deployment | `ha-surge-controller` (container `manager`) |
| Webhook Service | `ha-surge-webhook` (443 → 9443) |
| Serving-cert Secret | `ha-surge-webhook-tls` (`tls.crt`/`tls.key`/`ca.crt`) |
| ValidatingWebhookConfiguration | `ha-surge` (webhook `eviction.ha-surge.lhns.de`, path `/validate-eviction`) |

## RBAC

The `ha-surge-manager` ClusterRole grants:

| API group | Resources | Verbs |
| --- | --- | --- |
| core (`""`) | `pods` | get, list, watch, patch, delete |
| core (`""`) | `namespaces` | get, list, watch |
| core (`""`) | `persistentvolumeclaims` | get, list, watch |
| core (`""`) | `services` | get, list, watch |
| core (`""`) | `events` | create, patch |
| `apps` | `deployments`, `replicasets` | get, list, watch |

The `pods` `patch` verb is what detaches the doomed pod (removing its
`pod-template-hash` label); `services` `get`/`list`/`watch` lets the webhook
check whether a Service selects on `pod-template-hash` (which detaching would
break) before acting; `namespaces` `get`/`list`/`watch` reads the namespace-level
`enabled` label for the opt-in cascade (ADR 0009).

Plus a namespaced leader-election Role (`coordination.k8s.io` leases + events)
in the release namespace.
