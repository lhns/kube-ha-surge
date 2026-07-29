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
func pbool(b bool) *bool  { return &b }

// build a Deployment -> ReplicaSet -> Pod owner chain.
func chain(ns, dep string, replicas int32, opted bool) (*appsv1.Deployment, *appsv1.ReplicaSet, *corev1.Pod) {
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: dep, Namespace: ns, UID: types.UID(dep + "-uid")},
		Spec:       appsv1.DeploymentSpec{Replicas: i32(replicas)},
	}
	if opted {
		d.Annotations = map[string]string{surge.AnnotationEnabled: "true"}
	}
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: dep + "-rs", Namespace: ns,
			OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: dep, UID: d.UID, Controller: pbool(true)}},
		},
	}
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: dep + "-rs-pod", Namespace: ns,
			Labels:          map[string]string{"app": dep},
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: rs.Name, Controller: pbool(true)}},
		},
	}
	return d, rs, p
}

func handlerWith(objs ...client.Object) *Handler {
	sch := clientgoscheme.Scheme
	c := fake.NewClientBuilder().WithScheme(sch).WithObjects(objs...).Build()
	h := NewHandler(c, 120*time.Second, logr.Discard())
	return h
}

func req(pod *corev1.Pod) admission.Request {
	return admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Name: pod.Name, Namespace: pod.Namespace,
	}}
}

func TestHandle_PodNotFound_Allows(t *testing.T) {
	h := handlerWith()
	resp := h.Handle(context.Background(), admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{Name: "ghost", Namespace: "x"}})
	if !resp.Allowed {
		t.Fatal("missing pod should be allowed")
	}
}

func TestHandle_NotDeploymentOwned_Allows(t *testing.T) {
	bare := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "bare", Namespace: "x"}}
	h := handlerWith(bare)
	if !h.Handle(context.Background(), req(bare)).Allowed {
		t.Fatal("bare pod should be allowed")
	}
}

func TestHandle_NotOptedIn_Allows(t *testing.T) {
	d, rs, p := chain("x", "web", 1, false)
	h := handlerWith(d, rs, p)
	if !h.Handle(context.Background(), req(p)).Allowed {
		t.Fatal("non-opted-in should be allowed")
	}
}

func TestHandle_AlreadyHA_Allows(t *testing.T) {
	d, rs, p := chain("x", "web", 2, true)
	h := handlerWith(d, rs, p)
	if !h.Handle(context.Background(), req(p)).Allowed {
		t.Fatal("multi-replica should be allowed")
	}
}

func TestHandle_RWO_Allows(t *testing.T) {
	d, rs, p := chain("x", "web", 1, true)
	p.Spec.Volumes = []corev1.Volume{{
		Name:         "data",
		VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}},
	}}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "x"},
		Spec:       corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}},
	}
	h := handlerWith(d, rs, p, pvc)
	if !h.Handle(context.Background(), req(p)).Allowed {
		t.Fatal("RWO-backed pod should be allowed (can't surge)")
	}
}

func TestHandle_SingleReplica_CreatesSurgeAndDenies429(t *testing.T) {
	d, rs, p := chain("x", "web", 1, true)
	h := handlerWith(d, rs, p)
	resp := h.Handle(context.Background(), req(p))
	if resp.Allowed {
		t.Fatal("first eviction should be denied while surging")
	}
	if resp.Result == nil || resp.Result.Code != 429 {
		t.Fatalf("want 429, got %+v", resp.Result)
	}
	// surge pod should now exist
	s := &corev1.Pod{}
	if err := h.Client.Get(context.Background(), types.NamespacedName{Namespace: "x", Name: surge.SurgeName("web")}, s); err != nil {
		t.Fatalf("surge pod not created: %v", err)
	}
	if s.Labels[surge.LabelSurge] != "true" || s.Annotations[surge.AnnotationReplacing] != p.Name {
		t.Errorf("surge pod mislabelled: %+v", s.ObjectMeta)
	}
}

func TestHandle_SurgeReady_Allows(t *testing.T) {
	d, rs, p := chain("x", "web", 1, true)
	s := surge.BuildSurgePod(p, d)
	s.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	h := handlerWith(d, rs, p, s)
	resp := h.Handle(context.Background(), req(p))
	if !resp.Allowed {
		t.Fatal("eviction should be allowed once surge is Ready")
	}
}

func TestHandle_SurgeNotReady_Denies429(t *testing.T) {
	d, rs, p := chain("x", "web", 1, true)
	s := surge.BuildSurgePod(p, d)
	s.CreationTimestamp = metav1.NewTime(time.Now())
	h := handlerWith(d, rs, p, s)
	resp := h.Handle(context.Background(), req(p))
	if resp.Allowed || resp.Result.Code != 429 {
		t.Fatalf("not-ready surge should still deny 429, got %+v", resp)
	}
}

func TestHandle_SurgeTimeout_FailsOpen(t *testing.T) {
	d, rs, p := chain("x", "web", 1, true)
	s := surge.BuildSurgePod(p, d)
	s.CreationTimestamp = metav1.NewTime(time.Now().Add(-10 * time.Minute))
	h := handlerWith(d, rs, p, s)
	h.now = func() time.Time { return time.Now() }
	if !h.Handle(context.Background(), req(p)).Allowed {
		t.Fatal("a surge past max-surge should fail open (allow)")
	}
}

func TestHandle_SurgePodItself_Allows(t *testing.T) {
	d, rs, p := chain("x", "web", 1, true)
	s := surge.BuildSurgePod(p, d)
	h := handlerWith(d, rs, p, s)
	if !h.Handle(context.Background(), req(s)).Allowed {
		t.Fatal("evicting the surge pod itself should be allowed")
	}
}
