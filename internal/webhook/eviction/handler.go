// Package eviction implements the ValidatingWebhook on the pods/eviction
// subresource. For an opted-in single-replica Deployment it holds the eviction
// with a 429 (which drains retry on natively) while a stand-in surge pod comes
// up, and allows the eviction only once the stand-in is Ready — make-before-break.
package eviction

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	// StripLabelPrefixes are label-key prefixes dropped from the cloned surge
	// pod (in addition to the ReplicaSet's pod-template-hash). Cluster-specific;
	// see surge.BuildSurgePod.
	StripLabelPrefixes []string
	Log                logr.Logger
	// now is overridable in tests.
	now func() time.Time
}

// NewHandler builds a Handler with a real clock.
func NewHandler(c client.Client, maxSurge time.Duration, stripLabelPrefixes []string, log logr.Logger) *Handler {
	return &Handler{Client: c, MaxSurge: maxSurge, StripLabelPrefixes: stripLabelPrefixes, Log: log, now: time.Now}
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
	if surge.IsSurgePod(pod) {
		return admission.Allowed("surge pod")
	}

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
		return admission.Allowed("single-writer (RWO) volume; cannot surge")
	}

	log := h.Log.WithValues("pod", req.Name, "namespace", req.Namespace, "deployment", dep.Name)

	// Is there already a Ready stand-in? Then make-before-break is satisfied.
	existing := h.findSurge(ctx, dep)
	if existing != nil && surge.PodReady(existing) {
		log.Info("surge ready, allowing eviction")
		return admission.Allowed("surge replica ready")
	}

	// No stand-in yet: create one and hold the eviction.
	if existing == nil {
		want := surge.BuildSurgePod(pod, dep, h.StripLabelPrefixes)
		if err := h.Client.Create(ctx, want); err != nil && !apierrors.IsAlreadyExists(err) {
			// Can't create the stand-in -> don't block the drain.
			log.Error(err, "failed to create surge pod, failing open")
			return admission.Allowed("surge creation failed")
		}
		log.Info("created surge pod, holding eviction")
		return deny429("ha-surge: bringing up a stand-in replica")
	}

	// Stand-in exists but isn't Ready yet. Fail open if it's taking too long.
	if age := h.now().Sub(existing.CreationTimestamp.Time); age > h.MaxSurge {
		log.Info("surge timeout, failing open", "age", age.String())
		return admission.Allowed("surge did not become ready in time")
	}
	return deny429("ha-surge: stand-in replica not ready yet")
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
	dep := &appsv1.Deployment{}
	if err := h.Client.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: depRef.Name}, dep); err != nil {
		return nil
	}
	return dep
}

func (h *Handler) findSurge(ctx context.Context, dep *appsv1.Deployment) *corev1.Pod {
	p := &corev1.Pod{}
	err := h.Client.Get(ctx, types.NamespacedName{Namespace: dep.Namespace, Name: surge.SurgeName(dep.Name)}, p)
	if err != nil {
		return nil
	}
	if !surge.IsSurgePod(p) || p.Labels[surge.LabelFor] != dep.Name {
		return nil
	}
	return p
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
			// Unknown -> treat conservatively as single-writer so we don't try
			// to co-mount something we can't see.
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
	return admission.Response{
		AdmissionResponse: admissionResponseDenied(msg),
	}
}
