package cloudflare

import (
	"context"
	"testing"

	cloudflarev1alpha1 "github.com/appthrust/dns-api/pkg/go/api/cloudflare/v1alpha1"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCloudflareRecordSetReconcilerDoesNotCompleteDeletionFromUnownedStatus(t *testing.T) {
	for _, tt := range []struct {
		name      string
		statusUID types.UID
	}{
		{name: "missing UID"},
		{name: "predecessor UID", statusUID: types.UID("386cde02-3890-4ee6-a01a-037ed8354b5d")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			zone := cloudflareProgrammedZone("app", "apps-example-com")
			recordSet := cloudflareARecordSet("app", "www-a")
			recordSet.UID = types.UID("42f149e7-107d-48bb-885c-7a6c539678a6")
			unit := cloudflareRecordSetLifecycleUnit(recordSet, zone, true)
			external := cloudflareLifecycleARecord("023e105f4ecef8ad9ca31a8372d0c010")
			unit.Status.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetStatus{
				{
					RecordSetNamespace: recordSet.Namespace,
					RecordSetName:      recordSet.Name,
					RecordSetUID:       tt.statusUID,
					ObservedGeneration: recordSet.Generation,
					Provider: &dnsv1alpha1.ProviderStatus{Data: cloudflareRaw(t, cloudflarev1alpha1.CloudflareRecordSetStatusData{
						Records: cloudflareDNSRecordStatuses([]CloudflareDNSRecord{external}),
					})},
					DeletionCompleted: true,
					Conditions: []metav1.Condition{
						{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionTrue, Reason: "Programmed", ObservedGeneration: recordSet.Generation},
					},
				},
			}
			k8sClient := cloudflareRecordSetLifecycleClient(t, zone, unit)
			provider := &fakeCloudflareRecordSetProvider{records: []CloudflareDNSRecord{external}}
			reconciler := &recordSetReconciler{Client: k8sClient, Provider: provider}

			if _, err := reconciler.reconcileZoneUnitRecordSets(ctx, unit); err != nil {
				t.Fatalf("Reconcile returned error: %v", err)
			}
			if len(provider.batches) != 0 || len(provider.deleted) != 0 {
				t.Fatalf("provider deletion requests = batches %#v deleted %#v, want none", provider.batches, provider.deleted)
			}

			var got dnsv1alpha1.ZoneUnit
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &got); err != nil {
				t.Fatalf("ZoneUnit was not found: %v", err)
			}
			status := cloudflareLifecycleRecordSetStatus(t, &got, recordSet)
			if status.RecordSetUID != recordSet.UID {
				t.Fatalf("status recordSetUID = %q, want %q", status.RecordSetUID, recordSet.UID)
			}
			if status.Provider != nil {
				t.Fatalf("unowned provider status was retained: %#v", status.Provider)
			}
			if status.DeletionCompleted {
				t.Fatalf("deletion completed from unowned status")
			}
			assertCloudflareCondition(t, status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ProviderOwnershipNotEstablished")
		})
	}
}

