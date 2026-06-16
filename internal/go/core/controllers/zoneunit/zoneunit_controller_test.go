package zoneunit

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/appthrust/dns-api/internal/go/core/providercontract"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestZoneUnitCompositionReconcilerCreatesRecordSetItemsWithScalarClaimKey(t *testing.T) {
	ctx := context.Background()
	zone := zone("app", "apps-example-com", "apps.example.com")
	zone.Spec.ZoneClassRef.Namespace = ptr("platform")
	recordSet := aRecordSet("app", "www-a", "app", "apps-example-com", "www")
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(route53ProviderForZoneUnitTest(), route53ZoneClassForZoneUnitTest(), zone, recordSet).
		Build()
	reconciler := &ZoneUnitCompositionReconciler{Client: k8sClient}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(zone)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var unit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &unit); err != nil {
		t.Fatalf("ZoneUnit was not created: %v", err)
	}
	if len(unit.Spec.RecordSets) != 1 {
		t.Fatalf("recordSets = %#v, want one item", unit.Spec.RecordSets)
	}
	item := unit.Spec.RecordSets[0]
	if item.RecordSetNamespace != "app" || item.RecordSetName != "www-a" {
		t.Fatalf("recordSet key = %q/%q, want app/www-a", item.RecordSetNamespace, item.RecordSetName)
	}
	if item.Name != "www" || item.Type != dnsv1alpha1.RecordTypeA {
		t.Fatalf("record identity = %q/%q, want www/A", item.Name, item.Type)
	}
}

func TestZoneUnitCompositionReconcilerConvertsProviderPayloadsToStorageVersion(t *testing.T) {
	ctx := context.Background()
	zone := zone("app", "apps-example-com", "apps.example.com")
	zone.Spec.ZoneClassRef.Namespace = ptr("platform")
	zone.Spec.Provider.Version = "v1beta1"
	zone.Spec.Adoption = raw(t, map[string]any{"zoneID": "ZINPUT"})
	recordSet := aRecordSet("app", "www-a", "app", "apps-example-com", "www")
	recordSet.Spec.Provider.Version = "v1beta1"
	recordSet.Spec.Options = raw(t, map[string]any{"alias": map[string]any{"dnsName": "target.example.com."}})
	zoneClass := route53ZoneClassForZoneUnitTest()
	zoneClass.Spec.Provider.Version = "v1beta1"
	provider := route53ProviderForZoneUnitTest()
	provider.Spec.Versions = append(provider.Spec.Versions, dnsv1alpha1.ProviderVersion{
		Name:   "v1beta1",
		Served: true,
		Zone: dnsv1alpha1.ProviderZone{
			Conversion: dnsv1alpha1.ProviderConversion{
				ToStorage: dnsv1alpha1.ProviderConversionTarget{
					CEL: "{'adoption': {'storageZoneID': self.adoption.zoneID}}",
				},
			},
		},
		RecordSet: dnsv1alpha1.ProviderRecordSet{
			SupportedTypes: []dnsv1alpha1.RecordType{dnsv1alpha1.RecordTypeA},
			Conversion: dnsv1alpha1.ProviderConversion{
				ToStorage: dnsv1alpha1.ProviderConversionTarget{
					CEL: "{'options': {'storageAlias': self.options.alias}}",
				},
			},
		},
	})
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(provider, zoneClass, zone, recordSet).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}).
		Build()
	reconciler := &ZoneUnitCompositionReconciler{Client: k8sClient}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(zone)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var unit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &unit); err != nil {
		var gotZone dnsv1alpha1.Zone
		if getErr := k8sClient.Get(ctx, client.ObjectKeyFromObject(zone), &gotZone); getErr != nil {
			t.Fatalf("ZoneUnit was not created: %v; Zone get error: %v", err, getErr)
		}
		t.Fatalf("ZoneUnit was not created: %v; Zone status: %#v", err, gotZone.Status)
	}
	if unit.Spec.Provider.Version != "v1alpha1" {
		t.Fatalf("ZoneUnit provider version = %q, want storage version v1alpha1", unit.Spec.Provider.Version)
	}
	assertRawEqual(t, unit.Spec.Zone.Adoption, map[string]any{"storageZoneID": "ZINPUT"})
	if len(unit.Spec.RecordSets) != 1 {
		t.Fatalf("recordSets = %#v, want one item", unit.Spec.RecordSets)
	}
	assertRawEqual(t, unit.Spec.RecordSets[0].Options, map[string]any{
		"storageAlias": map[string]any{"dnsName": "target.example.com."},
	})
}

func TestZoneUnitCompositionReconcilerCopiesZoneReconcileRequestAnnotation(t *testing.T) {
	ctx := context.Background()
	zone := zone("app", "apps-example-com", "apps.example.com")
	zone.Spec.ZoneClassRef.Namespace = ptr("platform")
	zone.Annotations = map[string]string{
		reconcileRequestAnnotation: "2026-06-09T09:00:00Z",
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(route53ProviderForZoneUnitTest(), route53ZoneClassForZoneUnitTest(), zone).
		Build()
	reconciler := &ZoneUnitCompositionReconciler{Client: k8sClient}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(zone)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var unit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &unit); err != nil {
		t.Fatalf("ZoneUnit was not created: %v", err)
	}
	if got := unit.Annotations[reconcileRequestAnnotation]; got != "2026-06-09T09:00:00Z" {
		t.Fatalf("ZoneUnit reconcile annotation = %q, want copied request", got)
	}
}

func TestZoneUnitCompositionReconcilerUpdatesReconcileRequestAnnotationWithoutSpecChange(t *testing.T) {
	ctx := context.Background()
	zone := zone("app", "apps-example-com", "apps.example.com")
	zone.Spec.ZoneClassRef.Namespace = ptr("platform")
	zone.Annotations = map[string]string{
		reconcileRequestAnnotation: "2026-06-09T09:05:00Z",
	}
	unit := &dnsv1alpha1.ZoneUnit{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "app",
			Name:      "apps-example-com",
			Annotations: map[string]string{
				reconcileRequestAnnotation: "2026-06-09T09:00:00Z",
			},
		},
		Spec: zoneUnitSpecForZoneUnitTest(zone),
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(route53ProviderForZoneUnitTest(), route53ZoneClassForZoneUnitTest(), zone, unit).
		Build()
	reconciler := &ZoneUnitCompositionReconciler{Client: k8sClient}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(zone)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var got dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &got); err != nil {
		t.Fatalf("ZoneUnit was not found: %v", err)
	}
	if got.Annotations[reconcileRequestAnnotation] != "2026-06-09T09:05:00Z" {
		t.Fatalf("ZoneUnit reconcile annotation = %q, want updated request", got.Annotations[reconcileRequestAnnotation])
	}
	if !equality.Semantic.DeepEqual(got.Spec, unit.Spec) {
		t.Fatalf("ZoneUnit spec changed: %#v", got.Spec)
	}
}

func TestZoneUnitCompositionReconcilerRejectsRecordSetsNotAllowedByZone(t *testing.T) {
	ctx := context.Background()
	zone := zone("zone-ns", "apps-example-com", "apps.example.com")
	zone.Spec.ZoneClassRef.Namespace = ptr("platform")
	zone.Spec.AllowedRecordSets = []dnsv1alpha1.AllowedRecordSet{
		{
			Namespaces: dnsv1alpha1.AllowedRecordSetNamespaces{
				Selector: metav1.LabelSelector{MatchLabels: map[string]string{"access": "dns"}},
			},
			Records: []dnsv1alpha1.AllowedRecord{
				{Name: dnsv1alpha1.RecordNamePolicy{Pattern: "www"}, Types: []dnsv1alpha1.RecordType{dnsv1alpha1.RecordTypeA}},
			},
		},
	}
	recordSet := aRecordSet("record-ns", "www-a", "zone-ns", "apps-example-com", "www")
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(
			route53ProviderForZoneUnitTest(),
			route53ZoneClassForZoneUnitTest(),
			zone,
			recordSet,
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "record-ns"}},
		).
		WithStatusSubresource(&dnsv1alpha1.RecordSet{}).
		Build()
	reconciler := &ZoneUnitCompositionReconciler{Client: k8sClient}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(zone)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var unit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "zone-ns", Name: "apps-example-com"}, &unit); err != nil {
		t.Fatalf("ZoneUnit was not created: %v", err)
	}
	if len(unit.Spec.RecordSets) != 0 {
		t.Fatalf("recordSets = %#v, want no accepted items", unit.Spec.RecordSets)
	}
	var got dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(recordSet), &got); err != nil {
		t.Fatalf("RecordSet was not found: %v", err)
	}
	condition := assertCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionFalse, "NotAllowedByZone")
	if condition.Message != "RecordSet namespace is not allowed by the referenced Zone." {
		t.Fatalf("Accepted message = %q", condition.Message)
	}
	assertRecordSetZoneStatus(t, got.Status.Zone, "zone-ns", "apps-example-com")
}

