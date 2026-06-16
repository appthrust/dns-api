package route53

import (
	"context"
	"errors"
	"testing"
	"time"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var route53IdentityCheckTime = time.Date(2026, 5, 29, 9, 0, 0, 0, time.UTC)

func TestIdentityReconcilerAcceptsRuntimeCredentials(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(&route53v1alpha1.Route53Identity{
			ObjectMeta: metav1.ObjectMeta{Namespace: "platform", Name: "route53-dev"},
			Spec: route53v1alpha1.Route53IdentitySpec{
				AccountID: "123456789012",
				Region:    "ap-northeast-1",
				Credentials: route53v1alpha1.Route53Credentials{
					Runtime: &route53v1alpha1.Route53RuntimeCredentials{},
				},
			},
		}).
		WithStatusSubresource(&route53v1alpha1.Route53Identity{}).
		Build()
	reconciler := &IdentityReconciler{
		Client:   k8sClient,
		Resolver: &fakeIdentityResolver{resolution: IdentityResolution{AccountID: "123456789012"}},
		Clock:    fixedIdentityClock(route53IdentityCheckTime),
	}

	result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "route53-dev"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	assertRequeueAfter(t, result, 10*time.Minute)

	var identity route53v1alpha1.Route53Identity
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "platform", Name: "route53-dev"}, &identity); err != nil {
		t.Fatalf("Route53Identity was not found: %v", err)
	}
	assertCondition(t, identity.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted")
	assertCondition(t, identity.Status.Conditions, "Ready", metav1.ConditionTrue, "Ready")
	if identity.Status.ObservedGeneration != identity.Generation {
		t.Fatalf("observedGeneration = %d, want %d", identity.Status.ObservedGeneration, identity.Generation)
	}
	if identity.Status.AccountID != "123456789012" {
		t.Fatalf("status.accountID = %q, want 123456789012", identity.Status.AccountID)
	}
	assertStatusTime(t, identity.Status.LastCredentialCheckTime, route53IdentityCheckTime, "lastCredentialCheckTime")
	assertStatusTime(t, identity.Status.NextCredentialCheckTime, route53IdentityCheckTime.Add(10*time.Minute), "nextCredentialCheckTime")
}

func TestIdentityReconcilerRecordsConditionEventsOnReasonChange(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	recorder := &capturingRecorder{}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(&route53v1alpha1.Route53Identity{
			ObjectMeta: metav1.ObjectMeta{Namespace: "platform", Name: "route53-dev"},
			Spec: route53v1alpha1.Route53IdentitySpec{
				AccountID: "123456789012",
				Region:    "ap-northeast-1",
				Credentials: route53v1alpha1.Route53Credentials{
					Runtime: &route53v1alpha1.Route53RuntimeCredentials{},
				},
			},
		}).
		WithStatusSubresource(&route53v1alpha1.Route53Identity{}).
		Build()
	reconciler := &IdentityReconciler{
		Client:   k8sClient,
		Resolver: &fakeIdentityResolver{resolution: IdentityResolution{AccountID: "123456789012"}},
		Recorder: recorder,
		Clock:    fixedIdentityClock(route53IdentityCheckTime),
	}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "route53-dev"}}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	if !recorder.has("Route53Identity", "Accepted") {
		t.Fatalf("Accepted event was not recorded on Route53Identity: %#v", recorder.events)
	}
	if !recorder.has("Route53Identity", "Ready") {
		t.Fatalf("Ready event was not recorded on Route53Identity: %#v", recorder.events)
	}
}

func TestIdentityReconcilerDoesNotRepeatUnchangedConditionEvents(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	recorder := &capturingRecorder{}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(&route53v1alpha1.Route53Identity{
			ObjectMeta: metav1.ObjectMeta{Namespace: "platform", Name: "route53-dev"},
			Spec: route53v1alpha1.Route53IdentitySpec{
				AccountID: "123456789012",
				Region:    "ap-northeast-1",
				Credentials: route53v1alpha1.Route53Credentials{
					Runtime: &route53v1alpha1.Route53RuntimeCredentials{},
				},
			},
			Status: route53v1alpha1.Route53IdentityStatus{
				Conditions: []metav1.Condition{
					{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted", Message: "Route53Identity is accepted"},
					{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Ready", Message: "resolved AWS account \"123456789012\""},
				},
			},
		}).
		WithStatusSubresource(&route53v1alpha1.Route53Identity{}).
		Build()
	reconciler := &IdentityReconciler{
		Client:   k8sClient,
		Resolver: &fakeIdentityResolver{resolution: IdentityResolution{AccountID: "123456789012"}},
		Recorder: recorder,
		Clock:    fixedIdentityClock(route53IdentityCheckTime),
	}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "route53-dev"}}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	if len(recorder.events) != 0 {
		t.Fatalf("unchanged condition events were repeated: %#v", recorder.events)
	}
}

