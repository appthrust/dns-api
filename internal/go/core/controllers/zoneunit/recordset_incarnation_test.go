package zoneunit

import (
	"testing"
	"time"

	"github.com/appthrust/dns-api/internal/go/core/providercontract"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestZoneUnitCompositionReconcilerDoesNotFinalizeDeletingRecordSetFromPredecessorOrLegacyCompletion(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		completionUID types.UID
	}{
		{name: "predecessor UID", completionUID: types.UID("predecessor-recordset")},
		{name: "missing UID"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := t.Context()
			claim := zone("app", "apps-example-com", "apps.example.com")
			claim.Spec.ZoneClassRef.Namespace = ptr("platform")
			recordSet := aRecordSet("app", "www-a", "app", "apps-example-com", "www")
			recordSet.UID = types.UID("current-recordset")
			deletionTime := metav1.NewTime(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
			recordSet.DeletionTimestamp = &deletionTime
			recordSet.Finalizers = []string{coreRecordSetFinalizer}
			recordSet.Status.Provider = &dnsv1alpha1.ProviderStatus{Data: runtime.RawExtension{Raw: []byte(`{"source":"stale"}`)}}
			unit := &dnsv1alpha1.ZoneUnit{
				ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "apps-example-com"},
				Spec: dnsv1alpha1.ZoneUnitSpec{
					RecordSets: []dnsv1alpha1.ZoneUnitRecordSetSpec{
						zoneUnitRecordSetItem(recordSet, providercontract.Payload{}),
					},
				},
				Status: dnsv1alpha1.ZoneUnitStatus{RecordSets: []dnsv1alpha1.ZoneUnitRecordSetStatus{{
					RecordSetNamespace: "app",
					RecordSetName:      "www-a",
					RecordSetUID:       testCase.completionUID,
					DeletionCompleted:  true,
					Provider:           &dnsv1alpha1.ProviderStatus{Data: runtime.RawExtension{Raw: []byte(`{"source":"stale"}`)}},
					Conditions: []metav1.Condition{
						{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted"},
						{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionTrue, Reason: "Programmed"},
					},
				}}},
			}
			k8sClient := fake.NewClientBuilder().
				WithScheme(testScheme(t)).
				WithObjects(route53ProviderForZoneUnitTest(), route53ZoneClassForZoneUnitTest(), claim, recordSet, unit).
				WithStatusSubresource(&dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneUnit{}).
				Build()

			if _, err := (&ZoneUnitCompositionReconciler{Client: k8sClient}).Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(claim)}); err != nil {
				t.Fatalf("Reconcile returned error: %v", err)
			}

			var retained dnsv1alpha1.RecordSet
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(recordSet), &retained); err != nil {
				t.Fatalf("deleting RecordSet was removed using %s completion evidence: %v", testCase.name, err)
			}
			if len(retained.Finalizers) != 1 || retained.Finalizers[0] != coreRecordSetFinalizer {
				t.Fatalf("deleting RecordSet finalizers = %v, want %q retained", retained.Finalizers, coreRecordSetFinalizer)
			}
			if retained.Status.Provider != nil {
				t.Fatalf("RecordSet status.provider = %#v, want stale provider state cleared", retained.Status.Provider)
			}
			assertCondition(t, retained.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionUnknown, "OwnerStateNotResolved")
			assertCondition(t, retained.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionUnknown, "Reconciling")
		})
	}
}

