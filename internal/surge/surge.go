// Package surge holds the pure, side-effect-free building blocks of
// kube-ha-surge: the label/annotation contract, the decision helpers used by
// the eviction webhook, and the clone that turns a doomed pod into a temporary
// stand-in ("surge") pod. Keeping these free of a Kubernetes client makes them
// trivial to unit-test.
package surge

import (
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
)

const (
	// AnnotationEnabled on a Deployment opts its pods into make-before-break
	// surging. Absent/"false" => the webhook passes evictions straight through.
	AnnotationEnabled = "ha-surge.lhns.de/enabled"

	// LabelSurge marks a pod created by this operator as a temporary stand-in.
	LabelSurge = "ha-surge.lhns.de/surge"

	// LabelFor records which Deployment a surge pod is standing in for.
	LabelFor = "ha-surge.lhns.de/for"

	// AnnotationReplacing records the doomed pod a surge pod is covering for, so
	// the controller only cleans the stand-in up once that pod is actually gone
	// (evicted) and the ReplicaSet's own replacement is Ready.
	AnnotationReplacing = "ha-surge.lhns.de/replacing"

	// NamespaceLabelManaged gates the webhook's namespaceSelector; only
	// evictions in namespaces carrying it reach the operator (blast-radius
	// limit). Enforced in the ValidatingWebhookConfiguration, not in code.
	NamespaceLabelManaged = "ha-surge.lhns.de/managed"

	// podTemplateHashLabel is injected by the ReplicaSet; a surge pod must NOT
	// carry it, otherwise it would match the RS selector. Stripping it means the
	// RS ignores the surge pod entirely while the Service (which selects on app
	// labels, not the hash) still routes to it.
	podTemplateHashLabel = "pod-template-hash"
)

// OperatorManagedLabelPrefixes are label prefixes injected onto pods by other
// operators at admission/runtime. A clone must NOT carry them: the owning
// operator re-derives them, and some are guarded by admission policies that
// reject any user-set value — e.g. kube-vnet's `kube-vnet.system/` labels are
// denied by a ValidatingAdmissionPolicy, which would block the surge pod's
// creation. Extend this list for other such operators in your cluster.
var OperatorManagedLabelPrefixes = []string{
	"kube-vnet.system/",
}

func isOperatorManagedLabel(key string) bool {
	for _, p := range OperatorManagedLabelPrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

// IsSurgePod reports whether a pod is one of our stand-ins.
func IsSurgePod(pod *corev1.Pod) bool {
	return pod.Labels[LabelSurge] == "true"
}

// Enabled reports whether a Deployment has opted in.
func Enabled(dep *appsv1.Deployment) bool {
	return dep.Annotations[AnnotationEnabled] == "true"
}

// AlreadyHA reports whether the Deployment already runs (intends to run) more
// than one replica, in which case normal replicas + PDB handle disruptions and
// surging is unnecessary.
func AlreadyHA(dep *appsv1.Deployment) bool {
	return dep.Spec.Replicas != nil && *dep.Spec.Replicas >= 2
}

// HasReadWriteOnceVolume reports whether the pod mounts any RWO PVC. A stand-in
// can't co-mount a single-writer volume (Multi-Attach), so such workloads can't
// be helped and must be passed through. Only inline PVC access modes are
// visible here; the webhook resolves referenced PVCs and merges the result.
func HasReadWriteOnceVolume(pod *corev1.Pod, pvcAccessModes map[string][]corev1.PersistentVolumeAccessMode) bool {
	for _, v := range pod.Spec.Volumes {
		if v.PersistentVolumeClaim == nil {
			continue
		}
		modes := pvcAccessModes[v.PersistentVolumeClaim.ClaimName]
		if isSingleWriter(modes) {
			return true
		}
	}
	return false
}

func isSingleWriter(modes []corev1.PersistentVolumeAccessMode) bool {
	hasShared := false
	for _, m := range modes {
		if m == corev1.ReadWriteMany || m == corev1.ReadOnlyMany {
			hasShared = true
		}
	}
	// RWO (or ReadWriteOncePod) with no shared mode => single writer.
	if hasShared {
		return false
	}
	for _, m := range modes {
		if m == corev1.ReadWriteOnce || m == corev1.ReadWriteOncePod {
			return true
		}
	}
	return false
}

// PodReady reports whether the pod's Ready condition is true.
func PodReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// SurgeName is the deterministic name of the stand-in for a Deployment. A fixed
// name makes creation idempotent (a second attempt collides with AlreadyExists)
// and guarantees at most one stand-in per target.
func SurgeName(deployment string) string {
	const suffix = "-hasurge"
	max := 63 - len(suffix)
	if len(deployment) > max {
		deployment = deployment[:max]
	}
	return deployment + suffix
}

// BuildSurgePod clones a doomed pod into a stand-in for its Deployment: same
// spec and app labels (so the Service routes to it) but stripped of node
// pinning and the ReplicaSet's pod-template-hash, marked as a surge pod, and
// owned (controller=true) by the target Deployment so the ReplicaSet ignores it
// and it is garbage-collected with the Deployment.
func BuildSurgePod(target *corev1.Pod, dep *appsv1.Deployment) *corev1.Pod {
	labels := map[string]string{}
	for k, v := range target.Labels {
		if k == podTemplateHashLabel || isOperatorManagedLabel(k) {
			continue
		}
		labels[k] = v
	}
	labels[LabelSurge] = "true"
	labels[LabelFor] = dep.Name

	spec := *target.Spec.DeepCopy()
	// Let the scheduler place the stand-in on a healthy node (the target's node
	// is being drained/cordoned).
	spec.NodeName = ""
	spec.Hostname = ""

	annotations := cloneAnnotations(target.Annotations)
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[AnnotationReplacing] = target.Name

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        SurgeName(dep.Name),
			Namespace:   target.Namespace,
			Labels:      labels,
			Annotations: annotations,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         "apps/v1",
				Kind:               "Deployment",
				Name:               dep.Name,
				UID:                dep.UID,
				Controller:         ptrBool(true),
				BlockOwnerDeletion: ptrBool(false),
			}},
		},
		Spec: spec,
	}
}

// cloneAnnotations copies user annotations but drops ones that must not be
// carried onto a brand-new pod.
func cloneAnnotations(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		switch k {
		case "kubectl.kubernetes.io/last-applied-configuration",
			"cni.projectcalico.org/podIP",
			"cni.projectcalico.org/podIPs":
			continue
		}
		out[k] = v
	}
	return out
}

func ptrBool(b bool) *bool { return &b }

// RandSuffix is exposed for callers that want a unique name variant; unused by
// the deterministic SurgeName path but handy in tests.
func RandSuffix() string { return rand.String(5) }