func TestZoneUnitCompositionReconcilerRetainsExistingRecordSetNotAllowedByZone(t *testing.T) {
	ctx := context.Background()
	zone := zone("zone-ns", "apps-example-com", "apps.example.com")
	zone.Spec.ZoneClassRef.Namespace = ptr("platform")
	zone.Spec.AllowedRecordSets = []dnsv1alpha1.AllowedRecordSet{
		{
			Namespaces: dnsv1alpha1.AllowedRecordSetNamespaces{
				Selector: metav1.LabelSelector{MatchLabels: map[string]string{"access": "dns"}},
			},
			Records: []dnsv1alpha1.AllowedRecord{
				{Name: dnsv1alpha1.RecordNamePolicy{Pattern: "www"}, Types: []dnsv1alpha1.RecordType{dnsv1alpha1.RecordTypeA}},
			},
		},
	}
	recordSet := aRecordSet("record-ns", "www-a", "zone-ns", "apps-example-com", "www")
	recordSet.Finalizers = []string{coreRecordSetFinalizer}
	unit := &dnsv1alpha1.ZoneUnit{
		ObjectMeta: metav1.ObjectMeta{Namespace: "zone-ns", Name: "apps-example-com"},
		Spec:       zoneUnitSpecForZoneUnitTest(zone),
		Status: dnsv1alpha1.ZoneUnitStatus{
			RecordSets: []dnsv1alpha1.ZoneUnitRecordSetStatus{
				{
					RecordSetNamespace: "record-ns",
					RecordSetName:      "www-a",
					Conditions: []metav1.Condition{
						{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted"},
						{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionTrue, Reason: "Programmed"},
					},
				},
			},
		},
	}
	unit.Spec.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetSpec{
		zoneUnitRecordSetItem(recordSet, providercontract.Payload{}),
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(
			route53ProviderForZoneUnitTest(),
			route53ZoneClassForZoneUnitTest(),
			zone,
			recordSet,
			unit,
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "record-ns"}},
		).
		WithStatusSubresource(&dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneUnitCompositionReconciler{Client: k8sClient}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(zone)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var gotUnit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &gotUnit); err != nil {
		t.Fatalf("ZoneUnit was not found: %v", err)
	}
	if len(gotUnit.Spec.RecordSets) != 1 {
		t.Fatalf("recordSets = %#v, want retained item", gotUnit.Spec.RecordSets)
	}
	item := gotUnit.Spec.RecordSets[0]
	if item.RecordSetNamespace != "record-ns" || item.RecordSetName != "www-a" {
		t.Fatalf("recordSet item = %#v, want record-ns/www-a", item)
	}
	if item.IsAllowed() {
		t.Fatalf("recordSet item allowed = true, want false: %#v", item)
	}
	if item.DeletionRequested {
		t.Fatalf("recordSet item deletionRequested = true, want false")
	}

	var gotRecordSet dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(recordSet), &gotRecordSet); err != nil {
		t.Fatalf("RecordSet was not found: %v", err)
	}
	assertCondition(t, gotRecordSet.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionFalse, "NotAllowedByZone")
}

func TestZoneUnitCompositionReconcilerRequestsDeletionForRetainedNotAllowedRecordSet(t *testing.T) {
	ctx := context.Background()
	zone := zone("zone-ns", "apps-example-com", "apps.example.com")
	zone.Spec.ZoneClassRef.Namespace = ptr("platform")
	zone.Spec.AllowedRecordSets = []dnsv1alpha1.AllowedRecordSet{
		{
			Namespaces: dnsv1alpha1.AllowedRecordSetNamespaces{
				Selector: metav1.LabelSelector{MatchLabels: map[string]string{"access": "dns"}},
			},
			Records: []dnsv1alpha1.AllowedRecord{
				{Name: dnsv1alpha1.RecordNamePolicy{Pattern: "www"}, Types: []dnsv1alpha1.RecordType{dnsv1alpha1.RecordTypeA}},
			},
		},
	}
	recordSet := aRecordSet("record-ns", "www-a", "zone-ns", "apps-example-com", "www")
	deletionTime := metav1.NewTime(time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC))
	recordSet.DeletionTimestamp = &deletionTime
	recordSet.Finalizers = []string{coreRecordSetFinalizer}
	unit := &dnsv1alpha1.ZoneUnit{
		ObjectMeta: metav1.ObjectMeta{Namespace: "zone-ns", Name: "apps-example-com"},
		Spec:       zoneUnitSpecForZoneUnitTest(zone),
	}
	unit.Spec.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetSpec{
		zoneUnitRecordSetItem(recordSet, providercontract.Payload{}),
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(
			route53ProviderForZoneUnitTest(),
			route53ZoneClassForZoneUnitTest(),
			zone,
			recordSet,
			unit,
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "record-ns"}},
		).
		WithStatusSubresource(&dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneUnitCompositionReconciler{Client: k8sClient}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(zone)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var gotUnit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &gotUnit); err != nil {
		t.Fatalf("ZoneUnit was not found: %v", err)
	}
	if len(gotUnit.Spec.RecordSets) != 1 {
		t.Fatalf("recordSets = %#v, want retained deleting item", gotUnit.Spec.RecordSets)
	}
	item := gotUnit.Spec.RecordSets[0]
	if item.IsAllowed() {
		t.Fatalf("recordSet item allowed = true, want false: %#v", item)
	}
	if !item.DeletionRequested {
		t.Fatalf("recordSet item deletionRequested = false, want true")
	}
}

func TestZoneUnitCompositionReconcilerAcceptsRecordSetsAllowedByZone(t *testing.T) {
	ctx := context.Background()
	zone := zone("zone-ns", "apps-example-com", "apps.example.com")
	zone.Spec.ZoneClassRef.Namespace = ptr("platform")
	zone.Spec.AllowedRecordSets = []dnsv1alpha1.AllowedRecordSet{
		{
			Namespaces: dnsv1alpha1.AllowedRecordSetNamespaces{
				Selector: metav1.LabelSelector{MatchLabels: map[string]string{"access": "dns"}},
			},
			Records: []dnsv1alpha1.AllowedRecord{
				{Name: dnsv1alpha1.RecordNamePolicy{Pattern: "www"}, Types: []dnsv1alpha1.RecordType{dnsv1alpha1.RecordTypeA}},
			},
		},
	}
	recordSet := aRecordSet("record-ns", "www-a", "zone-ns", "apps-example-com", "www")
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(
			route53ProviderForZoneUnitTest(),
			route53ZoneClassForZoneUnitTest(),
			zone,
			recordSet,
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "record-ns", Labels: map[string]string{"access": "dns"}}},
		).
		Build()
	reconciler := &ZoneUnitCompositionReconciler{Client: k8sClient}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(zone)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var unit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "zone-ns", Name: "apps-example-com"}, &unit); err != nil {
		t.Fatalf("ZoneUnit was not created: %v", err)
	}
	if len(unit.Spec.RecordSets) != 1 {
		t.Fatalf("recordSets = %#v, want one accepted item", unit.Spec.RecordSets)
	}
	if unit.Spec.RecordSets[0].RecordSetNamespace != "record-ns" || unit.Spec.RecordSets[0].RecordSetName != "www-a" {
		t.Fatalf("recordSet key = %q/%q, want record-ns/www-a", unit.Spec.RecordSets[0].RecordSetNamespace, unit.Spec.RecordSets[0].RecordSetName)
	}
}

