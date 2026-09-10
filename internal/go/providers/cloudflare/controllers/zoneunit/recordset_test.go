package cloudflare

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	zoneunitcontroller "github.com/appthrust/dns-api/internal/go/core/controllers/zoneunit"
	cloudflarev1alpha1 "github.com/appthrust/dns-api/pkg/go/api/cloudflare/v1alpha1"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCloudflarerecordSetReconcilerCreatesARecords(t *testing.T) {
	ctx := context.Background()
	zone := cloudflareProgrammedZone("app", "apps-example-com")
	recordSet := cloudflareARecordSet("app", "www-a")
	zoneUnit := cloudflareZoneUnitWithRecordSetItem("app", "apps-example-com", "app", "www-a", "www", dnsv1alpha1.RecordTypeA)
	k8sClient := fake.NewClientBuilder().
		WithScheme(cloudflareTestScheme(t)).
		WithObjects(
			cloudflareRecordSetProvider(),
			acceptedReadyCloudflareIdentity("platform", "cloudflare-ci"),
			acceptedCloudflareZoneClass("platform", "cloudflare-public"),
			zone,
			recordSet,
			zoneUnit,
		).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneClass{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneUnit{}, &cloudflarev1alpha1.CloudflareIdentity{}).
		Build()
	provider := &fakeCloudflareRecordSetProvider{}
	reconciler := &recordSetReconciler{Client: k8sClient, Provider: provider}

	if _, err := reconcileCloudflareRecordSetAndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "www-a"}}); err != nil {
		t.Fatalf("Reconcile returned error on first pass: %v", err)
	}
	if len(provider.batches) != 1 {
		t.Fatalf("batch requests = %#v, want 1", provider.batches)
	}
	if len(provider.batches[0].Posts) != 2 {
		t.Fatalf("batch posts = %#v, want 2 records", provider.batches[0].Posts)
	}
	var pending dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "www-a"}, &pending); err != nil {
		t.Fatalf("RecordSet was not found: %v", err)
	}
	assertCloudflareCondition(t, pending.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ProviderChangePending")

	for i := 0; i < 4; i++ {
		if _, err := reconcileCloudflareRecordSetAndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "www-a"}}); err != nil {
			t.Fatalf("Reconcile returned error on pass %d: %v", i+1, err)
		}
	}

	if len(provider.created) != 2 {
		t.Fatalf("created records = %#v, want 2 records", provider.created)
	}
	for _, record := range provider.created {
		if record.Type != "A" || record.Name != "www.apps.example.com" || record.TTL == nil || *record.TTL != 300 {
			t.Fatalf("created record = %#v", record)
		}
		if record.Proxied == nil || *record.Proxied {
			t.Fatalf("created record proxied = %#v, want false", record.Proxied)
		}
	}

	var got dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "www-a"}, &got); err != nil {
		t.Fatalf("RecordSet was not found: %v", err)
	}
	assertCloudflareCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted")
	assertCloudflareCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionTrue, "Programmed")
	data, err := cloudflareRecordSetStatusData(&got)
	if err != nil {
		t.Fatalf("status data error: %v", err)
	}
	if len(data.Records) != 2 || data.Records[0].ID == "" {
		t.Fatalf("status records = %#v", data.Records)
	}
	if rawData := string(got.Status.Provider.Data.Raw); rawData == "" || strings.Contains(rawData, "www.apps.example.com") || strings.Contains(rawData, "192.0.2.") {
		t.Fatalf("status.provider.data must contain public record IDs only: %s", string(got.Status.Provider.Data.Raw))
	}
	if got.Status.Provider.State.Raw != nil {
		t.Fatalf("RecordSet.status.provider.state must not be projected to claim status: %s", string(got.Status.Provider.State.Raw))
	}
	var unit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &unit); err != nil {
		t.Fatalf("ZoneUnit was not found: %v", err)
	}
	index := slices.IndexFunc(unit.Status.RecordSets, func(status dnsv1alpha1.ZoneUnitRecordSetStatus) bool {
		return status.RecordSetNamespace == "app" && status.RecordSetName == "www-a"
	})
	if index < 0 {
		t.Fatalf("ZoneUnit status did not contain RecordSet status: %#v", unit.Status.RecordSets)
	}
	var state cloudflarev1alpha1.CloudflareRecordSetState
	if err := json.Unmarshal(unit.Status.RecordSets[index].Provider.State.Raw, &state); err != nil {
		t.Fatalf("ZoneUnit status provider state error: %v", err)
	}
	if len(state.Records) != 2 || state.Records[0].Name != "www.apps.example.com" || state.Records[0].Content == "" {
		t.Fatalf("ZoneUnit status provider state records = %#v", state.Records)
	}
}

