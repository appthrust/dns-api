package route53

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	zoneunitcontroller "github.com/appthrust/dns-api/internal/go/core/controllers/zoneunit"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestZoneStatusUpdateDoesNotResurrectCompletedRecordSetChange(t *testing.T) {
	ctx := t.Context()
	unit := route53ZoneUnit("app", "apps-example-com", "app", "route53-public")
	recordSet := route53ARecordSet("app", "www-a")
	unit.Spec.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetSpec{route53ZoneUnitRecordSetSpec(recordSet)}
	pending := route53v1alpha1.Route53ZoneStatusData{HostedZoneID: "Z000001", PendingRecordSetChange: &route53v1alpha1.Route53PendingRecordSetChange{ID: "/change/old-delete", Status: route53v1alpha1.Route53ChangeStatusPending, Operation: "DELETE_BATCH", AffectedRecordSets: []route53v1alpha1.Route53AffectedRecordSet{{Namespace: "app", Name: "www-a"}}}}
	raw, err := json.Marshal(pending)
	if err != nil {
		t.Fatal(err)
	}
	unit.Status.Provider = &dnsv1alpha1.ProviderStatus{State: runtime.RawExtension{Raw: raw}}
	cachedZone := route53ZoneFromZoneUnit(unit)
	applyRoute53ZoneStatusFromZoneUnit(&cachedZone, unit)
	// The pending DELETE completed after the initial informer observation. A
	// subsequent condition/name-server write must not replay that old operation.
	unit.Status.Provider.State = runtime.RawExtension{Raw: []byte(`{"hostedZoneID":"Z000001"}`)}
	objects := route53RecordSetObjects(t, recordSet)
	for i, obj := range objects {
		if _, ok := obj.(*dnsv1alpha1.ZoneUnit); ok {
			objects[i] = unit
		}
	}
	k8sClient := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objects...).WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneUnit{}, &dnsv1alpha1.RecordSet{}).Build()
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{ID: "Z000001", Name: "apps.example.com", CallerReference: "dns-api:11111111-2222-3333-4444-555555555555"}
	r := &ZoneReconciler{Client: k8sClient, Provider: provider}
	if err := r.patchZoneStatus(ctx, &cachedZone, func(status *dnsv1alpha1.ZoneStatus) {
		status.NameServers = []string{"ns1.example.net"}
	}); err != nil {
		t.Fatal(err)
	}
	var got dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &got); err != nil {
		t.Fatal(err)
	}
	data, err := route53ZoneUnitStatusData(&got)
	if err != nil {
		t.Fatal(err)
	}
	if data.PendingRecordSetChange != nil {
		t.Fatalf("completed DELETE was resurrected, blocking same-name recreation: %#v", data.PendingRecordSetChange)
	}
	if got.Status.Zone == nil || len(got.Status.Zone.NameServers) != 1 || got.Status.Zone.NameServers[0] != "ns1.example.net" {
		t.Fatalf("new hosted-zone observation was lost: %#v", got.Status.Zone)
	}
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(unit)}
	result, err := r.Reconcile(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	observed, ok := provider.records[recordKey("Z000001", "www.apps.example.com.", dnsv1alpha1.RecordTypeA)]
	if !ok || len(observed.Values) != 1 || observed.Values[0] != "192.0.2.10" {
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &got); err != nil {
			t.Fatal(err)
		}
		t.Fatalf("same-name record was not recreated after completed deletion: result=%#v status=%#v zone=%#v", result, got.Status, got.Status.Zone)
	}
	upsert := mustZoneUnitStatusData(t, ctx, k8sClient, unit.Namespace, unit.Name).PendingRecordSetChange
	if upsert == nil {
		t.Fatal("replacement did not enter provider convergence")
	}
	provider.changes[upsert.ID].Status = route53v1alpha1.Route53ChangeStatusInSync
	if _, err := r.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	if _, err := (&zoneunitcontroller.ZoneUnitCompositionReconciler{Client: k8sClient}).Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	var published dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(recordSet), &published); err != nil {
		t.Fatal(err)
	}
	for _, condition := range published.Status.Conditions {
		if condition.Type == string(dnsv1alpha1.ConditionProgrammed) && condition.Status == "True" {
			return
		}
	}
	t.Fatalf("replacement did not reach Programmed: %#v", published.Status)
}