func TestZoneUnitCompositionReconcilerKeepsDeletingRecordSetUntilProviderDeletionCompletes(t *testing.T) {
	ctx := context.Background()
	zone := zone("app", "apps-example-com", "apps.example.com")
	zone.Spec.ZoneClassRef.Namespace = ptr("platform")
	recordSet := aRecordSet("app", "www-a", "app", "apps-example-com", "www")
	deletionTime := metav1.NewTime(time.Date(2026, 6, 10, 12, 10, 35, 0, time.UTC))
	recordSet.DeletionTimestamp = &deletionTime
	recordSet.Finalizers = []string{coreRecordSetFinalizer}
	unit := &dnsv1alpha1.ZoneUnit{
		ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "apps-example-com"},
		Spec:       zoneUnitSpecForZoneUnitTest(zone),
		Status: dnsv1alpha1.ZoneUnitStatus{
			RecordSets: []dnsv1alpha1.ZoneUnitRecordSetStatus{
				{
					RecordSetNamespace: "app",
					RecordSetName:      "www-a",
					Conditions: []metav1.Condition{
						{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted"},
						{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionTrue, Reason: "Programmed"},
					},
				},
			},
		},
	}
	unit.Spec.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetSpec{
		zoneUnitRecordSetItem(recordSet, providercontract.Payload{}),
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(route53ProviderForZoneUnitTest(), route53ZoneClassForZoneUnitTest(), zone, recordSet, unit).
		WithStatusSubresource(&dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneUnitCompositionReconciler{Client: k8sClient}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(zone)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var got dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &got); err != nil {
		t.Fatalf("ZoneUnit was not found: %v", err)
	}
	if len(got.Spec.RecordSets) != 1 {
		t.Fatalf("recordSets = %#v, want deleting item", got.Spec.RecordSets)
	}
	item := got.Spec.RecordSets[0]
	if item.RecordSetNamespace != "app" || item.RecordSetName != "www-a" || !item.DeletionRequested {
		t.Fatalf("recordSet item = %#v, want app/www-a with deletionRequested", item)
	}
}

func TestZoneUnitCompositionReconcilerDoesNotDeleteZoneUnitWhileZoneStillHasRecordSets(t *testing.T) {
	ctx := context.Background()
	zone := zone("app", "apps-example-com", "apps.example.com")
	zone.Spec.ZoneClassRef.Namespace = ptr("platform")
	deletionTime := metav1.NewTime(time.Date(2026, 6, 10, 12, 10, 35, 0, time.UTC))
	zone.DeletionTimestamp = &deletionTime
	zone.Finalizers = []string{coreZoneFinalizer}
	recordSet := aRecordSet("app", "www-a", "app", "apps-example-com", "www")
	unit := &dnsv1alpha1.ZoneUnit{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:  "app",
			Name:       "apps-example-com",
			Finalizers: []string{coreZoneUnitFinalizer, "route53.dns.appthrust.io/zoneunit-finalizer"},
		},
		Spec: zoneUnitSpecForZoneUnitTest(zone),
	}
	unit.Spec.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetSpec{
		zoneUnitRecordSetItem(recordSet, providercontract.Payload{}),
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(route53ProviderForZoneUnitTest(), route53ZoneClassForZoneUnitTest(), zone, recordSet, unit).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneUnitCompositionReconciler{Client: k8sClient}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(zone)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var gotUnit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &gotUnit); err != nil {
		t.Fatalf("ZoneUnit was not found: %v", err)
	}
	if !gotUnit.DeletionTimestamp.IsZero() {
		t.Fatalf("ZoneUnit deletionTimestamp = %v, want not deleting", gotUnit.DeletionTimestamp)
	}
	if len(gotUnit.Spec.RecordSets) != 1 {
		t.Fatalf("recordSets = %#v, want one remaining item", gotUnit.Spec.RecordSets)
	}
	var gotZone dnsv1alpha1.Zone
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(zone), &gotZone); err != nil {
		t.Fatalf("Zone was not found: %v", err)
	}
	condition := assertCondition(t, gotZone.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ProviderChangePending")
	if condition.Message != "1 RecordSet resources still reference this Zone; delete or move them before Zone cleanup can continue" {
		t.Fatalf("Programmed message = %q", condition.Message)
	}
}

func TestZoneUnitCompositionReconcilerCreatesZoneUnitForDeletingZoneWithRecordSets(t *testing.T) {
	ctx := context.Background()
	zone := zone("app", "apps-example-com", "apps.example.com")
	zone.Spec.ZoneClassRef.Namespace = ptr("platform")
	deletionTime := metav1.NewTime(time.Date(2026, 6, 10, 12, 10, 35, 0, time.UTC))
	zone.DeletionTimestamp = &deletionTime
	zone.Finalizers = []string{coreZoneFinalizer}
	recordSet := aRecordSet("app", "www-a", "app", "apps-example-com", "www")
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(route53ProviderForZoneUnitTest(), route53ZoneClassForZoneUnitTest(), zone, recordSet).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneUnitCompositionReconciler{Client: k8sClient}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(zone)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var gotUnit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(zone), &gotUnit); err != nil {
		t.Fatalf("ZoneUnit was not created: %v", err)
	}
	if !gotUnit.DeletionTimestamp.IsZero() {
		t.Fatalf("ZoneUnit deletionTimestamp = %v, want not deleting", gotUnit.DeletionTimestamp)
	}
	if len(gotUnit.Spec.RecordSets) != 1 {
		t.Fatalf("recordSets = %#v, want one remaining item", gotUnit.Spec.RecordSets)
	}
}

func TestZoneUnitCompositionReconcilerRemovesZoneFinalizerWhenDeletingZoneHasNoZoneUnitOrRecordSets(t *testing.T) {
	ctx := context.Background()
	zone := zone("app", "apps-example-com", "apps.example.com")
	zone.Spec.ZoneClassRef.Namespace = ptr("platform")
	deletionTime := metav1.NewTime(time.Date(2026, 6, 10, 12, 10, 35, 0, time.UTC))
	zone.DeletionTimestamp = &deletionTime
	zone.Finalizers = []string{coreZoneFinalizer}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(route53ProviderForZoneUnitTest(), route53ZoneClassForZoneUnitTest(), zone).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneUnitCompositionReconciler{Client: k8sClient}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(zone)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var gotZone dnsv1alpha1.Zone
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(zone), &gotZone); err != nil {
		if apierrors.IsNotFound(err) {
			return
		}
		t.Fatalf("Zone get returned error: %v", err)
	}
	if len(gotZone.Finalizers) != 0 {
		t.Fatalf("Zone finalizers = %#v, want none", gotZone.Finalizers)
	}
}

func TestZoneUnitCompositionReconcilerKeepsZoneUnitActiveForDeletingRecordSetsWhileZoneDeletes(t *testing.T) {
	ctx := context.Background()
	zone := zone("app", "apps-example-com", "apps.example.com")
	zone.Spec.ZoneClassRef.Namespace = ptr("platform")
	deletionTime := metav1.NewTime(time.Date(2026, 6, 10, 12, 10, 35, 0, time.UTC))
	zone.DeletionTimestamp = &deletionTime
	zone.Finalizers = []string{coreZoneFinalizer}
	recordSet := aRecordSet("app", "www-a", "app", "apps-example-com", "www")
	recordSet.DeletionTimestamp = &deletionTime
	recordSet.Finalizers = []string{coreRecordSetFinalizer}
	unit := &dnsv1alpha1.ZoneUnit{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:  "app",
			Name:       "apps-example-com",
			Finalizers: []string{coreZoneUnitFinalizer, "route53.dns.appthrust.io/zoneunit-finalizer"},
		},
		Spec: zoneUnitSpecForZoneUnitTest(zone),
		Status: dnsv1alpha1.ZoneUnitStatus{
			RecordSets: []dnsv1alpha1.ZoneUnitRecordSetStatus{
				{
					RecordSetNamespace: "app",
					RecordSetName:      "www-a",
					Conditions: []metav1.Condition{
						{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted"},
						{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionTrue, Reason: "Programmed"},
					},
				},
			},
		},
	}
	unit.Spec.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetSpec{
		zoneUnitRecordSetItem(recordSet, providercontract.Payload{}),
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(route53ProviderForZoneUnitTest(), route53ZoneClassForZoneUnitTest(), zone, recordSet, unit).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneUnitCompositionReconciler{Client: k8sClient}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(zone)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var gotUnit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &gotUnit); err != nil {
		t.Fatalf("ZoneUnit was not found: %v", err)
	}
	if !gotUnit.DeletionTimestamp.IsZero() {
		t.Fatalf("ZoneUnit deletionTimestamp = %v, want not deleting", gotUnit.DeletionTimestamp)
	}
	if len(gotUnit.Spec.RecordSets) != 1 {
		t.Fatalf("recordSets = %#v, want deleting item", gotUnit.Spec.RecordSets)
	}
	if !gotUnit.Spec.RecordSets[0].DeletionRequested {
		t.Fatalf("recordSet item = %#v, want deletionRequested", gotUnit.Spec.RecordSets[0])
	}
}