func TestCloudflareRecordSetReconcilerCompletesDeletionForCurrentUID(t *testing.T) {
	ctx := context.Background()
	zone := cloudflareProgrammedZone("app", "apps-example-com")
	recordSet := cloudflareARecordSet("app", "www-a")
	recordSet.UID = types.UID("649f11b6-03b1-44ec-b433-578042cbddfb")
	unit := cloudflareRecordSetLifecycleUnit(recordSet, zone, true)
	external := cloudflareLifecycleARecord("023e105f4ecef8ad9ca31a8372d0c010")
	unit.Status.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetStatus{
		{
			RecordSetNamespace: recordSet.Namespace,
			RecordSetName:      recordSet.Name,
			RecordSetUID:       recordSet.UID,
			ObservedGeneration: recordSet.Generation,
			Provider: &dnsv1alpha1.ProviderStatus{Data: cloudflareRaw(t, cloudflarev1alpha1.CloudflareRecordSetStatusData{
				Records: cloudflareDNSRecordStatuses([]CloudflareDNSRecord{external}),
			})},
			Conditions: []metav1.Condition{
				{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionTrue, Reason: "Programmed", ObservedGeneration: recordSet.Generation},
			},
		},
	}
	k8sClient := cloudflareRecordSetLifecycleClient(t, zone, unit)
	provider := &fakeCloudflareRecordSetProvider{records: []CloudflareDNSRecord{external}}
	reconciler := &recordSetReconciler{Client: k8sClient, Provider: provider}

	if _, err := reconciler.reconcileZoneUnitRecordSets(ctx, unit); err != nil {
		t.Fatalf("Reconcile returned error deleting current record: %v", err)
	}
	if len(provider.deleted) != 1 || provider.deleted[0] != external.ID {
		t.Fatalf("deleted records = %#v, want %q", provider.deleted, external.ID)
	}
	var afterDelete dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &afterDelete); err != nil {
		t.Fatalf("ZoneUnit was not found after provider delete: %v", err)
	}
	if status := cloudflareLifecycleRecordSetStatus(t, &afterDelete, recordSet); status.DeletionCompleted {
		t.Fatalf("deletion completed before fresh absence was observed")
	}

	if _, err := reconciler.reconcileZoneUnitRecordSets(ctx, &afterDelete); err != nil {
		t.Fatalf("Reconcile returned error confirming deletion: %v", err)
	}
	var complete dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &complete); err != nil {
		t.Fatalf("ZoneUnit was not found after deletion confirmation: %v", err)
	}
	status := cloudflareLifecycleRecordSetStatus(t, &complete, recordSet)
	if status.RecordSetUID != recordSet.UID {
		t.Fatalf("status recordSetUID = %q, want %q", status.RecordSetUID, recordSet.UID)
	}
	if !status.DeletionCompleted {
		t.Fatalf("deletion did not complete for current UID")
	}
	assertCloudflareCondition(t, status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionTrue, "Programmed")
}

func TestCloudflareRecordSetReconcilerCompletesDeletionAfterAlreadyAbsentRecord(t *testing.T) {
	ctx := context.Background()
	zone := cloudflareProgrammedZone("app", "apps-example-com")
	recordSet := cloudflareARecordSet("app", "www-a")
	recordSet.UID = types.UID("649f11b6-03b1-44ec-b433-578042cbddfb")
	unit := cloudflareRecordSetLifecycleUnit(recordSet, zone, true)
	external := cloudflareLifecycleARecord("023e105f4ecef8ad9ca31a8372d0c010")
	unit.Status.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetStatus{{
		RecordSetNamespace: recordSet.Namespace,
		RecordSetName:      recordSet.Name,
		RecordSetUID:       recordSet.UID,
		ObservedGeneration: recordSet.Generation,
		Provider: &dnsv1alpha1.ProviderStatus{Data: cloudflareRaw(t, cloudflarev1alpha1.CloudflareRecordSetStatusData{
			Records: cloudflareDNSRecordStatuses([]CloudflareDNSRecord{external}),
		})},
		Conditions: []metav1.Condition{
			{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted", Message: "RecordSet is accepted by Cloudflare policy", ObservedGeneration: recordSet.Generation},
			{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionTrue, Reason: "Programmed", Message: "Cloudflare DNS record deletion is complete", ObservedGeneration: recordSet.Generation},
		},
	}}
	k8sClient := cloudflareRecordSetLifecycleClient(t, zone, unit)
	reconciler := &recordSetReconciler{Client: k8sClient, Provider: &fakeCloudflareRecordSetProvider{}}

	if _, err := reconciler.reconcileZoneUnitRecordSets(ctx, unit); err != nil {
		t.Fatalf("Reconcile returned error after the record was already absent: %v", err)
	}
	var afterAbsent dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &afterAbsent); err != nil {
		t.Fatalf("ZoneUnit was not found after absence observation: %v", err)
	}
	status := cloudflareLifecycleRecordSetStatus(t, &afterAbsent, recordSet)
	if status.DeletionCompleted {
		t.Fatalf("deletion completed before a fresh empty list")
	}
	data, err := cloudflareRecordSetStatusDataFromProvider(status.Provider)
	if err != nil {
		t.Fatalf("status provider data error after absence observation: %v", err)
	}
	if len(data.Records) != 0 {
		t.Fatalf("status record IDs after absence observation = %#v, want none", data.Records)
	}

	if _, err := reconciler.reconcileZoneUnitRecordSets(ctx, &afterAbsent); err != nil {
		t.Fatalf("Reconcile returned error confirming deletion completion: %v", err)
	}
	var complete dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &complete); err != nil {
		t.Fatalf("ZoneUnit was not found after deletion completion: %v", err)
	}
	status = cloudflareLifecycleRecordSetStatus(t, &complete, recordSet)
	if !status.DeletionCompleted {
		t.Fatalf("deletion did not complete after fresh absence")
	}
	assertCloudflareCondition(t, status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionTrue, "Programmed")
}

