package surge

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func i32(i int32) *int32 { return &i }

func TestEnabled(t *testing.T) {
	if Enabled(&appsv1.Deployment{}) {
		t.Fatal("no annotation => not enabled")
	}
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{AnnotationEnabled: "true"}}}
	if !Enabled(d) {
		t.Fatal("annotation true => enabled")
	}
	d.Annotations[AnnotationEnabled] = "false"
	if Enabled(d) {
		t.Fatal("annotation false => not enabled")
	}
}

func TestIsDetachedAndPodTemplateHash(t *testing.T) {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"pod-template-hash": "abc"}}}
	if IsDetached(p) {
		t.Error("plain pod is not detached")
	}
	if PodTemplateHash(p) != "abc" {
		t.Error("hash accessor")
	}
	p.Labels[LabelDetached] = "true"
	if !IsDetached(p) {
		t.Error("marked pod is detached")
	}
}

func TestAlreadyHA(t *testing.T) {
	for _, c := range []struct {
		r    *int32
		want bool
	}{{nil, false}, {i32(0), false}, {i32(1), false}, {i32(2), true}, {i32(5), true}} {
		if got := AlreadyHA(&appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: c.r}}); got != c.want {
			t.Errorf("replicas=%v got %v want %v", c.r, got, c.want)
		}
	}
}

func TestSelectorUsesPodTemplateHash(t *testing.T) {
	if SelectorUsesPodTemplateHash(map[string]string{"app": "web"}) {
		t.Error("app-only selector must not flag")
	}
	if !SelectorUsesPodTemplateHash(map[string]string{"app": "web", "pod-template-hash": "x"}) {
		t.Error("hash selector must flag")
	}
}

func TestHasReadWriteOnceVolume(t *testing.T) {
	podWith := func(claims ...string) *corev1.Pod {
		p := &corev1.Pod{}
		for _, c := range claims {
			p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{
				VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: c}},
			})
		}
		return p
	}
	rwo := []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
	rwx := []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}
	rom := []corev1.PersistentVolumeAccessMode{corev1.ReadOnlyMany}
	rwop := []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOncePod}

	if !HasReadWriteOnceVolume(podWith("a"), map[string][]corev1.PersistentVolumeAccessMode{"a": rwo}) {
		t.Error("RWO is single-writer")
	}
	if !HasReadWriteOnceVolume(podWith("a"), map[string][]corev1.PersistentVolumeAccessMode{"a": rwop}) {
		t.Error("RWOncePod is single-writer")
	}
	if HasReadWriteOnceVolume(podWith("a"), map[string][]corev1.PersistentVolumeAccessMode{"a": rwx}) {
		t.Error("RWX shareable")
	}
	if HasReadWriteOnceVolume(podWith("a"), map[string][]corev1.PersistentVolumeAccessMode{"a": rom}) {
		t.Error("ROM shareable")
	}
	if HasReadWriteOnceVolume(podWith(), nil) {
		t.Error("no volumes")
	}
	if !HasReadWriteOnceVolume(podWith("a", "b"), map[string][]corev1.PersistentVolumeAccessMode{"a": rwx, "b": rwo}) {
		t.Error("any RWO blocks")
	}
}

func TestPodReady(t *testing.T) {
	ready := &corev1.Pod{Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	notReady := &corev1.Pod{Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}}}}
	if !PodReady(ready) || PodReady(notReady) || PodReady(&corev1.Pod{}) {
		t.Error("PodReady wrong")
	}
}

func TestMarkDetached(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	in := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web-abc-xyz",
			Namespace: "shop",
			Labels: map[string]string{
				"app":                          "web",
				"pod-template-hash":            "abc",
				"kube-vnet.system/net.cluster": "egress", // an operator-injected label we must NOT touch
			},
			Annotations: map[string]string{"prometheus.io/scrape": "true"},
		},
	}
	out := MarkDetached(in, "web", now)

	// input untouched
	if _, ok := in.Labels["pod-template-hash"]; !ok {
		t.Error("input pod must not be mutated")
	}
	if _, ok := out.Labels["pod-template-hash"]; ok {
		t.Error("pod-template-hash must be removed (detaches from the ReplicaSet)")
	}
	if out.Labels["app"] != "web" {
		t.Error("app label must be kept (Service keeps routing)")
	}
	if out.Labels["kube-vnet.system/net.cluster"] != "egress" {
		t.Error("other injected labels must be left alone (network identity preserved)")
	}
	if out.Labels[LabelDetached] != "true" || out.Labels[LabelFor] != "web" {
		t.Error("detach marker labels missing")
	}
	if out.Annotations["prometheus.io/scrape"] != "true" {
		t.Error("user annotations preserved")
	}
	at, ok := DetachedAt(out)
	if !ok || !at.Equal(now) {
		t.Errorf("detached-at not round-tripped: %v %v", at, ok)
	}
}

func TestDesiredReplicasAndSelector(t *testing.T) {
	if DesiredReplicas(&appsv1.Deployment{}) != 1 {
		t.Error("default desired = 1")
	}
	d := &appsv1.Deployment{Spec: appsv1.DeploymentSpec{
		Replicas: i32(3),
		Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
	}}
	if DesiredReplicas(d) != 3 {
		t.Error("desired from spec")
	}
	if SelectorMatchLabels(d)["app"] != "web" {
		t.Error("selector labels")
	}
	if !MatchesLabels(map[string]string{"app": "web", "x": "y"}, map[string]string{"app": "web"}) {
		t.Error("superset matches")
	}
	if MatchesLabels(map[string]string{"app": "db"}, map[string]string{"app": "web"}) {
		t.Error("mismatch")
	}
}