func TestCloudflareZoneReconcilerCreatesARecordsThroughZoneUnit(t *testing.T) {
	ctx := context.Background()
	zone := cloudflareProgrammedZone("app", "apps-example-com")
	recordSet := cloudflareARecordSet("app", "www-a")
	zoneUnit := cloudflareZoneUnitWithRecordSetItem("app", "apps-example-com", "app", "www-a", "www", dnsv1alpha1.RecordTypeA)
	k8sClient := fake.NewClientBuilder().
		WithScheme(cloudflareTestScheme(t)).
		WithObjects(
			cloudflareRecordSetProvider(),
			acceptedReadyCloudflareIdentity("platform", "cloudflare-ci"),
			acceptedCloudflareZoneClass("platform", "cloudflare-public"),
			zone,
			recordSet,
			zoneUnit,
		).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneClass{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneUnit{}, &cloudflarev1alpha1.CloudflareIdentity{}).
		Build()
	provider := &fakeCloudflareZoneRecordProvider{
		fakeCloudflareZoneProvider: &fakeCloudflareZoneProvider{
			zonesByID: map[string]CloudflareZone{
				"023e105f4ecef8ad9ca31a8372d0c353": {
					ID:        "023e105f4ecef8ad9ca31a8372d0c353",
					AccountID: "023e105f4ecef8ad9ca31a8372d0c354",
					Name:      "apps.example.com",
					Status:    "active",
					Type:      "full",
				},
			},
		},
		fakeCloudflareRecordSetProvider: &fakeCloudflareRecordSetProvider{},
	}
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}
	syncCloudflareZoneStatusToUnit(t, ctx, k8sClient, types.NamespacedName{Namespace: "app", Name: "apps-example-com"})

	for i := 0; i < 5; i++ {
		if _, err := reconcileCloudflareZoneAndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}}); err != nil {
			t.Fatalf("Reconcile returned error on pass %d: %v", i+1, err)
		}
	}

	if len(provider.fakeCloudflareRecordSetProvider.created) != 2 {
		t.Fatalf("created records = %#v, want 2 records", provider.fakeCloudflareRecordSetProvider.created)
	}
	var got dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "www-a"}, &got); err != nil {
		t.Fatalf("RecordSet was not found: %v", err)
	}
	assertCloudflareCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionTrue, "Programmed")
	data, err := cloudflareRecordSetStatusData(&got)
	if err != nil {
		t.Fatalf("status data error: %v", err)
	}
	if len(data.Records) != 2 {
		t.Fatalf("status records = %#v, want 2 records", data.Records)
	}
}

func reconcileCloudflareRecordSetAndProject(t *testing.T, ctx context.Context, k8sClient client.Client, reconciler *recordSetReconciler, request ctrl.Request) (ctrl.Result, error) {
	t.Helper()
	var recordSet dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, request.NamespacedName, &recordSet); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	zoneNamespace, zoneName := recordSetZoneKey(&recordSet)
	zoneRequest := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: zoneNamespace, Name: zoneName}}
	syncCloudflareRecordSetSpecToUnit(t, ctx, k8sClient, &recordSet)
	if _, projectErr := (&zoneunitcontroller.ZoneUnitCompositionReconciler{Client: k8sClient}).Reconcile(ctx, zoneRequest); projectErr != nil {
		return ctrl.Result{}, projectErr
	}
	syncCloudflareZoneStatusToUnit(t, ctx, k8sClient, zoneRequest.NamespacedName)
	syncCloudflareRecordSetStatusToUnit(t, ctx, k8sClient, &recordSet)
	var unit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, zoneRequest.NamespacedName, &unit); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	result, err := reconciler.reconcileZoneUnitRecordSets(ctx, &unit)
	if err != nil {
		return result, err
	}
	if _, projectErr := (&zoneunitcontroller.ZoneUnitCompositionReconciler{Client: k8sClient}).Reconcile(ctx, zoneRequest); projectErr != nil {
		return result, projectErr
	}
	return result, nil
}

func syncCloudflareRecordSetSpecToUnit(t *testing.T, ctx context.Context, k8sClient client.Client, recordSet *dnsv1alpha1.RecordSet) {
	t.Helper()
	zoneNamespace, zoneName := recordSetZoneKey(recordSet)
	var unit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: zoneNamespace, Name: zoneName}, &unit); err != nil {
		return
	}
	next := cloudflareZoneUnitRecordSetSpec(recordSet)
	index := slices.IndexFunc(unit.Spec.RecordSets, func(item dnsv1alpha1.ZoneUnitRecordSetSpec) bool {
		return item.RecordSetNamespace == recordSet.Namespace && item.RecordSetName == recordSet.Name
	})
	if index >= 0 {
		unit.Spec.RecordSets[index] = next
	} else {
		unit.Spec.RecordSets = append(unit.Spec.RecordSets, next)
	}
	if err := k8sClient.Update(ctx, &unit); err != nil {
		t.Fatalf("sync ZoneUnit recordSet spec fixture: %v", err)
	}
}

