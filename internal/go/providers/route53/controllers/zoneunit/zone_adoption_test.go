package route53

import (
	"context"
	"slices"
	"testing"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	"github.com/aws/smithy-go"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestZoneReconcilerAdoptsHostedZoneByName(t *testing.T) {
	private := HostedZone{ID: "ZPRIVATE", Name: "apps.example.com.", Private: true}
	public := HostedZone{ID: "ZPUBLIC", Name: "apps.example.com", NameServers: []string{"ns-1.awsdns.example"}}
	for _, test := range []struct {
		name       string
		parameters string
		zones      []HostedZone
		listErr    error
		wantID     string
		wantType   route53v1alpha1.ZoneType
		reason     string
		message    string
	}{
		{name: "private single match", parameters: `{"zoneType":"Private"}`, zones: []HostedZone{private}, wantID: "ZPRIVATE", wantType: route53v1alpha1.ZoneTypePrivate},
		{name: "default public single match", parameters: `{}`, zones: []HostedZone{public}, wantID: "ZPUBLIC", wantType: route53v1alpha1.ZoneTypePublic},
		{name: "private filters public", parameters: `{"zoneType":"Private"}`, zones: []HostedZone{private, public}, wantID: "ZPRIVATE", wantType: route53v1alpha1.ZoneTypePrivate},
		{name: "public filters private", parameters: `{"zoneType":"Public"}`, zones: []HostedZone{private, public}, wantID: "ZPUBLIC", wantType: route53v1alpha1.ZoneTypePublic},
		{name: "filters other names", parameters: `{"zoneType":"Private"}`, zones: []HostedZone{private, {ID: "ZOTHER", Name: "other.example.com", Private: true}}, wantID: "ZPRIVATE", wantType: route53v1alpha1.ZoneTypePrivate},
		{name: "no zones", parameters: `{"zoneType":"Private"}`, reason: "ExternalResourceNotFound", message: "no same-name Route 53 hosted zone matches ZoneClass zoneType Private"},
		{name: "only wrong type", parameters: `{"zoneType":"Private"}`, zones: []HostedZone{public}, reason: "ExternalResourceNotFound", message: "no same-name Route 53 hosted zone matches ZoneClass zoneType Private"},
		{name: "only wrong name", parameters: `{"zoneType":"Private"}`, zones: []HostedZone{{ID: "ZOTHER", Name: "other.example.com", Private: true}}, reason: "ExternalResourceNotFound", message: "no same-name Route 53 hosted zone matches ZoneClass zoneType Private"},
		{name: "two private matches", parameters: `{"zoneType":"Private"}`, zones: []HostedZone{private, {ID: "ZDUPLICATE", Name: "apps.example.com", Private: true}}, reason: "ExternalResourceMismatch", message: "2 same-name Route 53 hosted zones match ZoneClass zoneType Private; adoption.byName requires exactly one"},
		{name: "two public matches", parameters: `{}`, zones: []HostedZone{public, {ID: "ZDUPLICATE", Name: "apps.example.com."}}, reason: "ExternalResourceMismatch", message: "2 same-name Route 53 hosted zones match ZoneClass zoneType Public; adoption.byName requires exactly one"},
		{name: "listing access denied", parameters: `{"zoneType":"Private"}`, listErr: &smithy.GenericAPIError{Code: "AccessDenied", Message: "denied"}, reason: "ProviderAccessDenied"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			provider := newFakeProvider()
			for _, zone := range test.zones {
				provider.zones[zone.ID] = zone
			}
			provider.listHostedZonesByName = func(string) ([]HostedZone, error) { return test.zones, test.listErr }
			class := route53ZoneClass("app", "route53", nil)
			class.Spec.Parameters = runtime.RawExtension{Raw: []byte(test.parameters)}
			unit := route53ZoneUnit("app", "apps-example-com", "app", class.Name)
			unit.Spec.Zone.Adoption = runtime.RawExtension{Raw: []byte(`{"byName":true}`)}
			k8sClient := fake.NewClientBuilder().WithScheme(testScheme(t)).
				WithObjects(route53Provider(), class, acceptedRoute53Identity("app", "route53-dev"), unit).
				WithStatusSubresource(&dnsv1alpha1.ZoneUnit{}).Build()
			reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}
			request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(unit)}
			if _, err := reconciler.Reconcile(ctx, request); err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			if err := k8sClient.Get(ctx, request.NamespacedName, unit); err != nil {
				t.Fatalf("Get ZoneUnit: %v", err)
			}
			if unit.Status.Zone == nil {
				t.Fatal("status.zone was nil")
			}
			assertCondition(t, unit.Status.Zone.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted")
			if !slices.Equal(provider.listed, []string{"apps.example.com"}) {
				t.Fatalf("ListHostedZonesByName calls = %v, want one domain lookup", provider.listed)
			}
			if len(provider.created) != 0 || len(provider.deleted) != 0 {
				t.Fatalf("unexpected hosted zone mutation: created=%v deleted=%v", provider.created, provider.deleted)
			}
			if test.reason != "" {
				condition := assertCondition(t, unit.Status.Zone.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, test.reason)
				if test.message != "" && condition.Message != test.message {
					t.Fatalf("message = %q, want %q", condition.Message, test.message)
				}
				if len(provider.fetched) != 0 || len(provider.tags) != 0 {
					t.Fatalf("unresolved adoption fetched or tagged zones: fetched=%v tags=%v", provider.fetched, provider.tags)
				}
				state := mustZoneUnitStatusData(t, ctx, k8sClient, unit.Namespace, unit.Name)
				if state.HostedZoneID != "" {
					t.Fatalf("unresolved adoption recorded hosted zone ID %q", state.HostedZoneID)
				}
				return
			}
			assertCondition(t, unit.Status.Zone.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionTrue, "Programmed")
			if unit.Status.Zone.Provider == nil {
				t.Fatal("status.zone.provider was nil")
			}
			assertRoute53RawEqual(t, unit.Status.Zone.Provider.Data, map[string]any{"hostedZoneID": test.wantID, "zoneType": string(test.wantType)})
			if !slices.Equal(unit.Status.Zone.NameServers, provider.zones[test.wantID].NameServers) {
				t.Fatalf("nameServers = %v", unit.Status.Zone.NameServers)
			}

			// Newly ambiguous discovery must not retarget a recorded adoption.
			provider.listHostedZonesByName = func(string) ([]HostedZone, error) {
				t.Fatal("recorded adoption must not list hosted zones again")
				return nil, nil
			}
			if _, err := reconciler.Reconcile(ctx, request); err != nil {
				t.Fatalf("second Reconcile: %v", err)
			}
			if !slices.Equal(provider.fetched, []string{test.wantID, test.wantID}) {
				t.Fatalf("GetHostedZone calls = %v, want recorded ID twice", provider.fetched)
			}
			if err := k8sClient.Get(ctx, request.NamespacedName, unit); err != nil {
				t.Fatalf("Get ZoneUnit: %v", err)
			}
			assertCondition(t, unit.Status.Zone.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted")
			assertCondition(t, unit.Status.Zone.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionTrue, "Programmed")

			// Losing the recorded resource must not adopt another same-name zone.
			delete(provider.zones, test.wantID)
			if _, err := reconciler.Reconcile(ctx, request); err != nil {
				t.Fatalf("missing recorded zone Reconcile: %v", err)
			}
			if err := k8sClient.Get(ctx, request.NamespacedName, unit); err != nil {
				t.Fatalf("Get ZoneUnit: %v", err)
			}
			assertCondition(t, unit.Status.Zone.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ExternalResourceNotFound")
			state := mustZoneUnitStatusData(t, ctx, k8sClient, unit.Namespace, unit.Name)
			if state.HostedZoneID != test.wantID {
				t.Fatalf("recorded ID = %q, want %q", state.HostedZoneID, test.wantID)
			}
		})
	}
}

func TestZoneReconcilerRejectsInvalidAdoption(t *testing.T) {
	for _, adoption := range []string{
		`{}`, `[]`, `{"byName":false}`, `{"byName":"true"}`, `{"byName":null}`,
		`{"hostedZoneId":"ZID","byName":true}`, `{"hostedZoneId":"ZID","byName":false}`,
		`{"hostedZoneId":"ZID","byName":null}`, `{"hostedZoneId":null,"byName":true}`,
		`{"hostedZoneId":"/hostedzone/ZID"}`, `{"hostedZoneId":""}`,
	} {
		t.Run(adoption, func(t *testing.T) {
			ctx := context.Background()
			unit := route53ZoneUnit("app", "apps-example-com", "app", "route53")
			unit.Spec.Zone.Adoption = runtime.RawExtension{Raw: []byte(adoption)}
			k8sClient := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(unit).
				WithStatusSubresource(&dnsv1alpha1.ZoneUnit{}).Build()
			provider := newFakeProvider()
			reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}
			request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(unit)}
			if _, err := reconciler.Reconcile(ctx, request); err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			if err := k8sClient.Get(ctx, request.NamespacedName, unit); err != nil {
				t.Fatalf("Get ZoneUnit: %v", err)
			}
			if unit.Status.Zone == nil {
				t.Fatal("status.zone was nil")
			}
			assertCondition(t, unit.Status.Zone.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionFalse, "InvalidAdoption")
			if len(provider.listed) != 0 || len(provider.fetched) != 0 || len(provider.created) != 0 {
				t.Fatal("invalid adoption reached provider")
			}
		})
	}
}

