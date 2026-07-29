package controller

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/lhns/kube-ha-surge/internal/surge"
)

func i32(i int32) *int32 { return &i }

func dep(ns, name string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: appsv1.DeploymentSpec{
			Replicas: i32(1),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
		},
	}
}

func detachedPod(ns, name, app string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: ns,
		Labels: map[string]string{"app": app, surge.LabelDetached: "true", surge.LabelFor: app},
	}}
}

func readyRSPod(ns, name, app string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{"app": app, "pod-template-hash": "H"}},
		Status:     corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}
}

func reconcileOnce(objs ...client.Object) (client.Client, error) {
	c := fake.NewClientBuilder().WithScheme(clientgoscheme.Scheme).WithObjects(objs...).Build()
	r := NewDetachReconciler(c, logr.Discard())
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "x", Name: "cover"}})
	return c, err
}

func exists(t *testing.T, c client.Client, name string) bool {
	t.Helper()
	err := c.Get(context.Background(), types.NamespacedName{Namespace: "x", Name: name}, &corev1.Pod{})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
	return err == nil
}

func TestReconcile_ReplacementReady_DeletesCover(t *testing.T) {
	c, err := reconcileOnce(dep("x", "web"), detachedPod("x", "cover", "web"), readyRSPod("x", "web-new", "web"))
	if err != nil {
		t.Fatal(err)
	}
	if exists(t, c, "cover") {
		t.Error("cover should be deleted once a Ready replacement exists")
	}
}

func TestReconcile_NoReplacement_KeepsCover(t *testing.T) {
	c, err := reconcileOnce(dep("x", "web"), detachedPod("x", "cover", "web"))
	if err != nil {
		t.Fatal(err)
	}
	if !exists(t, c, "cover") {
		t.Error("cover must be kept while no replacement is Ready (it's the only thing serving)")
	}
}

func TestReconcile_DeploymentGone_DeletesCover(t *testing.T) {
	c, err := reconcileOnce(detachedPod("x", "cover", "web")) // no Deployment
	if err != nil {
		t.Fatal(err)
	}
	if exists(t, c, "cover") {
		t.Error("orphan cover (deployment gone) should be deleted")
	}
}

func TestReconcile_NotDetached_Ignored(t *testing.T) {
	// a normal pod named "cover" should be left alone
	plain := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "cover", Namespace: "x", Labels: map[string]string{"app": "web"}}}
	c, err := reconcileOnce(dep("x", "web"), plain)
	if err != nil {
		t.Fatal(err)
	}
	if !exists(t, c, "cover") {
		t.Error("non-detached pod must not be touched")
	}
}
