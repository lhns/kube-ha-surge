package eviction

import (
	"net/http"

	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// admissionResponseDenied builds a denial carrying HTTP 429 TooManyRequests.
// controller-runtime's admission.Denied hardcodes 403, which drains treat as a
// hard failure; 429 is the retryable signal, so we construct the response by
// hand. (Validated end-to-end against a live apiserver: a webhook-supplied 429
// surfaces to `kubectl drain` as TooManyRequests and is retried.)
func admissionResponseDenied(msg string) admissionv1.AdmissionResponse {
	return admissionv1.AdmissionResponse{
		Allowed: false,
		Result: &metav1.Status{
			Status:  metav1.StatusFailure,
			Code:    http.StatusTooManyRequests,
			Reason:  metav1.StatusReasonTooManyRequests,
			Message: msg,
		},
	}
}
