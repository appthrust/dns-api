package cloudflare

import (
	"context"
	"errors"
	"testing"
	"time"

	cloudflarev1alpha1 "github.com/appthrust/dns-api/pkg/go/api/cloudflare/v1alpha1"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var cloudflareIdentityCheckTime = time.Date(2026, 6, 3, 9, 0, 0, 0, time.UTC)

func TestCloudflareIdentityReconcilerAcceptsSecretToken(t *testing.T) {
	ctx := context.Background()
	k8sClient := fake.NewClientBuilder().
		WithScheme(cloudflareTestScheme(t)).
		WithObjects(validCloudflareIdentity(), cloudflareTokenSecret("raw-token")).
		WithStatusSubresource(&cloudflarev1alpha1.CloudflareIdentity{}).
		Build()
	resolver := &fakeCloudflareResolver{resolution: IdentityResolution{
		Account:     cloudflarev1alpha1.CloudflareAccountStatus{ID: "023e105f4ecef8ad9ca31a8372d0c354", Name: "ci", Type: "standard"},
		AccessToken: cloudflarev1alpha1.CloudflareAccessTokenStatus{ID: "token-id", Status: "active"},
	}}
	reconciler := &IdentityReconciler{
		Client:   k8sClient,
		Resolver: resolver,
		Clock:    fixedCloudflareClock(cloudflareIdentityCheckTime),
	}

	result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "cloudflare-ci"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	assertCloudflareRequeueAfter(t, result, 10*time.Minute)

	var identity cloudflarev1alpha1.CloudflareIdentity
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "platform", Name: "cloudflare-ci"}, &identity); err != nil {
		t.Fatalf("CloudflareIdentity was not found: %v", err)
	}
	assertCloudflareCondition(t, identity.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted")
	assertCloudflareCondition(t, identity.Status.Conditions, "Ready", metav1.ConditionTrue, "Ready")
	if identity.Status.Account == nil || identity.Status.Account.ID != "023e105f4ecef8ad9ca31a8372d0c354" {
		t.Fatalf("status.account = %#v", identity.Status.Account)
	}
	if identity.Status.AccessToken == nil || identity.Status.AccessToken.ID != "token-id" {
		t.Fatalf("status.accessToken = %#v", identity.Status.AccessToken)
	}
	assertCloudflareStatusTime(t, identity.Status.LastCredentialCheckTime, cloudflareIdentityCheckTime, "lastCredentialCheckTime")
	assertCloudflareStatusTime(t, identity.Status.NextCredentialCheckTime, cloudflareIdentityCheckTime.Add(10*time.Minute), "nextCredentialCheckTime")
}

func TestCloudflareIdentityReconcilerRejectsInvalidSecretRef(t *testing.T) {
	ctx := context.Background()
	resolver := &fakeCloudflareResolver{}
	identity := validCloudflareIdentity()
	identity.Spec.AccessToken.SecretRef.Name = ""
	k8sClient := fake.NewClientBuilder().
		WithScheme(cloudflareTestScheme(t)).
		WithObjects(identity).
		WithStatusSubresource(&cloudflarev1alpha1.CloudflareIdentity{}).
		Build()
	reconciler := &IdentityReconciler{Client: k8sClient, Resolver: resolver, Clock: fixedCloudflareClock(cloudflareIdentityCheckTime)}

	result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "cloudflare-ci"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	assertCloudflareRequeueAfter(t, result, 0)

	var got cloudflarev1alpha1.CloudflareIdentity
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "platform", Name: "cloudflare-ci"}, &got); err != nil {
		t.Fatalf("CloudflareIdentity was not found: %v", err)
	}
	assertCloudflareCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionFalse, "InvalidSecretRef")
	assertCloudflareCondition(t, got.Status.Conditions, "Ready", metav1.ConditionFalse, "AccessTokenUnavailable")
	assertCloudflareStatusTimeNil(t, got.Status.LastCredentialCheckTime, "lastCredentialCheckTime")
	if resolver.called {
		t.Fatalf("resolver was called for invalid secretRef")
	}
}

