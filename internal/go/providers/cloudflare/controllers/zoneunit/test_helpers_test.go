package cloudflare

import (
	"testing"

	cloudflarev1alpha1 "github.com/appthrust/dns-api/pkg/go/api/cloudflare/v1alpha1"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func cloudflareTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	if err := dnsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add dns scheme: %v", err)
	}
	if err := cloudflarev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add cloudflare scheme: %v", err)
	}
	return scheme
}

func assertCloudflareCondition(t *testing.T, conditions []metav1.Condition, conditionType string, status metav1.ConditionStatus, reason string) {
	t.Helper()
	condition := meta.FindStatusCondition(conditions, conditionType)
	if condition == nil {
		t.Fatalf("condition %s was not set", conditionType)
	}
	if condition.Status != status || condition.Reason != reason {
		t.Fatalf("condition %s = %s/%s, want %s/%s", conditionType, condition.Status, condition.Reason, status, reason)
	}
}

func validCloudflareIdentity() *cloudflarev1alpha1.CloudflareIdentity {
	return &cloudflarev1alpha1.CloudflareIdentity{
		ObjectMeta: metav1.ObjectMeta{Namespace: "platform", Name: "cloudflare-ci"},
		Spec: cloudflarev1alpha1.CloudflareIdentitySpec{
			AccessToken: cloudflarev1alpha1.CloudflareAccessTokenSource{
				SecretRef: cloudflarev1alpha1.CloudflareSecretKeyReference{Name: "cloudflare-ci-api-token", Key: "api-token"},
			},
		},
	}
}
