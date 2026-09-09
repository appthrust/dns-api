package zoneunit

import (
	"context"
	"testing"
	"time"

	"github.com/appthrust/dns-api/internal/go/core/providercontract"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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
	if item.RecordSetUID != recordSet.UID {
		t.Fatalf("recordSetUID = %q, want claim UID %q", item.RecordSetUID, recordSet.UID)
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
					RecordSetUID:       recordSet.UID,
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
	if item.RecordSetUID != recordSet.UID {
		t.Fatalf("recordSetUID = %q, want claim UID %q", item.RecordSetUID, recordSet.UID)
	}
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
					RecordSetUID:       recordSet.UID,
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
					RecordSetUID:       recordSet.UID,
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
					RecordSetUID:       deletingA.UID,
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
					RecordSetUID:       types.UID("predecessor-recordset"),
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
					RecordSetUID:       cname.UID,
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