func TestZoneUnitCompositionReconcilerPrioritizesDeletingRecordSetOverNewConflictingRecordSet(t *testing.T) {
	ctx := context.Background()
	zone := zone("app", "apps-example-com", "apps.example.com")
	zone.Spec.ZoneClassRef.Namespace = ptr("platform")
	deletingA := aRecordSet("app", "www-a", "app", "apps-example-com", "www")
	deletionTime := metav1.NewTime(time.Date(2026, 6, 10, 12, 10, 35, 0, time.UTC))
	deletingA.DeletionTimestamp = &deletionTime
	deletingA.Finalizers = []string{coreRecordSetFinalizer}
	cname := cnameRecordSet("app", "www-cname", "app", "apps-example-com", "www")
	provider := route53ProviderForZoneUnitTest()
	provider.Spec.Versions[0].RecordSet.SupportedTypes = append(provider.Spec.Versions[0].RecordSet.SupportedTypes, dnsv1alpha1.RecordTypeCNAME)
	unit := &dnsv1alpha1.ZoneUnit{
		ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "apps-example-com"},
		Spec:       zoneUnitSpecForZoneUnitTest(zone),
		Status: dnsv1alpha1.ZoneUnitStatus{
			RecordSets: []dnsv1alpha1.ZoneUnitRecordSetStatus{
				{
					RecordSetNamespace: "app",
					RecordSetName:      "www-a",
					Conditions: []metav1.Condition{
						{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted"},
						{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionTrue, Reason: "Programmed"},
					},
				},
			},
		},
	}
	unit.Spec.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetSpec{
		zoneUnitRecordSetItem(cname, providercontract.Payload{}),
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(provider, route53ZoneClassForZoneUnitTest(), zone, deletingA, cname, unit).
		WithStatusSubresource(&dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneUnitCompositionReconciler{Client: k8sClient}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(zone)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var gotUnit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &gotUnit); err != nil {
		t.Fatalf("ZoneUnit was not found: %v", err)
	}
	if len(gotUnit.Spec.RecordSets) != 1 {
		t.Fatalf("recordSets = %#v, want only deleting A item", gotUnit.Spec.RecordSets)
	}
	item := gotUnit.Spec.RecordSets[0]
	if item.RecordSetName != "www-a" || item.Type != dnsv1alpha1.RecordTypeA || !item.DeletionRequested {
		t.Fatalf("recordSet item = %#v, want deleting A item", item)
	}

	var gotCNAME dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(cname), &gotCNAME); err != nil {
		t.Fatalf("CNAME RecordSet was not found: %v", err)
	}
	accepted := assertCondition(t, gotCNAME.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionFalse, "RecordSetConflict")
	if accepted.Message != "RecordSet type=CNAME name=www in Zone app/apps-example-com conflicts with owner RecordSet app/www-a." {
		t.Fatalf("Accepted message = %q", accepted.Message)
	}
	programmed := assertCondition(t, gotCNAME.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionUnknown, "Reconciling")
	if programmed.Message != recordSetNotAcceptedProgrammedMessage {
		t.Fatalf("Programmed message = %q", programmed.Message)
	}
}

func TestZoneUnitCompositionReconcilerDoesNotProjectStaleDeletedRecordSetStatusToRecreatedRecordSet(t *testing.T) {
	ctx := context.Background()
	zone := zone("app", "apps-example-com", "apps.example.com")
	zone.Spec.ZoneClassRef.Namespace = ptr("platform")
	recreatedA := aRecordSet("app", "www-a", "app", "apps-example-com", "www")
	cname := cnameRecordSet("app", "www-cname", "app", "apps-example-com", "www")
	provider := route53ProviderForZoneUnitTest()
	provider.Spec.Versions[0].RecordSet.SupportedTypes = append(provider.Spec.Versions[0].RecordSet.SupportedTypes, dnsv1alpha1.RecordTypeCNAME)
	unit := &dnsv1alpha1.ZoneUnit{
		ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "apps-example-com"},
		Spec:       zoneUnitSpecForZoneUnitTest(zone),
		Status: dnsv1alpha1.ZoneUnitStatus{
			RecordSets: []dnsv1alpha1.ZoneUnitRecordSetStatus{
				{
					RecordSetNamespace: "app",
					RecordSetName:      "www-a",
					ObservedGeneration: 2,
					DeletionCompleted:  true,
					Conditions: []metav1.Condition{
						{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted"},
						{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionTrue, Reason: "Programmed"},
					},
				},
				{
					RecordSetNamespace: "app",
					RecordSetName:      "www-cname",
					ObservedGeneration: cname.Generation,
					Conditions: []metav1.Condition{
						{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted"},
						{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionTrue, Reason: "Programmed"},
					},
				},
			},
		},
	}
	unit.Spec.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetSpec{
		zoneUnitRecordSetItem(cname, providercontract.Payload{}),
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(provider, route53ZoneClassForZoneUnitTest(), zone, recreatedA, cname, unit).
		WithStatusSubresource(&dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneUnitCompositionReconciler{Client: k8sClient}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(zone)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var gotA dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(recreatedA), &gotA); err != nil {
		t.Fatalf("A RecordSet was not found: %v", err)
	}
	accepted := assertCondition(t, gotA.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionFalse, "RecordSetConflict")
	if accepted.Message != "RecordSet type=A name=www in Zone app/apps-example-com conflicts with owner RecordSet app/www-cname." {
		t.Fatalf("Accepted message = %q", accepted.Message)
	}
	programmed := assertCondition(t, gotA.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionUnknown, "Reconciling")
	if programmed.Message != recordSetNotAcceptedProgrammedMessage {
		t.Fatalf("Programmed message = %q", programmed.Message)
	}

	var gotCNAME dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(cname), &gotCNAME); err != nil {
		t.Fatalf("CNAME RecordSet was not found: %v", err)
	}
	assertCondition(t, gotCNAME.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted")
	assertCondition(t, gotCNAME.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionTrue, "Programmed")
}

func TestZoneUnitCompositionReconcilerAllowsNonCNAMERecordSetsAtSameName(t *testing.T) {
	ctx := context.Background()
	zone := zone("app", "apps-example-com", "apps.example.com")
	zone.Spec.ZoneClassRef.Namespace = ptr("platform")
	a := aRecordSet("app", "www-a", "app", "apps-example-com", "www")
	txt := txtRecordSet("app", "www-txt", "app", "apps-example-com", "www")
	provider := route53ProviderForZoneUnitTest()
	provider.Spec.Versions[0].RecordSet.SupportedTypes = append(provider.Spec.Versions[0].RecordSet.SupportedTypes, dnsv1alpha1.RecordTypeTXT)
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(provider, route53ZoneClassForZoneUnitTest(), zone, a, txt).
		WithStatusSubresource(&dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneUnitCompositionReconciler{Client: k8sClient}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(zone)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var unit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &unit); err != nil {
		t.Fatalf("ZoneUnit was not found: %v", err)
	}
	if len(unit.Spec.RecordSets) != 2 {
		t.Fatalf("recordSets = %#v, want A and TXT items", unit.Spec.RecordSets)
	}
}

func TestZoneUnitCompositionReconcilerAllowsCloudflareCNAMEWithTXTAndMXAndCAAAtSameName(t *testing.T) {
	ctx := context.Background()
	zone := zone("app", "apps-example-com", "apps.example.com")
	zone.Spec.Provider = dnsv1alpha1.ProviderReference{Name: "cloudflare.dns.appthrust.io", Version: "v1alpha1"}
	zone.Spec.ZoneClassRef.Namespace = ptr("platform")
	zone.Spec.ZoneClassRef.Name = "cloudflare-public"
	cname := cloudflareRecordSet(cnameRecordSet("app", "www-cname", "app", "apps-example-com", "www"))
	txt := cloudflareRecordSet(txtRecordSet("app", "www-txt", "app", "apps-example-com", "www"))
	mx := cloudflareRecordSet(mxRecordSet("app", "www-mx", "app", "apps-example-com", "www"))
	caa := cloudflareRecordSet(caaRecordSet("app", "www-caa", "app", "apps-example-com", "www"))
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(cloudflareProviderForZoneUnitTest(), cloudflareZoneClassForZoneUnitTest(), zone, cname, txt, mx, caa).
		WithStatusSubresource(&dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneUnitCompositionReconciler{Client: k8sClient}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(zone)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var unit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &unit); err != nil {
		t.Fatalf("ZoneUnit was not found: %v", err)
	}
	if len(unit.Spec.RecordSets) != 4 {
		t.Fatalf("recordSets = %#v, want CNAME, TXT, MX, and CAA items", unit.Spec.RecordSets)
	}
}

func TestZoneUnitRecordSetsConflictUsesCloudflareCNAMECoexistencePolicy(t *testing.T) {
	provider := cloudflareProviderForZoneUnitTest()
	version := &provider.Spec.Versions[0]
	zone := zone("app", "apps-example-com", "apps.example.com")
	cname := zoneUnitRecordSetItem(cloudflareRecordSet(cnameRecordSet("app", "www-cname", "app", "apps-example-com", "www")), providercontract.Payload{})
	txt := zoneUnitRecordSetItem(cloudflareRecordSet(txtRecordSet("app", "www-txt", "app", "apps-example-com", "www")), providercontract.Payload{})
	a := zoneUnitRecordSetItem(cloudflareRecordSet(aRecordSet("app", "www-a", "app", "apps-example-com", "www")), providercontract.Payload{})

	if zoneUnitRecordSetsConflict(cname, txt, version, zone, provider) {
		t.Fatalf("CNAME and TXT should coexist for Cloudflare")
	}
	if !zoneUnitRecordSetsConflict(cname, a, version, zone, provider) {
		t.Fatalf("CNAME and A should conflict for Cloudflare")
	}
}

func TestZoneUnitCompositionReconcilerRejectsCloudflareCNAMEWithAAtSameName(t *testing.T) {
	ctx := context.Background()
	zone := zone("app", "apps-example-com", "apps.example.com")
	zone.Spec.Provider = dnsv1alpha1.ProviderReference{Name: "cloudflare.dns.appthrust.io", Version: "v1alpha1"}
	zone.Spec.ZoneClassRef.Namespace = ptr("platform")
	zone.Spec.ZoneClassRef.Name = "cloudflare-public"
	a := cloudflareRecordSet(aRecordSet("app", "www-a", "app", "apps-example-com", "www"))
	cname := cloudflareRecordSet(cnameRecordSet("app", "www-cname", "app", "apps-example-com", "www"))
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(cloudflareProviderForZoneUnitTest(), cloudflareZoneClassForZoneUnitTest(), zone, a, cname).
		WithStatusSubresource(&dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneUnitCompositionReconciler{Client: k8sClient}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(zone)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var got dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(cname), &got); err != nil {
		t.Fatalf("CNAME RecordSet was not found: %v", err)
	}
	condition := assertCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionFalse, "RecordSetConflict")
	if condition.Message != "RecordSet type=CNAME name=www in Zone app/apps-example-com conflicts with owner RecordSet app/www-a." {
		t.Fatalf("Accepted message = %q", condition.Message)
	}
}

func TestZoneUnitCompositionReconcilerRejectsCloudflareNSWithOtherTypeAtSameName(t *testing.T) {
	ctx := context.Background()
	zone := zone("app", "apps-example-com", "apps.example.com")
	zone.Spec.Provider = dnsv1alpha1.ProviderReference{Name: "cloudflare.dns.appthrust.io", Version: "v1alpha1"}
	zone.Spec.ZoneClassRef.Namespace = ptr("platform")
	zone.Spec.ZoneClassRef.Name = "cloudflare-public"
	ns := cloudflareRecordSet(nsRecordSet("app", "www-ns", "app", "apps-example-com", "www"))
	txt := cloudflareRecordSet(txtRecordSet("app", "www-txt", "app", "apps-example-com", "www"))
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(cloudflareProviderForZoneUnitTest(), cloudflareZoneClassForZoneUnitTest(), zone, ns, txt).
		WithStatusSubresource(&dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneUnitCompositionReconciler{Client: k8sClient}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(zone)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var got dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(txt), &got); err != nil {
		t.Fatalf("TXT RecordSet was not found: %v", err)
	}
	condition := assertCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionFalse, "RecordSetConflict")
	if condition.Message != "RecordSet type=TXT name=www in Zone app/apps-example-com conflicts with owner RecordSet app/www-ns." {
		t.Fatalf("Accepted message = %q", condition.Message)
	}
}

