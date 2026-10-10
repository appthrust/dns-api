package route53

import (
	"context"
	"testing"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestZoneClassReconcilerAcceptsRoute53Parameters(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	zoneClass := route53ZoneClass("platform", "route53-public", nil)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(route53Provider(), acceptedRoute53Identity("platform", "route53-dev"), zoneClass).
		WithStatusSubresource(&dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}).
		Build()
	reconciler := &ZoneClassReconciler{Client: k8sClient}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "route53-public"}}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var got dnsv1alpha1.ZoneClass
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "platform", Name: "route53-public"}, &got); err != nil {
		t.Fatalf("ZoneClass was not found: %v", err)
	}
	assertCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted")
}

func TestZoneClassReconcilerValidatesPrivateZonePolicies(t *testing.T) {
	for _, test := range []struct {
		name       string
		parameters string
		message    string
	}{
		{
			name:       "private defaults",
			parameters: `{"zoneType":"Private"}`,
		},
		{
			name:       "private explicit policies and tags",
			parameters: `{"zoneType":"Private","zoneCreationPolicy":"Deny","zoneDeletionPolicy":"Retain","sameNameZonePolicy":"Allow","tags":{"project":"dns-api"}}`,
		},
		{
			name:       "private cannot create",
			parameters: `{"zoneType":"Private","zoneCreationPolicy":"Create"}`,
			message:    "zoneType Private requires zoneCreationPolicy Deny",
		},
		{
			name:       "private cannot delete",
			parameters: `{"zoneType":"Private","zoneDeletionPolicy":"Delete"}`,
			message:    "zoneType Private requires zoneDeletionPolicy Retain",
		},
		{
			name:       "private creation policy checked first",
			parameters: `{"zoneType":"Private","zoneCreationPolicy":"Create","zoneDeletionPolicy":"Delete"}`,
			message:    "zoneType Private requires zoneCreationPolicy Deny",
		},
		{
			name:       "public lifecycle unchanged",
			parameters: `{"zoneType":"Public","zoneCreationPolicy":"Create","zoneDeletionPolicy":"Delete"}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			zoneClass := route53ZoneClass("platform", "route53-private", nil)
			zoneClass.Spec.Parameters = runtime.RawExtension{Raw: []byte(test.parameters)}
			k8sClient := fake.NewClientBuilder().
				WithScheme(testScheme(t)).
				WithObjects(route53Provider(), acceptedRoute53Identity("platform", "route53-dev"), zoneClass).
				WithStatusSubresource(&dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}).
				Build()
			reconciler := &ZoneClassReconciler{Client: k8sClient}
			key := client.ObjectKeyFromObject(zoneClass)
			if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
				t.Fatalf("Reconcile returned error: %v", err)
			}

			var got dnsv1alpha1.ZoneClass
			if err := k8sClient.Get(ctx, key, &got); err != nil {
				t.Fatalf("ZoneClass was not found: %v", err)
			}
			if test.message == "" {
				assertCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted")
				return
			}
			condition := assertCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionFalse, "InvalidParameters")
			if condition.Message != test.message {
				t.Fatalf("Accepted message = %q, want %q", condition.Message, test.message)
			}
		})
	}
}

func TestZoneClassReconcilerRejectsReservedTags(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	zoneClass := route53ZoneClass("platform", "route53-public", map[string]string{
		"appthrust.io/zone-name": "reserved",
	})
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(route53Provider(), acceptedRoute53Identity("platform", "route53-dev"), zoneClass).
		WithStatusSubresource(&dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}).
		Build()
	reconciler := &ZoneClassReconciler{Client: k8sClient}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "route53-public"}}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var got dnsv1alpha1.ZoneClass
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "platform", Name: "route53-public"}, &got); err != nil {
		t.Fatalf("ZoneClass was not found: %v", err)
	}
	assertCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionFalse, "DeniedByPolicy")
}

func TestZoneClassReconcilerRejectsMissingProvider(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(route53ZoneClass("platform", "route53-public", nil)).
		WithStatusSubresource(&dnsv1alpha1.ZoneClass{}).
		Build()
	reconciler := &ZoneClassReconciler{Client: k8sClient}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "route53-public"}}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var got dnsv1alpha1.ZoneClass
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "platform", Name: "route53-public"}, &got); err != nil {
		t.Fatalf("ZoneClass was not found: %v", err)
	}
	assertCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionFalse, "InvalidProvider")
}

func TestZoneClassReconcilerUsesConfiguredProviderBinding(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := route53Provider()
	provider.Name = "route53-fork.example.com"
	provider.Spec.Versions[0].Name = "v1fork"
	zoneClass := route53ZoneClass("platform", "route53-public", nil)
	zoneClass.Spec.Provider = dnsv1alpha1.ProviderReference{Name: provider.Name, Version: "v1fork"}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(provider, acceptedRoute53Identity("platform", "route53-dev"), zoneClass).
		WithStatusSubresource(&dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}).
		Build()
	reconciler := &ZoneClassReconciler{
		Client:          k8sClient,
		ProviderName:    "route53-fork.example.com",
		ProviderVersion: "v1fork",
	}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "route53-public"}}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var got dnsv1alpha1.ZoneClass
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "platform", Name: "route53-public"}, &got); err != nil {
		t.Fatalf("ZoneClass was not found: %v", err)
	}
	assertCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted")
}

func TestZoneClassReconcilerRejectsProviderOutsideConfiguredBinding(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := route53Provider()
	provider.Name = "route53-fork.example.com"
	provider.Spec.Versions[0].Name = "v1fork"
	zoneClass := route53ZoneClass("platform", "route53-public", nil)
	zoneClass.Spec.Provider = dnsv1alpha1.ProviderReference{Name: provider.Name, Version: "v1fork"}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(provider, acceptedRoute53Identity("platform", "route53-dev"), zoneClass).
		WithStatusSubresource(&dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}).
		Build()
	reconciler := &ZoneClassReconciler{Client: k8sClient}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "route53-public"}}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var got dnsv1alpha1.ZoneClass
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "platform", Name: "route53-public"}, &got); err != nil {
		t.Fatalf("ZoneClass was not found: %v", err)
	}
	assertCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionFalse, "InvalidProvider")
}