func syncCloudflareZoneStatusToUnit(t *testing.T, ctx context.Context, k8sClient client.Client, key types.NamespacedName) {
	t.Helper()
	var zone dnsv1alpha1.Zone
	if err := k8sClient.Get(ctx, key, &zone); err != nil || zone.Status.Provider == nil {
		return
	}
	var unit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, key, &unit); err != nil {
		return
	}
	if unit.Status.Zone == nil {
		unit.Status.Zone = &dnsv1alpha1.ZoneUnitZoneStatus{}
	}
	unit.Status.Zone.Provider = &dnsv1alpha1.ProviderStatus{Data: zone.Status.Provider.Data}
	unit.Status.Zone.Conditions = slices.Clone(zone.Status.Conditions)
	if err := k8sClient.Status().Update(ctx, &unit); err != nil {
		t.Fatalf("sync ZoneUnit status fixture: %v", err)
	}
}

func syncCloudflareRecordSetStatusToUnit(t *testing.T, ctx context.Context, k8sClient client.Client, recordSet *dnsv1alpha1.RecordSet) {
	t.Helper()
	if recordSet.Status.Provider == nil && len(recordSet.Status.Conditions) == 0 {
		return
	}
	zoneNamespace, zoneName := recordSetZoneKey(recordSet)
	var unit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: zoneNamespace, Name: zoneName}, &unit); err != nil {
		return
	}
	index := slices.IndexFunc(unit.Status.RecordSets, func(status dnsv1alpha1.ZoneUnitRecordSetStatus) bool {
		return status.RecordSetNamespace == recordSet.Namespace && status.RecordSetName == recordSet.Name
	})
	next := dnsv1alpha1.ZoneUnitRecordSetStatus{
		RecordSetNamespace: recordSet.Namespace,
		RecordSetName:      recordSet.Name,
		RecordSetUID:       recordSet.UID,
		ObservedGeneration: recordSet.Generation,
		Provider:           recordSet.Status.Provider,
		Conditions:         slices.Clone(recordSet.Status.Conditions),
	}
	if index >= 0 {
		unit.Status.RecordSets[index] = next
	} else {
		unit.Status.RecordSets = append(unit.Status.RecordSets, next)
	}
	if err := k8sClient.Status().Update(ctx, &unit); err != nil {
		t.Fatalf("sync ZoneUnit recordSet status fixture: %v", err)
	}
}

func cloudflareZoneUnitRecordSetSpec(recordSet *dnsv1alpha1.RecordSet) dnsv1alpha1.ZoneUnitRecordSetSpec {
	return dnsv1alpha1.ZoneUnitRecordSetSpec{
		RecordSetNamespace: recordSet.Namespace,
		RecordSetName:      recordSet.Name,
		RecordSetUID:       recordSet.UID,
		ObservedGeneration: recordSet.Generation,
		Name:               recordSet.Spec.Name,
		Type:               recordSet.Spec.Type,
		TTL:                recordSet.Spec.TTL,
		A:                  recordSet.Spec.A,
		AAAA:               recordSet.Spec.AAAA,
		TXT:                recordSet.Spec.TXT,
		CNAME:              recordSet.Spec.CNAME,
		MX:                 recordSet.Spec.MX,
		CAA:                recordSet.Spec.CAA,
		NS:                 recordSet.Spec.NS,
		Options:            recordSet.Spec.Options,
		Adoption:           recordSet.Spec.Adoption,
		DeletionRequested:  !recordSet.DeletionTimestamp.IsZero(),
	}
}

func TestCloudflarerecordSetReconcilerPatchesMatchingValue(t *testing.T) {
	ctx := context.Background()
	zone := cloudflareProgrammedZone("app", "apps-example-com")
	recordSet := cloudflareARecordSet("app", "www-a")
	recordSet.Finalizers = []string{RecordSetFinalizer}
	ttl := int32(120)
	existing := CloudflareDNSRecord{
		ID:      "023e105f4ecef8ad9ca31a8372d0c010",
		Type:    "A",
		Name:    "www.apps.example.com",
		Content: "192.0.2.10",
		TTL:     &ttl,
		Proxied: ptrBool(false),
	}
	rawStatus, err := json.Marshal(cloudflarev1alpha1.CloudflareRecordSetStatusData{
		Records: cloudflareDNSRecordStatuses([]CloudflareDNSRecord{existing}),
	})
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	recordSet.Status.Provider = &dnsv1alpha1.ProviderStatus{Data: runtime.RawExtension{Raw: rawStatus}}
	k8sClient := fake.NewClientBuilder().
		WithScheme(cloudflareTestScheme(t)).
		WithObjects(
			cloudflareRecordSetProvider(),
			acceptedReadyCloudflareIdentity("platform", "cloudflare-ci"),
			acceptedCloudflareZoneClass("platform", "cloudflare-public"),
			zone,
			recordSet,
			cloudflareZoneUnitWithRecordSetItem("app", "apps-example-com", "app", "www-a", "www", dnsv1alpha1.RecordTypeA),
		).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneClass{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneUnit{}, &cloudflarev1alpha1.CloudflareIdentity{}).
		Build()
	provider := &fakeCloudflareRecordSetProvider{records: []CloudflareDNSRecord{existing}}
	reconciler := &recordSetReconciler{Client: k8sClient, Provider: provider}

	if _, err := reconcileCloudflareRecordSetAndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "www-a"}}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	if len(provider.patched) != 1 {
		t.Fatalf("patched records = %#v, want 1", provider.patched)
	}
	if len(provider.deleted) != 0 {
		t.Fatalf("deleted records = %#v, want none", provider.deleted)
	}
	if provider.patched[0].TTL == nil || *provider.patched[0].TTL != 300 {
		t.Fatalf("patched TTL = %#v, want 300", provider.patched[0].TTL)
	}
}

