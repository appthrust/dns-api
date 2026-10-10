package route53

import (
	"context"
	"slices"
	"testing"

	zoneclass "github.com/appthrust/dns-api/internal/go/providers/route53/controllers/zoneclass"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestZoneReconcilerAdoptionMatchesZoneType(t *testing.T) {
	for _, test := range []struct {
		name       string
		parameters string
		private    bool
		zoneType   route53v1alpha1.ZoneType
		message    string
	}{
		{
			name:       "private adoption",
			parameters: `{"zoneType":"Private"}`,
			private:    true,
			zoneType:   route53v1alpha1.ZoneTypePrivate,
		},
		{
			name:       "default public adoption",
			parameters: `{}`,
			zoneType:   route53v1alpha1.ZoneTypePublic,
		},
		{
			name:       "explicit public adoption",
			parameters: `{"zoneType":"Public"}`,
			zoneType:   route53v1alpha1.ZoneTypePublic,
		},
		{
			name:       "private class rejects public zone",
			parameters: `{"zoneType":"Private"}`,
			message:    "Route 53 hosted zone is public; ZoneClass zoneType is Private",
		},
		{
			name:       "default public class rejects private zone",
			parameters: `{}`,
			private:    true,
			message:    "Route 53 hosted zone is private; only public hosted zones are supported",
		},
		{
			name:       "explicit public class rejects private zone",
			parameters: `{"zoneType":"Public"}`,
			private:    true,
			message:    "Route 53 hosted zone is private; only public hosted zones are supported",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			provider := newFakeProvider()
			hostedZone := HostedZone{ID: "ZADOPT", Name: "apps.example.com", Private: test.private}
			if !test.private {
				hostedZone.NameServers = []string{"ns-1.awsdns.example", "ns-2.awsdns.example"}
			}
			provider.zones[hostedZone.ID] = hostedZone
			objects := sameNamespaceClassObjects("app", nil)
			zone := objects[len(objects)-1].(*dnsv1alpha1.Zone)
			zone.Spec.Adoption = runtime.RawExtension{Raw: []byte(`{"hostedZoneId":"ZADOPT"}`)}
			for _, object := range objects {
				if class, ok := object.(*dnsv1alpha1.ZoneClass); ok {
					class.Spec.Parameters = runtime.RawExtension{Raw: []byte(test.parameters)}
				}
			}
			k8sClient := fake.NewClientBuilder().
				WithScheme(testScheme(t)).
				WithObjects(objects...).
				WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
				Build()
			if _, err := (&zoneclass.ZoneClassReconciler{Client: k8sClient}).Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Namespace: "app", Name: "route53-public"}}); err != nil {
				t.Fatalf("ZoneClass reconcile returned error: %v", err)
			}
			reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}
			key := client.ObjectKeyFromObject(zone)
			if _, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: key}); err != nil {
				t.Fatalf("Reconcile returned error: %v", err)
			}
			if len(provider.created) != 0 || len(provider.deleted) != 0 {
				t.Fatalf("hosted zone mutations: created = %d, deleted = %v, want none", len(provider.created), provider.deleted)
			}

			var got dnsv1alpha1.Zone
			if err := k8sClient.Get(ctx, key, &got); err != nil {
				t.Fatalf("Zone was not found: %v", err)
			}
			assertCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted")
			var unit dnsv1alpha1.ZoneUnit
			if err := k8sClient.Get(ctx, key, &unit); err != nil {
				t.Fatalf("ZoneUnit was not found: %v", err)
			}
			if unit.Status.Zone == nil {
				t.Fatal("status.zone was nil")
			}
			if test.message != "" {
				assertCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ExternalResourceMismatch")
				condition := assertCondition(t, unit.Status.Zone.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ExternalResourceMismatch")
				if condition.Message != test.message {
					t.Fatalf("Programmed message = %q, want %q", condition.Message, test.message)
				}
				if len(provider.tags) != 0 {
					t.Fatalf("mismatched zone was tagged: %#v", provider.tags)
				}
				return
			}
			assertCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionTrue, "Programmed")
			if got.Status.Provider == nil {
				t.Fatal("status.provider was nil")
			}
			assertRoute53RawEqual(t, got.Status.Provider.Data, map[string]any{"hostedZoneID": "ZADOPT", "zoneType": string(test.zoneType)})
			state := mustZoneUnitStatusData(t, ctx, k8sClient, key.Namespace, key.Name)
			if state.HostedZoneID != "ZADOPT" || state.ZoneType != test.zoneType {
				t.Fatalf("provider state = %#v, want ZADOPT and %s", state, test.zoneType)
			}
			if !slices.Equal(got.Status.NameServers, hostedZone.NameServers) {
				t.Fatalf("nameServers = %#v, want %#v", got.Status.NameServers, hostedZone.NameServers)
			}
			if test.private && slices.Contains(unit.Finalizers, ZoneFinalizer) {
				t.Fatalf("private ZoneUnit has hosted zone deletion finalizer: %v", unit.Finalizers)
			}
		})
	}
}