func TestZoneReconcilerRetainsByNameAdoptedPrivateZone(t *testing.T) {
	for _, parameters := range []string{`{"zoneType":"Private"}`, `{"zoneDeletionPolicy":"Delete"}`} {
		t.Run(parameters, func(t *testing.T) {
			ctx := context.Background()
			provider := newFakeProvider()
			provider.zones["ZPRIVATE"] = HostedZone{ID: "ZPRIVATE", Name: "apps.example.com", Private: true}
			class := route53ZoneClass("app", "route53", nil)
			class.Spec.Parameters = runtime.RawExtension{Raw: []byte(`{"zoneType":"Private"}`)}
			unit := route53ZoneUnit("app", "apps-example-com", "app", class.Name)
			unit.Spec.Zone.Adoption = runtime.RawExtension{Raw: []byte(`{"byName":true}`)}
			k8sClient := fake.NewClientBuilder().WithScheme(testScheme(t)).
				WithObjects(route53Provider(), class, acceptedRoute53Identity("app", "route53-dev"), unit).
				WithStatusSubresource(&dnsv1alpha1.ZoneUnit{}).Build()
			recorder := &capturingRecorder{}
			reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider, Recorder: recorder}
			request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(unit)}
			if _, err := reconciler.Reconcile(ctx, request); err != nil {
				t.Fatalf("adoption Reconcile: %v", err)
			}
			state := mustZoneUnitStatusData(t, ctx, k8sClient, unit.Namespace, unit.Name)
			if state.HostedZoneID != "ZPRIVATE" {
				t.Fatalf("recorded ID = %q, want ZPRIVATE", state.HostedZoneID)
			}
			if err := k8sClient.Get(ctx, request.NamespacedName, unit); err != nil {
				t.Fatalf("Get ZoneUnit: %v", err)
			}
			unit.Finalizers = append(unit.Finalizers, ZoneFinalizer)
			if err := k8sClient.Update(ctx, unit); err != nil {
				t.Fatalf("add deletion finalizer: %v", err)
			}
			class.Spec.Parameters = runtime.RawExtension{Raw: []byte(parameters)}
			if err := k8sClient.Update(ctx, class); err != nil {
				t.Fatalf("update deletion policy: %v", err)
			}
			if err := k8sClient.Delete(ctx, unit); err != nil {
				t.Fatalf("delete ZoneUnit: %v", err)
			}
			if _, err := reconciler.Reconcile(ctx, request); err != nil {
				t.Fatalf("deletion Reconcile: %v", err)
			}
			if len(provider.deleted) != 0 || len(provider.listed) != 1 {
				t.Fatalf("deletion calls=%v lookup calls=%v", provider.deleted, provider.listed)
			}
			if _, exists := provider.zones["ZPRIVATE"]; !exists {
				t.Fatal("private hosted zone was deleted")
			}
			if !recorder.has("Zone", "ExternalResourceRetained") {
				t.Fatalf("events = %v, want ExternalResourceRetained", recorder.events)
			}
			if err := k8sClient.Get(ctx, request.NamespacedName, unit); err != nil {
				if !apierrors.IsNotFound(err) {
					t.Fatalf("Get deleted ZoneUnit: %v", err)
				}
			} else if slices.Contains(unit.Finalizers, ZoneFinalizer) {
				t.Fatalf("provider deletion finalizer remains: %v", unit.Finalizers)
			}
		})
	}
}