func TestCloudflareRecordSetReconcilerDoesNotAdoptForeignSameTypeRecordsAcrossDesiredUpdates(t *testing.T) {
	ctx := context.Background()
	zone := cloudflareProgrammedZone("app", "apps-example-com")
	recordSet := cloudflareARecordSet("app", "www-a")
	recordSet.Finalizers = []string{RecordSetFinalizer}
	recordSet.Spec.A.Addresses = []string{"192.0.2.10", "192.0.2.11"}
	ttl := int32(300)
	proxied := false
	owned := CloudflareDNSRecord{
		ID:      "023e105f4ecef8ad9ca31a8372d0c010",
		Type:    "A",
		Name:    "www.apps.example.com",
		Content: "192.0.2.10",
		TTL:     &ttl,
		Proxied: &proxied,
	}
	foreign := CloudflareDNSRecord{
		ID:      "023e105f4ecef8ad9ca31a8372d0c011",
		Type:    "A",
		Name:    "www.apps.example.com",
		Content: "192.0.2.11",
		TTL:     &ttl,
		Proxied: &proxied,
	}
	unit := cloudflareRecordSetLifecycleUnit(recordSet, zone, false)
	unit.Status.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetStatus{{
		RecordSetNamespace: recordSet.Namespace,
		RecordSetName:      recordSet.Name,
		RecordSetUID:       recordSet.UID,
		ObservedGeneration: recordSet.Generation,
		Provider: &dnsv1alpha1.ProviderStatus{Data: cloudflareRaw(t, cloudflarev1alpha1.CloudflareRecordSetStatusData{
			Records: cloudflareDNSRecordStatuses([]CloudflareDNSRecord{owned}),
		})},
	}}
	k8sClient := cloudflareRecordSetLifecycleClient(t, zone, unit)
	provider := &fakeCloudflareRecordSetProvider{records: []CloudflareDNSRecord{owned, foreign}}
	reconciler := &recordSetReconciler{Client: k8sClient, Provider: provider}

	if _, err := reconciler.reconcileZoneUnitRecordSets(ctx, unit); err != nil {
		t.Fatalf("Reconcile returned error when desired records matched the owned and foreign union: %v", err)
	}
	var afterExpansion dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &afterExpansion); err != nil {
		t.Fatalf("ZoneUnit was not found after desired expansion: %v", err)
	}
	status := cloudflareLifecycleRecordSetStatus(t, &afterExpansion, recordSet)
	data, err := cloudflareRecordSetStatusDataFromProvider(status.Provider)
	if err != nil {
		t.Fatalf("status provider data error after desired expansion: %v", err)
	}
	if len(data.Records) != 1 || data.Records[0].ID != owned.ID {
		t.Fatalf("foreign record was bound to current ownership: %#v", data.Records)
	}
	assertCloudflareCondition(t, status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ProviderConflict")
	if len(provider.created) != 0 || len(provider.patched) != 0 || len(provider.deleted) != 0 {
		t.Fatalf("provider writes during foreign record conflict = created %#v patched %#v deleted %#v, want none", provider.created, provider.patched, provider.deleted)
	}

	recordSet.Generation = 2
	recordSet.Spec.A.Addresses = []string{"192.0.2.10"}
	afterExpansion.Spec.RecordSets[0] = cloudflareZoneUnitRecordSetSpec(recordSet)
	if err := k8sClient.Update(ctx, &afterExpansion); err != nil {
		t.Fatalf("update ZoneUnit for desired contraction: %v", err)
	}
	if _, err := reconciler.reconcileZoneUnitRecordSets(ctx, &afterExpansion); err != nil {
		t.Fatalf("Reconcile returned error after desired contraction: %v", err)
	}
	var afterContraction dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &afterContraction); err != nil {
		t.Fatalf("ZoneUnit was not found after desired contraction: %v", err)
	}
	status = cloudflareLifecycleRecordSetStatus(t, &afterContraction, recordSet)
	data, err = cloudflareRecordSetStatusDataFromProvider(status.Provider)
	if err != nil {
		t.Fatalf("status provider data error after desired contraction: %v", err)
	}
	if len(data.Records) != 1 || data.Records[0].ID != owned.ID {
		t.Fatalf("foreign record was bound after desired contraction: %#v", data.Records)
	}
	assertCloudflareCondition(t, status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ProviderConflict")
	if len(provider.created) != 0 || len(provider.patched) != 0 || len(provider.deleted) != 0 {
		t.Fatalf("provider writes after desired contraction = created %#v patched %#v deleted %#v, want none", provider.created, provider.patched, provider.deleted)
	}
}