func TestPendingRecordSetCompletionSurvivesDelayedCacheObservation(t *testing.T) {
	ctx := t.Context()
	cached := route53ZoneUnit("app", "apps-example-com", "app", "route53-public")
	cached.Status.Provider = &dnsv1alpha1.ProviderStatus{State: runtime.RawExtension{Raw: []byte(`{"hostedZoneID":"Z000001"}`)}}
	cacheLag := false
	k8sClient := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(cached).WithStatusSubresource(&dnsv1alpha1.ZoneUnit{}).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if unit, ok := obj.(*dnsv1alpha1.ZoneUnit); ok && cacheLag {
				*unit = *cached.DeepCopy()
				return nil
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}).Build()
	r := &ZoneReconciler{Client: k8sClient}
	zone := route53ZoneFromZoneUnit(cached)
	applyRoute53ZoneStatusFromZoneUnit(&zone, cached)
	if err := r.patchRoute53ZoneStatus(ctx, &zone, func(data *route53v1alpha1.Route53ZoneStatusData) {
		data.PendingRecordSetChange = &route53v1alpha1.Route53PendingRecordSetChange{ID: "/change/delete", Status: route53v1alpha1.Route53ChangeStatusPending, Operation: "DELETE_BATCH"}
	}); err != nil {
		t.Fatal(err)
	}
	cacheLag = true
	if err := r.patchRoute53ZoneStatus(ctx, &zone, func(data *route53v1alpha1.Route53ZoneStatusData) { data.PendingRecordSetChange = nil }); err != nil {
		t.Fatal(err)
	}
	cacheLag = false
	data := mustZoneUnitStatusData(t, ctx, k8sClient, cached.Namespace, cached.Name)
	if data.PendingRecordSetChange != nil {
		t.Fatalf("completed DELETE still blocks DNS changes after cached read: %#v", data.PendingRecordSetChange)
	}
}

