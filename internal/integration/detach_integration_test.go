//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	admissionv1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/lhns/kube-ha-surge/internal/controller"
	"github.com/lhns/kube-ha-surge/internal/surge"
	"github.com/lhns/kube-ha-surge/internal/webhook/eviction"
)

func p32(i int32) *int32 { return &i }
func pb(b bool) *bool    { return &b }

func markReady(t *testing.T, ctx context.Context, pod *corev1.Pod) {
	t.Helper()
	pod.Status.Phase = corev1.PodRunning
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	if err := k8sClient.Status().Update(ctx, pod); err != nil {
		t.Fatalf("set pod ready: %v", err)
	}
}

func evictReq(pod *corev1.Pod) admission.Request {
	return admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{Name: pod.Name, Namespace: pod.Namespace}}
}

// TestDetachFlow drives the webhook + controller against a real apiserver.
func TestDetachFlow(t *testing.T) {
	ctx := context.Background()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "detach-it"}}
	if err := k8sClient.Create(ctx, ns); err != nil {
		t.Fatal(err)
	}

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "web", Namespace: ns.Name,
			Annotations: map[string]string{surge.AnnotationEnabled: "true"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: p32(1),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "nginx"}}},
			},
		},
	}
	if err := k8sClient.Create(ctx, dep); err != nil {
		t.Fatal(err)
	}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Name: "web-rs", Namespace: ns.Name,
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", UID: dep.UID, Controller: pb(true)}},
	}, Spec: appsv1.ReplicaSetSpec{
		Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web", "pod-template-hash": "H"}},
		Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web", "pod-template-hash": "H"}},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "nginx"}}},
		},
	}}
	if err := k8sClient.Create(ctx, rs); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "web-rs-abc", Namespace: ns.Name,
		Labels:          map[string]string{"app": "web", "pod-template-hash": "H"},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-rs", UID: rs.UID, Controller: pb(true)}},
	}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "nginx"}}}}
	if err := k8sClient.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	markReady(t, ctx, pod)

	h := eviction.NewHandler(k8sClient, 120*time.Second, logr.Discard())

	// 1. First eviction: must deny 429 and detach the pod on the real apiserver.
	resp := h.Handle(ctx, evictReq(pod))
	if resp.Allowed || resp.Result == nil || resp.Result.Code != 429 {
		t.Fatalf("first eviction: want 429 deny, got %+v", resp)
	}
	got := &corev1.Pod{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: ns.Name, Name: pod.Name}, got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Labels["pod-template-hash"]; ok {
		t.Fatal("pod-template-hash should be removed on the apiserver after detach")
	}
	if !surge.IsDetached(got) || got.Labels[surge.LabelFor] != "web" || got.Labels["app"] != "web" {
		t.Fatalf("detach markers/app label wrong: %v", got.Labels)
	}

	// 2. Still no replacement -> a retry stays denied.
	if r := h.Handle(ctx, evictReq(got)); r.Allowed {
		t.Fatal("retry with no ready replacement should still deny")
	}

	// 3. Bring up the RS's replacement (envtest has no RS controller, so we
	//    create it) -> the retry is allowed.
	repl := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "web-rs-new", Namespace: ns.Name,
		Labels: map[string]string{"app": "web", "pod-template-hash": "H"},
	}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "nginx"}}}}
	if err := k8sClient.Create(ctx, repl); err != nil {
		t.Fatal(err)
	}
	markReady(t, ctx, repl)

	if r := h.Handle(ctx, evictReq(got)); !r.Allowed {
		t.Fatalf("retry with a ready replacement should allow, got %+v", r.Result)
	}

	// 4. The controller deletes the detached cover once the replacement is Ready.
	rec := controller.NewDetachReconciler(k8sClient, logr.Discard())
	if _, err := rec.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns.Name, Name: pod.Name}}); err != nil {
		t.Fatal(err)
	}
	err := k8sClient.Get(ctx, types.NamespacedName{Namespace: ns.Name, Name: pod.Name}, &corev1.Pod{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("detached cover pod should have been deleted, got err=%v", err)
	}
}
