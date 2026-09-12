package route53

import (
	"context"
	"testing"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// legacyLedgerFixture models a ZoneUnit whose ledger entry for the claim was
// written before recordSetUID existed, with the provider-created record live.
type legacyLedgerFixture struct {
	recordSet *dnsv1alpha1.RecordSet
	unit      *dnsv1alpha1.ZoneUnit
	provider  *fakeProvider
	client    client.Client
	recorder  *capturingRecorder
}

func newLegacyLedgerFixture(t *testing.T, recordSet *dnsv1alpha1.RecordSet, mutateReceipt func(*dnsv1alpha1.ZoneUnitRecordSetStatus), liveValues []string) legacyLedgerFixture {
	t.Helper()
	recordSet.UID = types.UID("11111111-2222-3333-4444-000000000001")
	unit := route53ZoneUnit("app", "apps-example-com", "app", "route53-public")
	unit.Generation = 1
	unit.Spec.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetSpec{route53ZoneUnitRecordSetSpec(recordSet)}
	setRoute53ReadyZoneUnitStatus(t, unit)
	legacy := route53RecordSetStatusWithState(t, recordSet, "www.apps.example.com.")
	legacy.RecordSetUID = ""
	if mutateReceipt != nil {
		mutateReceipt(&legacy)
	}
	unit.Status.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetStatus{legacy}
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
		Values:       liveValues,
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(route53Provider(), route53ZoneClass("app", "route53-public", nil), acceptedRoute53Identity("app", "route53-dev"), unit).
		WithStatusSubresource(&dnsv1alpha1.ZoneUnit{}).
		Build()
	return legacyLedgerFixture{recordSet: recordSet, unit: unit, provider: provider, client: k8sClient, recorder: &capturingRecorder{}}
}

func (f legacyLedgerFixture) reconcile(t *testing.T, ctx context.Context) ctrl.Result {
	t.Helper()
	reconciler := &ZoneReconciler{Client: f.client, Provider: f.provider, Recorder: f.recorder}
	result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.unit)})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	return result
}

func (f legacyLedgerFixture) ledger(t *testing.T, ctx context.Context) []dnsv1alpha1.ZoneUnitRecordSetStatus {
	t.Helper()
	var got dnsv1alpha1.ZoneUnit
	if err := f.client.Get(ctx, client.ObjectKeyFromObject(f.unit), &got); err != nil {
		t.Fatal(err)
	}
	return got.Status.RecordSets
}

func TestZoneReconcileBindsLegacyReceiptWhenLiveRecordMatchesDesired(t *testing.T) {
	ctx := t.Context()
	fixture := newLegacyLedgerFixture(t, route53ARecordSet("app", "www"), nil, []string{"192.0.2.10"})

	fixture.reconcile(t, ctx)

	if len(fixture.provider.upserted) != 0 || len(fixture.provider.deletedRRs) != 0 {
		t.Fatalf("binding a matching legacy receipt mutated Route 53: upserts=%#v deletes=%#v", fixture.provider.upserted, fixture.provider.deletedRRs)
	}
	ledger := fixture.ledger(t, ctx)
	if len(ledger) != 1 {
		t.Fatalf("legacy entry was appended instead of bound in place: %#v", ledger)
	}
	status := ledger[0]
	if status.RecordSetUID != fixture.recordSet.UID {
		t.Fatalf("ledger UID = %q, want current %q", status.RecordSetUID, fixture.recordSet.UID)
	}
	data, err := route53RecordSetStatusDataFromProvider(status.Provider)
	if err != nil {
		t.Fatal(err)
	}
	if data.HostedZoneID != "Z000001" || data.RecordName != "www.apps.example.com." || data.RecordType != "A" {
		t.Fatalf("bound provider state = %#v, want the desired record identity", data)
	}
	assertCondition(t, status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionTrue, "Programmed")
	if !fixture.recorder.has("RecordSet", "Route53RecordSetLegacyOwnershipBound") {
		t.Fatalf("binding was not recorded as an event: %#v", fixture.recorder.events)
	}
}