func TestUnrelatedZoneStatusUpdatePreservesCurrentProgrammedState(t *testing.T) {
	ctx := t.Context()
	unit := route53ZoneUnit("app", "apps-example-com", "app", "route53-public")
	unit.Status.Zone = &dnsv1alpha1.ZoneUnitZoneStatus{Conditions: []metav1.Condition{{
		Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionFalse,
		Reason: "ProviderChangePending", Message: "pending",
	}}}
	stale := route53ZoneFromZoneUnit(unit)
	applyRoute53ZoneStatusFromZoneUnit(&stale, unit)
	unit.Status.Zone.Conditions[0].Status = metav1.ConditionTrue
	unit.Status.Zone.Conditions[0].Reason = "Programmed"
	unit.Status.Zone.Conditions[0].Message = "programmed"
	setZoneUnitProgrammedCondition(unit)
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(unit).
		WithStatusSubresource(&dnsv1alpha1.ZoneUnit{}).Build()
	r := &ZoneReconciler{Client: c}
	if err := r.patchZoneStatus(ctx, &stale, func(status *dnsv1alpha1.ZoneStatus) {
		status.NameServers = []string{"ns1.example.net"}
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(unit), unit); err != nil {
		t.Fatal(err)
	}
	assertCondition(t, unit.Status.Zone.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionTrue, "Programmed")
	assertCondition(t, unit.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionTrue, "Programmed")
}

type recordSetListErrorProvider struct {
	Provider
	err error
}

func (p recordSetListErrorProvider) ListRecordSets(context.Context, string) ([]RecordSetResource, error) {
	return nil, p.err
}

func TestZoneReconcileFailsClosedForStaleRecordSetStatusWrites(t *testing.T) {
	ctx := t.Context()
	recordA := route53ARecordSet("app", "www-a")
	recordB := route53ARecordSet("app", "api-a")
	recordB.Spec.Name = "api"
	unit := route53ZoneUnit("app", "apps-example-com", "app", "route53-public")
	unit.Generation = 1
	unit.Spec.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetSpec{
		route53ZoneUnitRecordSetSpec(recordA),
		route53ZoneUnitRecordSetSpec(recordB),
	}
	setRoute53ReadyZoneUnitStatus(t, unit)
	unit.Status.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetStatus{
		route53RecordSetStatusWithState(t, recordA, "www.apps.example.com."),
		route53RecordSetStatusWithState(t, recordB, "api.apps.example.com."),
	}

	var cached dnsv1alpha1.ZoneUnit
	cacheLag := false
	zoneUnitReads := 0
	recordSetStatusWrites := 0
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(route53Provider(), route53ZoneClass("app", "route53-public", nil), acceptedRoute53Identity("app", "route53-dev"), unit).
		WithStatusSubresource(&dnsv1alpha1.ZoneUnit{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if observed, ok := obj.(*dnsv1alpha1.ZoneUnit); ok {
					zoneUnitReads++
					if zoneUnitReads == 2 || cacheLag {
						*observed = *cached.DeepCopy()
						return nil
					}
				}
				return c.Get(ctx, key, obj, opts...)
			},
			SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				data, err := patch.Data(obj)
				if err != nil {
					return err
				}
				if err := c.SubResource(subResourceName).Patch(ctx, obj, patch, opts...); err != nil {
					return err
				}
				if subResourceName == "status" && bytes.Contains(data, []byte(`"recordSets"`)) {
					recordSetStatusWrites++
					if recordSetStatusWrites == 1 {
						cacheLag = true
					}
				}
				return nil
			},
		}).
		Build()
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &cached); err != nil {
		t.Fatal(err)
	}
	for index := range cached.Status.RecordSets {
		cached.Status.RecordSets[index].Provider = nil
	}
	zoneUnitReads = 0

	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
	}
	reconciler := &ZoneReconciler{
		Client: k8sClient,
		Provider: recordSetListErrorProvider{
			Provider: provider,
			err:      errors.New("Route 53 list record sets unavailable"),
		},
	}
	_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(unit)})
	if !apierrors.IsConflict(err) {
		t.Fatalf("Reconcile error = %v, want stale status conflict", err)
	}
	if !cacheLag {
		t.Fatal("record-set status writer did not observe delayed cache data")
	}

	cacheLag = false
	var got dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &got); err != nil {
		t.Fatal(err)
	}
	for _, recordSet := range []struct {
		name       string
		recordName string
	}{
		{name: "www-a", recordName: "www.apps.example.com."},
		{name: "api-a", recordName: "api.apps.example.com."},
	} {
		data := mustZoneUnitRecordSetStatusState(t, ctx, k8sClient, unit.Namespace, unit.Name, unit.Namespace, recordSet.name)
		if data.HostedZoneID != "Z000001" || data.RecordName != recordSet.recordName || data.RecordType != string(dnsv1alpha1.RecordTypeA) {
			t.Fatalf("%s provider state = %#v, want retained Route 53 record identity", recordSet.name, data)
		}
	}
}

