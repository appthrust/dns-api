package route53

import (
	"context"
	"testing"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	"github.com/aws/smithy-go"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestZoneReconcilerCreatedZoneNegativeCacheTTL(t *testing.T) {
	ctx := context.Background()
	provider := newFakeProvider()
	class := route53ZoneClass("app", "route53", nil)
	class.Spec.Parameters = runtime.RawExtension{Raw: []byte(`{"negativeCacheTTLSeconds":60}`)}
	unit := route53ZoneUnit("app", "apps-example-com", "app", class.Name)
	k8sClient := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(route53Provider(), class, acceptedRoute53Identity("app", "route53-dev"), unit).
		WithStatusSubresource(&dnsv1alpha1.ZoneUnit{}).Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(unit)}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	state := mustZoneUnitStatusData(t, ctx, k8sClient, unit.Namespace, unit.Name)
	if len(provider.created) != 1 || state.PendingHostedZoneChange == nil {
		t.Fatalf("created zones = %d, state = %#v", len(provider.created), state)
	}
	provider.changes[state.PendingHostedZoneChange.ID].Status = route53v1alpha1.Route53ChangeStatusInSync
	soa := RecordSetResource{HostedZoneID: state.HostedZoneID, Name: "apps.example.com.", Type: "SOA", TTL: ptr(int64(900)), Values: []string{defaultSOA}}
	provider.records[recordKey(soa.HostedZoneID, soa.Name, soa.Type)] = soa
	for range 2 {
		if _, err := reconciler.Reconcile(ctx, request); err != nil {
			t.Fatal(err)
		}
	}
	if len(provider.created) != 1 || len(provider.upserted) != 1 || provider.upserted[0].HostedZoneID != state.HostedZoneID {
		t.Fatalf("created-zone SOA not converged: created=%d, upserted=%#v", len(provider.created), provider.upserted)
	}
	state = mustZoneUnitStatusData(t, ctx, k8sClient, unit.Namespace, unit.Name)
	if state.PendingHostedZoneChange == nil || state.PendingHostedZoneChange.Operation != "NegativeCacheTTL" {
		t.Fatalf("SOA change not tracked: %#v", state)
	}
}

func TestZoneReconcilerNegativeCacheTTLProviderErrors(t *testing.T) {
	for _, operation := range []string{"read", "write"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			provider := newFakeProvider()
			provider.zones["ZSOA"] = HostedZone{ID: "ZSOA", Name: "apps.example.com"}
			soa := RecordSetResource{HostedZoneID: "ZSOA", Name: "apps.example.com.", Type: "SOA", TTL: ptr(int64(900)), Values: []string{defaultSOA}}
			provider.records[recordKey(soa.HostedZoneID, soa.Name, soa.Type)] = soa
			denied := &smithy.GenericAPIError{Code: "AccessDenied", Message: "private provider diagnostic"}
			if operation == "read" {
				provider.getRecordSetErr = denied
			} else {
				provider.changeRecordSetsErr = func(string, []RecordSetChange) error { return denied }
			}
			class := route53ZoneClass("app", "route53", nil)
			class.Spec.Parameters = runtime.RawExtension{Raw: []byte(`{"negativeCacheTTLSeconds":60}`)}
			unit := route53ZoneUnit("app", "apps-example-com", "app", class.Name)
			unit.Spec.Zone.Adoption = runtime.RawExtension{Raw: []byte(`{"hostedZoneId":"ZSOA"}`)}
			k8sClient := fake.NewClientBuilder().WithScheme(testScheme(t)).
				WithObjects(route53Provider(), class, acceptedRoute53Identity("app", "route53-dev"), unit).
				WithStatusSubresource(&dnsv1alpha1.ZoneUnit{}).Build()
			reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}
			if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(unit)}); err != nil {
				t.Fatal(err)
			}
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(unit), unit); err != nil {
				t.Fatal(err)
			}
			assertCondition(t, unit.Status.Zone.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ProviderAccessDenied")
			if len(provider.upserted) != 0 {
				t.Fatal("failed provider operation wrote SOA")
			}
		})
	}
}
