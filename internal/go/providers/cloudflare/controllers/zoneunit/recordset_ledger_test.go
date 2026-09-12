package cloudflare

import (
	"testing"

	cloudflarev1alpha1 "github.com/appthrust/dns-api/pkg/go/api/cloudflare/v1alpha1"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// cloudflareLegacyReceipt is a pre-upgrade ledger entry: no recordSetUID, but
// the provider's own record IDs for the claim name.
func cloudflareLegacyReceipt(t *testing.T, recordSet *dnsv1alpha1.RecordSet, records ...CloudflareDNSRecord) dnsv1alpha1.ZoneUnitRecordSetStatus {
	t.Helper()
	return dnsv1alpha1.ZoneUnitRecordSetStatus{
		RecordSetNamespace: recordSet.Namespace,
		RecordSetName:      recordSet.Name,
		ObservedGeneration: recordSet.Generation,
		Provider: &dnsv1alpha1.ProviderStatus{Data: cloudflareRaw(t, cloudflarev1alpha1.CloudflareRecordSetStatusData{
			Records: cloudflareDNSRecordStatuses(records),
		})},
		Conditions: []metav1.Condition{
			{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionTrue, Reason: "Programmed", ObservedGeneration: recordSet.Generation},
		},
	}
}

func cloudflareLegacyLiveRecords() []CloudflareDNSRecord {
	first := cloudflareLifecycleARecord("023e105f4ecef8ad9ca31a8372d0c010")
	second := cloudflareLifecycleARecord("023e105f4ecef8ad9ca31a8372d0c011")
	second.Content = "192.0.2.11"
	return []CloudflareDNSRecord{first, second}
}

func TestCloudflareRecordSetBindsLegacyReceiptWhenLiveRecordsMatchDesired(t *testing.T) {
	ctx := t.Context()
	zone := cloudflareProgrammedZone("app", "apps-example-com")
	recordSet := cloudflareARecordSet("app", "www-a")
	recordSet.UID = types.UID("42f149e7-107d-48bb-885c-7a6c539678a6")
	unit := cloudflareRecordSetLifecycleUnit(recordSet, zone, false)
	live := cloudflareLegacyLiveRecords()
	unit.Status.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetStatus{cloudflareLegacyReceipt(t, recordSet, live...)}
	k8sClient := cloudflareRecordSetLifecycleClient(t, zone, unit)
	provider := &fakeCloudflareRecordSetProvider{records: live}
	reconciler := &recordSetReconciler{Client: k8sClient, Provider: provider}

	if _, err := reconciler.reconcileZoneUnitRecordSets(ctx, unit); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if len(provider.batches) != 0 || len(provider.patched) != 0 || len(provider.deleted) != 0 {
		t.Fatalf("binding a matching legacy receipt mutated Cloudflare: %#v %#v %#v", provider.batches, provider.patched, provider.deleted)
	}
	var got dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Status.RecordSets) != 1 {
		t.Fatalf("legacy entry was appended instead of bound in place: %#v", got.Status.RecordSets)
	}
	status := got.Status.RecordSets[0]
	if status.RecordSetUID != recordSet.UID {
		t.Fatalf("ledger UID = %q, want current %q", status.RecordSetUID, recordSet.UID)
	}
	data, err := cloudflareRecordSetStatusDataFromProvider(status.Provider)
	if err != nil {
		t.Fatal(err)
	}
	if ids := cloudflareRecordStatusIDs(data.Records); len(ids) != 2 || ids[0] != live[0].ID || ids[1] != live[1].ID {
		t.Fatalf("bound record IDs = %#v, want the receipt's live IDs", ids)
	}
	assertCloudflareCondition(t, status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionTrue, "Programmed")
}