func TestZoneUnitCompositionReconcilerRejectsRecordSetTypeNotSupportedByProvider(t *testing.T) {
	ctx := context.Background()
	zone := zone("app", "apps-example-com", "apps.example.com")
	zone.Spec.ZoneClassRef.Namespace = ptr("platform")
	recordSet := aRecordSet("app", "www-aaaa", "app", "apps-example-com", "www")
	recordSet.Spec.Type = dnsv1alpha1.RecordTypeAAAA
	recordSet.Spec.A = nil
	recordSet.Spec.AAAA = &dnsv1alpha1.AAAARecordSet{Addresses: []string{"2001:db8::10"}}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(route53ProviderForZoneUnitTest(), route53ZoneClassForZoneUnitTest(), zone, recordSet).
		WithStatusSubresource(&dnsv1alpha1.RecordSet{}).
		Build()
	reconciler := &ZoneUnitCompositionReconciler{Client: k8sClient}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(zone)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var unit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &unit); err != nil {
		t.Fatalf("ZoneUnit was not created: %v", err)
	}
	if len(unit.Spec.RecordSets) != 0 {
		t.Fatalf("recordSets = %#v, want no accepted items", unit.Spec.RecordSets)
	}
	var got dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(recordSet), &got); err != nil {
		t.Fatalf("RecordSet was not found: %v", err)
	}
	condition := assertCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionFalse, "InvalidProvider")
	if condition.Message != "RecordSet type is not supported by the provider" {
		t.Fatalf("Accepted message = %q", condition.Message)
	}
}

func TestZoneUnitCompositionReconcilerRejectsRecordSetProviderMismatch(t *testing.T) {
	ctx := context.Background()
	zone := zone("app", "apps-example-com", "apps.example.com")
	zone.Spec.ZoneClassRef.Namespace = ptr("platform")
	recordSet := aRecordSet("app", "www-a", "app", "apps-example-com", "www")
	recordSet.Spec.Provider = dnsv1alpha1.ProviderReference{Name: "cloudflare.dns.appthrust.io", Version: "v1alpha1"}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(route53ProviderForZoneUnitTest(), route53ZoneClassForZoneUnitTest(), zone, recordSet).
		WithStatusSubresource(&dnsv1alpha1.RecordSet{}).
		Build()
	reconciler := &ZoneUnitCompositionReconciler{Client: k8sClient}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(zone)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var unit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &unit); err != nil {
		t.Fatalf("ZoneUnit was not created: %v", err)
	}
	if len(unit.Spec.RecordSets) != 0 {
		t.Fatalf("recordSets = %#v, want no accepted items", unit.Spec.RecordSets)
	}
	var got dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(recordSet), &got); err != nil {
		t.Fatalf("RecordSet was not found: %v", err)
	}
	condition := assertCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionFalse, "ProviderMismatch")
	if condition.Message != "RecordSet provider does not match referenced ZoneClass" {
		t.Fatalf("Accepted message = %q", condition.Message)
	}
}

func TestZoneUnitCompositionReconcilerPropagatesZoneProviderMismatchToRecordSets(t *testing.T) {
	ctx := context.Background()
	zone := zone("app", "apps-example-com", "apps.example.com")
	zone.Spec.ZoneClassRef.Namespace = ptr("platform")
	zone.Spec.Provider = dnsv1alpha1.ProviderReference{Name: "cloudflare.dns.appthrust.io", Version: "v1alpha1"}
	recordSet := aRecordSet("app", "www-a", "app", "apps-example-com", "www")
	recordSet.Status.Conditions = []metav1.Condition{
		{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted", ObservedGeneration: 1},
		{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionTrue, Reason: "Programmed", ObservedGeneration: 1},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(route53ProviderForZoneUnitTest(), route53ZoneClassForZoneUnitTest(), zone, recordSet).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}).
		Build()
	reconciler := &ZoneUnitCompositionReconciler{Client: k8sClient}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(zone)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var gotZone dnsv1alpha1.Zone
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(zone), &gotZone); err != nil {
		t.Fatalf("Zone was not found: %v", err)
	}
	assertCondition(t, gotZone.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionFalse, "ProviderMismatch")

	var gotRecordSet dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(recordSet), &gotRecordSet); err != nil {
		t.Fatalf("RecordSet was not found: %v", err)
	}
	assertCondition(t, gotRecordSet.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionFalse, "ZoneNotAccepted")
	assertCondition(t, gotRecordSet.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionUnknown, "Reconciling")
}