func TestCloudflareRecordSetReconcilerDoesNotMutateExternalRecordFromUnownedStatus(t *testing.T) {
	ctx := context.Background()
	zone := cloudflareProgrammedZone("app", "apps-example-com")
	recordSet := cloudflareARecordSet("app", "www-a")
	recordSet.UID = types.UID("4c574ddc-af01-4b99-9096-ea35eec1c7ec")
	unit := cloudflareRecordSetLifecycleUnit(recordSet, zone, false)
	external := cloudflareLifecycleARecord("023e105f4ecef8ad9ca31a8372d0c010")
	unit.Status.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetStatus{
		{
			RecordSetNamespace: recordSet.Namespace,
			RecordSetName:      recordSet.Name,
			RecordSetUID:       types.UID("e4a31b74-b40e-4b16-ad30-b2c8a924351a"),
			ObservedGeneration: recordSet.Generation,
			Provider: &dnsv1alpha1.ProviderStatus{Data: cloudflareRaw(t, cloudflarev1alpha1.CloudflareRecordSetStatusData{
				Records: cloudflareDNSRecordStatuses([]CloudflareDNSRecord{external}),
			})},
		},
	}
	k8sClient := cloudflareRecordSetLifecycleClient(t, zone, unit)
	provider := &fakeCloudflareRecordSetProvider{records: []CloudflareDNSRecord{external}}
	reconciler := &recordSetReconciler{Client: k8sClient, Provider: provider}

	if _, err := reconciler.reconcileZoneUnitRecordSets(ctx, unit); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if len(provider.batches) != 0 || len(provider.patched) != 0 || len(provider.deleted) != 0 {
		t.Fatalf("provider mutations = batches %#v patched %#v deleted %#v, want none", provider.batches, provider.patched, provider.deleted)
	}
	var got dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &got); err != nil {
		t.Fatalf("ZoneUnit was not found: %v", err)
	}
	status := cloudflareLifecycleRecordSetStatus(t, &got, recordSet)
	if status.RecordSetUID != recordSet.UID {
		t.Fatalf("status recordSetUID = %q, want %q", status.RecordSetUID, recordSet.UID)
	}
	if status.Provider != nil {
		t.Fatalf("unowned provider status was retained: %#v", status.Provider)
	}
	assertCloudflareCondition(t, status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ProviderConflict")
}

