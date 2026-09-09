package route53

import (
	"context"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"testing"
)

func TestZoneReconcileDispatchFenceRejectsRevokedRecordSet(t *testing.T) {
	ctx := t.Context()
	recordSet := route53ARecordSet("app", "www")
	unit, provider := route53RecordSetDispatchFixture(t, recordSet, []string{"192.0.2.20"})
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(route53Provider(), route53ZoneClass("app", "route53-public", nil), acceptedRoute53Identity("app", "route53-dev"), unit).
		WithStatusSubresource(&dnsv1alpha1.ZoneUnit{}).
		Build()
	revoked := false
	reconciler := &ZoneReconciler{
		Client: k8sClient,
		Provider: generationChangingListProvider{
			Provider: provider,
			beforeList: func() {
				if revoked {
					return
				}
				revoked = true
				var current dnsv1alpha1.ZoneUnit
				if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &current); err != nil {
					t.Fatal(err)
				}
				current.Spec.RecordSets[0].Allowed = ptr(false)
				if err := k8sClient.Update(ctx, &current); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(unit)}); !apierrors.IsConflict(err) {
		t.Fatalf("Reconcile error = %v, want revoked source conflict", err)
	}
	if !revoked {
		t.Fatal("record-set allowance was not revoked before dispatch")
	}
	if len(provider.upserted) != 0 || len(provider.deletedRRs) != 0 {
		t.Fatalf("revoked RecordSet authorized provider mutation: upserts=%#v deletes=%#v", provider.upserted, provider.deletedRRs)
	}
}

func TestZoneReconcileDispatchFenceRejectsStaleCachedZoneUnit(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		deleting bool
	}{
		{name: "upsert", deleting: false},
		{name: "delete", deleting: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := t.Context()
			recordSet := route53ARecordSet("app", "www")
			if testCase.deleting {
				recordSet = deletingRoute53ARecordSet(t)
			}
			externalValues := []string{"192.0.2.20"}
			if testCase.deleting {
				externalValues = []string{"192.0.2.10"}
			}
			unit, provider := route53RecordSetDispatchFixture(t, recordSet, externalValues)
			var cached dnsv1alpha1.ZoneUnit
			serveCached := false
			k8sClient := fake.NewClientBuilder().
				WithScheme(testScheme(t)).
				WithObjects(route53Provider(), route53ZoneClass("app", "route53-public", nil), acceptedRoute53Identity("app", "route53-dev"), unit).
				WithStatusSubresource(&dnsv1alpha1.ZoneUnit{}).
				WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if observed, ok := obj.(*dnsv1alpha1.ZoneUnit); ok && serveCached {
							*observed = *cached.DeepCopy()
							return nil
						}
						return c.Get(ctx, key, obj, opts...)
					},
				}).
				Build()
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &cached); err != nil {
				t.Fatal(err)
			}
			current := cached.DeepCopy()
			current.Generation = 2
			current.Spec.RecordSets[0].ObservedGeneration = 2
			current.Spec.RecordSets[0].RecordSetUID = types.UID("11111111-2222-3333-4444-000000000002")
			if err := k8sClient.Update(ctx, current); err != nil {
				t.Fatal(err)
			}
			serveCached = true

			reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}
			if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(unit)}); !apierrors.IsConflict(err) {
				t.Fatalf("Reconcile error = %v, want resource-version conflict", err)
			}
			if len(provider.upserted) != 0 || len(provider.deletedRRs) != 0 {
				t.Fatalf("stale cached source authorized provider mutation: upserts=%#v deletes=%#v", provider.upserted, provider.deletedRRs)
			}

			serveCached = false
			var got dnsv1alpha1.ZoneUnit
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &got); err != nil {
				t.Fatal(err)
			}
			item := got.Spec.RecordSets[0]
			if got.Generation != 2 || item.ObservedGeneration != 2 || item.RecordSetUID != types.UID("11111111-2222-3333-4444-000000000002") {
				t.Fatalf("backing ZoneUnit source changed after rejected dispatch: %#v", got.Spec.RecordSets)
			}
			if testCase.deleting {
				if _, found := provider.records[recordKey("Z000001", "www.apps.example.com.", dnsv1alpha1.RecordTypeA)]; !found {
					t.Fatal("stale cached source deleted the external record")
				}
			}
		})
	}
}

