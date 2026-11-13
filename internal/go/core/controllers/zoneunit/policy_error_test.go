package zoneunit

import (
	"context"
	"errors"
	"reflect"
	"testing"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestNamespaceReadFailureRetainsComposedRecords(t *testing.T) {
	z := zone("app", "example", "example.com")
	z.Spec.ZoneClassRef.Namespace = ptr("platform")
	z.Spec.AllowedRecordSets = []dnsv1alpha1.AllowedRecordSet{{
		Namespaces: dnsv1alpha1.AllowedRecordSetNamespaces{Selector: metav1.LabelSelector{MatchLabels: map[string]string{"dns": "allowed"}}},
		Records:    []dnsv1alpha1.AllowedRecord{{Name: dnsv1alpha1.RecordNamePolicy{Pattern: "www"}, Types: []dnsv1alpha1.RecordType{dnsv1alpha1.RecordTypeA}}},
	}}
	record := aRecordSet("writer", "www", z.Namespace, z.Name, "www")
	base := fake.NewClientBuilder().WithScheme(testScheme(t)).WithStatusSubresource(z, record).
		WithObjects(route53ProviderForZoneUnitTest(), route53ZoneClassForZoneUnitTest(), z, record, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "writer", Labels: map[string]string{"dns": "allowed"}}}).Build()
	r := &ZoneUnitCompositionReconciler{Client: base}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(z)}
	if _, err := r.Reconcile(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	var before dnsv1alpha1.ZoneUnit
	if err := base.Get(t.Context(), req.NamespacedName, &before); err != nil {
		t.Fatal(err)
	}
	if len(before.Spec.RecordSets) != 1 {
		t.Fatal("fixture did not establish a record")
	}
	if err := base.Get(t.Context(), client.ObjectKeyFromObject(record), record); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("namespace observation unavailable")
	r.Client = interceptor.NewClient(base, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if _, ok := obj.(*corev1.Namespace); ok {
			return failure
		}
		return c.Get(ctx, key, obj, opts...)
	}})
	if _, err := r.Reconcile(t.Context(), req); !errors.Is(err, failure) {
		t.Fatalf("reconcile error=%v", err)
	}
	var after dnsv1alpha1.ZoneUnit
	if err := base.Get(t.Context(), req.NamespacedName, &after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("Namespace read error changed the composed desired records")
	}
	var current dnsv1alpha1.RecordSet
	if err := base.Get(t.Context(), client.ObjectKeyFromObject(record), &current); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(record.Status, current.Status) {
		t.Fatal("Namespace read error was published as a rejection")
	}
}

func TestNamespaceReadFailureRetainsWithdrawalAuthorization(t *testing.T) {
	provider, class, z, claim := withdrawalFixture()
	claim.Spec.Name = "*.apps"
	z.Spec.AllowedRecordSets = withdrawalGrant()
	base := fake.NewClientBuilder().WithScheme(testScheme(t)).WithStatusSubresource(claim, z, &dnsv1alpha1.ZoneUnit{}, &dnsv1alpha1.RecordWithdrawalWaiver{}).
		WithObjects(provider, class, z, claim, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "app", Labels: map[string]string{"dns": "allowed"}}}).Build()
	r := &ZoneUnitCompositionReconciler{Client: base}
	key := client.ObjectKeyFromObject(z)
	if _, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	var before dnsv1alpha1.ZoneUnit
	if err := base.Get(t.Context(), key, &before); err != nil {
		t.Fatal(err)
	}
	if len(before.Spec.Withdrawals) != 1 || !before.Spec.Withdrawals[0].Allowed {
		t.Fatal("fixture did not establish withdrawal authorization")
	}
	if err := base.Get(t.Context(), client.ObjectKeyFromObject(claim), claim); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("namespace observation unavailable")
	r.Client = interceptor.NewClient(base, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if _, ok := obj.(*corev1.Namespace); ok {
			return failure
		}
		return c.Get(ctx, key, obj, opts...)
	}})
	if err := r.reconcileWithdrawals(t.Context(), key, []dnsv1alpha1.RecordWithdrawal{*claim}); !errors.Is(err, failure) {
		t.Fatalf("withdrawal error=%v", err)
	}
	var after dnsv1alpha1.ZoneUnit
	if err := base.Get(t.Context(), key, &after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("Namespace read error revoked withdrawal authorization")
	}
	var current dnsv1alpha1.RecordWithdrawal
	if err := base.Get(t.Context(), client.ObjectKeyFromObject(claim), &current); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(claim.Status, current.Status) {
		t.Fatal("Namespace read error was published as NotAllowed")
	}
}