func TestCloudflareRecordSetReconcilerAdoptsForCurrentUIDAfterUnownedReceipt(t *testing.T) {
	ctx := context.Background()
	zone := cloudflareProgrammedZone("app", "apps-example-com")
	recordSet := cloudflareARecordSet("app", "www-a")
	recordSet.UID = types.UID("b75fc063-442f-4d44-8812-4ffcf2799897")
	recordSet.Spec.A.Addresses = []string{"192.0.2.10"}
	external := cloudflareLifecycleARecord("023e105f4ecef8ad9ca31a8372d0c010")
	recordSet.Spec.Adoption = cloudflareRaw(t, cloudflarev1alpha1.CloudflareRecordSetAdoption{RecordIDs: []string{external.ID}})
	unit := cloudflareRecordSetLifecycleUnit(recordSet, zone, false)
	unit.Status.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetStatus{
		{
			RecordSetNamespace: recordSet.Namespace,
			RecordSetName:      recordSet.Name,
			RecordSetUID:       types.UID("e4a31b74-b40e-4b16-ad30-b2c8a924351a"),
			ObservedGeneration: recordSet.Generation,
			Provider: &dnsv1alpha1.ProviderStatus{Data: cloudflareRaw(t, cloudflarev1alpha1.CloudflareRecordSetStatusData{
				Records: cloudflareDNSRecordStatuses([]CloudflareDNSRecord{external}),
			})},
		},
	}
	k8sClient := cloudflareRecordSetLifecycleClient(t, zone, unit)
	provider := &fakeCloudflareRecordSetProvider{records: []CloudflareDNSRecord{external}}
	reconciler := &recordSetReconciler{Client: k8sClient, Provider: provider}

	if _, err := reconciler.reconcileZoneUnitRecordSets(ctx, unit); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if len(provider.batches) != 0 {
		t.Fatalf("provider mutations = %#v, want none", provider.batches)
	}
	var got dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &got); err != nil {
		t.Fatalf("ZoneUnit was not found: %v", err)
	}
	status := cloudflareLifecycleRecordSetStatus(t, &got, recordSet)
	if status.RecordSetUID != recordSet.UID {
		t.Fatalf("status recordSetUID = %q, want %q", status.RecordSetUID, recordSet.UID)
	}
	data, err := cloudflareRecordSetStatusDataFromProvider(status.Provider)
	if err != nil {
		t.Fatalf("status provider data error: %v", err)
	}
	if len(data.Records) != 1 || data.Records[0].ID != external.ID {
		t.Fatalf("adopted record IDs = %#v", data.Records)
	}
	assertCloudflareCondition(t, status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionTrue, "Programmed")
}

func TestCloudflareRecordSetMutationFenceRejectsChangedSource(t *testing.T) {
	for _, tt := range []struct {
		name    string
		replace func(*dnsv1alpha1.ZoneUnit)
	}{
		{
			name: "same UID newer generation",
			replace: func(unit *dnsv1alpha1.ZoneUnit) {
				unit.Spec.RecordSets[0].ObservedGeneration++
			},
		},
		{
			name: "replacement UID",
			replace: func(unit *dnsv1alpha1.ZoneUnit) {
				unit.Spec.RecordSets[0].RecordSetUID = types.UID("b57bc8fe-7a0d-4f89-9dc4-296c8a002f06")
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			zone := cloudflareProgrammedZone("app", "apps-example-com")
			recordSet := cloudflareARecordSet("app", "www-a")
			unit := cloudflareMutationFenceUnit(recordSet, zone)
			k8sClient := cloudflareRecordSetLifecycleClient(t, zone, unit)

			var stale dnsv1alpha1.ZoneUnit
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &stale); err != nil {
				t.Fatalf("get ZoneUnit source snapshot: %v", err)
			}
			current := stale.DeepCopy()
			tt.replace(current)
			if err := k8sClient.Update(ctx, current); err != nil {
				t.Fatalf("replace ZoneUnit source: %v", err)
			}

			record := cloudflareRecordSetFromZoneUnitItem(&stale, stale.Spec.RecordSets[0], stale.Status.RecordSets[0])
			provider := &fakeCloudflareRecordSetProvider{}
			reconciler := &recordSetReconciler{Client: k8sClient, Provider: provider}
			ctxData := cloudflareRecordSetContext{
				ZoneID:          "023e105f4ecef8ad9ca31a8372d0c353",
				RecordSetSource: stale.Spec.RecordSets[0].DeepCopy(),
			}
			if _, err := reconciler.applyCloudflareRecordSetDiff(ctx, provider, &record, ctxData, nil, []CloudflareDNSRecord{{Type: "A"}}); !apierrors.IsConflict(err) {
				t.Fatalf("mutation fence error = %v, want source conflict", err)
			}
			if len(provider.batches) != 0 {
				t.Fatalf("changed source reached Cloudflare batch write: %#v", provider.batches)
			}
		})
	}
}