func TestCloudflareRecordSetRejectsLegacyReceiptWithoutEvidence(t *testing.T) {
	cases := []struct {
		name    string
		receipt func(*testing.T, *dnsv1alpha1.RecordSet, []CloudflareDNSRecord) dnsv1alpha1.ZoneUnitRecordSetStatus
		live    func() []CloudflareDNSRecord
	}{
		{
			name: "live record differs from desired",
			receipt: func(t *testing.T, recordSet *dnsv1alpha1.RecordSet, live []CloudflareDNSRecord) dnsv1alpha1.ZoneUnitRecordSetStatus {
				return cloudflareLegacyReceipt(t, recordSet, live...)
			},
			live: func() []CloudflareDNSRecord {
				live := cloudflareLegacyLiveRecords()
				live[1].Content = "192.0.2.99"
				return live
			},
		},
		{
			name: "a same-type record is not in the receipt",
			receipt: func(t *testing.T, recordSet *dnsv1alpha1.RecordSet, live []CloudflareDNSRecord) dnsv1alpha1.ZoneUnitRecordSetStatus {
				return cloudflareLegacyReceipt(t, recordSet, live[0])
			},
			live: cloudflareLegacyLiveRecords,
		},
		{
			name: "receipt already completed deletion",
			receipt: func(t *testing.T, recordSet *dnsv1alpha1.RecordSet, live []CloudflareDNSRecord) dnsv1alpha1.ZoneUnitRecordSetStatus {
				receipt := cloudflareLegacyReceipt(t, recordSet, live...)
				receipt.DeletionCompleted = true
				return receipt
			},
			live: cloudflareLegacyLiveRecords,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := t.Context()
			zone := cloudflareProgrammedZone("app", "apps-example-com")
			recordSet := cloudflareARecordSet("app", "www-a")
			recordSet.UID = types.UID("42f149e7-107d-48bb-885c-7a6c539678a6")
			unit := cloudflareRecordSetLifecycleUnit(recordSet, zone, false)
			live := testCase.live()
			unit.Status.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetStatus{testCase.receipt(t, recordSet, live)}
			k8sClient := cloudflareRecordSetLifecycleClient(t, zone, unit)
			provider := &fakeCloudflareRecordSetProvider{records: live}
			reconciler := &recordSetReconciler{Client: k8sClient, Provider: provider}

			if _, err := reconciler.reconcileZoneUnitRecordSets(ctx, unit); err != nil {
				t.Fatalf("Reconcile returned error: %v", err)
			}
			if len(provider.batches) != 0 || len(provider.patched) != 0 || len(provider.deleted) != 0 {
				t.Fatalf("unbound legacy receipt authorized a provider mutation: %#v %#v %#v", provider.batches, provider.patched, provider.deleted)
			}
			var got dnsv1alpha1.ZoneUnit
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &got); err != nil {
				t.Fatal(err)
			}
			if len(got.Status.RecordSets) != 1 {
				t.Fatalf("ledger = %#v, want one entry for the claim", got.Status.RecordSets)
			}
			status := got.Status.RecordSets[0]
			if status.RecordSetUID != recordSet.UID {
				t.Fatalf("ledger UID = %q, want current %q", status.RecordSetUID, recordSet.UID)
			}
			if ids := cloudflareRecordStatusIDs(mustCloudflareStatusData(t, status).Records); len(ids) != 0 {
				t.Fatalf("legacy record IDs were rebound without evidence: %#v", ids)
			}
			assertCloudflareCondition(t, status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ProviderConflict")
		})
	}
}

func mustCloudflareStatusData(t *testing.T, status dnsv1alpha1.ZoneUnitRecordSetStatus) cloudflarev1alpha1.CloudflareRecordSetStatusData {
	t.Helper()
	data, err := cloudflareRecordSetStatusDataFromProvider(status.Provider)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestCloudflareRecordSetDeletesRecordsThroughLegacyReceipt(t *testing.T) {
	ctx := t.Context()
	zone := cloudflareProgrammedZone("app", "apps-example-com")
	recordSet := cloudflareARecordSet("app", "www-a")
	recordSet.UID = types.UID("42f149e7-107d-48bb-885c-7a6c539678a6")
	unit := cloudflareRecordSetLifecycleUnit(recordSet, zone, true)
	live := cloudflareLegacyLiveRecords()
	unit.Status.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetStatus{cloudflareLegacyReceipt(t, recordSet, live...)}
	k8sClient := cloudflareRecordSetLifecycleClient(t, zone, unit)
	provider := &fakeCloudflareRecordSetProvider{records: live}
	reconciler := &recordSetReconciler{Client: k8sClient, Provider: provider}

	if _, err := reconciler.reconcileZoneUnitRecordSets(ctx, unit); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if len(provider.deleted) != 2 {
		t.Fatalf("deleting claim with a matching legacy receipt did not delete its records: %#v", provider.deleted)
	}
	var got dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Status.RecordSets) != 1 || got.Status.RecordSets[0].RecordSetUID != recordSet.UID {
		t.Fatalf("ledger = %#v, want the legacy entry bound to the deleting claim", got.Status.RecordSets)
	}
}

