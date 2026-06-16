package webhook

import (
	"strings"
	"testing"

	cloudflarev1alpha1 "github.com/appthrust/dns-api/pkg/go/api/cloudflare/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCloudflareIdentityStatusValidationRejectsForgedReady(t *testing.T) {
	identity := validCloudflareIdentity()
	identity.Generation = 1
	identity.Status.ObservedGeneration = 999
	identity.Status.Account = &cloudflarev1alpha1.CloudflareAccountStatus{ID: "not-an-account-id", Name: "fake"}
	identity.Status.Conditions = []metav1.Condition{
		{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			Reason:             "ManuallyForged",
			Message:            "fake ready",
			ObservedGeneration: 999,
			LastTransitionTime: metav1.Now(),
		},
	}

	err := validateCloudflareIdentityStatus(identity)
	if err == nil {
		t.Fatal("validateCloudflareIdentityStatus returned nil, want forged status failure")
	}
	for _, want := range []string{"observedGeneration", "account.id", "reason"} {
		mutated := validCloudflareIdentity()
		mutated.Generation = 1
		switch want {
		case "observedGeneration":
			mutated.Status.ObservedGeneration = 2
		case "account.id":
			mutated.Status.Account = &cloudflarev1alpha1.CloudflareAccountStatus{ID: "not-an-account-id"}
		case "reason":
			mutated.Status.Conditions = []metav1.Condition{
				{Type: "Ready", Status: metav1.ConditionTrue, Reason: "ManuallyForged"},
			}
		}
		err := validateCloudflareIdentityStatus(mutated)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("validateCloudflareIdentityStatus(%s) error = %v, want %q", want, err, want)
		}
	}
}

func validCloudflareIdentity() *cloudflarev1alpha1.CloudflareIdentity {
	return &cloudflarev1alpha1.CloudflareIdentity{
		ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "cloudflare", Generation: 1},
		Spec: cloudflarev1alpha1.CloudflareIdentitySpec{
			AccessToken: cloudflarev1alpha1.CloudflareAccessTokenSource{
				SecretRef: cloudflarev1alpha1.CloudflareSecretKeyReference{
					Name: "cloudflare-token",
					Key:  "token",
				},
			},
		},
	}
}