func TestZoneReconcileDoesNotCompleteReplacedDeletingRecordSet(t *testing.T) {
	ctx := t.Context()
	recordSet := deletingRoute53ARecordSet(t)
	recordSet.Finalizers = []string{"dns.appthrust.io/recordset-finalizer"}
	cachedItem := route53ZoneUnitRecordSetSpec(recordSet)
	replacementItem := cachedItem
	replacementItem.Type = dnsv1alpha1.RecordTypeCNAME
	replacementItem.A = nil
	replacementItem.CNAME = &dnsv1alpha1.CNAMERecordSet{Target: "replacement.apps.example.com"}
	unit := route53ZoneUnit("app", "apps-example-com", "app", "route53-public")
	unit.Generation = 1
	unit.Spec.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetSpec{replacementItem}
	setRoute53ReadyZoneUnitStatus(t, unit)
	unit.Status.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetStatus{
		route53RecordSetStatusWithState(t, recordSet, "www.apps.example.com."),
	}

	var cached dnsv1alpha1.ZoneUnit
	useCachedSource := false
	zoneUnitReads := 0
	objects := route53RecordSetObjects(t, recordSet)
	for index, object := range objects {
		if _, ok := object.(*dnsv1alpha1.ZoneUnit); ok {
			objects[index] = unit
		}
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneUnit{}, &dnsv1alpha1.RecordSet{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if observed, ok := obj.(*dnsv1alpha1.ZoneUnit); ok && useCachedSource {
					zoneUnitReads++
					if zoneUnitReads <= 2 {
						*observed = *cached.DeepCopy()
						return nil
					}
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}).
		Build()
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &cached); err != nil {
		t.Fatal(err)
	}
	cached.Spec.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetSpec{cachedItem}
	useCachedSource = true

	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
	}
	ttl := int64(300)
	provider.records[recordKey("Z000001", "www.apps.example.com.", dnsv1alpha1.RecordTypeA)] = RecordSetResource{
		HostedZoneID: "Z000001",
		Name:         "www.apps.example.com.",
		Type:         dnsv1alpha1.RecordTypeA,
		TTL:          &ttl,
		Values:       []string{"192.0.2.10"},
	}
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}
	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(unit)}); !apierrors.IsConflict(err) {
		t.Fatalf("Reconcile error = %v, want conflict for replaced deleting RecordSet source", err)
	}
	if _, found := provider.records[recordKey("Z000001", "www.apps.example.com.", dnsv1alpha1.RecordTypeA)]; !found {
		t.Fatal("provider deleted the external record for a replaced deleting source")
	}
	if len(provider.deletedRRs) != 0 {
		t.Fatalf("provider delete requests = %#v, want none", provider.deletedRRs)
	}

	useCachedSource = false
	var gotUnit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &gotUnit); err != nil {
		t.Fatal(err)
	}
	status := zoneUnitRecordSetStatus(t, &gotUnit, recordSet.Namespace, recordSet.Name)
	if status.DeletionCompleted {
		t.Fatalf("replaced deleting RecordSet was marked complete without a provider DELETE: %#v", status)
	}
	if _, err := (&zoneunitcontroller.ZoneUnitCompositionReconciler{Client: k8sClient}).Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(unit)}); err != nil {
		t.Fatalf("core composition Reconcile returned error: %v", err)
	}
	var gotRecordSet dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(recordSet), &gotRecordSet); err != nil {
		t.Fatalf("deleting RecordSet was removed before provider DELETE: %v", err)
	}
	if !hasRecordSetFinalizer(&gotRecordSet, "dns.appthrust.io/recordset-finalizer") {
		t.Fatalf("RecordSet finalizers = %#v, want core deletion finalizer retained", gotRecordSet.Finalizers)
	}
}

func TestRecordSetDeleteConflictDoesNotCompleteDeletion(t *testing.T) {
	ctx := t.Context()
	recordSet := deletingRoute53ARecordSet(t)
	unit := route53ZoneUnit("app", "apps-example-com", "app", "route53-public")
	unit.Generation = 1
	item := route53ZoneUnitRecordSetSpec(recordSet)
	unit.Spec.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetSpec{item}
	setRoute53ReadyZoneUnitStatus(t, unit)
	unit.Status.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetStatus{
		route53RecordSetStatusWithState(t, recordSet, "www.apps.example.com."),
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(unit).
		WithStatusSubresource(&dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient}
	source := route53RecordSetFromZoneUnitItem(unit, item, unit.Status.RecordSets[0])
	conflictingItem := item
	conflictingItem.Type = dnsv1alpha1.RecordTypeCNAME
	if _, planned, err := reconciler.planRecordSetDelete(ctx, HostedZone{ID: "Z000001"}, &source, newZoneUnitRecordSetOwnership([]dnsv1alpha1.ZoneUnitRecordSetSpec{conflictingItem}), RecordSetResource{}, "www.apps.example.com.", dnsv1alpha1.ZoneUnitRecordSetStatus{}, false); err != nil {
		t.Fatal(err)
	} else if planned {
		t.Fatal("delete was planned despite conflicting owner")
	}

	var got dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &got); err != nil {
		t.Fatal(err)
	}
	status := zoneUnitRecordSetStatus(t, &got, recordSet.Namespace, recordSet.Name)
	if status.DeletionCompleted {
		t.Fatalf("blocked delete completed without external deletion: %#v", status)
	}
	assertCondition(t, status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionFalse, "RecordSetConflict")
	assertCondition(t, status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "RecordSetConflict")
}