func TestZoneUnitCompositionReconcilerRejectsZoneNotAllowedByZoneClass(t *testing.T) {
	ctx := context.Background()
	zone := zone("app", "apps-example-com", "apps.example.com")
	zone.Spec.ZoneClassRef.Namespace = ptr("platform")
	recordSet := aRecordSet("app", "www-a", "app", "apps-example-com", "www")
	zoneClass := route53ZoneClassForZoneUnitTest()
	zoneClass.Spec.AllowedZones = dnsv1alpha1.ZoneClassAllowedZones{
		Namespaces: dnsv1alpha1.NamespacePolicy{From: dnsv1alpha1.NamespacesFromSame},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(route53ProviderForZoneUnitTest(), zoneClass, zone, recordSet).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}).
		Build()
	reconciler := &ZoneUnitCompositionReconciler{Client: k8sClient}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(zone)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var unit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &unit); !apierrors.IsNotFound(err) {
		t.Fatalf("ZoneUnit get error = %v, want not found", err)
	}
	var gotZone dnsv1alpha1.Zone
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(zone), &gotZone); err != nil {
		t.Fatalf("Zone was not found: %v", err)
	}
	zoneAccepted := assertCondition(t, gotZone.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionFalse, "NotAllowedByZoneClass")
	if zoneAccepted.Message != "Zone namespace is not allowed by ZoneClass" {
		t.Fatalf("Zone Accepted message = %q", zoneAccepted.Message)
	}
	var gotRecordSet dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(recordSet), &gotRecordSet); err != nil {
		t.Fatalf("RecordSet was not found: %v", err)
	}
	assertCondition(t, gotRecordSet.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionFalse, "ZoneNotAccepted")
}

func TestZoneUnitCompositionReconcilerAcceptsZoneAllowedByZoneClassSelector(t *testing.T) {
	ctx := context.Background()
	zone := zone("app", "apps-example-com", "apps.example.com")
	zone.Spec.ZoneClassRef.Namespace = ptr("platform")
	zoneClass := route53ZoneClassForZoneUnitTest()
	zoneClass.Spec.AllowedZones = dnsv1alpha1.ZoneClassAllowedZones{
		Namespaces: dnsv1alpha1.NamespacePolicy{
			From:     dnsv1alpha1.NamespacesFromSelector,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"tenant": "app"}},
		},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(
			route53ProviderForZoneUnitTest(),
			zoneClass,
			zone,
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "app", Labels: map[string]string{"tenant": "app"}}},
		).
		Build()
	reconciler := &ZoneUnitCompositionReconciler{Client: k8sClient}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(zone)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var unit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &unit); err != nil {
		t.Fatalf("ZoneUnit was not created: %v", err)
	}
	if unit.Spec.Zone.ZoneClassRef.Namespace != "platform" || unit.Spec.Zone.ZoneClassRef.Name != "route53-public" {
		t.Fatalf("zoneClassRef = %#v", unit.Spec.Zone.ZoneClassRef)
	}
}

func TestZoneUnitCompositionReconcilerProjectsRecordSetStatusByScalarClaimKey(t *testing.T) {
	ctx := context.Background()
	zone := zone("app", "apps-example-com", "apps.example.com")
	zone.Spec.ZoneClassRef.Namespace = ptr("platform")
	recordSet := aRecordSet("app", "www-a", "app", "apps-example-com", "www")
	zoneClass := route53ZoneClassForZoneUnitTest()
	unit := &dnsv1alpha1.ZoneUnit{
		ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "apps-example-com"},
		Spec: dnsv1alpha1.ZoneUnitSpec{
			Zone: dnsv1alpha1.ZoneUnitZoneSpec{
				Ref: dnsv1alpha1.ObjectReference{Namespace: "app", Name: "apps-example-com"},
			},
			RecordSets: []dnsv1alpha1.ZoneUnitRecordSetSpec{
				zoneUnitRecordSetItem(recordSet, providercontract.Payload{}),
			},
		},
		Status: dnsv1alpha1.ZoneUnitStatus{
			RecordSets: []dnsv1alpha1.ZoneUnitRecordSetStatus{
				{
					RecordSetNamespace: "app",
					RecordSetName:      "www-a",
					Conditions: []metav1.Condition{
						{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted"},
						{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionTrue, Reason: "Programmed"},
					},
				},
			},
		},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(zone, zoneClass, recordSet, unit).
		WithStatusSubresource(&dnsv1alpha1.RecordSet{}).
		Build()
	reconciler := &ZoneUnitCompositionReconciler{Client: k8sClient}

	if err := reconciler.projectZoneUnitStatus(ctx, zone, zoneClass, []dnsv1alpha1.RecordSet{*recordSet}, unit); err != nil {
		t.Fatalf("projectZoneUnitStatus returned error: %v", err)
	}

	var got dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(recordSet), &got); err != nil {
		t.Fatalf("RecordSet was not found: %v", err)
	}
	assertCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted")
	assertCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionTrue, "Programmed")
	assertRecordSetZoneStatus(t, got.Status.Zone, "app", "apps-example-com")
}

func TestZoneUnitCompositionReconcilerProviderAcceptanceOverridesStaleClaimCondition(t *testing.T) {
	ctx := context.Background()
	zone := zone("app", "apps-example-com", "apps.example.com")
	zone.Spec.ZoneClassRef.Namespace = ptr("platform")
	zone.Status.Conditions = []metav1.Condition{
		{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionFalse, Reason: "ZoneClassNotAccepted", ObservedGeneration: 1},
		{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionUnknown, Reason: "Reconciling", ObservedGeneration: 1},
	}
	zoneClass := route53ZoneClassForZoneUnitTest()
	unit := &dnsv1alpha1.ZoneUnit{
		ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "apps-example-com"},
		Spec: dnsv1alpha1.ZoneUnitSpec{
			Zone: dnsv1alpha1.ZoneUnitZoneSpec{
				Ref: dnsv1alpha1.ObjectReference{Namespace: "app", Name: "apps-example-com"},
			},
		},
		Status: dnsv1alpha1.ZoneUnitStatus{
			Zone: &dnsv1alpha1.ZoneUnitZoneStatus{
				Conditions: []metav1.Condition{
					{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted"},
					{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionTrue, Reason: "Programmed"},
				},
			},
		},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(zone, zoneClass, unit).
		WithStatusSubresource(&dnsv1alpha1.Zone{}).
		Build()
	reconciler := &ZoneUnitCompositionReconciler{Client: k8sClient}

	if err := reconciler.projectZoneUnitStatus(ctx, zone, zoneClass, nil, unit); err != nil {
		t.Fatalf("projectZoneUnitStatus returned error: %v", err)
	}

	var got dnsv1alpha1.Zone
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(zone), &got); err != nil {
		t.Fatalf("Zone was not found: %v", err)
	}
	assertCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted")
	assertCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionTrue, "Programmed")
}