func TestZoneUnitCompositionReconcilerFinalizesDeletingRecordSetFromCurrentUIDCompletion(t *testing.T) {
	ctx := t.Context()
	claim := zone("app", "apps-example-com", "apps.example.com")
	claim.Spec.ZoneClassRef.Namespace = ptr("platform")
	recordSet := aRecordSet("app", "www-a", "app", "apps-example-com", "www")
	recordSet.UID = types.UID("current-recordset")
	deletionTime := metav1.NewTime(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	recordSet.DeletionTimestamp = &deletionTime
	recordSet.Finalizers = []string{coreRecordSetFinalizer}
	unit := &dnsv1alpha1.ZoneUnit{
		ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "apps-example-com"},
		Spec: dnsv1alpha1.ZoneUnitSpec{
			RecordSets: []dnsv1alpha1.ZoneUnitRecordSetSpec{
				zoneUnitRecordSetItem(recordSet, providercontract.Payload{}),
			},
		},
		Status: dnsv1alpha1.ZoneUnitStatus{RecordSets: []dnsv1alpha1.ZoneUnitRecordSetStatus{{
			RecordSetNamespace: "app",
			RecordSetName:      "www-a",
			RecordSetUID:       recordSet.UID,
			DeletionCompleted:  true,
		}}},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(route53ProviderForZoneUnitTest(), route53ZoneClassForZoneUnitTest(), claim, recordSet, unit).
		WithStatusSubresource(&dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneUnit{}).
		Build()

	if _, err := (&ZoneUnitCompositionReconciler{Client: k8sClient}).Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(claim)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var finalized dnsv1alpha1.RecordSet
	err := k8sClient.Get(ctx, client.ObjectKeyFromObject(recordSet), &finalized)
	if apierrors.IsNotFound(err) {
		return
	}
	if err != nil {
		t.Fatalf("RecordSet get returned error: %v", err)
	}
	if len(finalized.Finalizers) != 0 {
		t.Fatalf("deleting RecordSet finalizers = %v, want none", finalized.Finalizers)
	}
}

func TestZoneUnitCompositionReconcilerReplacesCompletedPredecessorWithoutProjectingItsState(t *testing.T) {
	ctx := t.Context()
	claim := zone("app", "apps-example-com", "apps.example.com")
	claim.Spec.ZoneClassRef.Namespace = ptr("platform")
	recordSet := aRecordSet("app", "www-a", "app", "apps-example-com", "www")
	recordSet.UID = types.UID("current-recordset")
	recordSet.Spec.Options = raw(t, map[string]any{"source": "current"})
	recordSet.Status.Provider = &dnsv1alpha1.ProviderStatus{Data: runtime.RawExtension{Raw: []byte(`{"source":"predecessor"}`)}}
	predecessor := recordSet.DeepCopy()
	predecessor.UID = types.UID("predecessor-recordset")
	deletionTime := metav1.NewTime(time.Date(2026, 9, 9, 11, 0, 0, 0, time.UTC))
	predecessor.DeletionTimestamp = &deletionTime
	unit := &dnsv1alpha1.ZoneUnit{
		ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "apps-example-com"},
		Spec: dnsv1alpha1.ZoneUnitSpec{
			RecordSets: []dnsv1alpha1.ZoneUnitRecordSetSpec{
				zoneUnitRecordSetItem(predecessor, providercontract.Payload{Options: raw(t, map[string]any{"source": "predecessor"})}),
			},
		},
		Status: dnsv1alpha1.ZoneUnitStatus{RecordSets: []dnsv1alpha1.ZoneUnitRecordSetStatus{{
			RecordSetNamespace: "app",
			RecordSetName:      "www-a",
			RecordSetUID:       predecessor.UID,
			DeletionCompleted:  true,
			Provider:           &dnsv1alpha1.ProviderStatus{Data: runtime.RawExtension{Raw: []byte(`{"source":"predecessor"}`)}},
			Conditions: []metav1.Condition{
				{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted"},
				{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionTrue, Reason: "Programmed"},
			},
		}}},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(route53ProviderForZoneUnitTest(), route53ZoneClassForZoneUnitTest(), claim, recordSet, unit).
		WithStatusSubresource(&dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneUnit{}).
		Build()

	if _, err := (&ZoneUnitCompositionReconciler{Client: k8sClient}).Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(claim)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var gotUnit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &gotUnit); err != nil {
		t.Fatalf("ZoneUnit was not found: %v", err)
	}
	if len(gotUnit.Spec.RecordSets) != 1 {
		t.Fatalf("recordSets = %#v, want one current item", gotUnit.Spec.RecordSets)
	}
	item := gotUnit.Spec.RecordSets[0]
	if item.RecordSetUID != recordSet.UID {
		t.Fatalf("recordSetUID = %q, want current UID %q", item.RecordSetUID, recordSet.UID)
	}
	assertRawEqual(t, item.Options, map[string]any{"source": "current"})

	var gotRecordSet dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(recordSet), &gotRecordSet); err != nil {
		t.Fatalf("RecordSet was not found: %v", err)
	}
	if gotRecordSet.Status.Provider != nil {
		t.Fatalf("RecordSet status.provider = %#v, want predecessor state cleared", gotRecordSet.Status.Provider)
	}
	assertCondition(t, gotRecordSet.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionUnknown, "OwnerStateNotResolved")
	assertCondition(t, gotRecordSet.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionUnknown, "Reconciling")
}

func TestZoneUnitCompositionReconcilerRetainsPendingPredecessorBeforeReplacement(t *testing.T) {
	ctx := t.Context()
	claim := zone("app", "apps-example-com", "apps.example.com")
	claim.Spec.ZoneClassRef.Namespace = ptr("platform")
	recordSet := aRecordSet("app", "www-a", "app", "apps-example-com", "www")
	recordSet.UID = types.UID("current-recordset")
	predecessor := recordSet.DeepCopy()
	predecessor.UID = types.UID("predecessor-recordset")
	deletionTime := metav1.NewTime(time.Date(2026, 9, 9, 11, 0, 0, 0, time.UTC))
	predecessor.DeletionTimestamp = &deletionTime
	unit := &dnsv1alpha1.ZoneUnit{
		ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "apps-example-com"},
		Spec: dnsv1alpha1.ZoneUnitSpec{
			RecordSets: []dnsv1alpha1.ZoneUnitRecordSetSpec{
				zoneUnitRecordSetItem(predecessor, providercontract.Payload{Options: raw(t, map[string]any{"source": "predecessor"})}),
			},
		},
		Status: dnsv1alpha1.ZoneUnitStatus{RecordSets: []dnsv1alpha1.ZoneUnitRecordSetStatus{{
			RecordSetNamespace: "app",
			RecordSetName:      "www-a",
			RecordSetUID:       predecessor.UID,
		}}},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(route53ProviderForZoneUnitTest(), route53ZoneClassForZoneUnitTest(), claim, recordSet, unit).
		WithStatusSubresource(&dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneUnit{}).
		Build()

	if _, err := (&ZoneUnitCompositionReconciler{Client: k8sClient}).Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(claim)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var gotUnit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &gotUnit); err != nil {
		t.Fatalf("ZoneUnit was not found: %v", err)
	}
	if len(gotUnit.Spec.RecordSets) != 1 {
		t.Fatalf("recordSets = %#v, want retained predecessor", gotUnit.Spec.RecordSets)
	}
	item := gotUnit.Spec.RecordSets[0]
	if item.RecordSetUID != predecessor.UID || !item.DeletionRequested {
		t.Fatalf("recordSet item = %#v, want pending predecessor", item)
	}
	assertRawEqual(t, item.Options, map[string]any{"source": "predecessor"})

	var gotRecordSet dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(recordSet), &gotRecordSet); err != nil {
		t.Fatalf("RecordSet was not found: %v", err)
	}
	assertCondition(t, gotRecordSet.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionFalse, "RecordSetConflict")
}