func TestCloudflarerecordSetReconcilerPatchesCNAMEContentChange(t *testing.T) {
	ctx := context.Background()
	zone := cloudflareProgrammedZone("app", "apps-example-com")
	recordSet := cloudflareCNAMERecordSet("app", "www-cname", "target2.example.net")
	recordSet.Finalizers = []string{RecordSetFinalizer}
	recordSet.Generation = 2
	ttl := int32(300)
	existing := CloudflareDNSRecord{
		ID:      "023e105f4ecef8ad9ca31a8372d0c010",
		Type:    "CNAME",
		Name:    "www.apps.example.com",
		Content: "target.example.net",
		TTL:     &ttl,
		Proxied: ptrBool(false),
	}
	rawStatus, err := json.Marshal(cloudflarev1alpha1.CloudflareRecordSetStatusData{
		Records: cloudflareDNSRecordStatuses([]CloudflareDNSRecord{existing}),
	})
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	recordSet.Status.Provider = &dnsv1alpha1.ProviderStatus{Data: runtime.RawExtension{Raw: rawStatus}}
	k8sClient := fake.NewClientBuilder().
		WithScheme(cloudflareTestScheme(t)).
		WithObjects(
			cloudflareRecordSetProvider(),
			acceptedReadyCloudflareIdentity("platform", "cloudflare-ci"),
			acceptedCloudflareZoneClass("platform", "cloudflare-public"),
			zone,
			recordSet,
			cloudflareZoneUnitWithRecordSetItem("app", "apps-example-com", "app", "www-cname", "www", dnsv1alpha1.RecordTypeCNAME),
		).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneClass{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneUnit{}, &cloudflarev1alpha1.CloudflareIdentity{}).
		Build()
	provider := &fakeCloudflareRecordSetProvider{records: []CloudflareDNSRecord{existing}}
	reconciler := &recordSetReconciler{Client: k8sClient, Provider: provider}

	if _, err := reconcileCloudflareRecordSetAndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "www-cname"}}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	if len(provider.created) != 0 {
		t.Fatalf("created records = %#v, want none", provider.created)
	}
	if len(provider.deleted) != 0 {
		t.Fatalf("deleted records = %#v, want none", provider.deleted)
	}
	if len(provider.patched) != 1 {
		t.Fatalf("patched records = %#v, want 1", provider.patched)
	}
	if provider.patched[0].Content != "target2.example.net" {
		t.Fatalf("patched content = %q, want target2.example.net", provider.patched[0].Content)
	}
}

func TestCloudflarerecordSetReconcilerRejectsAdoptionWithExtraSameNameRecord(t *testing.T) {
	ctx := context.Background()
	recordSet := cloudflareARecordSet("app", "www-a")
	recordSet.Finalizers = []string{RecordSetFinalizer}
	recordSet.Spec.A.Addresses = []string{"192.0.2.10"}
	adoptionRef, err := json.Marshal(cloudflarev1alpha1.CloudflareRecordSetAdoption{
		RecordIDs: []string{"023e105f4ecef8ad9ca31a8372d0c010"},
	})
	if err != nil {
		t.Fatalf("marshal adoption ref: %v", err)
	}
	recordSet.Spec.Adoption = runtime.RawExtension{Raw: adoptionRef}
	ttl := int32(300)
	proxied := false
	records := []CloudflareDNSRecord{
		{
			ID:      "023e105f4ecef8ad9ca31a8372d0c010",
			Type:    "A",
			Name:    "www.apps.example.com",
			Content: "192.0.2.10",
			TTL:     &ttl,
			Proxied: &proxied,
		},
		{
			ID:      "023e105f4ecef8ad9ca31a8372d0c011",
			Type:    "A",
			Name:    "www.apps.example.com",
			Content: "192.0.2.11",
			TTL:     &ttl,
			Proxied: &proxied,
		},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(cloudflareTestScheme(t)).
		WithObjects(
			cloudflareRecordSetProvider(),
			acceptedReadyCloudflareIdentity("platform", "cloudflare-ci"),
			acceptedCloudflareZoneClass("platform", "cloudflare-public"),
			cloudflareProgrammedZone("app", "apps-example-com"),
			recordSet,
			cloudflareZoneUnitWithRecordSetItem("app", "apps-example-com", "app", "www-a", "www", dnsv1alpha1.RecordTypeA),
		).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneClass{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneUnit{}, &cloudflarev1alpha1.CloudflareIdentity{}).
		Build()
	provider := &fakeCloudflareRecordSetProvider{records: records}
	reconciler := &recordSetReconciler{Client: k8sClient, Provider: provider}

	if _, err := reconcileCloudflareRecordSetAndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "www-a"}}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var got dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "www-a"}, &got); err != nil {
		t.Fatalf("RecordSet was not found: %v", err)
	}
	assertCloudflareCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ProviderConflict")
}

