package surge

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func i32(i int32) *int32 { return &i }

func TestIsSurgePod(t *testing.T) {
	if IsSurgePod(&corev1.Pod{}) {
		t.Fatal("empty pod should not be a surge pod")
	}
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{LabelSurge: "true"}}}
	if !IsSurgePod(p) {
		t.Fatal("labelled pod should be a surge pod")
	}
}

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

func TestAlreadyHA(t *testing.T) {
	cases := []struct {
		replicas *int32
		want     bool
	}{
		{nil, false},
		{i32(1), false},
		{i32(2), true},
		{i32(3), true},
		{i32(0), false},
	}
	for _, c := range cases {
		d := &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: c.replicas}}
		if got := AlreadyHA(d); got != c.want {
			t.Errorf("replicas=%v: got %v want %v", c.replicas, got, c.want)
		}
	}
}

func TestHasReadWriteOnceVolume(t *testing.T) {
	podWith := func(claims ...string) *corev1.Pod {
		p := &corev1.Pod{}
		for _, c := range claims {
			p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: c},
				},
			})
		}
		return p
	}
	rwo := []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
	rwx := []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}
	rom := []corev1.PersistentVolumeAccessMode{corev1.ReadOnlyMany}
	rwop := []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOncePod}

	if !HasReadWriteOnceVolume(podWith("a"), map[string][]corev1.PersistentVolumeAccessMode{"a": rwo}) {
		t.Error("RWO should be single-writer")
	}
	if !HasReadWriteOnceVolume(podWith("a"), map[string][]corev1.PersistentVolumeAccessMode{"a": rwop}) {
		t.Error("ReadWriteOncePod should be single-writer")
	}
	if HasReadWriteOnceVolume(podWith("a"), map[string][]corev1.PersistentVolumeAccessMode{"a": rwx}) {
		t.Error("RWX should be shareable")
	}
	if HasReadWriteOnceVolume(podWith("a"), map[string][]corev1.PersistentVolumeAccessMode{"a": rom}) {
		t.Error("ROM should be shareable")
	}
	if HasReadWriteOnceVolume(podWith(), nil) {
		t.Error("no volumes => not single-writer")
	}
	// one shareable + one RWO => blocked (the RWO can't be co-mounted).
	modes := map[string][]corev1.PersistentVolumeAccessMode{"a": rwx, "b": rwo}
	if !HasReadWriteOnceVolume(podWith("a", "b"), modes) {
		t.Error("any RWO volume should block")
	}
}

func TestSurgeName(t *testing.T) {
	if got := SurgeName("web"); got != "web-hasurge" {
		t.Errorf("got %q", got)
	}
	long := ""
	for i := 0; i < 80; i++ {
		long += "x"
	}
	got := SurgeName(long)
	if len(got) > 63 {
		t.Errorf("name too long: %d", len(got))
	}
	if got[len(got)-8:] != "-hasurge" {
		t.Errorf("suffix lost: %q", got)
	}
}

func TestBuildSurgePod(t *testing.T) {
	target := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web-abc123-xyz",
			Namespace: "shop",
			Labels: map[string]string{
				"app":                    "web",
				"pod-template-hash":      "abc123",
				"injected.example.com/x": "y",
			},
			Annotations: map[string]string{
				"prometheus.io/scrape":                             "true",
				"kubectl.kubernetes.io/last-applied-configuration": "{...}",
			},
		},
		Spec: corev1.PodSpec{
			NodeName:   "worker-02",
			Containers: []corev1.Container{{Name: "web", Image: "nginx"}},
		},
	}
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", UID: "dep-uid"}}

	s := BuildSurgePod(target, dep, []string{"injected.example.com/"})

	if s.Name != "web-hasurge" {
		t.Errorf("name %q", s.Name)
	}
	if s.Namespace != "shop" {
		t.Errorf("namespace %q", s.Namespace)
	}
	if _, ok := s.Labels["pod-template-hash"]; ok {
		t.Error("pod-template-hash must be stripped so the RS ignores the surge pod")
	}
	if _, ok := s.Labels["injected.example.com/x"]; ok {
		t.Error("labels matching a configured strip prefix must be dropped")
	}
	if s.Labels["app"] != "web" {
		t.Error("app label must be kept so the Service routes to it")
	}
	if s.Labels[LabelSurge] != "true" || s.Labels[LabelFor] != "web" {
		t.Error("surge marker labels missing")
	}
	if s.Annotations[AnnotationReplacing] != "web-abc123-xyz" {
		t.Errorf("replacing annotation = %q", s.Annotations[AnnotationReplacing])
	}
	if s.Annotations["prometheus.io/scrape"] != "true" {
		t.Error("user annotations should be preserved")
	}
	if _, ok := s.Annotations["kubectl.kubernetes.io/last-applied-configuration"]; ok {
		t.Error("last-applied-configuration should be dropped")
	}
	if s.Spec.NodeName != "" {
		t.Error("nodeName must be cleared so the scheduler can place it on a healthy node")
	}
	if len(s.Spec.Containers) != 1 || s.Spec.Containers[0].Image != "nginx" {
		t.Error("spec should be cloned")
	}
	if len(s.OwnerReferences) != 1 {
		t.Fatalf("want 1 owner ref, got %d", len(s.OwnerReferences))
	}
	ref := s.OwnerReferences[0]
	if ref.Kind != "Deployment" || ref.Name != "web" || ref.UID != "dep-uid" {
		t.Errorf("owner ref wrong: %+v", ref)
	}
	if ref.Controller == nil || !*ref.Controller {
		t.Error("owner ref must be controller=true so the ReplicaSet won't adopt/count it")
	}
}

func TestPodReady(t *testing.T) {
	notReady := &corev1.Pod{Status: corev1.PodStatus{Conditions: []corev1.PodCondition{
		{Type: corev1.PodReady, Status: corev1.ConditionFalse},
	}}}
	ready := &corev1.Pod{Status: corev1.PodStatus{Conditions: []corev1.PodCondition{
		{Type: corev1.PodReady, Status: corev1.ConditionTrue},
	}}}
	if PodReady(notReady) {
		t.Error("false condition => not ready")
	}
	if !PodReady(ready) {
		t.Error("true condition => ready")
	}
	if PodReady(&corev1.Pod{}) {
		t.Error("no condition => not ready")
	}
}