func TestZoneReconcileRejectsLegacyReceiptWithoutEvidence(t *testing.T) {
	cases := []struct {
		name          string
		mutateReceipt func(*dnsv1alpha1.ZoneUnitRecordSetStatus)
		liveValues    []string
	}{
		{
			name:       "live record differs from desired",
			liveValues: []string{"192.0.2.99"},
		},
		{
			name: "receipt names another record identity",
			mutateReceipt: func(status *dnsv1alpha1.ZoneUnitRecordSetStatus) {
				other := route53RecordSetStatusWithState(t, route53ARecordSet("app", "www"), "other.apps.example.com.")
				status.Provider = other.Provider
			},
			liveValues: []string{"192.0.2.10"},
		},
		{
			name: "receipt already completed deletion",
			mutateReceipt: func(status *dnsv1alpha1.ZoneUnitRecordSetStatus) {
				status.DeletionCompleted = true
			},
			liveValues: []string{"192.0.2.10"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := t.Context()
			fixture := newLegacyLedgerFixture(t, route53ARecordSet("app", "www"), testCase.mutateReceipt, testCase.liveValues)

			fixture.reconcile(t, ctx)

			if len(fixture.provider.upserted) != 0 || len(fixture.provider.deletedRRs) != 0 {
				t.Fatalf("unbound legacy receipt authorized a provider mutation: upserts=%#v deletes=%#v", fixture.provider.upserted, fixture.provider.deletedRRs)
			}
			ledger := fixture.ledger(t, ctx)
			if len(ledger) != 1 {
				t.Fatalf("ledger = %#v, want one entry for the claim", ledger)
			}
			if ledger[0].RecordSetUID != fixture.recordSet.UID {
				t.Fatalf("ledger UID = %q, want current %q", ledger[0].RecordSetUID, fixture.recordSet.UID)
			}
			if ledger[0].Provider != nil {
				t.Fatalf("legacy receipt was rebound without evidence: %#v", ledger[0].Provider)
			}
			assertCondition(t, ledger[0].Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ProviderConflict")
		})
	}
}

func TestZoneReconcileDeletesRecordThroughLegacyReceipt(t *testing.T) {
	ctx := t.Context()
	fixture := newLegacyLedgerFixture(t, deletingRoute53ARecordSet(t), nil, []string{"192.0.2.10"})

	fixture.reconcile(t, ctx)

	if len(fixture.provider.deletedRRs) != 1 {
		t.Fatalf("deleting claim with a matching legacy receipt did not delete its record: %#v", fixture.provider.deletedRRs)
	}
	ledger := fixture.ledger(t, ctx)
	if len(ledger) != 1 || ledger[0].RecordSetUID != fixture.recordSet.UID {
		t.Fatalf("ledger = %#v, want the legacy entry bound to the deleting claim", ledger)
	}
	data, err := route53RecordSetStatusDataFromProvider(ledger[0].Provider)
	if err != nil {
		t.Fatal(err)
	}
	if data.RecordName != "www.apps.example.com." {
		t.Fatalf("bound provider state = %#v", data)
	}
}

func TestZoneReconcileDoesNotDeleteRecordThroughLegacyReceiptWhenLiveDiffers(t *testing.T) {
	ctx := t.Context()
	fixture := newLegacyLedgerFixture(t, deletingRoute53ARecordSet(t), nil, []string{"192.0.2.99"})

	fixture.reconcile(t, ctx)

	if len(fixture.provider.deletedRRs) != 0 {
		t.Fatalf("legacy receipt deleted a record that differs from the claim: %#v", fixture.provider.deletedRRs)
	}
	ledger := fixture.ledger(t, ctx)
	if len(ledger) != 1 || ledger[0].Provider != nil || ledger[0].DeletionCompleted {
		t.Fatalf("ledger = %#v, want an unbound, incomplete entry", ledger)
	}
	assertCondition(t, ledger[0].Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ProviderConflict")
}

func TestZoneReconcilePrunesLedgerEntriesWithoutSpecItem(t *testing.T) {
	ctx := t.Context()
	recordSet := route53ARecordSet("app", "www")
	unit := route53ZoneUnit("app", "apps-example-com", "app", "route53-public")
	unit.Generation = 1
	unit.Spec.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetSpec{route53ZoneUnitRecordSetSpec(recordSet)}
	setRoute53ReadyZoneUnitStatus(t, unit)
	current := route53RecordSetStatusWithState(t, recordSet, "www.apps.example.com.")
	absentOrphan := route53RecordSetStatusWithState(t, route53ARecordSet("app", "gone-absent"), "gone-absent.apps.example.com.")
	absentOrphan.RecordSetUID = ""
	completedOrphan := route53RecordSetStatusWithState(t, route53ARecordSet("app", "gone-completed"), "gone-completed.apps.example.com.")
	completedOrphan.DeletionCompleted = true
	leakedOrphan := route53RecordSetStatusWithState(t, route53ARecordSet("app", "gone-leaked"), "leaked.apps.example.com.")
	leakedOrphan.RecordSetUID = ""
	unit.Status.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetStatus{absentOrphan, current, completedOrphan, leakedOrphan}
	ttl := int64(300)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
	}
	provider.records[recordKey("Z000001", "www.apps.example.com.", dnsv1alpha1.RecordTypeA)] = RecordSetResource{
		HostedZoneID: "Z000001", Name: "www.apps.example.com.", Type: dnsv1alpha1.RecordTypeA, TTL: &ttl, Values: []string{"192.0.2.10"},
	}
	provider.records[recordKey("Z000001", "leaked.apps.example.com.", dnsv1alpha1.RecordTypeA)] = RecordSetResource{
		HostedZoneID: "Z000001", Name: "leaked.apps.example.com.", Type: dnsv1alpha1.RecordTypeA, TTL: &ttl, Values: []string{"192.0.2.20"},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(route53Provider(), route53ZoneClass("app", "route53-public", nil), acceptedRoute53Identity("app", "route53-dev"), unit).
		WithStatusSubresource(&dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(unit)})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if !result.Requeue {
		t.Fatalf("prune did not request re-observation: %#v", result)
	}
	if len(provider.upserted) != 0 || len(provider.deletedRRs) != 0 {
		t.Fatalf("prune pass mutated Route 53: upserts=%#v deletes=%#v", provider.upserted, provider.deletedRRs)
	}
	var got dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &got); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(got.Status.RecordSets))
	for _, status := range got.Status.RecordSets {
		names = append(names, status.RecordSetName)
	}
	// The orphan whose record is still live is the only evidence of a leaked
	// record and may still bind a returning spec item; it survives the prune.
	if len(names) != 2 || names[0] != recordSet.Name || names[1] != "gone-leaked" {
		t.Fatalf("ledger after prune = %#v, want the claimed entry and the leaked-record orphan", names)
	}
	if got.Status.RecordSets[0].Provider == nil || got.Status.RecordSets[0].RecordSetUID != recordSet.UID {
		t.Fatalf("prune altered the claimed entry: %#v", got.Status.RecordSets[0])
	}
	assertCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionTrue, "Programmed")
}