func TestHostedZoneIDForDeleteAdoption(t *testing.T) {
	for _, test := range []struct {
		name     string
		adoption string
		statusID string
		wantID   string
	}{
		{name: "by name uses recorded ID", adoption: `{"byName":true}`, statusID: "ZRECORDED", wantID: "ZRECORDED"},
		{name: "unresolved by name has no delete target", adoption: `{"byName":true}`},
		{name: "explicit ID without status", adoption: `{"hostedZoneId":"ZEXPLICIT"}`, wantID: "ZEXPLICIT"},
		{name: "status wins over explicit ID", adoption: `{"hostedZoneId":"ZEXPLICIT"}`, statusID: "ZRECORDED", wantID: "ZRECORDED"},
	} {
		t.Run(test.name, func(t *testing.T) {
			zone := &dnsv1alpha1.Zone{Spec: dnsv1alpha1.ZoneSpec{Adoption: runtime.RawExtension{Raw: []byte(test.adoption)}}}
			id, err := hostedZoneIDForDelete(zone, route53v1alpha1.Route53ZoneStatusData{HostedZoneID: test.statusID})
			if err != nil || id != test.wantID {
				t.Fatalf("hostedZoneIDForDelete = (%q, %v), want (%q, nil)", id, err, test.wantID)
			}
		})
	}
}