func TestCloudflareIdentityReconcilerReportsMissingSecret(t *testing.T) {
	ctx := context.Background()
	resolver := &fakeCloudflareResolver{}
	k8sClient := fake.NewClientBuilder().
		WithScheme(cloudflareTestScheme(t)).
		WithObjects(validCloudflareIdentity()).
		WithStatusSubresource(&cloudflarev1alpha1.CloudflareIdentity{}).
		Build()
	reconciler := &IdentityReconciler{Client: k8sClient, Resolver: resolver, Clock: fixedCloudflareClock(cloudflareIdentityCheckTime)}

	result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "cloudflare-ci"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	assertCloudflareRequeueAfter(t, result, 0)

	var identity cloudflarev1alpha1.CloudflareIdentity
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "platform", Name: "cloudflare-ci"}, &identity); err != nil {
		t.Fatalf("CloudflareIdentity was not found: %v", err)
	}
	assertCloudflareCondition(t, identity.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted")
	assertCloudflareCondition(t, identity.Status.Conditions, "Ready", metav1.ConditionFalse, "SecretNotFound")
	assertCloudflareStatusTimeNil(t, identity.Status.LastCredentialCheckTime, "lastCredentialCheckTime")
	if resolver.called {
		t.Fatalf("resolver was called when Secret was missing")
	}
}

func TestCloudflareIdentityReconcilerReportsResolverReason(t *testing.T) {
	ctx := context.Background()
	k8sClient := fake.NewClientBuilder().
		WithScheme(cloudflareTestScheme(t)).
		WithObjects(validCloudflareIdentity(), cloudflareTokenSecret("raw-token")).
		WithStatusSubresource(&cloudflarev1alpha1.CloudflareIdentity{}).
		Build()
	reconciler := &IdentityReconciler{
		Client:   k8sClient,
		Resolver: &fakeCloudflareResolver{err: &identityReasonError{reason: "AccessTokenInvalid", message: "Cloudflare API token verification failed"}},
		Clock:    fixedCloudflareClock(cloudflareIdentityCheckTime),
	}

	result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "cloudflare-ci"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	assertCloudflareRequeueAfter(t, result, 0)

	var identity cloudflarev1alpha1.CloudflareIdentity
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "platform", Name: "cloudflare-ci"}, &identity); err != nil {
		t.Fatalf("CloudflareIdentity was not found: %v", err)
	}
	assertCloudflareCondition(t, identity.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted")
	assertCloudflareCondition(t, identity.Status.Conditions, "Ready", metav1.ConditionFalse, "AccessTokenInvalid")
	if identity.Status.AccessToken != nil || identity.Status.Account != nil {
		t.Fatalf("secret-derived status was stored on failure: account=%#v token=%#v", identity.Status.Account, identity.Status.AccessToken)
	}
}

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

func cloudflareTokenSecret(token string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "platform", Name: "cloudflare-ci-api-token"},
		Data:       map[string][]byte{"api-token": []byte(token)},
	}
}

func fixedCloudflareClock(t time.Time) func() time.Time {
	return func() time.Time {
		return t
	}
}

func assertCloudflareRequeueAfter(t *testing.T, result ctrl.Result, want time.Duration) {
	t.Helper()
	if result.RequeueAfter != want {
		t.Fatalf("RequeueAfter = %s, want %s", result.RequeueAfter, want)
	}
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

func assertCloudflareStatusTime(t *testing.T, got *metav1.Time, want time.Time, field string) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s was nil, want %s", field, want.Format(time.RFC3339))
	}
	if !got.Time.Equal(want) {
		t.Fatalf("%s = %s, want %s", field, got.Time.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

func assertCloudflareStatusTimeNil(t *testing.T, got *metav1.Time, field string) {
	t.Helper()
	if got != nil {
		t.Fatalf("%s = %s, want nil", field, got.Time.Format(time.RFC3339))
	}
}

type fakeCloudflareResolver struct {
	resolution IdentityResolution
	err        error
	called     bool
}

func (r *fakeCloudflareResolver) ResolveIdentity(_ context.Context, token string) (IdentityResolution, error) {
	r.called = true
	if token == "" {
		return IdentityResolution{}, errors.New("token missing")
	}
	if r.err != nil {
		return IdentityResolution{}, r.err
	}
	return r.resolution, nil
}