func TestZoneReconcileBindsRecordSetReceiptAndPendingChangeToCurrentUID(t *testing.T) {
	ctx := t.Context()
	recordSet := route53ARecordSet("app", "www")
	recordSet.UID = types.UID("11111111-2222-3333-4444-000000000001")
	unit := route53ZoneUnit("app", "apps-example-com", "app", "route53-public")
	unit.Generation = 1
	unit.Spec.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetSpec{route53ZoneUnitRecordSetSpec(recordSet)}
	setRoute53ReadyZoneUnitStatus(t, unit)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(route53Provider(), route53ZoneClass("app", "route53-public", nil), acceptedRoute53Identity("app", "route53-dev"), unit).
		WithStatusSubresource(&dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(unit)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if len(provider.upserted) != 1 {
		t.Fatalf("upserted record sets = %#v, want one fresh claim", provider.upserted)
	}

	var got dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &got); err != nil {
		t.Fatal(err)
	}
	status := zoneUnitRecordSetStatus(t, &got, recordSet.Namespace, recordSet.Name)
	if status.RecordSetUID != recordSet.UID {
		t.Fatalf("recordSet status UID = %q, want %q", status.RecordSetUID, recordSet.UID)
	}
	if status.Provider == nil || len(status.Provider.State.Raw) == 0 {
		t.Fatalf("recordSet provider state = %#v, want current receipt", status.Provider)
	}
	pending := mustZoneUnitStatusData(t, ctx, k8sClient, unit.Namespace, unit.Name).PendingRecordSetChange
	if pending == nil || len(pending.AffectedRecordSets) != 1 {
		t.Fatalf("pending record set change = %#v, want one affected current claim", pending)
	}
	if affected := pending.AffectedRecordSets[0]; affected.Namespace != recordSet.Namespace || affected.Name != recordSet.Name || affected.UID != recordSet.UID {
		t.Fatalf("pending affected record set = %#v, want current claim identity", affected)
	}
}