func TestCloudflarerecordSetReconcilerRejectsAdoptionIDsChangedAfterManage(t *testing.T) {
	ctx := context.Background()
	recordSet := cloudflareARecordSet("app", "www-a")
	recordSet.Spec.A.Addresses = []string{"192.0.2.10"}
	recordSet.Spec.Adoption = cloudflareRaw(t, map[string]any{
		"recordIDs": []string{"023e105f4ecef8ad9ca31a8372d0c011"},
	})
	oldRecord := CloudflareDNSRecord{
		ID:      "023e105f4ecef8ad9ca31a8372d0c010",
		Type:    "A",
		Name:    "www.apps.example.com",
		Content: "192.0.2.10",
	}
	rawStatus, err := json.Marshal(cloudflarev1alpha1.CloudflareRecordSetStatusData{
		Records: cloudflareDNSRecordStatuses([]CloudflareDNSRecord{oldRecord}),
	})
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	recordSet.Status.Provider = &dnsv1alpha1.ProviderStatus{Data: runtime.RawExtension{Raw: rawStatus}}
	k8sClient := fake.NewClientBuilder().
		WithScheme(cloudflareTestScheme(t)).
		WithObjects(
			cloudflareRecordSetProvider(),
			acceptedReadyCloudflareIdentity("platform", "cloudflare-ci"),
			acceptedCloudflareZoneClass("platform", "cloudflare-public"),
			cloudflareProgrammedZone("app", "apps-example-com"),
			recordSet,
			cloudflareZoneUnitWithRecordSetItem("app", "apps-example-com", "app", "www-a", "www", dnsv1alpha1.RecordTypeA),
		).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneClass{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneUnit{}, &cloudflarev1alpha1.CloudflareIdentity{}).
		Build()
	provider := &fakeCloudflareRecordSetProvider{}
	reconciler := &recordSetReconciler{Client: k8sClient, Provider: provider}

	if _, err := reconcileCloudflareRecordSetAndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "www-a"}}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	if len(provider.created) != 0 || len(provider.patched) != 0 || len(provider.deleted) != 0 {
		t.Fatalf("provider writes = created %#v patched %#v deleted %#v, want none", provider.created, provider.patched, provider.deleted)
	}
	var got dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "www-a"}, &got); err != nil {
		t.Fatalf("RecordSet was not found: %v", err)
	}
	assertCloudflareCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionFalse, "ManagedResourceMismatch")
}

func TestCloudflarerecordSetReconcilerRejectsProxiedTXT(t *testing.T) {
	ctx := context.Background()
	recordSet := cloudflareTXTRecordSetWithOptions("app", "txt", map[string]any{
		"ttl":     "Auto",
		"proxied": true,
	})
	k8sClient := fake.NewClientBuilder().
		WithScheme(cloudflareTestScheme(t)).
		WithObjects(
			cloudflareRecordSetProvider(),
			acceptedReadyCloudflareIdentity("platform", "cloudflare-ci"),
			acceptedCloudflareZoneClass("platform", "cloudflare-public"),
			cloudflareProgrammedZone("app", "apps-example-com"),
			recordSet,
			cloudflareZoneUnitWithRecordSetItem("app", "apps-example-com", "app", "txt", "txt", dnsv1alpha1.RecordTypeTXT),
		).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneClass{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneUnit{}, &cloudflarev1alpha1.CloudflareIdentity{}).
		Build()
	reconciler := &recordSetReconciler{Client: k8sClient, Provider: &fakeCloudflareRecordSetProvider{}}

	if _, err := reconcileCloudflareRecordSetAndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "txt"}}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var got dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "txt"}, &got); err != nil {
		t.Fatalf("RecordSet was not found: %v", err)
	}
	assertCloudflareCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionFalse, "DeniedByPolicy")
}