func TestZoneUnitCompositionReconcilerClearsStaleClaimProviderStatus(t *testing.T) {
	ctx := context.Background()
	zone := zone("app", "apps-example-com", "apps.example.com")
	zone.Spec.ZoneClassRef.Namespace = ptr("platform")
	zone.Status.Provider = &dnsv1alpha1.ProviderStatus{Data: runtime.RawExtension{Raw: []byte(`{"hostedZoneID":"ZSTALE"}`)}}
	recordSet := aRecordSet("app", "www-a", "app", "apps-example-com", "www")
	recordSet.Status.Provider = &dnsv1alpha1.ProviderStatus{Data: runtime.RawExtension{Raw: []byte(`{"records":[{"id":"stale"}]}`)}}
	zoneClass := route53ZoneClassForZoneUnitTest()
	unit := &dnsv1alpha1.ZoneUnit{
		ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "apps-example-com"},
		Spec: dnsv1alpha1.ZoneUnitSpec{
			Zone: dnsv1alpha1.ZoneUnitZoneSpec{
				Ref: dnsv1alpha1.ObjectReference{Namespace: "app", Name: "apps-example-com"},
			},
			RecordSets: []dnsv1alpha1.ZoneUnitRecordSetSpec{
				zoneUnitRecordSetItem(recordSet, providercontract.Payload{}),
			},
		},
		Status: dnsv1alpha1.ZoneUnitStatus{
			Zone: &dnsv1alpha1.ZoneUnitZoneStatus{
				Conditions: []metav1.Condition{
					{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted"},
					{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionTrue, Reason: "Programmed"},
				},
			},
			RecordSets: []dnsv1alpha1.ZoneUnitRecordSetStatus{
				{
					RecordSetNamespace: "app",
					RecordSetName:      "www-a",
					Conditions: []metav1.Condition{
						{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted"},
						{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionTrue, Reason: "Programmed"},
					},
				},
			},
		},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(zone, zoneClass, recordSet, unit).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}).
		Build()
	reconciler := &ZoneUnitCompositionReconciler{Client: k8sClient}

	if err := reconciler.projectZoneUnitStatus(ctx, zone, zoneClass, []dnsv1alpha1.RecordSet{*recordSet}, unit); err != nil {
		t.Fatalf("projectZoneUnitStatus returned error: %v", err)
	}

	var gotZone dnsv1alpha1.Zone
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(zone), &gotZone); err != nil {
		t.Fatalf("Zone was not found: %v", err)
	}
	if gotZone.Status.Provider != nil {
		t.Fatalf("zone status.provider = %#v, want nil", gotZone.Status.Provider)
	}

	var gotRecordSet dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(recordSet), &gotRecordSet); err != nil {
		t.Fatalf("RecordSet was not found: %v", err)
	}
	if gotRecordSet.Status.Provider != nil {
		t.Fatalf("recordSet status.provider = %#v, want nil", gotRecordSet.Status.Provider)
	}
}

func TestZoneUnitCompositionReconcilerProjectsRecordSetProviderDataOnly(t *testing.T) {
	ctx := context.Background()
	zone := zone("app", "apps-example-com", "apps.example.com")
	zone.Spec.ZoneClassRef.Namespace = ptr("platform")
	recordSet := aRecordSet("app", "www-a", "app", "apps-example-com", "www")
	zoneClass := route53ZoneClassForZoneUnitTest()
	unit := &dnsv1alpha1.ZoneUnit{
		ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "apps-example-com"},
		Spec: dnsv1alpha1.ZoneUnitSpec{
			Zone: dnsv1alpha1.ZoneUnitZoneSpec{
				Ref: dnsv1alpha1.ObjectReference{Namespace: "app", Name: "apps-example-com"},
			},
			RecordSets: []dnsv1alpha1.ZoneUnitRecordSetSpec{
				zoneUnitRecordSetItem(recordSet, providercontract.Payload{}),
			},
		},
		Status: dnsv1alpha1.ZoneUnitStatus{
			Zone: &dnsv1alpha1.ZoneUnitZoneStatus{
				Conditions: []metav1.Condition{
					{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted"},
					{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionTrue, Reason: "Programmed"},
				},
			},
			RecordSets: []dnsv1alpha1.ZoneUnitRecordSetStatus{
				{
					RecordSetNamespace: "app",
					RecordSetName:      "www-a",
					Provider: &dnsv1alpha1.ProviderStatus{
						Data:  runtime.RawExtension{Raw: []byte(`{"records":[{"id":"023e105f4ecef8ad9ca31a8372d0c010"}]}`)},
						State: runtime.RawExtension{Raw: []byte(`{"records":[{"id":"023e105f4ecef8ad9ca31a8372d0c010","content":"192.0.2.10"}]}`)},
					},
					Conditions: []metav1.Condition{
						{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted"},
						{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionTrue, Reason: "Programmed"},
					},
				},
			},
		},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(zone, zoneClass, recordSet, unit).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}).
		Build()
	reconciler := &ZoneUnitCompositionReconciler{Client: k8sClient}

	if err := reconciler.projectZoneUnitStatus(ctx, zone, zoneClass, []dnsv1alpha1.RecordSet{*recordSet}, unit); err != nil {
		t.Fatalf("projectZoneUnitStatus returned error: %v", err)
	}

	var got dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(recordSet), &got); err != nil {
		t.Fatalf("RecordSet was not found: %v", err)
	}
	if got.Status.Provider == nil {
		t.Fatalf("RecordSet status.provider = nil, want projected provider data")
	}
	if string(got.Status.Provider.Data.Raw) != `{"records":[{"id":"023e105f4ecef8ad9ca31a8372d0c010"}]}` {
		t.Fatalf("RecordSet status.provider.data = %s", string(got.Status.Provider.Data.Raw))
	}
	if len(got.Status.Provider.State.Raw) != 0 {
		t.Fatalf("RecordSet status.provider.state = %s, want empty", string(got.Status.Provider.State.Raw))
	}
}

func route53ProviderForZoneUnitTest() *dnsv1alpha1.Provider {
	return &dnsv1alpha1.Provider{
		ObjectMeta: metav1.ObjectMeta{Name: "route53.dns.appthrust.io"},
		Spec: dnsv1alpha1.ProviderSpec{
			Display: dnsv1alpha1.ProviderDisplay{Name: "Amazon Route 53"},
			Versions: []dnsv1alpha1.ProviderVersion{
				{
					Name:    "v1alpha1",
					Served:  true,
					Storage: true,
					RecordSet: dnsv1alpha1.ProviderRecordSet{
						SupportedTypes: []dnsv1alpha1.RecordType{dnsv1alpha1.RecordTypeA},
					},
				},
			},
		},
	}
}

func cloudflareProviderForZoneUnitTest() *dnsv1alpha1.Provider {
	provider := route53ProviderForZoneUnitTest()
	provider.Name = "cloudflare.dns.appthrust.io"
	provider.Spec.Display.Name = "Cloudflare DNS"
	provider.Spec.Versions[0].RecordSet.SupportedTypes = []dnsv1alpha1.RecordType{
		dnsv1alpha1.RecordTypeA,
		dnsv1alpha1.RecordTypeAAAA,
		dnsv1alpha1.RecordTypeTXT,
		dnsv1alpha1.RecordTypeCNAME,
		dnsv1alpha1.RecordTypeMX,
		dnsv1alpha1.RecordTypeCAA,
		dnsv1alpha1.RecordTypeNS,
	}
	provider.Spec.Versions[0].ZoneUnit.DisableValidations = []dnsv1alpha1.ZoneUnitValidationToggle{
		{
			Name: "forbid-cname-coexistence",
			When: "(self.type == 'CNAME' && other.type in ['TXT', 'MX', 'CAA']) || " +
				"(other.type == 'CNAME' && self.type in ['TXT', 'MX', 'CAA'])",
		},
	}
	provider.Spec.Versions[0].ZoneUnit.ValidationRules = []dnsv1alpha1.ProviderValidationRule{
		{
			Rule:    "self.name != other.name || !(self.type == 'NS' || other.type == 'NS')",
			Message: "cloudflare does not allow NS records to coexist with another record type at the same owner name",
		},
	}
	return provider
}

func route53ZoneClassForZoneUnitTest() *dnsv1alpha1.ZoneClass {
	return &dnsv1alpha1.ZoneClass{
		ObjectMeta: metav1.ObjectMeta{Namespace: "platform", Name: "route53-public"},
		Spec: dnsv1alpha1.ZoneClassSpec{
			Provider:       dnsv1alpha1.ProviderReference{Name: "route53.dns.appthrust.io", Version: "v1alpha1"},
			ControllerName: "route53.dns.appthrust.io/controller",
			IdentityRef:    dnsv1alpha1.LocalObjectReference{Name: "route53-dev"},
			AllowedZones: dnsv1alpha1.ZoneClassAllowedZones{
				Namespaces: dnsv1alpha1.NamespacePolicy{From: dnsv1alpha1.NamespacesFromAll},
			},
		},
	}
}

