package route53

import (
	"context"
	"testing"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestRoute53PendingChangeCannotAttestNewDesiredGeneration(t *testing.T) {
	ctx := t.Context()
	r, provider, unit, recordSet := route53PendingGenerationFixture(t)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(unit)}
	pending := mustZoneUnitStatusData(t, ctx, r.Client, unit.Namespace, unit.Name).PendingRecordSetChange
	if pending == nil {
		t.Fatal("initial operation is not pending")
	}
	if err := r.Get(ctx, request.NamespacedName, unit); err != nil {
		t.Fatal(err)
	}
	unit.Spec.RecordSets[0].ObservedGeneration = 2
	unit.Spec.RecordSets[0].A.Addresses = []string{"192.0.2.99"}
	if err := r.Update(ctx, unit); err != nil {
		t.Fatal(err)
	}
	provider.changes[pending.ID].Status = route53v1alpha1.Route53ChangeStatusInSync
	if _, err := r.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, request.NamespacedName, unit); err != nil {
		t.Fatal(err)
	}
	status := zoneUnitRecordSetStatus(t, unit, recordSet.Namespace, recordSet.Name)
	condition := meta.FindStatusCondition(status.Conditions, string(dnsv1alpha1.ConditionProgrammed))
	if condition != nil && condition.Status == metav1.ConditionTrue && condition.ObservedGeneration == 2 {
		t.Fatal("old pending operation attested the new desired generation before provider convergence")
	}
	// The same UID retains its provider ownership and converges through an ordinary update.
	if _, err := r.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	pending = mustZoneUnitStatusData(t, ctx, r.Client, unit.Namespace, unit.Name).PendingRecordSetChange
	if pending == nil {
		t.Fatal("new desired generation did not enter provider convergence")
	}
	provider.changes[pending.ID].Status = route53v1alpha1.Route53ChangeStatusInSync
	for range 2 {
		if _, err := r.Reconcile(ctx, request); err != nil {
			t.Fatal(err)
		}
	}
	observed := provider.records[recordKey("Z000001", "www.apps.example.com.", dnsv1alpha1.RecordTypeA)]
	if len(observed.Values) != 1 || observed.Values[0] != "192.0.2.99" {
		t.Fatalf("provider did not converge to generation 2: %#v", observed)
	}
	if err := r.Get(ctx, request.NamespacedName, unit); err != nil {
		t.Fatal(err)
	}
	status = zoneUnitRecordSetStatus(t, unit, recordSet.Namespace, recordSet.Name)
	condition = meta.FindStatusCondition(status.Conditions, string(dnsv1alpha1.ConditionProgrammed))
	if condition == nil || condition.Status != metav1.ConditionTrue || condition.ObservedGeneration != 2 {
		t.Fatalf("current generation did not recover: %#v", status)
	}
}

func TestRoute53NoOpPreclaimCannotSubmitStaleSource(t *testing.T) {
	ctx := t.Context()
	r, provider, unit, _ := route53PendingGenerationFixture(t)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(unit)}
	pending := mustZoneUnitStatusData(t, ctx, r.Client, unit.Namespace, unit.Name).PendingRecordSetChange
	provider.changes[pending.ID].Status = route53v1alpha1.Route53ChangeStatusInSync
	for range 2 {
		if _, err := r.Reconcile(ctx, request); err != nil {
			t.Fatal(err)
		}
	}
	delete(provider.records, recordKey("Z000001", "www.apps.example.com.", dnsv1alpha1.RecordTypeA))
	before := len(provider.upserted)
	changed := false
	armed := false
	baseClient := r.Client
	r.Provider = generationChangingListProvider{Provider: provider, beforeList: func() { armed = true }}
	r.Client = generationChangingGetClient{Client: baseClient, afterGet: func(obj client.Object) {
		observed, ok := obj.(*dnsv1alpha1.ZoneUnit)
		if !armed || changed || !ok {
			return
		}
		// Advance source immediately after the first preclaim's read. The following
		// no-op preclaim must still observe and reject that newer source.
		changed = true
		next := observed.DeepCopy()
		next.Spec.RecordSets[0].ObservedGeneration = 2
		next.Spec.RecordSets[0].A.Addresses = []string{"192.0.2.99"}
		if err := baseClient.Update(ctx, next); err != nil {
			t.Fatal(err)
		}
	}}
	_, err := r.Reconcile(ctx, request)
	if !changed {
		t.Fatal("source interleaving was not exercised")
	}
	if len(provider.upserted) != before {
		t.Fatal("a no-op status preclaim submitted the stale desired generation")
	}
	if !apierrors.IsConflict(err) {
		t.Fatalf("stale source did not fail closed: %v", err)
	}
}

type generationChangingListProvider struct {
	Provider
	beforeList func()
}

func (p generationChangingListProvider) ListRecordSets(ctx context.Context, zoneID string) ([]RecordSetResource, error) {
	p.beforeList()
	return p.Provider.ListRecordSets(ctx, zoneID)
}

type generationChangingGetClient struct {
	client.Client
	afterGet func(client.Object)
}

func (c generationChangingGetClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if err := c.Client.Get(ctx, key, obj, opts...); err != nil {
		return err
	}
	c.afterGet(obj)
	return nil
}

func route53PendingGenerationFixture(t *testing.T) (*ZoneReconciler, *fakeProvider, *dnsv1alpha1.ZoneUnit, *dnsv1alpha1.RecordSet) {
	t.Helper()
	recordSet := route53ARecordSet("app", "www-a")
	recordSet.Generation = 1
	unit := route53ZoneUnit("app", "apps-example-com", "app", "route53-public")
	unit.Generation = 1
	unit.Spec.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetSpec{route53ZoneUnitRecordSetSpec(recordSet)}
	setRoute53ReadyZoneUnitStatus(t, unit)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{ID: "Z000001", Name: "apps.example.com", CallerReference: "dns-api:11111111-2222-3333-4444-555555555555"}
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(route53Provider(), route53ZoneClass("app", "route53-public", nil), acceptedRoute53Identity("app", "route53-dev"), unit).WithStatusSubresource(&dnsv1alpha1.ZoneUnit{}).Build()
	r := &ZoneReconciler{Client: c, Provider: provider}
	if _, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(unit)}); err != nil {
		t.Fatal(err)
	}
	return r, provider, unit, recordSet
}
