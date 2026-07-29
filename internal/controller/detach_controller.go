// Package controller garbage-collects the pods the eviction webhook detaches
// from their ReplicaSet. The webhook only ever detaches; this controller deletes
// a detached pod once its ReplicaSet replacement is Ready (so the steady state
// is one replica again), or if its Deployment is gone. It never deletes a
// detached pod whose replacement isn't up yet — that pod is still the only thing
// serving.
package controller

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/lhns/kube-ha-surge/internal/surge"
)

// DetachReconciler cleans up detached cover pods.
type DetachReconciler struct {
	Client client.Client
	Log    logr.Logger
}

// NewDetachReconciler builds a reconciler.
func NewDetachReconciler(c client.Client, log logr.Logger) *DetachReconciler {
	return &DetachReconciler{Client: c, Log: log}
}

// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;patch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=apps,resources=deployments;replicasets,verbs=get;list;watch

// Reconcile decides whether a detached cover pod has done its job.
func (r *DetachReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	p := &corev1.Pod{}
	if err := r.Client.Get(ctx, req.NamespacedName, p); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	if !surge.IsDetached(p) || p.DeletionTimestamp != nil {
		return reconcile.Result{}, nil
	}
	log := r.Log.WithValues("pod", req.NamespacedName.String())

	dep := &appsv1.Deployment{}
	err := r.Client.Get(ctx, types.NamespacedName{Namespace: p.Namespace, Name: p.Labels[surge.LabelFor]}, dep)
	if apierrors.IsNotFound(err) {
		return r.delete(ctx, p, "target deployment gone")
	}
	if err != nil {
		return reconcile.Result{}, err
	}

	ready, err := r.replacementReady(ctx, dep)
	if err != nil {
		return reconcile.Result{}, err
	}
	if ready {
		return r.delete(ctx, p, "replacement ready")
	}
	// Keep the cover: it's still the only pod serving. Re-check soon.
	log.V(1).Info("waiting for replacement to become ready")
	return reconcile.Result{RequeueAfter: 5 * time.Second}, nil
}

// replacementReady counts Ready pods matching the Deployment selector that are
// NOT the detached cover.
func (r *DetachReconciler) replacementReady(ctx context.Context, dep *appsv1.Deployment) (bool, error) {
	var pods corev1.PodList
	if err := r.Client.List(ctx, &pods,
		client.InNamespace(dep.Namespace),
		client.MatchingLabels(surge.SelectorMatchLabels(dep)),
	); err != nil {
		return false, err
	}
	var n int32
	for i := range pods.Items {
		q := &pods.Items[i]
		if surge.IsDetached(q) || q.DeletionTimestamp != nil {
			continue
		}
		if surge.PodReady(q) {
			n++
		}
	}
	return n >= surge.DesiredReplicas(dep), nil
}

func (r *DetachReconciler) delete(ctx context.Context, p *corev1.Pod, reason string) (reconcile.Result, error) {
	r.Log.Info("deleting detached cover pod", "pod", p.Namespace+"/"+p.Name, "reason", reason)
	if err := r.Client.Delete(ctx, p); err != nil && !apierrors.IsNotFound(err) {
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, nil
}

// SetupWithManager wires the reconciler to watch only detached pods.
func (r *DetachReconciler) SetupWithManager(mgr ctrl.Manager) error {
	isDetached := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetLabels()[surge.LabelDetached] == "true"
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named("detach").
		For(&corev1.Pod{}, builder.WithPredicates(isDetached)).
		Complete(r)
}