// A receipt bound to a different incarnation never transfers, even when the
// live record equals desired. The UID-less pre-upgrade case is covered by
// recordset_ledger_test.go, where evidence may bind it.
func TestZoneReconcileDoesNotUseNoncurrentRecordSetReceipt(t *testing.T) {
	ctx := t.Context()
	recordSet := route53ARecordSet("app", "www")
	recordSet.UID = types.UID("11111111-2222-3333-4444-000000000001")
	unit := route53ZoneUnit("app", "apps-example-com", "app", "route53-public")
	unit.Generation = 1
	unit.Spec.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetSpec{route53ZoneUnitRecordSetSpec(recordSet)}
	setRoute53ReadyZoneUnitStatus(t, unit)
	legacy := route53RecordSetStatusWithState(t, recordSet, "www.apps.example.com.")
	legacy.RecordSetUID = types.UID("11111111-2222-3333-4444-000000000002")
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
		Values:       []string{"192.0.2.10"},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(route53Provider(), route53ZoneClass("app", "route53-public", nil), acceptedRoute53Identity("app", "route53-dev"), unit).
		WithStatusSubresource(&dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(unit)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if len(provider.upserted) != 0 || len(provider.deletedRRs) != 0 {
		t.Fatalf("noncurrent receipt authorized a provider mutation: upserts=%#v deletes=%#v", provider.upserted, provider.deletedRRs)
	}

	var got dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &got); err != nil {
		t.Fatal(err)
	}
	status := zoneUnitRecordSetStatus(t, &got, recordSet.Namespace, recordSet.Name)
	if status.RecordSetUID != recordSet.UID {
		t.Fatalf("recordSet status UID = %q, want current %q", status.RecordSetUID, recordSet.UID)
	}
	if status.Provider != nil {
		t.Fatalf("legacy provider receipt was rebound to current claim: %#v", status.Provider)
	}
	assertCondition(t, status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ProviderConflict")
}

func TestZoneReconcileDoesNotAttestCurrentClaimFromNoncurrentPendingChange(t *testing.T) {
	for _, priorUID := range []types.UID{"", "11111111-2222-3333-4444-000000000002"} {
		t.Run(string(priorUID), func(t *testing.T) {
			ctx := t.Context()
			recordSet := route53ARecordSet("app", "www")
			recordSet.UID = types.UID("11111111-2222-3333-4444-000000000001")
			unit := route53ZoneUnit("app", "apps-example-com", "app", "route53-public")
			unit.Generation = 1
			unit.Spec.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetSpec{route53ZoneUnitRecordSetSpec(recordSet)}
			setRoute53ReadyZoneUnitStatus(t, unit)
			state, err := json.Marshal(route53v1alpha1.Route53ZoneStatusData{
				HostedZoneID: "Z000001",
				PendingRecordSetChange: &route53v1alpha1.Route53PendingRecordSetChange{
					ID:        "/change/predecessor",
					Status:    route53v1alpha1.Route53ChangeStatusPending,
					Operation: "UPSERT_BATCH",
					AffectedRecordSets: []route53v1alpha1.Route53AffectedRecordSet{{
						Namespace: recordSet.Namespace,
						Name:      recordSet.Name,
						UID:       priorUID,
					}},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			unit.Status.Provider = &dnsv1alpha1.ProviderStatus{State: runtime.RawExtension{Raw: state}}
			provider := newFakeProvider()
			provider.zones["Z000001"] = HostedZone{
				ID:              "Z000001",
				Name:            "apps.example.com",
				CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
			}
			provider.changes["/change/predecessor"] = &route53v1alpha1.Route53Change{
				ID:     "/change/predecessor",
				Status: route53v1alpha1.Route53ChangeStatusInSync,
			}
			k8sClient := fake.NewClientBuilder().
				WithScheme(testScheme(t)).
				WithObjects(route53Provider(), route53ZoneClass("app", "route53-public", nil), acceptedRoute53Identity("app", "route53-dev"), unit).
				WithStatusSubresource(&dnsv1alpha1.ZoneUnit{}).
				Build()
			reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}
			request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(unit)}

			if _, err := reconciler.Reconcile(ctx, request); err != nil {
				t.Fatalf("pending completion Reconcile returned error: %v", err)
			}
			if len(provider.upserted) != 1 {
				t.Fatalf("current claim was not submitted after predecessor pending cleared: %#v", provider.upserted)
			}
			var afterPending dnsv1alpha1.ZoneUnit
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &afterPending); err != nil {
				t.Fatal(err)
			}
			status := zoneUnitRecordSetStatus(t, &afterPending, recordSet.Namespace, recordSet.Name)
			if status.RecordSetUID != recordSet.UID {
				t.Fatalf("recordSet status UID = %q, want current %q", status.RecordSetUID, recordSet.UID)
			}
			assertCondition(t, status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ProviderChangePending")
			if data, err := route53ZoneUnitStatusData(&afterPending); err != nil {
				t.Fatal(err)
			} else if data.PendingRecordSetChange == nil ||
				data.PendingRecordSetChange.ID == "/change/predecessor" ||
				data.PendingRecordSetChange.Status != route53v1alpha1.Route53ChangeStatusPending {
				t.Fatalf("fresh current change was not pending after predecessor INSYNC: %#v", data.PendingRecordSetChange)
			}
		})
	}
}

func TestZoneReconcileDoesNotDeleteExternalRecordFromNoncurrentReceipt(t *testing.T) {
	ctx := t.Context()
	recordSet := deletingRoute53ARecordSet(t)
	recordSet.UID = types.UID("11111111-2222-3333-4444-000000000001")
	unit := route53ZoneUnit("app", "apps-example-com", "app", "route53-public")
	unit.Generation = 1
	unit.Spec.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetSpec{route53ZoneUnitRecordSetSpec(recordSet)}
	setRoute53ReadyZoneUnitStatus(t, unit)
	legacy := route53RecordSetStatusWithState(t, recordSet, "www.apps.example.com.")
	legacy.RecordSetUID = types.UID("11111111-2222-3333-4444-000000000002")
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
		Values:       []string{"192.0.2.10"},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(route53Provider(), route53ZoneClass("app", "route53-public", nil), acceptedRoute53Identity("app", "route53-dev"), unit).
		WithStatusSubresource(&dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(unit)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if len(provider.deletedRRs) != 0 {
		t.Fatalf("noncurrent receipt authorized external delete: %#v", provider.deletedRRs)
	}
	var got dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &got); err != nil {
		t.Fatal(err)
	}
	status := zoneUnitRecordSetStatus(t, &got, recordSet.Namespace, recordSet.Name)
	if status.DeletionCompleted {
		t.Fatalf("noncurrent receipt completed deleting claim: %#v", status)
	}
	if status.RecordSetUID != recordSet.UID || status.Provider != nil {
		t.Fatalf("noncurrent receipt was preserved as current provider state: %#v", status)
	}
	assertCondition(t, status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ProviderConflict")
}

func TestZoneReconcileCompletesCurrentDeletionAfterFreshExternalAbsence(t *testing.T) {
	ctx := t.Context()
	recordSet := deletingRoute53ARecordSet(t)
	recordSet.UID = types.UID("11111111-2222-3333-4444-000000000001")
	unit := route53ZoneUnit("app", "apps-example-com", "app", "route53-public")
	unit.Generation = 1
	unit.Spec.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetSpec{route53ZoneUnitRecordSetSpec(recordSet)}
	setRoute53ReadyZoneUnitStatus(t, unit)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(route53Provider(), route53ZoneClass("app", "route53-public", nil), acceptedRoute53Identity("app", "route53-dev"), unit).
		WithStatusSubresource(&dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(unit)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if len(provider.deletedRRs) != 0 {
		t.Fatalf("external absence issued a delete: %#v", provider.deletedRRs)
	}
	var got dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &got); err != nil {
		t.Fatal(err)
	}
	status := zoneUnitRecordSetStatus(t, &got, recordSet.Namespace, recordSet.Name)
	if !status.DeletionCompleted || status.RecordSetUID != recordSet.UID {
		t.Fatalf("fresh external absence did not complete current claim deletion: %#v", status)
	}
}

func TestZoneReconcileRejectsMissingRecordSetUIDBeforeProviderMutation(t *testing.T) {
	ctx := t.Context()
	recordSet := route53ARecordSet("app", "www")
	item := route53ZoneUnitRecordSetSpec(recordSet)
	item.RecordSetUID = ""
	unit := route53ZoneUnit("app", "apps-example-com", "app", "route53-public")
	unit.Generation = 1
	unit.Spec.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetSpec{item}
	setRoute53ReadyZoneUnitStatus(t, unit)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(route53Provider(), route53ZoneClass("app", "route53-public", nil), acceptedRoute53Identity("app", "route53-dev"), unit).
		WithStatusSubresource(&dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(unit)}); !apierrors.IsConflict(err) {
		t.Fatalf("Reconcile error = %v, want missing-UID source conflict", err)
	}
	if len(provider.upserted) != 0 || len(provider.deletedRRs) != 0 {
		t.Fatalf("missing UID authorized a provider mutation: upserts=%#v deletes=%#v", provider.upserted, provider.deletedRRs)
	}
}

func TestRecordSetStatusWriterRejectsReplacedUIDSource(t *testing.T) {
	ctx := t.Context()
	recordSet := route53ARecordSet("app", "www")
	unit := route53ZoneUnit("app", "apps-example-com", "app", "route53-public")
	unit.Spec.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetSpec{route53ZoneUnitRecordSetSpec(recordSet)}
	unit.Status.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetStatus{
		route53RecordSetStatusWithState(t, recordSet, "www.apps.example.com."),
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(unit).
		WithStatusSubresource(&dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient}
	stale := route53RecordSetFromZoneUnitItem(unit, unit.Spec.RecordSets[0], unit.Status.RecordSets[0])
	stale.UID = types.UID("11111111-2222-3333-4444-000000000002")

	if err := reconciler.setRecordSetProgrammed(ctx, &stale, metav1.ConditionFalse, "ProviderChangePending", "stale source"); !apierrors.IsConflict(err) {
		t.Fatalf("status writer error = %v, want replaced-UID source conflict", err)
	}
	var got dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), &got); err != nil {
		t.Fatal(err)
	}
	status := zoneUnitRecordSetStatus(t, &got, recordSet.Namespace, recordSet.Name)
	if status.RecordSetUID != recordSet.UID || status.Provider == nil {
		t.Fatalf("replaced UID changed stored current receipt: %#v", status)
	}
}

func setRoute53ReadyZoneUnitStatus(t *testing.T, unit *dnsv1alpha1.ZoneUnit) {
	t.Helper()
	state, err := json.Marshal(route53v1alpha1.Route53ZoneStatusData{
		HostedZoneID:    "Z000001",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
	})
	if err != nil {
		t.Fatal(err)
	}
	public, err := json.Marshal(route53v1alpha1.Route53ZoneStatusData{HostedZoneID: "Z000001"})
	if err != nil {
		t.Fatal(err)
	}
	unit.Status.ObservedGeneration = unit.Generation
	unit.Status.Provider = &dnsv1alpha1.ProviderStatus{State: runtime.RawExtension{Raw: state}}
	unit.Status.Zone = &dnsv1alpha1.ZoneUnitZoneStatus{
		Provider: &dnsv1alpha1.ProviderStatus{Data: runtime.RawExtension{Raw: public}},
		Conditions: []metav1.Condition{
			{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted", Message: "Zone is accepted by Route 53 policy", ObservedGeneration: unit.Spec.Zone.ObservedGeneration},
			{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionTrue, Reason: "Programmed", Message: "Route 53 hosted zone is programmed", ObservedGeneration: unit.Spec.Zone.ObservedGeneration},
		},
	}
	unit.Status.Conditions = []metav1.Condition{
		{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionTrue, Reason: "Programmed", Message: "ZoneUnit is programmed", ObservedGeneration: unit.Generation},
	}
}

func route53RecordSetStatusWithState(t *testing.T, recordSet *dnsv1alpha1.RecordSet, recordName string) dnsv1alpha1.ZoneUnitRecordSetStatus {
	t.Helper()
	raw, err := json.Marshal(route53v1alpha1.Route53RecordSetStatusData{
		HostedZoneID: "Z000001",
		RecordName:   recordName,
		RecordType:   string(recordSet.Spec.Type),
	})
	if err != nil {
		t.Fatal(err)
	}
	return dnsv1alpha1.ZoneUnitRecordSetStatus{
		RecordSetNamespace: recordSet.Namespace,
		RecordSetName:      recordSet.Name,
		RecordSetUID:       recordSet.UID,
		ObservedGeneration: recordSet.Generation,
		Provider:           &dnsv1alpha1.ProviderStatus{State: runtime.RawExtension{Raw: raw}},
		Conditions: []metav1.Condition{
			{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted", Message: "RecordSet is accepted by Route 53 policy", ObservedGeneration: recordSet.Generation},
			{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionTrue, Reason: "Programmed", Message: "Route 53 record set is programmed", ObservedGeneration: recordSet.Generation},
		},
	}
}

func zoneUnitRecordSetStatus(t *testing.T, unit *dnsv1alpha1.ZoneUnit, namespace, name string) dnsv1alpha1.ZoneUnitRecordSetStatus {
	t.Helper()
	for _, status := range unit.Status.RecordSets {
		if status.RecordSetNamespace == namespace && status.RecordSetName == name {
			return status
		}
	}
	t.Fatalf("ZoneUnit status.recordSets has no entry for %s/%s: %#v", namespace, name, unit.Status.RecordSets)
	return dnsv1alpha1.ZoneUnitRecordSetStatus{}
}

func hasRecordSetFinalizer(recordSet *dnsv1alpha1.RecordSet, finalizer string) bool {
	for _, value := range recordSet.Finalizers {
		if value == finalizer {
			return true
		}
	}
	return false
}