func TestZoneReconcilerDeniesPrivateZoneWithoutAdoption(t *testing.T) {
	for _, parameters := range []string{
		`{"zoneType":"Private"}`,
		`{"zoneType":"Private","zoneCreationPolicy":"Deny"}`,
		`{"zoneType":"Private","zoneCreationPolicy":"Create"}`,
	} {
		t.Run(parameters, func(t *testing.T) {
			ctx := context.Background()
			provider := newFakeProvider()
			objects := sameNamespaceClassObjects("app", nil)
			for _, object := range objects {
				if class, ok := object.(*dnsv1alpha1.ZoneClass); ok {
					class.Spec.Parameters = runtime.RawExtension{Raw: []byte(parameters)}
				}
			}
			k8sClient := fake.NewClientBuilder().
				WithScheme(testScheme(t)).
				WithObjects(objects...).
				WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
				Build()
			reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}
			key := client.ObjectKey{Namespace: "app", Name: "apps-example-com"}
			if _, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: key}); err != nil {
				t.Fatalf("Reconcile returned error: %v", err)
			}
			var got dnsv1alpha1.Zone
			if err := k8sClient.Get(ctx, key, &got); err != nil {
				t.Fatalf("Zone was not found: %v", err)
			}
			assertCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionFalse, "DeniedByPolicy")
			if len(provider.created) != 0 {
				t.Fatalf("created hosted zones = %d, want 0", len(provider.created))
			}
		})
	}
}

func TestZoneReconcilerRetainsPrivateHostedZoneOnDeletion(t *testing.T) {
	for _, parameters := range []string{
		`{"zoneType":"Private"}`,
		`{"zoneType":"Private","zoneDeletionPolicy":"Retain"}`,
		`{"zoneType":"Private","zoneDeletionPolicy":"Delete"}`,
		`{"zoneDeletionPolicy":"Delete"}`,
	} {
		t.Run(parameters, func(t *testing.T) {
			ctx := context.Background()
			provider := newFakeProvider()
			provider.zones["ZPRIVATE"] = HostedZone{ID: "ZPRIVATE", Name: "apps.example.com", Private: true}
			class := route53ZoneClass("app", "route53-private", nil)
			class.Spec.Parameters = runtime.RawExtension{Raw: []byte(parameters)}
			unit := route53ZoneUnit("app", "apps-example-com", "app", class.Name)
			unit.Spec.Zone.Adoption = runtime.RawExtension{Raw: []byte(`{"hostedZoneId":"ZPRIVATE"}`)}
			markRoute53ZoneUnitDeleting(unit, metav1.Now())
			k8sClient := fake.NewClientBuilder().
				WithScheme(testScheme(t)).
				WithObjects(route53Provider(), class, acceptedRoute53Identity("app", "route53-dev"), unit).
				WithStatusSubresource(&dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
				Build()
			recorder := &capturingRecorder{}
			reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider, Recorder: recorder}
			key := client.ObjectKeyFromObject(unit)
			if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
				t.Fatalf("Reconcile returned error: %v", err)
			}
			if len(provider.deleted) != 0 {
				t.Fatalf("DeleteHostedZone calls = %v, want none", provider.deleted)
			}
			if _, ok := provider.zones["ZPRIVATE"]; !ok {
				t.Fatal("private hosted zone was deleted")
			}
			if !recorder.has("Zone", "ExternalResourceRetained") {
				t.Fatalf("events = %#v, want ExternalResourceRetained", recorder.events)
			}
			var got dnsv1alpha1.ZoneUnit
			if err := k8sClient.Get(ctx, key, &got); err != nil {
				if apierrors.IsNotFound(err) {
					return
				}
				t.Fatalf("ZoneUnit get returned error: %v", err)
			}
			if slices.Contains(got.Finalizers, ZoneFinalizer) {
				t.Fatalf("ZoneUnit finalizers = %v, want hosted zone deletion finalizer removed", got.Finalizers)
			}
		})
	}
}

func TestZoneReconcilerWritesRecordSetIntoAdoptedPrivateZone(t *testing.T) {
	ctx := context.Background()
	provider := newFakeProvider()
	provider.zones["ZPRIVATE"] = HostedZone{ID: "ZPRIVATE", Name: "apps.example.com", Private: true}
	recordSet := route53ARecordSet("app", "www")
	objects := route53RecordSetObjects(t, recordSet)
	for _, object := range objects {
		switch typed := object.(type) {
		case *dnsv1alpha1.ZoneClass:
			typed.Spec.Parameters = runtime.RawExtension{Raw: []byte(`{"zoneType":"Private"}`)}
		case *dnsv1alpha1.Zone:
			typed.Spec.Adoption = runtime.RawExtension{Raw: []byte(`{"hostedZoneId":"ZPRIVATE"}`)}
			typed.Status = dnsv1alpha1.ZoneStatus{}
		}
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}
	request := ctrl.Request{NamespacedName: client.ObjectKey{Namespace: "app", Name: "apps-example-com"}}
	if _, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, request); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if len(provider.upserted) != 1 {
		t.Fatalf("upserted record sets = %d, want 1", len(provider.upserted))
	}
	upserted := provider.upserted[0]
	if upserted.HostedZoneID != "ZPRIVATE" || upserted.Name != "www.apps.example.com." || upserted.Type != dnsv1alpha1.RecordTypeA {
		t.Fatalf("upserted identity = %#v", upserted)
	}
	if upserted.TTL == nil || *upserted.TTL != 300 || !slices.Equal(upserted.Values, []string{"192.0.2.10"}) {
		t.Fatalf("upserted record = %#v, want TTL 300 and value 192.0.2.10", upserted)
	}
	for _, change := range provider.changes {
		change.Status = route53v1alpha1.Route53ChangeStatusInSync
	}
	if _, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, request); err != nil {
		t.Fatalf("second Reconcile returned error: %v", err)
	}
	var got dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(recordSet), &got); err != nil {
		t.Fatalf("RecordSet was not found: %v", err)
	}
	assertCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted")
	assertCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionTrue, "Programmed")
	if len(provider.created) != 0 || len(provider.deleted) != 0 {
		t.Fatalf("hosted zone mutations: created = %d, deleted = %v, want none", len(provider.created), provider.deleted)
	}
}