func cloudflareZoneClassForZoneUnitTest() *dnsv1alpha1.ZoneClass {
	zoneClass := route53ZoneClassForZoneUnitTest()
	zoneClass.Name = "cloudflare-public"
	zoneClass.Spec.Provider = dnsv1alpha1.ProviderReference{Name: "cloudflare.dns.appthrust.io", Version: "v1alpha1"}
	zoneClass.Spec.ControllerName = "cloudflare.dns.appthrust.io/controller"
	return zoneClass
}

func zoneUnitSpecForZoneUnitTest(zone *dnsv1alpha1.Zone) dnsv1alpha1.ZoneUnitSpec {
	return dnsv1alpha1.ZoneUnitSpec{
		Provider: dnsv1alpha1.ProviderReference{Name: "route53.dns.appthrust.io", Version: "v1alpha1"},
		Zone: dnsv1alpha1.ZoneUnitZoneSpec{
			Ref:                dnsv1alpha1.ObjectReference{Namespace: zone.Namespace, Name: zone.Name},
			ObservedGeneration: zone.Generation,
			DomainName:         zone.Spec.DomainName,
			ZoneClassRef:       dnsv1alpha1.ObjectReference{Namespace: "platform", Name: "route53-public"},
		},
	}
}

func assertCondition(t *testing.T, conditions []metav1.Condition, conditionType string, status metav1.ConditionStatus, reason string) *metav1.Condition {
	t.Helper()
	condition := meta.FindStatusCondition(conditions, conditionType)
	if condition == nil {
		t.Fatalf("condition %s was not found in %#v", conditionType, conditions)
	}
	if condition.Status != status || condition.Reason != reason {
		t.Fatalf("condition %s = (%s, %s), want (%s, %s)", conditionType, condition.Status, condition.Reason, status, reason)
	}
	return condition
}

func assertRecordSetZoneStatus(t *testing.T, status *dnsv1alpha1.RecordSetZoneStatus, namespace, name string) {
	t.Helper()
	if status == nil {
		t.Fatalf("status.zone was nil")
	}
	if status.Ref.Namespace != namespace || status.Ref.Name != name {
		t.Fatalf("status.zone.ref = %#v, want %s/%s", status.Ref, namespace, name)
	}
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("core AddToScheme: %v", err)
	}
	if err := dnsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("dns AddToScheme: %v", err)
	}
	return scheme
}

func zone(namespace, name, domainName string) *dnsv1alpha1.Zone {
	return &dnsv1alpha1.Zone{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: dnsv1alpha1.ZoneSpec{
			DomainName: domainName,
			Provider:   dnsv1alpha1.ProviderReference{Name: "route53.dns.appthrust.io", Version: "v1alpha1"},
			ZoneClassRef: dnsv1alpha1.ZoneClassReference{
				Name: "route53-public",
			},
		},
	}
}

func cloudflareRecordSet(recordSet *dnsv1alpha1.RecordSet) *dnsv1alpha1.RecordSet {
	recordSet.Spec.Provider = dnsv1alpha1.ProviderReference{Name: "cloudflare.dns.appthrust.io", Version: "v1alpha1"}
	return recordSet
}

func aRecordSet(namespace, name, zoneNamespace, zoneName, ownerName string) *dnsv1alpha1.RecordSet {
	return &dnsv1alpha1.RecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: dnsv1alpha1.RecordSetSpec{
			ZoneRef: dnsv1alpha1.ZoneReference{
				Namespace: ptr(zoneNamespace),
				Name:      zoneName,
			},
			Provider: dnsv1alpha1.ProviderReference{Name: "route53.dns.appthrust.io", Version: "v1alpha1"},
			Type:     dnsv1alpha1.RecordTypeA,
			Name:     ownerName,
			TTL:      ptr(int32(300)),
			A:        &dnsv1alpha1.ARecordSet{Addresses: []string{"192.0.2.10"}},
		},
	}
}

func cnameRecordSet(namespace, name, zoneNamespace, zoneName, ownerName string) *dnsv1alpha1.RecordSet {
	return &dnsv1alpha1.RecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: dnsv1alpha1.RecordSetSpec{
			ZoneRef: dnsv1alpha1.ZoneReference{
				Namespace: ptr(zoneNamespace),
				Name:      zoneName,
			},
			Provider: dnsv1alpha1.ProviderReference{Name: "route53.dns.appthrust.io", Version: "v1alpha1"},
			Type:     dnsv1alpha1.RecordTypeCNAME,
			Name:     ownerName,
			TTL:      ptr(int32(300)),
			CNAME:    &dnsv1alpha1.CNAMERecordSet{Target: "target.example.com"},
		},
	}
}

func txtRecordSet(namespace, name, zoneNamespace, zoneName, ownerName string) *dnsv1alpha1.RecordSet {
	return &dnsv1alpha1.RecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: dnsv1alpha1.RecordSetSpec{
			ZoneRef: dnsv1alpha1.ZoneReference{
				Namespace: ptr(zoneNamespace),
				Name:      zoneName,
			},
			Provider: dnsv1alpha1.ProviderReference{Name: "route53.dns.appthrust.io", Version: "v1alpha1"},
			Type:     dnsv1alpha1.RecordTypeTXT,
			Name:     ownerName,
			TTL:      ptr(int32(300)),
			TXT:      &dnsv1alpha1.TXTRecordSet{Values: []string{"test"}},
		},
	}
}

func mxRecordSet(namespace, name, zoneNamespace, zoneName, ownerName string) *dnsv1alpha1.RecordSet {
	return &dnsv1alpha1.RecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: dnsv1alpha1.RecordSetSpec{
			ZoneRef: dnsv1alpha1.ZoneReference{
				Namespace: ptr(zoneNamespace),
				Name:      zoneName,
			},
			Provider: dnsv1alpha1.ProviderReference{Name: "route53.dns.appthrust.io", Version: "v1alpha1"},
			Type:     dnsv1alpha1.RecordTypeMX,
			Name:     ownerName,
			TTL:      ptr(int32(300)),
			MX:       &dnsv1alpha1.MXRecordSet{Records: []dnsv1alpha1.MXRecord{{Preference: 10, Exchange: "mail.example.com"}}},
		},
	}
}

func caaRecordSet(namespace, name, zoneNamespace, zoneName, ownerName string) *dnsv1alpha1.RecordSet {
	return &dnsv1alpha1.RecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: dnsv1alpha1.RecordSetSpec{
			ZoneRef: dnsv1alpha1.ZoneReference{
				Namespace: ptr(zoneNamespace),
				Name:      zoneName,
			},
			Provider: dnsv1alpha1.ProviderReference{Name: "route53.dns.appthrust.io", Version: "v1alpha1"},
			Type:     dnsv1alpha1.RecordTypeCAA,
			Name:     ownerName,
			TTL:      ptr(int32(300)),
			CAA:      &dnsv1alpha1.CAARecordSet{Records: []dnsv1alpha1.CAARecord{{Flags: 0, Tag: "issue", Value: "letsencrypt.org"}}},
		},
	}
}

func nsRecordSet(namespace, name, zoneNamespace, zoneName, ownerName string) *dnsv1alpha1.RecordSet {
	return &dnsv1alpha1.RecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: dnsv1alpha1.RecordSetSpec{
			ZoneRef: dnsv1alpha1.ZoneReference{
				Namespace: ptr(zoneNamespace),
				Name:      zoneName,
			},
			Provider: dnsv1alpha1.ProviderReference{Name: "route53.dns.appthrust.io", Version: "v1alpha1"},
			Type:     dnsv1alpha1.RecordTypeNS,
			Name:     ownerName,
			TTL:      ptr(int32(300)),
			NS:       &dnsv1alpha1.NSRecordSet{NameServers: []string{"ns1.example.net", "ns2.example.net"}},
		},
	}
}

func ptr[T any](value T) *T {
	return &value
}

func raw(t *testing.T, value any) runtime.RawExtension {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json marshal: %v", err)
	}
	return runtime.RawExtension{Raw: data}
}

func assertRawEqual(t *testing.T, got runtime.RawExtension, want map[string]any) {
	t.Helper()
	var gotObject map[string]any
	if err := json.Unmarshal(got.Raw, &gotObject); err != nil {
		t.Fatalf("raw JSON = %s, decode error: %v", string(got.Raw), err)
	}
	if !equality.Semantic.DeepEqual(gotObject, want) {
		t.Fatalf("raw JSON = %#v, want %#v", gotObject, want)
	}
}