func TestCloudflareRecordSetDoesNotDeleteThroughLegacyReceiptWhenLiveDiffers(t *testing.T) {
	ctx := t.Context()
	zone := cloudflareProgrammedZone("app", "apps-example-com")
	recordSet := cloudflareARecordSet("app", "www-a")
	recordSet.UID = types.UID("42f149e7-107d-48bb-885c-7a6c539678a6")
	unit := cloudflareRecordSetLifecycleUnit(recordSet, zone, true)
	live := cloudflareLegacyLiveRecords()
	live[0].Content = "192.0.2.99"
	unit.Status.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetStatus{cloudflareLegacyReceipt(t, recordSet, live...)}
	k8sClient := cloudflareRecordSetLifecycleClient(t, zone, unit)
	provider := &fakeCloudflareRecordSetProvider{records: live}
	reconciler := &recordSetReconciler{Client: k8sClient, Provider: provider}

	if _, err := reconciler.reconcileZoneUnitRecordSets(ctx, unit); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if len(provider.deleted) != 0 || len(provider.batches) != 0 {
		t.Fatalf("legacy receipt deleted records that differ from the claim: %#v %#v", provider.deleted, provider.batches)
	}
	var got dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &got); err != nil {
		t.Fatal(err)
	}
	status := cloudflareLifecycleRecordSetStatus(t, &got, recordSet)
	if status.DeletionCompleted || len(cloudflareRecordStatusIDs(mustCloudflareStatusData(t, status).Records)) != 0 {
		t.Fatalf("ledger = %#v, want an unbound, incomplete entry", status)
	}
	assertCloudflareCondition(t, status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ProviderOwnershipNotEstablished")
}

func TestCloudflareRecordSetPrunesSpentLedgerEntriesWithoutSpecItem(t *testing.T) {
	ctx := t.Context()
	zone := cloudflareProgrammedZone("app", "apps-example-com")
	recordSet := cloudflareARecordSet("app", "www-a")
	unit := cloudflareRecordSetLifecycleUnit(recordSet, zone, false)
	live := cloudflareLegacyLiveRecords()
	current := dnsv1alpha1.ZoneUnitRecordSetStatus{
		RecordSetNamespace: recordSet.Namespace,
		RecordSetName:      recordSet.Name,
		RecordSetUID:       recordSet.UID,
		ObservedGeneration: recordSet.Generation,
		Provider: &dnsv1alpha1.ProviderStatus{Data: cloudflareRaw(t, cloudflarev1alpha1.CloudflareRecordSetStatusData{
			Records: cloudflareDNSRecordStatuses(live),
		})},
	}
	completedOrphan := cloudflareLegacyReceipt(t, cloudflareARecordSet("app", "gone-completed"), live[0])
	completedOrphan.RecordSetUID = types.UID("386cde02-3890-4ee6-a01a-037ed8354b5d")
	completedOrphan.DeletionCompleted = true
	emptyOrphan := cloudflareLegacyReceipt(t, cloudflareARecordSet("app", "gone-empty"))
	leakedOrphan := cloudflareLegacyReceipt(t, cloudflareARecordSet("app", "gone-leaked"), cloudflareLifecycleARecord("023e105f4ecef8ad9ca31a8372d0c099"))
	unit.Status.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetStatus{completedOrphan, current, emptyOrphan, leakedOrphan}
	k8sClient := cloudflareRecordSetLifecycleClient(t, zone, unit)
	provider := &fakeCloudflareRecordSetProvider{records: live}
	reconciler := &recordSetReconciler{Client: k8sClient, Provider: provider}

	result, err := reconciler.reconcileZoneUnitRecordSets(ctx, unit)
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if !result.Requeue {
		t.Fatalf("prune did not request re-observation: %#v", result)
	}
	if len(provider.batches) != 0 || len(provider.patched) != 0 || len(provider.deleted) != 0 {
		t.Fatalf("prune pass mutated Cloudflare: %#v %#v %#v", provider.batches, provider.patched, provider.deleted)
	}
	var got dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &got); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(got.Status.RecordSets))
	for _, status := range got.Status.RecordSets {
		names = append(names, status.RecordSetName)
	}
	if len(names) != 2 || names[0] != "www-a" || names[1] != "gone-leaked" {
		t.Fatalf("ledger after prune = %#v, want the claimed entry and the leaked-ID orphan", names)
	}
}