func TestZoneReconcileFallbackDispatchFenceRejectsStaleCachedZoneUnit(t *testing.T) {
	ctx := t.Context()
	recordSet := route53ARecordSet("app", "www")
	unit, provider := route53RecordSetDispatchFixture(t, recordSet, []string{"192.0.2.20"})
	var cached dnsv1alpha1.ZoneUnit
	serveCached := false
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(route53Provider(), route53ZoneClass("app", "route53-public", nil), acceptedRoute53Identity("app", "route53-dev"), unit).
		WithStatusSubresource(&dnsv1alpha1.ZoneUnit{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if observed, ok := obj.(*dnsv1alpha1.ZoneUnit); ok && serveCached {
					*observed = *cached.DeepCopy()
					return nil
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}).
		Build()
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &cached); err != nil {
		t.Fatal(err)
	}
	batchCalls := 0
	provider.changeRecordSetsErr = func(_ string, _ []RecordSetChange) error {
		batchCalls++
		if batchCalls != 1 {
			return nil
		}
		var current dnsv1alpha1.ZoneUnit
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &current); err != nil {
			t.Fatal(err)
		}
		current.Generation = 2
		current.Spec.RecordSets[0].ObservedGeneration = 2
		current.Spec.RecordSets[0].RecordSetUID = types.UID("11111111-2222-3333-4444-000000000002")
		if err := k8sClient.Update(ctx, &current); err != nil {
			t.Fatal(err)
		}
		serveCached = true
		return &providerReasonError{reason: "ProviderInvalidRequest", message: "batch must be retried singly"}
	}

	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}
	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(unit)}); !apierrors.IsConflict(err) {
		t.Fatalf("Reconcile error = %v, want fallback source conflict", err)
	}
	if batchCalls != 1 {
		t.Fatalf("provider dispatches = %d, want rejected batch only", batchCalls)
	}
	if len(provider.upserted) != 0 || len(provider.deletedRRs) != 0 {
		t.Fatalf("stale fallback source authorized provider mutation: upserts=%#v deletes=%#v", provider.upserted, provider.deletedRRs)
	}
}

func TestZoneReconcileDispatchFenceAllowsFreshRecordSetMutation(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		deleting bool
	}{
		{name: "upsert", deleting: false},
		{name: "delete", deleting: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := t.Context()
			recordSet := route53ARecordSet("app", "www")
			if testCase.deleting {
				recordSet = deletingRoute53ARecordSet(t)
			}
			externalValues := []string{"192.0.2.20"}
			if testCase.deleting {
				externalValues = []string{"192.0.2.10"}
			}
			unit, provider := route53RecordSetDispatchFixture(t, recordSet, externalValues)
			reconciler := &ZoneReconciler{
				Client: fake.NewClientBuilder().
					WithScheme(testScheme(t)).
					WithObjects(route53Provider(), route53ZoneClass("app", "route53-public", nil), acceptedRoute53Identity("app", "route53-dev"), unit).
					WithStatusSubresource(&dnsv1alpha1.ZoneUnit{}).
					Build(),
				Provider: provider,
			}

			if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(unit)}); err != nil {
				t.Fatalf("Reconcile returned error: %v", err)
			}
			if testCase.deleting {
				if len(provider.upserted) != 0 || len(provider.deletedRRs) != 1 {
					t.Fatalf("fresh deletion mutations = upserts=%#v deletes=%#v", provider.upserted, provider.deletedRRs)
				}
				if _, found := provider.records[recordKey("Z000001", "www.apps.example.com.", dnsv1alpha1.RecordTypeA)]; found {
					t.Fatal("fresh deletion did not remove the external record")
				}
			} else if len(provider.upserted) != 1 || len(provider.deletedRRs) != 0 {
				t.Fatalf("fresh update mutations = upserts=%#v deletes=%#v", provider.upserted, provider.deletedRRs)
			} else if observed := provider.records[recordKey("Z000001", "www.apps.example.com.", dnsv1alpha1.RecordTypeA)]; len(observed.Values) != 1 || observed.Values[0] != "192.0.2.10" {
				t.Fatalf("fresh update did not converge desired external values: %#v", observed)
			}
		})
	}
}

func TestZoneReconcileDoesNotPatchStatusWithoutRecordSetMutation(t *testing.T) {
	ctx := t.Context()
	recordSet := route53ARecordSet("app", "www")
	unit, provider := route53RecordSetDispatchFixture(t, recordSet, []string{"192.0.2.10"})
	statusPatchAttempts := 0
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(route53Provider(), route53ZoneClass("app", "route53-public", nil), acceptedRoute53Identity("app", "route53-dev"), unit).
		WithStatusSubresource(&dnsv1alpha1.ZoneUnit{}).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				if subResourceName == "status" {
					statusPatchAttempts++
				}
				return c.SubResource(subResourceName).Patch(ctx, obj, patch, opts...)
			},
		}).
		Build()

	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}
	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(unit)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if statusPatchAttempts != 0 {
		t.Fatalf("no-op record-set observation issued %d status patches", statusPatchAttempts)
	}
	if len(provider.upserted) != 0 || len(provider.deletedRRs) != 0 {
		t.Fatalf("no-op record-set observation submitted provider mutation: upserts=%#v deletes=%#v", provider.upserted, provider.deletedRRs)
	}
}

func route53RecordSetDispatchFixture(t *testing.T, recordSet *dnsv1alpha1.RecordSet, externalValues []string) (*dnsv1alpha1.ZoneUnit, *fakeProvider) {
	t.Helper()
	unit := route53ZoneUnit("app", "apps-example-com", "app", "route53-public")
	unit.Generation = 1
	unit.Spec.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetSpec{route53ZoneUnitRecordSetSpec(recordSet)}
	setRoute53ReadyZoneUnitStatus(t, unit)
	unit.Status.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetStatus{
		route53RecordSetStatusWithState(t, recordSet, "www.apps.example.com."),
	}
	ttl := int64(300)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
	}
	provider.records[recordKey("Z000001", "www.apps.example.com.", dnsv1alpha1.RecordTypeA)] = RecordSetResource{
		HostedZoneID: "Z000001",
		Name:         "www.apps.example.com.",
		Type:         dnsv1alpha1.RecordTypeA,
		TTL:          &ttl,
		Values:       externalValues,
	}
	return unit, provider
}