func TestCloudflareRecordSetDeletionMutationFenceRejectsChangedSource(t *testing.T) {
	ctx := t.Context()
	zone := cloudflareProgrammedZone("app", "apps-example-com")
	recordSet := cloudflareARecordSet("app", "www-a")
	unit := cloudflareMutationFenceUnit(recordSet, zone)
	unit.Spec.RecordSets[0].DeletionRequested = true
	external := cloudflareLifecycleARecord("023e105f4ecef8ad9ca31a8372d0c010")
	unit.Status.RecordSets[0].Provider = &dnsv1alpha1.ProviderStatus{Data: cloudflareRaw(t, cloudflarev1alpha1.CloudflareRecordSetStatusData{
		Records: cloudflareDNSRecordStatuses([]CloudflareDNSRecord{external}),
	})}
	k8sClient := cloudflareRecordSetLifecycleClient(t, zone, unit)

	var stale dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &stale); err != nil {
		t.Fatalf("get ZoneUnit source snapshot: %v", err)
	}
	current := stale.DeepCopy()
	current.Spec.RecordSets[0].ObservedGeneration++
	if err := k8sClient.Update(ctx, current); err != nil {
		t.Fatalf("replace ZoneUnit source: %v", err)
	}

	record := cloudflareRecordSetFromZoneUnitItem(&stale, stale.Spec.RecordSets[0], stale.Status.RecordSets[0])
	provider := &fakeCloudflareRecordSetProvider{records: []CloudflareDNSRecord{external}}
	reconciler := &recordSetReconciler{Client: k8sClient, Provider: provider}
	ctxData := cloudflareRecordSetContext{
		ZoneID:          "023e105f4ecef8ad9ca31a8372d0c353",
		FullName:        "www.apps.example.com",
		RecordSetSource: stale.Spec.RecordSets[0].DeepCopy(),
	}
	if _, err := reconciler.reconcileDeleteWithContext(ctx, &record, ctxData); !apierrors.IsConflict(err) {
		t.Fatalf("deletion mutation fence error = %v, want source conflict", err)
	}
	if len(provider.batches) != 0 {
		t.Fatalf("changed source reached Cloudflare delete batch: %#v", provider.batches)
	}
}

func TestCloudflareZoneReconcilerDoesNotMutateFromStaleRecordSetSource(t *testing.T) {
	for _, tt := range []struct {
		name    string
		replace func(*dnsv1alpha1.ZoneUnit)
	}{
		{
			name: "same UID newer generation",
			replace: func(unit *dnsv1alpha1.ZoneUnit) {
				unit.Spec.RecordSets[0].ObservedGeneration++
			},
		},
		{
			name: "replacement UID",
			replace: func(unit *dnsv1alpha1.ZoneUnit) {
				unit.Spec.RecordSets[0].RecordSetUID = types.UID("a4d5c1fa-7dca-47cb-a2ff-b87e2bed9609")
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			zone := cloudflareProgrammedZone("app", "apps-example-com")
			recordSet := cloudflareARecordSet("app", "www-a")
			unit := cloudflareMutationFenceUnit(recordSet, zone)
			k8sClient := cloudflareRecordSetLifecycleClient(t, zone, unit)

			var stale dnsv1alpha1.ZoneUnit
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &stale); err != nil {
				t.Fatalf("get ZoneUnit cache snapshot: %v", err)
			}
			current := stale.DeepCopy()
			tt.replace(current)
			if err := k8sClient.Update(ctx, current); err != nil {
				t.Fatalf("replace ZoneUnit backing source: %v", err)
			}

			provider := &fakeCloudflareZoneRecordProvider{
				fakeCloudflareZoneProvider: &fakeCloudflareZoneProvider{
					zonesByID: map[string]CloudflareZone{
						"023e105f4ecef8ad9ca31a8372d0c353": {
							ID:        "023e105f4ecef8ad9ca31a8372d0c353",
							AccountID: "023e105f4ecef8ad9ca31a8372d0c354",
							Name:      "apps.example.com",
							Status:    "pending",
							Type:      "full",
						},
					},
				},
				fakeCloudflareRecordSetProvider: &fakeCloudflareRecordSetProvider{},
			}
			reconciler := &ZoneReconciler{
				Client: &cloudflareStaleZoneUnitClient{
					Client:        k8sClient,
					staleZoneUnit: stale.DeepCopy(),
				},
				Provider: provider,
			}
			if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(unit)}); !apierrors.IsConflict(err) {
				t.Fatalf("reconcile error = %v, want resourceVersion conflict", err)
			}
			if len(provider.batches) != 0 {
				t.Fatalf("stale cache reached Cloudflare batch write: %#v", provider.batches)
			}
		})
	}
}

