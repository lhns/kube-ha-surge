// Package controller garbage-collects the stand-in ("surge") pods the eviction
// webhook creates. The webhook only ever makes stand-ins; this controller is
// what removes them again once they've served their purpose (or timed out), so
// the steady state is always one replica.
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

// SurgeReconciler cleans up stand-in pods.
type SurgeReconciler struct {
	Client   client.Client
	MaxSurge time.Duration
	Log      logr.Logger
	now      func() time.Time
}

// NewSurgeReconciler builds a reconciler with a real clock.
func NewSurgeReconciler(c client.Client, maxSurge time.Duration, log logr.Logger) *SurgeReconciler {
	return &SurgeReconciler{Client: c, MaxSurge: maxSurge, Log: log, now: time.Now}
}

// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;delete;patch
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=apps,resources=deployments;replicasets,verbs=get;list;watch
// +kubebuilder:rbac:groups=discovery.k8s.io,resources=endpointslices,verbs=get;list;watch

// Reconcile decides whether a stand-in pod has outlived its usefulness.
func (r *SurgeReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	s := &corev1.Pod{}
	if err := r.Client.Get(ctx, req.NamespacedName, s); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	if !surge.IsSurgePod(s) || s.DeletionTimestamp != nil {
		return reconcile.Result{}, nil
	}
	log := r.Log.WithValues("surge", req.NamespacedName.String())

	// Orphan: target Deployment gone.
	depName := s.Labels[surge.LabelFor]
	dep := &appsv1.Deployment{}
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: depName}, dep); err != nil {
		if apierrors.IsNotFound(err) {
			return r.delete(ctx, s, "target deployment gone")
		}
		return reconcile.Result{}, err
	}

	desired := int32(1)
	if dep.Spec.Replicas != nil {
		desired = *dep.Spec.Replicas
	}
	age := r.now().Sub(s.CreationTimestamp.Time)

	// Look at the pod this stand-in was covering for.
	replacing := s.Annotations[surge.AnnotationReplacing]
	replacedGone := replacing == ""
	replacedTerminating := false
	if replacing != "" {
		orig := &corev1.Pod{}
		err := r.Client.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: replacing}, orig)
		switch {
		case apierrors.IsNotFound(err):
			replacedGone = true
		case err == nil:
			replacedTerminating = orig.DeletionTimestamp != nil
		default:
			return reconcile.Result{}, err
		}
	}

	switch {
	case replacedGone && dep.Status.ReadyReplicas >= desired:
		// The eviction happened and the ReplicaSet's own replacement is Ready.
		return r.delete(ctx, s, "replacement ready")
	case !replacedGone && !replacedTerminating && age > r.MaxSurge:
		// The drain was apparently cancelled: the original pod is still around
		// and not terminating, yet we surged. Undo it.
		return r.delete(ctx, s, "drain cancelled; over-surged")
	case !surge.PodReady(s) && age > r.MaxSurge:
		// Stand-in never became Ready in time (pairs with the webhook fail-open).
		return r.delete(ctx, s, "surge timed out before Ready")
	default:
		log.V(1).Info("waiting", "replacedGone", replacedGone, "readyReplicas", dep.Status.ReadyReplicas, "desired", desired)
		return reconcile.Result{RequeueAfter: 5 * time.Second}, nil
	}
}

func (r *SurgeReconciler) delete(ctx context.Context, s *corev1.Pod, reason string) (reconcile.Result, error) {
	r.Log.Info("deleting surge pod", "surge", s.Namespace+"/"+s.Name, "reason", reason)
	if err := r.Client.Delete(ctx, s); err != nil && !apierrors.IsNotFound(err) {
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, nil
}

// SetupWithManager wires the reconciler to watch only stand-in pods.
func (r *SurgeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.now == nil {
		r.now = time.Now
	}
	isSurge := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetLabels()[surge.LabelSurge] == "true"
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named("surge").
		For(&corev1.Pod{}, builder.WithPredicates(isSurge)).
		Complete(r)
}