func TestCloudflarerecordSetReconcilerCreatesAutoTTLRecords(t *testing.T) {
	ctx := context.Background()
	recordSet := cloudflareARecordSet("app", "www-a")
	recordSet.Spec.TTL = nil
	rawOptions, err := json.Marshal(map[string]any{"ttl": "Auto"})
	if err != nil {
		t.Fatalf("marshal options: %v", err)
	}
	recordSet.Spec.Options = runtime.RawExtension{Raw: rawOptions}
	k8sClient := fake.NewClientBuilder().
		WithScheme(cloudflareTestScheme(t)).
		WithObjects(
			cloudflareRecordSetProvider(),
			acceptedReadyCloudflareIdentity("platform", "cloudflare-ci"),
			acceptedCloudflareZoneClass("platform", "cloudflare-public"),
			cloudflareProgrammedZone("app", "apps-example-com"),
			recordSet,
			cloudflareZoneUnitWithRecordSetItem("app", "apps-example-com", "app", "www-a", "www", dnsv1alpha1.RecordTypeA),
		).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneClass{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneUnit{}, &cloudflarev1alpha1.CloudflareIdentity{}).
		Build()
	provider := &fakeCloudflareRecordSetProvider{}
	reconciler := &recordSetReconciler{Client: k8sClient, Provider: provider}

	for i := 0; i < 5; i++ {
		if _, err := reconcileCloudflareRecordSetAndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "www-a"}}); err != nil {
			t.Fatalf("Reconcile returned error on pass %d: %v", i+1, err)
		}
	}

	if len(provider.created) != 2 {
		t.Fatalf("created records = %#v, want 2 records", provider.created)
	}
	for _, record := range provider.created {
		if record.TTL == nil || *record.TTL != 1 {
			t.Fatalf("created record TTL = %#v, want Cloudflare auto ttl value 1", record.TTL)
		}
	}
}

func TestCloudflarerecordSetReconcilerRejectsInvalidFixedTTL(t *testing.T) {
	tests := map[string]int32{
		"auto ttl sentinel": 1,
		"max int":           2147483647,
	}
	for name, ttl := range tests {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			recordSet := cloudflareARecordSet("app", "www-a")
			recordSet.Spec.TTL = &ttl
			k8sClient := fake.NewClientBuilder().
				WithScheme(cloudflareTestScheme(t)).
				WithObjects(
					cloudflareRecordSetProvider(),
					acceptedReadyCloudflareIdentity("platform", "cloudflare-ci"),
					acceptedCloudflareZoneClass("platform", "cloudflare-public"),
					cloudflareProgrammedZone("app", "apps-example-com"),
					recordSet,
					cloudflareZoneUnitWithRecordSetItem("app", "apps-example-com", "app", "www-a", "www", dnsv1alpha1.RecordTypeA),
				).
				WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneClass{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneUnit{}, &cloudflarev1alpha1.CloudflareIdentity{}).
				Build()
			provider := &fakeCloudflareRecordSetProvider{}
			reconciler := &recordSetReconciler{Client: k8sClient, Provider: provider}

			if _, err := reconcileCloudflareRecordSetAndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "www-a"}}); err != nil {
				t.Fatalf("Reconcile returned error: %v", err)
			}

			if len(provider.created) != 0 || len(provider.patched) != 0 {
				t.Fatalf("provider writes = created %#v patched %#v, want none", provider.created, provider.patched)
			}
			var got dnsv1alpha1.RecordSet
			if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "www-a"}, &got); err != nil {
				t.Fatalf("RecordSet was not found: %v", err)
			}
			assertCloudflareCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionFalse, "DeniedByPolicy")
		})
	}
}

func TestCloudflarerecordSetReconcilerRejectsDuplicateTags(t *testing.T) {
	ctx := context.Background()
	recordSet := cloudflareARecordSet("app", "www-a")
	rawOptions, err := json.Marshal(map[string]any{
		"tags": []string{"Owner:platform", "owner:platform"},
	})
	if err != nil {
		t.Fatalf("marshal options: %v", err)
	}
	recordSet.Spec.Options = runtime.RawExtension{Raw: rawOptions}
	k8sClient := fake.NewClientBuilder().
		WithScheme(cloudflareTestScheme(t)).
		WithObjects(
			cloudflareRecordSetProvider(),
			acceptedReadyCloudflareIdentity("platform", "cloudflare-ci"),
			acceptedCloudflareZoneClass("platform", "cloudflare-public"),
			cloudflareProgrammedZone("app", "apps-example-com"),
			recordSet,
			cloudflareZoneUnitWithRecordSetItem("app", "apps-example-com", "app", "www-a", "www", dnsv1alpha1.RecordTypeA),
		).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneClass{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneUnit{}, &cloudflarev1alpha1.CloudflareIdentity{}).
		Build()
	reconciler := &recordSetReconciler{Client: k8sClient, Provider: &fakeCloudflareRecordSetProvider{}}

	if _, err := reconcileCloudflareRecordSetAndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "www-a"}}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var got dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "www-a"}, &got); err != nil {
		t.Fatalf("RecordSet was not found: %v", err)
	}
	assertCloudflareCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionFalse, "DeniedByPolicy")
}

