package zoneunit

import (
	"context"
	"testing"

	"github.com/appthrust/dns-api/internal/go/core/providercontract"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestZoneUnitCompositionReconcilerDoesNotRebadgeStaleSameUIDProviderStatus(t *testing.T) {
	ctx := context.Background()
	zone := zone("app", "apps-example-com", "apps.example.com")
	zone.Spec.ZoneClassRef.Namespace = ptr("platform")
	zone.Generation = 2
	recordSet := aRecordSet("app", "www-a", "app", "apps-example-com", "www")
	recordSet.UID = types.UID("current-recordset")
	recordSet.Generation = 2
	staleItem := zoneUnitRecordSetItem(recordSet, providercontract.Payload{})
	staleItem.ObservedGeneration = 1
	unit := &dnsv1alpha1.ZoneUnit{
		ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "apps-example-com", Generation: 1},
		Spec: dnsv1alpha1.ZoneUnitSpec{
			Provider: dnsv1alpha1.ProviderReference{Name: "route53.dns.appthrust.io", Version: "v1alpha1"},
			Zone: dnsv1alpha1.ZoneUnitZoneSpec{
				Ref:                dnsv1alpha1.ObjectReference{Namespace: zone.Namespace, Name: zone.Name},
				ObservedGeneration: 1,
				DomainName:         zone.Spec.DomainName,
				ZoneClassRef:       dnsv1alpha1.ObjectReference{Namespace: "platform", Name: "route53-public"},
			},
			RecordSets: []dnsv1alpha1.ZoneUnitRecordSetSpec{staleItem},
		},
		Status: dnsv1alpha1.ZoneUnitStatus{
			ObservedGeneration: 1,
			Zone: &dnsv1alpha1.ZoneUnitZoneStatus{
				Conditions: []metav1.Condition{
					{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted", ObservedGeneration: 1},
					{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionTrue, Reason: "Programmed", ObservedGeneration: 1},
				},
			},
			RecordSets: []dnsv1alpha1.ZoneUnitRecordSetStatus{{
				RecordSetNamespace: "app",
				RecordSetName:      "www-a",
				RecordSetUID:       recordSet.UID,
				ObservedGeneration: 1,
				Provider:           &dnsv1alpha1.ProviderStatus{Data: runtime.RawExtension{Raw: []byte(`{"recordID":"current"}`)}},
				Conditions: []metav1.Condition{
					{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted", ObservedGeneration: 1},
					{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionTrue, Reason: "Programmed", ObservedGeneration: 1},
				},
			}},
		},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(route53ProviderForZoneUnitTest(), route53ZoneClassForZoneUnitTest(), zone, recordSet, unit).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneUnitCompositionReconciler{Client: k8sClient}
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(zone)}

	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("Reconcile with stale provider status returned error: %v", err)
	}

	var staleProjection dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(recordSet), &staleProjection); err != nil {
		t.Fatalf("get RecordSet after stale projection: %v", err)
	}
	assertConditionAtGeneration(t, staleProjection.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionUnknown, "OwnerStateNotResolved", recordSet.Generation)
	assertConditionAtGeneration(t, staleProjection.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionUnknown, "Reconciling", recordSet.Generation)
	if staleProjection.Status.Provider == nil {
		t.Fatal("same-UID stale provider status erased provider ownership")
	}
	assertRawEqual(t, staleProjection.Status.Provider.Data, map[string]any{"recordID": "current"})

	var staleZoneProjection dnsv1alpha1.Zone
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(zone), &staleZoneProjection); err != nil {
		t.Fatalf("get Zone after stale projection: %v", err)
	}
	assertConditionAtGeneration(t, staleZoneProjection.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionUnknown, "OwnerStateNotResolved", zone.Generation)
	assertConditionAtGeneration(t, staleZoneProjection.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionUnknown, "Reconciling", zone.Generation)

	var currentUnit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &currentUnit); err != nil {
		t.Fatalf("get ZoneUnit after desired generation update: %v", err)
	}
	if len(currentUnit.Spec.RecordSets) != 1 {
		t.Fatalf("ZoneUnit recordSets = %#v, want one current item", currentUnit.Spec.RecordSets)
	}
	if item := currentUnit.Spec.RecordSets[0]; item.RecordSetUID != recordSet.UID || item.ObservedGeneration != recordSet.Generation {
		t.Fatalf("ZoneUnit recordSet item = %#v, want UID %q at generation %d", item, recordSet.UID, recordSet.Generation)
	}
	currentUnit.Status.ObservedGeneration = currentUnit.Generation
	currentUnit.Status.Zone.Conditions = []metav1.Condition{
		{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted", ObservedGeneration: zone.Generation},
		{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionTrue, Reason: "Programmed", ObservedGeneration: zone.Generation},
	}
	currentUnit.Status.RecordSets[0].ObservedGeneration = recordSet.Generation
	currentUnit.Status.RecordSets[0].Conditions = []metav1.Condition{
		{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted", ObservedGeneration: recordSet.Generation},
		{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionTrue, Reason: "Programmed", ObservedGeneration: 1},
	}
	if err := k8sClient.Status().Update(ctx, &currentUnit); err != nil {
		t.Fatalf("write accepted-only current provider status: %v", err)
	}

	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("Reconcile with accepted-only current provider status returned error: %v", err)
	}

	var acceptedOnlyProjection dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(recordSet), &acceptedOnlyProjection); err != nil {
		t.Fatalf("get RecordSet after accepted-only provider status: %v", err)
	}
	assertConditionAtGeneration(t, acceptedOnlyProjection.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted", recordSet.Generation)
	assertConditionAtGeneration(t, acceptedOnlyProjection.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionUnknown, "Reconciling", recordSet.Generation)

	var recoveredZone dnsv1alpha1.Zone
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(zone), &recoveredZone); err != nil {
		t.Fatalf("get Zone after current provider status: %v", err)
	}
	assertConditionAtGeneration(t, recoveredZone.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted", zone.Generation)
	assertConditionAtGeneration(t, recoveredZone.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionTrue, "Programmed", zone.Generation)

	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &currentUnit); err != nil {
		t.Fatalf("get ZoneUnit before programmed recovery: %v", err)
	}
	currentUnit.Status.RecordSets[0].Conditions = []metav1.Condition{
		{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted", ObservedGeneration: recordSet.Generation},
		{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionTrue, Reason: "Programmed", ObservedGeneration: recordSet.Generation},
	}
	if err := k8sClient.Status().Update(ctx, &currentUnit); err != nil {
		t.Fatalf("write fully current provider status: %v", err)
	}

	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("Reconcile with fully current provider status returned error: %v", err)
	}

	var recovered dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(recordSet), &recovered); err != nil {
		t.Fatalf("get RecordSet after provider recovery: %v", err)
	}
	assertConditionAtGeneration(t, recovered.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted", recordSet.Generation)
	assertConditionAtGeneration(t, recovered.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionTrue, "Programmed", recordSet.Generation)
	if recovered.Status.Provider == nil {
		t.Fatal("provider ownership did not survive fresh recovery")
	}
	assertRawEqual(t, recovered.Status.Provider.Data, map[string]any{"recordID": "current"})
}

func assertConditionAtGeneration(t *testing.T, conditions []metav1.Condition, conditionType string, status metav1.ConditionStatus, reason string, generation int64) {
	t.Helper()
	condition := assertCondition(t, conditions, conditionType, status, reason)
	if condition.ObservedGeneration != generation {
		t.Fatalf("condition %s observedGeneration = %d, want %d", conditionType, condition.ObservedGeneration, generation)
	}
}
