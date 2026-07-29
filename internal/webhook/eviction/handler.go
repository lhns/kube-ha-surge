// Package eviction implements the ValidatingWebhook on the pods/eviction
// subresource. For an opted-in single-replica Deployment it detaches the doomed
// pod from its ReplicaSet (so the RS makes a real replacement) and holds the
// eviction with a 429 — which drains retry on natively — until that replacement
// is Ready; the detached pod keeps serving in the meantime. Make-before-break,
// no clone, no scaling.
package eviction

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/lhns/kube-ha-surge/internal/surge"
)

// Handler decides eviction admission for pods/eviction CREATE.
type Handler struct {
	Client   client.Client
	MaxSurge time.Duration
	Log      logr.Logger
	// now is overridable in tests.
	now func() time.Time
}

// NewHandler builds a Handler with a real clock.
func NewHandler(c client.Client, maxSurge time.Duration, log logr.Logger) *Handler {
	return &Handler{Client: c, MaxSurge: maxSurge, Log: log, now: time.Now}
}

// Handle is the admission entrypoint. Every branch that can't or needn't help
// returns Allowed — the webhook is fail-open by construction so it can never
// wedge a drain.
func (h *Handler) Handle(ctx context.Context, req admission.Request) admission.Response {
	// For the eviction subresource the request name/namespace identify the pod.
	pod := &corev1.Pod{}
	if err := h.Client.Get(ctx, types.NamespacedName{Namespace: req.Namespace, Name: req.Name}, pod); err != nil {
		return admission.Allowed("pod not found")
	}
	log := h.Log.WithValues("pod", req.Name, "namespace", req.Namespace)

	// Retry path: we already detached this pod. Allow once its ReplicaSet
	// replacement is Ready, or once we've waited too long (fail-open).
	if surge.IsDetached(pod) {
		dep := h.getDeployment(ctx, pod.Namespace, pod.Labels[surge.LabelFor])
		if dep == nil {
			return admission.Allowed("target deployment gone")
		}
		if h.replacementReady(ctx, dep) {
			log.Info("replacement ready, allowing eviction", "deployment", dep.Name)
			return admission.Allowed("replacement replica ready")
		}
		if at, ok := surge.DetachedAt(pod); ok && h.now().Sub(at) > h.MaxSurge {
			log.Info("detach timeout, failing open", "deployment", dep.Name)
			return admission.Allowed("replacement did not become ready in time")
		}
		return deny429("ha-surge: replacement replica not ready yet")
	}

	// First eviction: decide whether we can and should cover it.
	dep := h.ownerDeployment(ctx, pod)
	if dep == nil {
		return admission.Allowed("not Deployment-owned")
	}
	if !surge.Enabled(dep) {
		return admission.Allowed("not opted in")
	}
	if surge.AlreadyHA(dep) {
		return admission.Allowed("already multi-replica")
	}
	if surge.HasReadWriteOnceVolume(pod, h.pvcAccessModes(ctx, pod)) {
		return admission.Allowed("single-writer (RWO) volume; replacement can't co-mount it")
	}
	if h.serviceSelectsOnHash(ctx, pod) {
		return admission.Allowed("a Service selects on pod-template-hash; cannot detach safely")
	}

	// Detach: strip pod-template-hash so the RS makes a replacement, while the
	// pod keeps its app labels and keeps serving. Hold the eviction until ready.
	patch := client.MergeFrom(pod)
	detached := surge.MarkDetached(pod, dep.Name, h.now())
	if err := h.Client.Patch(ctx, detached, patch); err != nil {
		log.Error(err, "failed to detach pod, failing open", "deployment", dep.Name)
		return admission.Allowed("detach failed")
	}
	log.Info("detached pod from ReplicaSet, holding eviction", "deployment", dep.Name)
	return deny429("ha-surge: bringing up a replacement replica")
}

// replacementReady reports whether the ReplicaSet has produced a Ready pod to
// take the detached pod's place. It counts Ready pods matching the Deployment's
// selector that are NOT detached (excludes the cover) — robust against the brief
// lag in dep.Status after the detach.
func (h *Handler) replacementReady(ctx context.Context, dep *appsv1.Deployment) bool {
	var pods corev1.PodList
	if err := h.Client.List(ctx, &pods,
		client.InNamespace(dep.Namespace),
		client.MatchingLabels(surge.SelectorMatchLabels(dep)),
	); err != nil {
		return false
	}
	var ready int32
	for i := range pods.Items {
		p := &pods.Items[i]
		if surge.IsDetached(p) || p.DeletionTimestamp != nil {
			continue
		}
		if surge.PodReady(p) {
			ready++
		}
	}
	return ready >= surge.DesiredReplicas(dep)
}

// serviceSelectsOnHash reports whether any Service in the namespace that would
// route to this pod selects on pod-template-hash — in which case removing that
// label would drop the pod from the Service, so we must not detach.
func (h *Handler) serviceSelectsOnHash(ctx context.Context, pod *corev1.Pod) bool {
	var svcs corev1.ServiceList
	if err := h.Client.List(ctx, &svcs, client.InNamespace(pod.Namespace)); err != nil {
		return false
	}
	for i := range svcs.Items {
		sel := svcs.Items[i].Spec.Selector
		if len(sel) == 0 {
			continue
		}
		if surge.SelectorUsesPodTemplateHash(sel) && surge.MatchesLabels(pod.Labels, sel) {
			return true
		}
	}
	return false
}

func (h *Handler) getDeployment(ctx context.Context, ns, name string) *appsv1.Deployment {
	if name == "" {
		return nil
	}
	dep := &appsv1.Deployment{}
	if err := h.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, dep); err != nil {
		return nil
	}
	return dep
}

// ownerDeployment walks pod -> ReplicaSet -> Deployment. Returns nil for any
// pod not ultimately owned by a Deployment.
func (h *Handler) ownerDeployment(ctx context.Context, pod *corev1.Pod) *appsv1.Deployment {
	rsRef := controllerRef(pod.OwnerReferences)
	if rsRef == nil || rsRef.Kind != "ReplicaSet" {
		return nil
	}
	rs := &appsv1.ReplicaSet{}
	if err := h.Client.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: rsRef.Name}, rs); err != nil {
		return nil
	}
	depRef := controllerRef(rs.OwnerReferences)
	if depRef == nil || depRef.Kind != "Deployment" {
		return nil
	}
	return h.getDeployment(ctx, pod.Namespace, depRef.Name)
}

func (h *Handler) pvcAccessModes(ctx context.Context, pod *corev1.Pod) map[string][]corev1.PersistentVolumeAccessMode {
	out := map[string][]corev1.PersistentVolumeAccessMode{}
	for _, v := range pod.Spec.Volumes {
		if v.PersistentVolumeClaim == nil {
			continue
		}
		name := v.PersistentVolumeClaim.ClaimName
		pvc := &corev1.PersistentVolumeClaim{}
		if err := h.Client.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: name}, pvc); err != nil {
			// Unknown -> treat conservatively as single-writer.
			out[name] = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
			continue
		}
		out[name] = pvc.Spec.AccessModes
	}
	return out
}

func controllerRef(refs []metav1.OwnerReference) *metav1.OwnerReference {
	for i := range refs {
		if refs[i].Controller != nil && *refs[i].Controller {
			return &refs[i]
		}
	}
	return nil
}

// deny429 denies the eviction with HTTP 429 TooManyRequests so the draining
// client backs off and retries (the same signal a PodDisruptionBudget uses).
func deny429(msg string) admission.Response {
	return admission.Response{AdmissionResponse: admissionResponseDenied(msg)}
}
