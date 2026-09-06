package route53

import (
	"context"
	"encoding/json"
	"testing"

	zoneunitcontroller "github.com/appthrust/dns-api/internal/go/core/controllers/zoneunit"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