func cloudflareMutationFenceUnit(recordSet *dnsv1alpha1.RecordSet, zone *dnsv1alpha1.Zone) *dnsv1alpha1.ZoneUnit {
	unit := cloudflareRecordSetLifecycleUnit(recordSet, zone, false)
	unit.Generation = 1
	unit.Status.ObservedGeneration = unit.Generation
	unit.Status.Zone.Conditions = []metav1.Condition{
		{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted", Message: "Zone is accepted by Cloudflare policy", ObservedGeneration: 1},
		{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionTrue, Reason: "Programmed", Message: "Cloudflare zone matches desired state", ObservedGeneration: 1},
	}
	unit.Status.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetStatus{{
		RecordSetNamespace: recordSet.Namespace,
		RecordSetName:      recordSet.Name,
		RecordSetUID:       recordSet.UID,
		ObservedGeneration: recordSet.Generation,
		Conditions: []metav1.Condition{
			{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted", Message: "RecordSet is accepted by Cloudflare policy", ObservedGeneration: recordSet.Generation},
			{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionFalse, Reason: "ProviderChangePending", Message: "Cloudflare DNS record change is pending", ObservedGeneration: recordSet.Generation},
		},
	}}
	unit.Status.Conditions = []metav1.Condition{{
		Type:               string(dnsv1alpha1.ConditionProgrammed),
		Status:             metav1.ConditionFalse,
		Reason:             "ProviderChangePending",
		Message:            "Cloudflare DNS record change is pending",
		ObservedGeneration: unit.Generation,
	}}
	return unit
}

type cloudflareStaleZoneUnitClient struct {
	client.Client
	staleZoneUnit *dnsv1alpha1.ZoneUnit
}

func (c *cloudflareStaleZoneUnitClient) Get(ctx context.Context, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
	if unit, ok := object.(*dnsv1alpha1.ZoneUnit); ok && key == client.ObjectKeyFromObject(c.staleZoneUnit) {
		c.staleZoneUnit.DeepCopyInto(unit)
		return nil
	}
	return c.Client.Get(ctx, key, object, options...)
}

func cloudflareRecordSetLifecycleUnit(recordSet *dnsv1alpha1.RecordSet, zone *dnsv1alpha1.Zone, deleting bool) *dnsv1alpha1.ZoneUnit {
	unit := cloudflareZoneUnitWithRecordSetItem("app", "apps-example-com", recordSet.Namespace, recordSet.Name, recordSet.Spec.Name, recordSet.Spec.Type)
	item := cloudflareZoneUnitRecordSetSpec(recordSet)
	item.DeletionRequested = deleting
	unit.Spec.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetSpec{item}
	unit.Status.Zone = &dnsv1alpha1.ZoneUnitZoneStatus{
		Provider:   zone.Status.Provider,
		Conditions: zone.Status.Conditions,
	}
	return unit
}

func cloudflareRecordSetLifecycleClient(t *testing.T, zone *dnsv1alpha1.Zone, unit *dnsv1alpha1.ZoneUnit) client.Client {
	t.Helper()
	return fake.NewClientBuilder().
		WithScheme(cloudflareTestScheme(t)).
		WithObjects(
			cloudflareRecordSetProvider(),
			acceptedReadyCloudflareIdentity("platform", "cloudflare-ci"),
			acceptedCloudflareZoneClass("platform", "cloudflare-public"),
			zone,
			unit,
		).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneClass{}, &dnsv1alpha1.ZoneUnit{}, &cloudflarev1alpha1.CloudflareIdentity{}).
		Build()
}

func cloudflareLifecycleARecord(id string) CloudflareDNSRecord {
	ttl := int32(300)
	proxied := false
	return CloudflareDNSRecord{
		ID:      id,
		Type:    "A",
		Name:    "www.apps.example.com",
		Content: "192.0.2.10",
		TTL:     &ttl,
		Proxied: &proxied,
	}
}

func cloudflareLifecycleRecordSetStatus(t *testing.T, unit *dnsv1alpha1.ZoneUnit, recordSet *dnsv1alpha1.RecordSet) dnsv1alpha1.ZoneUnitRecordSetStatus {
	t.Helper()
	for _, status := range unit.Status.RecordSets {
		if status.RecordSetNamespace == recordSet.Namespace && status.RecordSetName == recordSet.Name {
			return status
		}
	}
	t.Fatalf("ZoneUnit status did not contain RecordSet status: %#v", unit.Status.RecordSets)
	return dnsv1alpha1.ZoneUnitRecordSetStatus{}
}

func TestCloudflareRecordSetRetainsOwnershipAcrossGenerationUpdate(t *testing.T) {
	ctx := t.Context()
	zone := cloudflareProgrammedZone("app", "apps-example-com")
	recordSet := cloudflareARecordSet("app", "www-a")
	recordSet.Generation = 2
	ttl := int32(600)
	recordSet.Spec.TTL = &ttl
	recordSet.Spec.A.Addresses = []string{"192.0.2.10"}
	unit := cloudflareRecordSetLifecycleUnit(recordSet, zone, false)
	external := cloudflareLifecycleARecord("023e105f4ecef8ad9ca31a8372d0c010")
	unit.Status.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetStatus{{
		RecordSetNamespace: recordSet.Namespace,
		RecordSetName:      recordSet.Name,
		RecordSetUID:       recordSet.UID,
		ObservedGeneration: 1,
		Provider: &dnsv1alpha1.ProviderStatus{Data: cloudflareRaw(t, cloudflarev1alpha1.CloudflareRecordSetStatusData{
			Records: cloudflareDNSRecordStatuses([]CloudflareDNSRecord{external}),
		})},
		Conditions: []metav1.Condition{{
			Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionTrue,
			Reason: "Programmed", ObservedGeneration: 1,
		}},
	}}
	k8sClient := cloudflareRecordSetLifecycleClient(t, zone, unit)
	provider := &fakeCloudflareRecordSetProvider{records: []CloudflareDNSRecord{external}}
	reconciler := &recordSetReconciler{Client: k8sClient, Provider: provider}
	if _, err := reconciler.reconcileZoneUnitRecordSets(ctx, unit); err != nil {
		t.Fatal(err)
	}
	if len(provider.records) != 1 || provider.records[0].ID != external.ID ||
		provider.records[0].TTL == nil || *provider.records[0].TTL != ttl {
		t.Fatalf("same-UID generation update did not update the existing provider record: %#v", provider.records)
	}
}

func TestCloudflareAggregateRequiresCurrentProgrammedObservation(t *testing.T) {
	zone := cloudflareProgrammedZone("app", "apps-example-com")
	recordSet := cloudflareARecordSet("app", "www-a")
	recordSet.Generation = 2
	unit := cloudflareRecordSetLifecycleUnit(recordSet, zone, false)
	unit.Status.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetStatus{{
		RecordSetNamespace: recordSet.Namespace,
		RecordSetName:      recordSet.Name,
		RecordSetUID:       recordSet.UID,
		ObservedGeneration: 2,
		Conditions: []metav1.Condition{{
			Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionTrue,
			Reason: "Programmed", ObservedGeneration: 1,
		}},
	}}
	if status, _, _ := zoneUnitProgrammedCondition(unit); status == metav1.ConditionTrue {
		t.Fatal("an Accepted refresh promoted the previous generation's Programmed observation")
	}
}