func TestIdentityReconcilerRetriesCredentialUnavailable(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(&route53v1alpha1.Route53Identity{
			ObjectMeta: metav1.ObjectMeta{Namespace: "platform", Name: "route53-dev"},
			Spec: route53v1alpha1.Route53IdentitySpec{
				AccountID: "123456789012",
				Region:    "ap-northeast-1",
				Credentials: route53v1alpha1.Route53Credentials{
					Runtime: &route53v1alpha1.Route53RuntimeCredentials{},
				},
			},
		}).
		WithStatusSubresource(&route53v1alpha1.Route53Identity{}).
		Build()
	reconciler := &IdentityReconciler{
		Client: k8sClient,
		Resolver: &fakeIdentityResolver{
			err: &identityReasonError{reason: "CredentialUnavailable", message: "runtime credentials are unavailable"},
		},
		Clock: fixedIdentityClock(route53IdentityCheckTime),
	}

	result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "route53-dev"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	assertRequeueAfter(t, result, time.Minute)

	var identity route53v1alpha1.Route53Identity
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "platform", Name: "route53-dev"}, &identity); err != nil {
		t.Fatalf("Route53Identity was not found: %v", err)
	}
	assertCondition(t, identity.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted")
	assertCondition(t, identity.Status.Conditions, "Ready", metav1.ConditionFalse, "CredentialUnavailable")
	assertStatusTime(t, identity.Status.LastCredentialCheckTime, route53IdentityCheckTime, "lastCredentialCheckTime")
	assertStatusTime(t, identity.Status.NextCredentialCheckTime, route53IdentityCheckTime.Add(time.Minute), "nextCredentialCheckTime")
}

func TestIdentityReconcilerUpdatesRetryTimestampsWithoutRepeatingEvent(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	recorder := &capturingRecorder{}
	oldLastCheck := metav1.NewTime(route53IdentityCheckTime.Add(-time.Hour))
	oldNextCheck := metav1.NewTime(route53IdentityCheckTime.Add(-time.Hour + time.Minute))
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(&route53v1alpha1.Route53Identity{
			ObjectMeta: metav1.ObjectMeta{Namespace: "platform", Name: "route53-dev"},
			Spec: route53v1alpha1.Route53IdentitySpec{
				AccountID: "123456789012",
				Region:    "ap-northeast-1",
				Credentials: route53v1alpha1.Route53Credentials{
					Runtime: &route53v1alpha1.Route53RuntimeCredentials{},
				},
			},
			Status: route53v1alpha1.Route53IdentityStatus{
				LastCredentialCheckTime: &oldLastCheck,
				NextCredentialCheckTime: &oldNextCheck,
				Conditions: []metav1.Condition{
					{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted", Message: "Route53Identity is accepted"},
					{Type: "Ready", Status: metav1.ConditionFalse, Reason: "CredentialUnavailable", Message: "runtime credentials are unavailable"},
				},
			},
		}).
		WithStatusSubresource(&route53v1alpha1.Route53Identity{}).
		Build()
	reconciler := &IdentityReconciler{
		Client: k8sClient,
		Resolver: &fakeIdentityResolver{
			err: &identityReasonError{reason: "CredentialUnavailable", message: "runtime credentials are unavailable"},
		},
		Recorder: recorder,
		Clock:    fixedIdentityClock(route53IdentityCheckTime),
	}

	result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "route53-dev"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	assertRequeueAfter(t, result, time.Minute)

	var identity route53v1alpha1.Route53Identity
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "platform", Name: "route53-dev"}, &identity); err != nil {
		t.Fatalf("Route53Identity was not found: %v", err)
	}
	assertStatusTime(t, identity.Status.LastCredentialCheckTime, route53IdentityCheckTime, "lastCredentialCheckTime")
	assertStatusTime(t, identity.Status.NextCredentialCheckTime, route53IdentityCheckTime.Add(time.Minute), "nextCredentialCheckTime")
	if len(recorder.events) != 0 {
		t.Fatalf("unchanged retry condition events were repeated: %#v", recorder.events)
	}
}

func TestIdentityReconcilerRejectsMissingRuntimeCredentials(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	resolver := &fakeIdentityResolver{}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(&route53v1alpha1.Route53Identity{
			ObjectMeta: metav1.ObjectMeta{Namespace: "platform", Name: "route53-dev"},
			Spec: route53v1alpha1.Route53IdentitySpec{
				AccountID: "123456789012",
				Region:    "ap-northeast-1",
			},
		}).
		WithStatusSubresource(&route53v1alpha1.Route53Identity{}).
		Build()
	reconciler := &IdentityReconciler{Client: k8sClient, Resolver: resolver, Clock: fixedIdentityClock(route53IdentityCheckTime)}

	result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "route53-dev"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	assertRequeueAfter(t, result, 0)

	var identity route53v1alpha1.Route53Identity
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "platform", Name: "route53-dev"}, &identity); err != nil {
		t.Fatalf("Route53Identity was not found: %v", err)
	}
	assertCondition(t, identity.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionFalse, "InvalidCredentialSource")
	assertCondition(t, identity.Status.Conditions, "Ready", metav1.ConditionFalse, "CredentialUnavailable")
	assertStatusTimeNil(t, identity.Status.LastCredentialCheckTime, "lastCredentialCheckTime")
	assertStatusTimeNil(t, identity.Status.NextCredentialCheckTime, "nextCredentialCheckTime")
	if resolver.called {
		t.Fatalf("resolver was called for an invalid Route53Identity")
	}
}

