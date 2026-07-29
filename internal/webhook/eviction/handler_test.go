package eviction

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	admissionv1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/lhns/kube-ha-surge/internal/surge"
)

func i32(i int32) *int32 { return &i }
func pbool(b bool) *bool { return &b }

const hash = "H"

// build a Deployment -> ReplicaSet -> Pod owner chain.
func chain(ns, dep string, replicas int32, opted bool) (*appsv1.Deployment, *appsv1.ReplicaSet, *corev1.Pod) {
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: dep, Namespace: ns, UID: types.UID(dep + "-uid")},
		Spec: appsv1.DeploymentSpec{
			Replicas: i32(replicas),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": dep}},
		},
	}
	if opted {
		d.Annotations = map[string]string{surge.AnnotationEnabled: "true"}
	}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Name: dep + "-rs", Namespace: ns,
		OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: dep, UID: d.UID, Controller: pbool(true)}},
	}}
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: dep + "-rs-pod", Namespace: ns,
		Labels:          map[string]string{"app": dep, "pod-template-hash": hash},
		OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: rs.Name, Controller: pbool(true)}},
	}}
	return d, rs, p
}

func readyPod(ns, name, app string, detached bool) *corev1.Pod {
	labels := map[string]string{"app": app, "pod-template-hash": hash}
	if detached {
		labels = map[string]string{"app": app, surge.LabelDetached: "true", surge.LabelFor: app}
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
		Status:     corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}
}

func handlerWith(objs ...client.Object) *Handler {
	c := fake.NewClientBuilder().WithScheme(clientgoscheme.Scheme).WithObjects(objs...).Build()
	return NewHandler(c, 120*time.Second, logr.Discard())
}

func req(pod *corev1.Pod) admission.Request {
	return admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{Name: pod.Name, Namespace: pod.Namespace}}
}

func allowed(t *testing.T, r admission.Response) {
	t.Helper()
	if !r.Allowed {
		t.Fatalf("expected allow, got deny: %+v", r.Result)
	}
}
func denied429(t *testing.T, r admission.Response) {
	t.Helper()
	if r.Allowed || r.Result == nil || r.Result.Code != 429 {
		t.Fatalf("expected 429 deny, got %+v", r)
	}
}

func TestHandle_PodNotFound_Allows(t *testing.T) {
	allowed(t, handlerWith().Handle(context.Background(), admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{Name: "ghost", Namespace: "x"}}))
}

func TestHandle_NotDeploymentOwned_Allows(t *testing.T) {
	bare := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "bare", Namespace: "x"}}
	allowed(t, handlerWith(bare).Handle(context.Background(), req(bare)))
}

func TestHandle_NotOptedIn_Allows(t *testing.T) {
	d, rs, p := chain("x", "web", 1, false)
	allowed(t, handlerWith(d, rs, p).Handle(context.Background(), req(p)))
}

func TestHandle_AlreadyHA_Allows(t *testing.T) {
	d, rs, p := chain("x", "web", 2, true)
	allowed(t, handlerWith(d, rs, p).Handle(context.Background(), req(p)))
}

func TestHandle_RWO_Allows(t *testing.T) {
	d, rs, p := chain("x", "web", 1, true)
	p.Spec.Volumes = []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}}}}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "x"},
		Spec:       corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}},
	}
	allowed(t, handlerWith(d, rs, p, pvc).Handle(context.Background(), req(p)))
}

func TestHandle_ServiceSelectsOnHash_Allows(t *testing.T) {
	d, rs, p := chain("x", "web", 1, true)
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "x"},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "web", "pod-template-hash": hash}},
	}
	allowed(t, handlerWith(d, rs, p, svc).Handle(context.Background(), req(p)))
}

func TestHandle_FirstEviction_DetachesAndDenies429(t *testing.T) {
	d, rs, p := chain("x", "web", 1, true)
	h := handlerWith(d, rs, p)
	denied429(t, h.Handle(context.Background(), req(p)))

	// the pod must now be detached: hash removed, marker labels set
	got := &corev1.Pod{}
	if err := h.Client.Get(context.Background(), types.NamespacedName{Namespace: "x", Name: p.Name}, got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Labels["pod-template-hash"]; ok {
		t.Error("pod-template-hash must be removed by the detach patch")
	}
	if !surge.IsDetached(got) || got.Labels[surge.LabelFor] != "web" {
		t.Errorf("detach markers missing: %v", got.Labels)
	}
	if got.Labels["app"] != "web" {
		t.Error("app label must survive the detach")
	}
}

func TestHandle_Detached_NoReplacement_Denies429(t *testing.T) {
	d, _, p := chain("x", "web", 1, true)
	det := surge.MarkDetached(p, "web", time.Now())
	denied429(t, handlerWith(d, det).Handle(context.Background(), req(det)))
}

func TestHandle_Detached_ReplacementReady_Allows(t *testing.T) {
	d, _, p := chain("x", "web", 1, true)
	det := surge.MarkDetached(p, "web", time.Now())
	repl := readyPod("x", "web-rs-new", "web", false) // RS's fresh pod, Ready, not detached
	allowed(t, handlerWith(d, det, repl).Handle(context.Background(), req(det)))
}

func TestHandle_Detached_Timeout_FailsOpen(t *testing.T) {
	d, _, p := chain("x", "web", 1, true)
	det := surge.MarkDetached(p, "web", time.Now().Add(-10*time.Minute)) // detached long ago
	// no replacement present; past MaxSurge => fail open
	allowed(t, handlerWith(d, det).Handle(context.Background(), req(det)))
}

func TestHandle_Detached_DeploymentGone_Allows(t *testing.T) {
	_, _, p := chain("x", "web", 1, true)
	det := surge.MarkDetached(p, "web", time.Now())
	allowed(t, handlerWith(det).Handle(context.Background(), req(det))) // no Deployment in the client
}
