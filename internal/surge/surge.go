// Package surge holds the pure, side-effect-free building blocks of ha-surge:
// the label/annotation contract, the eviction-webhook decision helpers, and the
// "detach" transformation that removes a doomed pod from its ReplicaSet so the
// RS makes a real replacement while the doomed pod keeps serving. Keeping these
// free of a Kubernetes client makes them trivial to unit-test.
package surge

import (
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

const (
	// AnnotationEnabled on a Deployment opts its pods into make-before-break.
	// Absent/"false" => the webhook passes evictions straight through.
	AnnotationEnabled = "ha-surge.lhns.de/enabled"

	// NamespaceLabelManaged gates the webhook's namespaceSelector; only evictions
	// in namespaces carrying it reach the operator (blast-radius limit). Enforced
	// in the ValidatingWebhookConfiguration, not in code.
	NamespaceLabelManaged = "ha-surge.lhns.de/managed"

	// PodTemplateHashLabel is injected by the Deployment/ReplicaSet and is part of
	// the RS selector but NOT the Service selector. Removing it from a pod detaches
	// it from the RS (which then makes a replacement) while the pod keeps matching
	// the Service (so it keeps serving). This asymmetry is the whole mechanism.
	PodTemplateHashLabel = "pod-template-hash"

	// LabelDetached marks a pod this operator has detached from its ReplicaSet to
	// cover an in-flight eviction. It is the operator's entire record of state.
	LabelDetached = "ha-surge.lhns.de/detached"

	// LabelFor records the Deployment a detached pod belongs to (the RS ownerRef
	// is gone once detached, so we remember it here).
	LabelFor = "ha-surge.lhns.de/for"

	// AnnotationDetachedAt records when the pod was detached (RFC3339), for the
	// fail-open timeout.
	AnnotationDetachedAt = "ha-surge.lhns.de/detached-at"
)

// Enabled reports whether a Deployment has opted in.
func Enabled(dep *appsv1.Deployment) bool {
	return dep.Annotations[AnnotationEnabled] == "true"
}

// IsDetached reports whether this operator has detached the pod.
func IsDetached(pod *corev1.Pod) bool {
	return pod.Labels[LabelDetached] == "true"
}

// PodTemplateHash returns the pod's pod-template-hash label value (may be empty).
func PodTemplateHash(pod *corev1.Pod) string {
	return pod.Labels[PodTemplateHashLabel]
}

// AlreadyHA reports whether the Deployment already runs (intends to run) more
// than one replica, in which case normal replicas + PDB handle disruptions and
// there's nothing for us to do.
func AlreadyHA(dep *appsv1.Deployment) bool {
	return dep.Spec.Replicas != nil && *dep.Spec.Replicas >= 2
}

// SelectorUsesPodTemplateHash reports whether a selector keys on the
// pod-template-hash label. If a Service selected on it, removing the label from
// the pod would drop it out of that Service — so we must not detach. (Standard
// Deployments never do this; Services select on app labels.)
func SelectorUsesPodTemplateHash(selector map[string]string) bool {
	_, ok := selector[PodTemplateHashLabel]
	return ok
}

// HasReadWriteOnceVolume reports whether the pod mounts any RWO PVC. The RS's
// replacement can't co-mount a single-writer volume (Multi-Attach) while the
// detached pod still holds it, so such workloads can't be helped and are passed
// through. The webhook resolves referenced PVCs and passes their access modes.
func HasReadWriteOnceVolume(pod *corev1.Pod, pvcAccessModes map[string][]corev1.PersistentVolumeAccessMode) bool {
	for _, v := range pod.Spec.Volumes {
		if v.PersistentVolumeClaim == nil {
			continue
		}
		if isSingleWriter(pvcAccessModes[v.PersistentVolumeClaim.ClaimName]) {
			return true
		}
	}
	return false
}

func isSingleWriter(modes []corev1.PersistentVolumeAccessMode) bool {
	for _, m := range modes {
		if m == corev1.ReadWriteMany || m == corev1.ReadOnlyMany {
			return false // shareable
		}
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

// MarkDetached returns a deep copy of pod transformed into a detached cover:
// the pod-template-hash label is removed (detaching it from the ReplicaSet, which
// will create a replacement), and our marker labels/annotation are added. Every
// other label — the app labels the Service selects on, and any operator-injected
// labels like network-membership labels — is left untouched, so serving and
// network identity are preserved. The input pod is not modified.
func MarkDetached(pod *corev1.Pod, deployment string, now time.Time) *corev1.Pod {
	out := pod.DeepCopy()
	if out.Labels == nil {
		out.Labels = map[string]string{}
	}
	delete(out.Labels, PodTemplateHashLabel)
	out.Labels[LabelDetached] = "true"
	out.Labels[LabelFor] = deployment
	if out.Annotations == nil {
		out.Annotations = map[string]string{}
	}
	out.Annotations[AnnotationDetachedAt] = now.UTC().Format(time.RFC3339)
	return out
}

// DetachedAt parses the detach timestamp; ok is false if absent/unparseable.
func DetachedAt(pod *corev1.Pod) (t time.Time, ok bool) {
	v := pod.Annotations[AnnotationDetachedAt]
	if v == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

// DesiredReplicas returns the Deployment's desired replica count (default 1).
func DesiredReplicas(dep *appsv1.Deployment) int32 {
	if dep.Spec.Replicas != nil {
		return *dep.Spec.Replicas
	}
	return 1
}

// SelectorMatchLabels returns the Deployment's selector match labels (nil-safe).
func SelectorMatchLabels(dep *appsv1.Deployment) map[string]string {
	if dep.Spec.Selector == nil {
		return nil
	}
	return dep.Spec.Selector.MatchLabels
}

// MatchesLabels reports whether labels contains all of want.
func MatchesLabels(labels, want map[string]string) bool {
	for k, v := range want {
		if labels[k] != v {
			return false
		}
	}
	return true
}