func TestIdentityReconcilerRejectsInvalidRegion(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	resolver := &fakeIdentityResolver{}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(&route53v1alpha1.Route53Identity{
			ObjectMeta: metav1.ObjectMeta{Namespace: "platform", Name: "route53-dev"},
			Spec: route53v1alpha1.Route53IdentitySpec{
				AccountID: "123456789012",
				Region:    "not_a_region",
				Credentials: route53v1alpha1.Route53Credentials{
					Runtime: &route53v1alpha1.Route53RuntimeCredentials{},
				},
			},
		}).
		WithStatusSubresource(&route53v1alpha1.Route53Identity{}).
		Build()
	reconciler := &IdentityReconciler{Client: k8sClient, Resolver: resolver, Clock: fixedIdentityClock(route53IdentityCheckTime)}

	result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "route53-dev"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	assertRequeueAfter(t, result, 0)

	var identity route53v1alpha1.Route53Identity
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "platform", Name: "route53-dev"}, &identity); err != nil {
		t.Fatalf("Route53Identity was not found: %v", err)
	}
	assertCondition(t, identity.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionFalse, "InvalidRegion")
	assertCondition(t, identity.Status.Conditions, "Ready", metav1.ConditionFalse, "CredentialUnavailable")
	assertStatusTimeNil(t, identity.Status.LastCredentialCheckTime, "lastCredentialCheckTime")
	assertStatusTimeNil(t, identity.Status.NextCredentialCheckTime, "nextCredentialCheckTime")
	if resolver.called {
		t.Fatalf("resolver was called for an invalid Route53Identity")
	}
}

func TestIdentityReconcilerReportsAccountMismatch(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(&route53v1alpha1.Route53Identity{
			ObjectMeta: metav1.ObjectMeta{Namespace: "platform", Name: "route53-dev"},
			Spec: route53v1alpha1.Route53IdentitySpec{
				AccountID: "123456789012",
				Region:    "ap-northeast-1",
				Credentials: route53v1alpha1.Route53Credentials{
					Runtime: &route53v1alpha1.Route53RuntimeCredentials{},
				},
			},
		}).
		WithStatusSubresource(&route53v1alpha1.Route53Identity{}).
		Build()
	reconciler := &IdentityReconciler{
		Client: k8sClient,
		Resolver: &fakeIdentityResolver{
			err: &identityReasonError{reason: "AccountMismatch", message: "resolved AWS account \"000000000000\" does not match Route53Identity accountID \"123456789012\""},
		},
		Clock: fixedIdentityClock(route53IdentityCheckTime),
	}

	result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "route53-dev"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	assertRequeueAfter(t, result, 0)

	var identity route53v1alpha1.Route53Identity
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "platform", Name: "route53-dev"}, &identity); err != nil {
		t.Fatalf("Route53Identity was not found: %v", err)
	}
	assertCondition(t, identity.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted")
	assertCondition(t, identity.Status.Conditions, "Ready", metav1.ConditionFalse, "AccountMismatch")
	if identity.Status.AccountID != "" {
		t.Fatalf("status.accountID = %q, want empty on failed resolution", identity.Status.AccountID)
	}
	assertStatusTime(t, identity.Status.LastCredentialCheckTime, route53IdentityCheckTime, "lastCredentialCheckTime")
	assertStatusTimeNil(t, identity.Status.NextCredentialCheckTime, "nextCredentialCheckTime")
}

func fixedIdentityClock(t time.Time) func() time.Time {
	return func() time.Time {
		return t
	}
}

func assertRequeueAfter(t *testing.T, result ctrl.Result, want time.Duration) {
	t.Helper()
	if result.RequeueAfter != want {
		t.Fatalf("RequeueAfter = %s, want %s", result.RequeueAfter, want)
	}
}

func assertStatusTime(t *testing.T, got *metav1.Time, want time.Time, field string) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s was nil, want %s", field, want.Format(time.RFC3339))
	}
	if !got.Time.Equal(want) {
		t.Fatalf("%s = %s, want %s", field, got.Time.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

func assertStatusTimeNil(t *testing.T, got *metav1.Time, field string) {
	t.Helper()
	if got != nil {
		t.Fatalf("%s = %s, want nil", field, got.Time.Format(time.RFC3339))
	}
}

type fakeIdentityResolver struct {
	resolution IdentityResolution
	err        error
	called     bool
}

func (r *fakeIdentityResolver) ResolveIdentity(_ context.Context, _ *route53v1alpha1.Route53Identity) (IdentityResolution, error) {
	r.called = true
	if r.err != nil {
		return IdentityResolution{}, r.err
	}
	if r.resolution.AccountID == "" {
		return IdentityResolution{}, errors.New("not configured")
	}
	return r.resolution, nil
}