func TestCloudflarerecordSetReconcilerRejectsInvalidObservedRecordIDs(t *testing.T) {
	tests := []struct {
		name      string
		recordSet func() *dnsv1alpha1.RecordSet
		provider  func() *fakeCloudflareRecordSetProvider
	}{
		{
			name: "created record has non-hex id",
			recordSet: func() *dnsv1alpha1.RecordSet {
				recordSet := cloudflareARecordSet("app", "www-a")
				recordSet.Spec.A.Addresses = []string{"192.0.2.10"}
				return recordSet
			},
			provider: func() *fakeCloudflareRecordSetProvider {
				return &fakeCloudflareRecordSetProvider{createIDs: []string{"023e105f4ecef8ad9ca31a8372d0c35z"}}
			},
		},
		{
			name: "adopted record response has uppercase id",
			recordSet: func() *dnsv1alpha1.RecordSet {
				recordSet := cloudflareARecordSet("app", "www-a")
				recordSet.Finalizers = []string{RecordSetFinalizer}
				recordSet.Spec.A.Addresses = []string{"192.0.2.10"}
				recordSet.Spec.Adoption = cloudflareRaw(t, map[string]any{"recordIDs": []string{"023e105f4ecef8ad9ca31a8372d0c010"}})
				return recordSet
			},
			provider: func() *fakeCloudflareRecordSetProvider {
				ttl := int32(300)
				proxied := false
				return &fakeCloudflareRecordSetProvider{recordsByID: map[string]CloudflareDNSRecord{
					"023e105f4ecef8ad9ca31a8372d0c010": {
						ID:      "023e105f4ecef8ad9ca31a8372d0C010",
						Type:    "A",
						Name:    "www.apps.example.com",
						Content: "192.0.2.10",
						TTL:     &ttl,
						Proxied: &proxied,
					},
				}}
			},
		},
		{
			name: "re-observed records have duplicate ids",
			recordSet: func() *dnsv1alpha1.RecordSet {
				recordSet := cloudflareARecordSet("app", "www-a")
				recordSet.Finalizers = []string{RecordSetFinalizer}
				recordSet.Spec.A.Addresses = []string{"192.0.2.10", "192.0.2.11"}
				recordSet.Status.Provider = &dnsv1alpha1.ProviderStatus{Data: cloudflareRaw(t, map[string]any{
					"records": []map[string]any{{"id": "023e105f4ecef8ad9ca31a8372d0c010"}},
				})}
				return recordSet
			},
			provider: func() *fakeCloudflareRecordSetProvider {
				ttl := int32(300)
				proxied := false
				return &fakeCloudflareRecordSetProvider{records: []CloudflareDNSRecord{
					{ID: "023e105f4ecef8ad9ca31a8372d0c010", Type: "A", Name: "www.apps.example.com", Content: "192.0.2.10", TTL: &ttl, Proxied: &proxied},
					{ID: "023e105f4ecef8ad9ca31a8372d0c010", Type: "A", Name: "www.apps.example.com", Content: "192.0.2.11", TTL: &ttl, Proxied: &proxied},
				}}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			recordSet := tt.recordSet()
			k8sClient := fake.NewClientBuilder().
				WithScheme(cloudflareTestScheme(t)).
				WithObjects(
					cloudflareRecordSetProvider(),
					acceptedReadyCloudflareIdentity("platform", "cloudflare-ci"),
					acceptedCloudflareZoneClass("platform", "cloudflare-public"),
					cloudflareProgrammedZone("app", "apps-example-com"),
					recordSet,
					cloudflareZoneUnitWithRecordSetItem("app", "apps-example-com", "app", "www-a", "www", dnsv1alpha1.RecordTypeA),
				).
				WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneClass{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneUnit{}, &cloudflarev1alpha1.CloudflareIdentity{}).
				Build()
			reconciler := &recordSetReconciler{Client: k8sClient, Provider: tt.provider()}

			if _, err := reconcileCloudflareRecordSetAndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "www-a"}}); err != nil {
				t.Fatalf("Reconcile returned error: %v", err)
			}

			var got dnsv1alpha1.RecordSet
			if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "www-a"}, &got); err != nil {
				t.Fatalf("RecordSet was not found: %v", err)
			}
			assertCloudflareCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ProviderInvalidRequest")
			if got.Status.Provider != nil {
				data, err := cloudflareRecordSetStatusData(&got)
				if err != nil {
					t.Fatalf("status provider data is invalid: %v", err)
				}
				seen := map[string]struct{}{}
				for _, record := range data.Records {
					if !cloudflareIDPattern.MatchString(record.ID) {
						t.Fatalf("invalid status.provider.data.records[].id was written: %#v", data.Records)
					}
					if _, ok := seen[record.ID]; ok {
						t.Fatalf("duplicate status.provider.data.records[].id was written: %#v", data.Records)
					}
					seen[record.ID] = struct{}{}
				}
			}
		})
	}
}
