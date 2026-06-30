package route53

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	zoneunitcontroller "github.com/appthrust/dns-api/internal/go/core/controllers/zoneunit"
	zoneclass "github.com/appthrust/dns-api/internal/go/providers/route53/controllers/zoneclass"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	"github.com/aws/smithy-go"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestZoneReconcilerCreatesHostedZone(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	zoneClass := route53ZoneClass("tenant-a-platform", "route53-public", map[string]string{
		"appthrust.io/project": "dns-api",
	})
	zoneClass.Spec.AllowedZones.Namespaces = dnsv1alpha1.NamespacePolicy{
		From: dnsv1alpha1.NamespacesFromSelector,
		Selector: &metav1.LabelSelector{
			MatchLabels: map[string]string{"appthrust.io/tenant": "tenant-a"},
		},
	}
	objects := []client.Object{
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "tenant-a-app",
				Labels: map[string]string{
					"appthrust.io/tenant": "tenant-a",
				},
			},
		},
		route53Provider(),
		zoneClass,
		acceptedRoute53Identity("tenant-a-platform", "route53-dev"),
		&dnsv1alpha1.Zone{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "tenant-a-app",
				Name:      "apps-example-com",
				UID:       types.UID("11111111-2222-3333-4444-555555555555"),
			},
			Spec: dnsv1alpha1.ZoneSpec{
				DomainName: "apps.example.com",
				Provider:   route53v1alpha1.ProviderRef,
				ZoneClassRef: dnsv1alpha1.ZoneClassReference{
					Namespace: ptr("tenant-a-platform"),
					Name:      "route53-public",
				},
			},
		},
		route53ZoneUnit("tenant-a-app", "apps-example-com", "tenant-a-platform", "route53-public"),
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{
		Client:         k8sClient,
		Provider:       provider,
		ControllerName: DefaultControllerName,
	}

	if _, err := (&zoneclass.ZoneClassReconciler{Client: k8sClient}).Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "tenant-a-platform", Name: "route53-public"}}); err != nil {
		t.Fatalf("ZoneClass reconcile returned error: %v", err)
	}
	result, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "tenant-a-app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("Reconcile did not request a requeue for a pending Route 53 change")
	}

	if len(provider.created) != 1 {
		t.Fatalf("created hosted zones = %d, want 1", len(provider.created))
	}
	created := provider.created[0]
	if created.domainName != "apps.example.com" {
		t.Fatalf("created domain = %q, want apps.example.com", created.domainName)
	}
	if created.callerReference != "dns-api:11111111-2222-3333-4444-555555555555" {
		t.Fatalf("callerReference = %q", created.callerReference)
	}
	if got := provider.tags["Z000001"]["appthrust.io/managed-by"]; got != "dns-api" {
		t.Fatalf("managed-by tag = %q, want dns-api", got)
	}
	if got := provider.tags["Z000001"]["appthrust.io/project"]; got != "dns-api" {
		t.Fatalf("custom tag = %q, want dns-api", got)
	}

	var zone dnsv1alpha1.Zone
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "tenant-a-app", Name: "apps-example-com"}, &zone); err != nil {
		t.Fatalf("Zone was not found: %v", err)
	}
	assertCondition(t, zone.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted")
	assertCondition(t, zone.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ProviderChangePending")
	data := mustZoneStatusData(t, &zone)
	if data.HostedZoneID != "Z000001" {
		t.Fatalf("hostedZoneID = %q, want Z000001", data.HostedZoneID)
	}
	unitData := mustZoneUnitStatusData(t, ctx, k8sClient, zone.Namespace, zone.Name)
	if unitData.PendingHostedZoneChange == nil || unitData.PendingHostedZoneChange.Status != route53v1alpha1.Route53ChangeStatusPending {
		t.Fatalf("pendingHostedZoneChange = %#v, want PENDING", unitData.PendingHostedZoneChange)
	}
	if len(zone.Status.NameServers) != 2 {
		t.Fatalf("nameServers = %#v, want two entries", zone.Status.NameServers)
	}
}

func reconcileRoute53AndProject(t *testing.T, ctx context.Context, k8sClient client.Client, reconciler *ZoneReconciler, request ctrl.Request) (ctrl.Result, error) {
	t.Helper()
	syncRoute53ZoneFixtureStatusToUnit(t, ctx, k8sClient, request.NamespacedName)
	result, err := reconciler.Reconcile(ctx, request)
	if err != nil {
		return result, err
	}
	var zone dnsv1alpha1.Zone
	if getErr := k8sClient.Get(ctx, request.NamespacedName, &zone); getErr == nil && !zone.DeletionTimestamp.IsZero() {
		return result, nil
	}
	if _, projectErr := (&zoneunitcontroller.ZoneUnitCompositionReconciler{Client: k8sClient}).Reconcile(ctx, request); projectErr != nil {
		return result, projectErr
	}
	return result, nil
}

func syncRoute53ZoneFixtureStatusToUnit(t *testing.T, ctx context.Context, k8sClient client.Client, key types.NamespacedName) {
	t.Helper()
	var zone dnsv1alpha1.Zone
	if err := k8sClient.Get(ctx, key, &zone); err != nil {
		return
	}
	var unit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, key, &unit); err != nil {
		return
	}
	if len(zone.Spec.Adoption.Raw) > 0 {
		unit.Spec.Zone.Adoption = zone.Spec.Adoption
		if err := k8sClient.Update(ctx, &unit); err != nil {
			t.Fatalf("sync ZoneUnit spec fixture: %v", err)
		}
	}
	if zone.DeletionTimestamp != nil {
		if err := k8sClient.Update(ctx, &unit); err != nil {
			t.Fatalf("sync ZoneUnit metadata fixture: %v", err)
		}
	}
	if zone.Status.Provider != nil {
		if unit.Status.Zone == nil {
			unit.Status.Zone = &dnsv1alpha1.ZoneUnitZoneStatus{}
		}
		unit.Status.Zone.Provider = &dnsv1alpha1.ProviderStatus{Data: zone.Status.Provider.Data}
		if len(zone.Status.Provider.State.Raw) > 0 {
			if unit.Status.Provider == nil {
				unit.Status.Provider = &dnsv1alpha1.ProviderStatus{}
			}
			unit.Status.Provider.State = zone.Status.Provider.State
		}
		if err := k8sClient.Status().Update(ctx, &unit); err != nil {
			t.Fatalf("sync ZoneUnit status fixture: %v", err)
		}
	}
}

func TestZoneReconcilerDeniesReservedRoute53Tags(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(sameNamespaceClassObjects("app", map[string]string{
			"appthrust.io/zone-name": "reserved",
		})...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	if _, err := (&zoneclass.ZoneClassReconciler{Client: k8sClient}).Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "route53-public"}}); err != nil {
		t.Fatalf("ZoneClass reconcile returned error: %v", err)
	}
	_, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var zone dnsv1alpha1.Zone
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &zone); err != nil {
		t.Fatalf("Zone was not found: %v", err)
	}
	assertCondition(t, zone.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionFalse, "ZoneClassNotAccepted")
	if len(provider.created) != 0 {
		t.Fatalf("created hosted zones = %d, want 0", len(provider.created))
	}
}

func TestZoneReconcilerStopsWhenIdentityIsNotReady(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	objects := sameNamespaceClassObjects("app", nil)
	for _, object := range objects {
		if identity, ok := object.(*route53v1alpha1.Route53Identity); ok {
			identity.Status.Conditions = []metav1.Condition{
				{
					Type:               string(dnsv1alpha1.ConditionAccepted),
					Status:             metav1.ConditionTrue,
					Reason:             "Accepted",
					ObservedGeneration: identity.Generation,
				},
				{
					Type:               "Ready",
					Status:             metav1.ConditionFalse,
					Reason:             "CredentialUnavailable",
					ObservedGeneration: identity.Generation,
				},
			}
		}
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	if _, err := (&zoneclass.ZoneClassReconciler{Client: k8sClient}).Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "route53-public"}}); err != nil {
		t.Fatalf("ZoneClass reconcile returned error: %v", err)
	}
	result, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("Reconcile did not request a requeue while Route53Identity is not Ready")
	}

	var zone dnsv1alpha1.Zone
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &zone); err != nil {
		t.Fatalf("Zone was not found: %v", err)
	}
	assertCondition(t, zone.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted")
	assertCondition(t, zone.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ProviderIdentityNotReady")
	if len(provider.created) != 0 {
		t.Fatalf("created hosted zones = %d, want 0", len(provider.created))
	}
}

func TestZoneReconcilerAdoptsExternalHostedZone(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["ZADOPT"] = HostedZone{
		ID:          "ZADOPT",
		Name:        "apps.example.com",
		NameServers: []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	objects := sameNamespaceClassObjects("app", nil)
	zone := objects[len(objects)-1].(*dnsv1alpha1.Zone)
	zone.Spec.Adoption = runtime.RawExtension{Raw: []byte(`{"hostedZoneId":"ZADOPT"}`)}
	for _, object := range objects {
		zoneClass, ok := object.(*dnsv1alpha1.ZoneClass)
		if !ok {
			continue
		}
		params, err := route53ZoneClassParameters(zoneClass)
		if err != nil {
			t.Fatalf("route53ZoneClassParameters returned error: %v", err)
		}
		params.ZoneCreationPolicy = route53v1alpha1.ZoneCreationPolicyDeny
		raw, err := json.Marshal(params)
		if err != nil {
			t.Fatalf("json.Marshal returned error: %v", err)
		}
		zoneClass.Spec.Parameters = runtime.RawExtension{Raw: raw}
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	if _, err := (&zoneclass.ZoneClassReconciler{Client: k8sClient}).Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "route53-public"}}); err != nil {
		t.Fatalf("ZoneClass reconcile returned error: %v", err)
	}
	_, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var got dnsv1alpha1.Zone
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &got); err != nil {
		t.Fatalf("Zone was not found: %v", err)
	}
	assertCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted")
	assertCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionTrue, "Programmed")
	data := mustZoneStatusData(t, &got)
	if data.HostedZoneID != "ZADOPT" {
		t.Fatalf("hostedZoneID = %q, want ZADOPT", data.HostedZoneID)
	}
	if len(provider.created) != 0 {
		t.Fatalf("created hosted zones = %d, want 0", len(provider.created))
	}
	if got := provider.tags["ZADOPT"]["appthrust.io/managed-by"]; got != "dns-api" {
		t.Fatalf("managed-by tag = %q, want dns-api", got)
	}
}

func TestZoneReconcilerAdoptsExternalHostedZoneFromComposedZoneUnit(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["ZADOPT"] = HostedZone{
		ID:          "ZADOPT",
		Name:        "apps.example.com",
		NameServers: []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	zoneClass := route53ZoneClass("platform", "route53-adoption", map[string]string{
		"appthrust.io/test-scope": "unit",
	})
	zoneClass.Spec.AllowedZones.Namespaces = dnsv1alpha1.NamespacePolicy{
		From: dnsv1alpha1.NamespacesFromSelector,
		Selector: &metav1.LabelSelector{
			MatchLabels: map[string]string{"appthrust.io/tenant": "unit"},
		},
	}
	objects := []client.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name:   "app",
			Labels: map[string]string{"appthrust.io/tenant": "unit"},
		}},
		route53Provider(),
		zoneClass,
		acceptedRoute53Identity("platform", "route53-dev"),
		&dnsv1alpha1.Zone{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "app",
				Name:       "apps-example-com",
				UID:        types.UID("11111111-2222-3333-4444-555555555555"),
				Generation: 1,
			},
			Spec: dnsv1alpha1.ZoneSpec{
				DomainName: "apps.example.com",
				Provider:   route53v1alpha1.ProviderRef,
				ZoneClassRef: dnsv1alpha1.ZoneClassReference{
					Namespace: ptr("platform"),
					Name:      "route53-adoption",
				},
				Adoption: runtime.RawExtension{Raw: []byte(`{"hostedZoneId":"ZADOPT"}`)},
			},
		},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	composer := &zoneunitcontroller.ZoneUnitCompositionReconciler{Client: k8sClient}
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	if _, err := composer.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}}); err != nil {
		t.Fatalf("ZoneUnit composition reconcile returned error: %v", err)
	}
	var unit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &unit); err != nil {
		t.Fatalf("ZoneUnit was not created: %v", err)
	}
	assertRoute53RawEqual(t, unit.Spec.Zone.Adoption, map[string]any{"hostedZoneId": "ZADOPT"})

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}}); err != nil {
		t.Fatalf("Route53 reconcile returned error: %v", err)
	}
	if _, err := composer.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}}); err != nil {
		t.Fatalf("ZoneUnit projection reconcile returned error: %v", err)
	}

	var got dnsv1alpha1.Zone
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &got); err != nil {
		t.Fatalf("Zone was not found: %v", err)
	}
	assertCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted")
	assertCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionTrue, "Programmed")
	data := mustZoneStatusData(t, &got)
	if data.HostedZoneID != "ZADOPT" {
		t.Fatalf("hostedZoneID = %q, want ZADOPT", data.HostedZoneID)
	}
	if got := provider.tags["ZADOPT"]["appthrust.io/test-scope"]; got != "unit" {
		t.Fatalf("test-scope tag = %q, want unit", got)
	}
}

func TestZoneReconcilerIgnoresCloudflareZoneUnit(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	unit := route53ZoneUnit("app", "apps-example-com", "platform", "cloudflare-public")
	unit.Spec.Provider = dnsv1alpha1.ProviderReference{Name: "cloudflare.dns.appthrust.io", Version: "v1alpha1"}
	unit.Spec.Zone.Adoption = runtime.RawExtension{Raw: []byte(`{"zoneID":"023e105f4ecef8ad9ca31a8372d0c353"}`)}
	unit.Status.Zone = &dnsv1alpha1.ZoneUnitZoneStatus{
		Conditions: []metav1.Condition{
			{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted", ObservedGeneration: 1},
		},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(unit).
		WithStatusSubresource(&dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var got dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &got); err != nil {
		t.Fatalf("ZoneUnit was not found: %v", err)
	}
	assertCondition(t, got.Status.Zone.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted")
}

func assertRoute53RawEqual(t *testing.T, got runtime.RawExtension, want map[string]any) {
	t.Helper()
	var gotObject map[string]any
	if err := json.Unmarshal(got.Raw, &gotObject); err != nil {
		t.Fatalf("raw JSON = %s, decode error: %v", string(got.Raw), err)
	}
	if !equality.Semantic.DeepEqual(gotObject, want) {
		t.Fatalf("raw JSON = %#v, want %#v", gotObject, want)
	}
}

func TestZoneReconcilerRejectsAdoptionRetargetFromManagedHostedZone(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	objects := sameNamespaceClassObjects("app", nil)
	zone := objects[len(objects)-1].(*dnsv1alpha1.Zone)
	zone.Spec.Adoption = runtime.RawExtension{Raw: []byte(`{"hostedZoneId":"ZNEW"}`)}
	setZoneStatusData(t, zone, route53v1alpha1.Route53ZoneStatusData{
		HostedZoneID: "ZOLD",
	})
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	if _, err := (&zoneclass.ZoneClassReconciler{Client: k8sClient}).Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "route53-public"}}); err != nil {
		t.Fatalf("ZoneClass reconcile returned error: %v", err)
	}
	if _, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var got dnsv1alpha1.Zone
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &got); err != nil {
		t.Fatalf("Zone was not found: %v", err)
	}
	assertCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionFalse, "ManagedResourceMismatch")
	if len(provider.created) != 0 {
		t.Fatalf("created hosted zones = %d, want 0", len(provider.created))
	}
	if len(provider.tags) != 0 {
		t.Fatalf("tagged hosted zones = %#v, want none", provider.tags)
	}
}

func TestZoneReconcilerRejectsSameNameHostedZone(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["ZEXTERNAL"] = HostedZone{
		ID:              "ZEXTERNAL",
		Name:            "apps.example.com",
		CallerReference: "external",
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(sameNamespaceClassObjects("app", nil)...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	if _, err := (&zoneclass.ZoneClassReconciler{Client: k8sClient}).Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "route53-public"}}); err != nil {
		t.Fatalf("ZoneClass reconcile returned error: %v", err)
	}
	_, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var zone dnsv1alpha1.Zone
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &zone); err != nil {
		t.Fatalf("Zone was not found: %v", err)
	}
	assertCondition(t, zone.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ProviderConflict")
	if len(provider.created) != 0 {
		t.Fatalf("created hosted zones = %d, want 0", len(provider.created))
	}
}

func TestZoneReconcilerRecreatesHostedZoneWhenStatusIDDisappears(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	objects := sameNamespaceClassObjects("app", nil)
	for _, object := range objects {
		zone, ok := object.(*dnsv1alpha1.Zone)
		if !ok {
			continue
		}
		setZoneStatusData(t, zone, route53v1alpha1.Route53ZoneStatusData{
			HostedZoneID:    "ZMISSING",
			CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		})
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	result, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("Reconcile did not request a requeue for a pending Route 53 change")
	}
	if len(provider.created) != 1 {
		t.Fatalf("created hosted zones = %d, want 1", len(provider.created))
	}

	var zone dnsv1alpha1.Zone
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &zone); err != nil {
		t.Fatalf("Zone was not found: %v", err)
	}
	data := mustZoneStatusData(t, &zone)
	if data.HostedZoneID != "Z000001" {
		t.Fatalf("hostedZoneID = %q, want recreated Z000001", data.HostedZoneID)
	}
	assertCondition(t, zone.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ProviderChangePending")
}

func TestZoneReconcilerChecksPendingHostedZoneChangeBeforeRecreate(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.changes["CPENDING"] = &route53v1alpha1.Route53Change{
		ID:     "CPENDING",
		Status: route53v1alpha1.Route53ChangeStatusPending,
	}
	objects := sameNamespaceClassObjects("app", nil)
	for _, object := range objects {
		zone, ok := object.(*dnsv1alpha1.Zone)
		if !ok {
			continue
		}
		setZoneStatusData(t, zone, route53v1alpha1.Route53ZoneStatusData{
			HostedZoneID: "ZMISSING",
			PendingHostedZoneChange: &route53v1alpha1.Route53PendingChange{
				ID:        "CPENDING",
				Status:    route53v1alpha1.Route53ChangeStatusPending,
				Operation: "CREATE",
			},
		})
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	result, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("Reconcile did not request a requeue for pending Route 53 change")
	}
	if len(provider.created) != 0 {
		t.Fatalf("created hosted zones = %d, want 0 while change is pending", len(provider.created))
	}

	provider.changes["CPENDING"].Status = route53v1alpha1.Route53ChangeStatusInSync
	result, err = reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("second Reconcile returned error: %v", err)
	}
	if !result.Requeue {
		t.Fatalf("second Reconcile did not request immediate requeue after INSYNC")
	}
	if len(provider.created) != 0 {
		t.Fatalf("created hosted zones = %d, want 0 before post-INSYNC reobserve", len(provider.created))
	}

	result, err = reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("third Reconcile returned error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("third Reconcile did not request a requeue for recreated Route 53 change")
	}
	if len(provider.created) != 1 {
		t.Fatalf("created hosted zones = %d, want 1 after reobserve", len(provider.created))
	}
}

func TestZoneReconcilerRejectsStatusHostedZoneMismatch(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["ZMISMATCH"] = HostedZone{
		ID:              "ZMISMATCH",
		Name:            "other.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
	}
	objects := sameNamespaceClassObjects("app", nil)
	for _, object := range objects {
		zone, ok := object.(*dnsv1alpha1.Zone)
		if !ok {
			continue
		}
		setZoneStatusData(t, zone, route53v1alpha1.Route53ZoneStatusData{
			HostedZoneID: "ZMISMATCH",
		})
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	if _, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if len(provider.created) != 0 {
		t.Fatalf("created hosted zones = %d, want 0", len(provider.created))
	}

	var zone dnsv1alpha1.Zone
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &zone); err != nil {
		t.Fatalf("Zone was not found: %v", err)
	}
	assertCondition(t, zone.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ExternalResourceMismatch")
}

func TestZoneReconcilerWaitsForHostedZoneDeletionChange(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["ZDELETE"] = HostedZone{
		ID:              "ZDELETE",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
	}

	objects := sameNamespaceClassObjects("app", nil)
	deletionTime := metav1.NewTime(time.Now())
	for _, object := range objects {
		switch typed := object.(type) {
		case *dnsv1alpha1.ZoneClass:
			params, err := route53ZoneClassParameters(typed)
			if err != nil {
				t.Fatalf("route53ZoneClassParameters returned error: %v", err)
			}
			params.ZoneDeletionPolicy = route53v1alpha1.ZoneDeletionPolicyDelete
			raw, err := json.Marshal(params)
			if err != nil {
				t.Fatalf("json.Marshal returned error: %v", err)
			}
			typed.Spec.Parameters = runtime.RawExtension{Raw: raw}
		case *dnsv1alpha1.Zone:
			typed.DeletionTimestamp = &deletionTime
			typed.Finalizers = []string{ZoneFinalizer}
			setZoneStatusData(t, typed, route53v1alpha1.Route53ZoneStatusData{
				HostedZoneID: "ZDELETE",
			})
		case *dnsv1alpha1.ZoneUnit:
			markRoute53ZoneUnitDeleting(typed, deletionTime)
		}
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	result, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("Reconcile did not request a requeue for a pending Route 53 deletion")
	}

	var zone dnsv1alpha1.Zone
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &zone); err != nil {
		t.Fatalf("Zone was not found: %v", err)
	}
	var unit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &unit); err != nil {
		t.Fatalf("ZoneUnit was not found: %v", err)
	}
	if !slices.Contains(unit.Finalizers, ZoneFinalizer) {
		t.Fatalf("ZoneUnit finalizers = %#v, want %q to remain", unit.Finalizers, ZoneFinalizer)
	}
	if unit.Status.Zone == nil {
		t.Fatalf("status.zone was nil")
	}
	assertCondition(t, unit.Status.Zone.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ProviderChangePending")
	unitData := mustZoneUnitStatusData(t, ctx, k8sClient, zone.Namespace, zone.Name)
	if unitData.PendingHostedZoneChange == nil || unitData.PendingHostedZoneChange.Status != route53v1alpha1.Route53ChangeStatusPending {
		t.Fatalf("pendingHostedZoneChange = %#v, want PENDING delete change", unitData.PendingHostedZoneChange)
	}

	provider.changes["CDELETE"].Status = route53v1alpha1.Route53ChangeStatusInSync
	if _, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}}); err != nil {
		t.Fatalf("second Reconcile returned error: %v", err)
	}
	if _, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}}); err != nil {
		t.Fatalf("third Reconcile returned error: %v", err)
	}
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &unit); err != nil {
		if apierrors.IsNotFound(err) {
			return
		}
		t.Fatalf("ZoneUnit get after second reconcile returned error: %v", err)
	}
	if slices.Contains(unit.Finalizers, ZoneFinalizer) {
		t.Fatalf("ZoneUnit finalizers = %#v, want %q removed after INSYNC", unit.Finalizers, ZoneFinalizer)
	}
}

func TestZoneReconcilerRetainsHostedZoneWhenDeletionPolicyIsRetain(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["ZRETAIN"] = HostedZone{
		ID:              "ZRETAIN",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
	}

	objects := sameNamespaceClassObjects("app", nil)
	deletionTime := metav1.NewTime(time.Now())
	for _, object := range objects {
		switch typed := object.(type) {
		case *dnsv1alpha1.Zone:
			typed.DeletionTimestamp = &deletionTime
			typed.Finalizers = []string{ZoneFinalizer}
			setZoneStatusData(t, typed, route53v1alpha1.Route53ZoneStatusData{
				HostedZoneID: "ZRETAIN",
			})
		case *dnsv1alpha1.ZoneUnit:
			markRoute53ZoneUnitDeleting(typed, deletionTime)
		}
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	if _, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if _, ok := provider.zones["ZRETAIN"]; !ok {
		t.Fatalf("hosted zone was deleted despite Retain policy")
	}

	var unit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &unit); err != nil {
		if apierrors.IsNotFound(err) {
			return
		}
		t.Fatalf("ZoneUnit was not found: %v", err)
	}
	if slices.Contains(unit.Finalizers, ZoneFinalizer) {
		t.Fatalf("ZoneUnit finalizers = %#v, want %q removed for Retain policy", unit.Finalizers, ZoneFinalizer)
	}
}

func TestZoneReconcilerDoesNotDeleteHostedZoneWithoutDeletionTarget(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["ZCALLER"] = HostedZone{
		ID:              "ZCALLER",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
	}

	objects := sameNamespaceClassObjects("app", nil)
	deletionTime := metav1.NewTime(time.Now())
	for _, object := range objects {
		switch typed := object.(type) {
		case *dnsv1alpha1.ZoneClass:
			params, err := route53ZoneClassParameters(typed)
			if err != nil {
				t.Fatalf("route53ZoneClassParameters returned error: %v", err)
			}
			params.ZoneDeletionPolicy = route53v1alpha1.ZoneDeletionPolicyDelete
			raw, err := json.Marshal(params)
			if err != nil {
				t.Fatalf("json.Marshal returned error: %v", err)
			}
			typed.Spec.Parameters = runtime.RawExtension{Raw: raw}
		case *dnsv1alpha1.Zone:
			typed.DeletionTimestamp = &deletionTime
			typed.Finalizers = []string{ZoneFinalizer}
			typed.Status.Provider = nil
		case *dnsv1alpha1.ZoneUnit:
			markRoute53ZoneUnitDeleting(typed, deletionTime)
		}
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	if _, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if _, ok := provider.zones["ZCALLER"]; !ok {
		t.Fatalf("hosted zone was deleted from CallerReference without status or adoption target")
	}

	var unit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &unit); err != nil {
		if apierrors.IsNotFound(err) {
			return
		}
		t.Fatalf("ZoneUnit was not found: %v", err)
	}
	if slices.Contains(unit.Finalizers, ZoneFinalizer) {
		t.Fatalf("ZoneUnit finalizers = %#v, want %q removed without deletion target", unit.Finalizers, ZoneFinalizer)
	}
}

func TestZoneReconcilerCreatesARecordFromZoneReconcile(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	recordSet := route53ARecordSet("app", "www")
	objects := route53RecordSetObjects(t, recordSet)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	result, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("Reconcile did not request a requeue for a pending Route 53 record change")
	}
	if len(provider.upserted) != 1 {
		t.Fatalf("upserted record sets = %d, want 1", len(provider.upserted))
	}
	upserted := provider.upserted[0]
	if upserted.HostedZoneID != "Z000001" || upserted.Name != "www.apps.example.com." || upserted.Type != dnsv1alpha1.RecordTypeA {
		t.Fatalf("upserted identity = %#v", upserted)
	}
	if upserted.TTL == nil || *upserted.TTL != 300 {
		t.Fatalf("upserted ttl = %#v, want 300", upserted.TTL)
	}
	if !slices.Equal(upserted.Values, []string{"192.0.2.10"}) {
		t.Fatalf("upserted values = %#v", upserted.Values)
	}

	var gotRecordSet dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(recordSet), &gotRecordSet); err != nil {
		t.Fatalf("RecordSet was not found: %v", err)
	}
	assertCondition(t, gotRecordSet.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted")
	assertCondition(t, gotRecordSet.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ProviderChangePending")
	if !slices.Contains(gotRecordSet.Finalizers, "dns.appthrust.io/recordset-finalizer") {
		t.Fatalf("finalizers = %#v, want core RecordSet finalizer", gotRecordSet.Finalizers)
	}
	statusData := mustZoneUnitRecordSetStatusState(t, ctx, k8sClient, "app", "apps-example-com", "app", "www")
	if statusData.HostedZoneID != "Z000001" || statusData.RecordName != "www.apps.example.com." || statusData.RecordType != string(dnsv1alpha1.RecordTypeA) {
		t.Fatalf("ZoneUnit recordSet provider state = %#v, want Route 53 record identity", statusData)
	}
	if gotRecordSet.Status.Provider != nil && len(gotRecordSet.Status.Provider.Data.Raw) > 0 {
		t.Fatalf("status.provider.data = %s, want Route 53 record identity only in state", string(gotRecordSet.Status.Provider.Data.Raw))
	}

	var gotZone dnsv1alpha1.Zone
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &gotZone); err != nil {
		t.Fatalf("Zone was not found: %v", err)
	}
	unitData := mustZoneUnitStatusData(t, ctx, k8sClient, gotZone.Namespace, gotZone.Name)
	if unitData.PendingRecordSetChange == nil || unitData.PendingRecordSetChange.Operation != "UPSERT_BATCH" {
		t.Fatalf("pendingRecordSetChange = %#v, want UPSERT batch", unitData.PendingRecordSetChange)
	}
}

func TestZoneReconcilerUpsertsRecordSetSelectedByZoneUnitAcrossNamespaces(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	recordSet := route53ARecordSet("record-ns", "www")
	objects := route53RecordSetObjects(t, recordSet)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	_, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if len(provider.upserted) != 1 {
		t.Fatalf("upserted record sets = %d, want 1", len(provider.upserted))
	}

	var unit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &unit); err != nil {
		t.Fatalf("ZoneUnit was not found: %v", err)
	}
	index := slices.IndexFunc(unit.Status.RecordSets, func(status dnsv1alpha1.ZoneUnitRecordSetStatus) bool {
		return status.RecordSetNamespace == recordSet.Namespace && status.RecordSetName == recordSet.Name
	})
	if index < 0 {
		t.Fatalf("ZoneUnit status.recordSets has no entry for %s/%s: %#v", recordSet.Namespace, recordSet.Name, unit.Status.RecordSets)
	}
	assertCondition(t, unit.Status.RecordSets[index].Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted")

}

func TestZoneReconcilerAcceptsRecordSetAdoptionFromDerivedIdentity(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	recordSet := route53ARecordSet("app", "www")
	recordSet.Spec.Adoption = runtime.RawExtension{Raw: []byte(`{"enabled":true}`)}
	setRecordSetStatusData(t, recordSet, route53v1alpha1.Route53RecordSetStatusData{
		HostedZoneID: "Z000001",
		RecordName:   "www.apps.example.com.",
		RecordType:   string(dnsv1alpha1.RecordTypeA),
	})
	objects := route53RecordSetObjects(t, recordSet)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	_, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var gotRecordSet dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(recordSet), &gotRecordSet); err != nil {
		t.Fatalf("RecordSet was not found: %v", err)
	}
	assertCondition(t, gotRecordSet.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted")
	if len(provider.deletedRRs) != 0 {
		t.Fatalf("deleted record sets = %d, want 0", len(provider.deletedRRs))
	}
}

func TestZoneReconcilerClassifiesNotAllowedByZoneMessage(t *testing.T) {
	ctx := context.Background()
	zone := &dnsv1alpha1.Zone{
		ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "apps-example-com"},
		Spec: dnsv1alpha1.ZoneSpec{
			Provider: route53v1alpha1.ProviderRef,
			AllowedRecordSets: []dnsv1alpha1.AllowedRecordSet{
				{
					Namespaces: dnsv1alpha1.AllowedRecordSetNamespaces{
						Selector: metav1.LabelSelector{MatchLabels: map[string]string{"access": "dns"}},
					},
					Records: []dnsv1alpha1.AllowedRecord{
						{
							Name:  dnsv1alpha1.RecordNamePolicy{Pattern: "www"},
							Types: []dnsv1alpha1.RecordType{dnsv1alpha1.RecordTypeA},
						},
					},
				},
			},
		},
	}

	tests := []struct {
		name            string
		namespaceLabels map[string]string
		recordName      string
		recordType      dnsv1alpha1.RecordType
		want            string
	}{
		{
			name:            "namespace",
			namespaceLabels: nil,
			recordName:      "www",
			recordType:      dnsv1alpha1.RecordTypeA,
			want:            "RecordSet namespace is not allowed by the referenced Zone.",
		},
		{
			name:            "record-name",
			namespaceLabels: map[string]string{"access": "dns"},
			recordName:      "api",
			recordType:      dnsv1alpha1.RecordTypeA,
			want:            "RecordSet name is not allowed by the referenced Zone.",
		},
		{
			name:            "record-type",
			namespaceLabels: map[string]string{"access": "dns"},
			recordName:      "www",
			recordType:      dnsv1alpha1.RecordTypeAAAA,
			want:            "RecordSet type is not allowed for this RecordSet name by the referenced Zone.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recordSet := route53ARecordSet("record-ns", "www")
			recordSet.Spec.Name = tt.recordName
			recordSet.Spec.Type = tt.recordType
			reconciler := &ZoneReconciler{
				Client: fake.NewClientBuilder().
					WithScheme(testScheme(t)).
					WithObjects(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "record-ns", Labels: tt.namespaceLabels}}).
					Build(),
			}

			allowed, message := reconciler.recordSetAllowedByZoneMessage(ctx, recordSet, zone)
			if allowed {
				t.Fatalf("recordSetAllowedByZoneMessage returned allowed, want denied")
			}
			if message != tt.want {
				t.Fatalf("message = %q, want %q", message, tt.want)
			}
		})
	}
}

func TestValidateRecordSetTXTValuesRejectsNonPrintableASCII(t *testing.T) {
	tests := map[string]string{
		"newline":   "line1\nline2",
		"tab":       "one\ttwo",
		"control":   "prefix\x7fsuffix",
		"non-ascii": "exämple",
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			err := validateRecordSetTXTValues([]string{value})
			if err == nil || !strings.Contains(err.Error(), "printable ASCII") {
				t.Fatalf("validateRecordSetTXTValues error = %v, want printable ASCII failure", err)
			}
		})
	}
}

func TestZoneReconcilerCreatesAAAARecordFromZoneReconcile(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	recordSet := route53AAAARecordSet("app", "www-v6")
	objects := route53RecordSetObjects(t, recordSet)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	result, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("Reconcile did not request a requeue for a pending Route 53 record change")
	}
	if len(provider.upserted) != 1 {
		t.Fatalf("upserted record sets = %d, want 1", len(provider.upserted))
	}
	upserted := provider.upserted[0]
	if upserted.HostedZoneID != "Z000001" || upserted.Name != "www.apps.example.com." || upserted.Type != dnsv1alpha1.RecordTypeAAAA {
		t.Fatalf("upserted identity = %#v", upserted)
	}
	if upserted.TTL == nil || *upserted.TTL != 300 {
		t.Fatalf("upserted ttl = %#v, want 300", upserted.TTL)
	}
	if !slices.Equal(upserted.Values, []string{"2001:db8::10"}) {
		t.Fatalf("upserted values = %#v", upserted.Values)
	}
}

func TestZoneReconcilerCreatesTXTRecordFromZoneReconcile(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	recordSet := route53TXTRecordSet("app", "acme-challenge")
	objects := route53RecordSetObjects(t, recordSet)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	result, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("Reconcile did not request a requeue for a pending Route 53 record change")
	}
	if len(provider.upserted) != 1 {
		t.Fatalf("upserted record sets = %d, want 1", len(provider.upserted))
	}
	upserted := provider.upserted[0]
	if upserted.HostedZoneID != "Z000001" || upserted.Name != "_acme-challenge.apps.example.com." || upserted.Type != dnsv1alpha1.RecordTypeTXT {
		t.Fatalf("upserted identity = %#v", upserted)
	}
	if upserted.TTL == nil || *upserted.TTL != 300 {
		t.Fatalf("upserted ttl = %#v, want 300", upserted.TTL)
	}
	want := []string{`"challenge-token"`, `"v=spf1 include:_spf.example.net ~all"`}
	if !slices.Equal(upserted.Values, want) {
		t.Fatalf("upserted values = %#v, want %#v", upserted.Values, want)
	}
}

func TestZoneReconcilerDoesNotPropagateInvalidBatchErrorToUnrelatedRecordSet(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	provider.changeRecordSetsErr = func(_ string, changes []RecordSetChange) error {
		if len(changes) > 1 || changes[0].RecordSet.Name == "alias.apps.example.com." {
			return &smithy.GenericAPIError{
				Code:    "InvalidChangeBatch",
				Message: "Tried to create an alias that targets https://not-a-dns-name.example.com.",
			}
		}
		return nil
	}
	alias := route53AliasARecordSet("app", "bad-alias")
	alias.Spec.Name = "alias"
	txt := route53TXTRecordSet("app", "acme-challenge")
	unit := route53ZoneUnit("app", "apps-example-com", "app", "route53-public")
	unit.Spec.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetSpec{
		route53ZoneUnitRecordSetSpec(alias),
		route53ZoneUnitRecordSetSpec(txt),
	}
	objects := []client.Object{
		route53Provider(),
		route53ZoneClass("app", "route53-public", nil),
		acceptedRoute53Identity("app", "route53-dev"),
		route53ReadyZone(t, "app", "apps-example-com"),
		unit,
		alias,
		txt,
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	result, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("Reconcile did not request a requeue for the retried Route 53 record change")
	}
	if len(provider.upserted) != 1 {
		t.Fatalf("upserted record sets = %d, want 1", len(provider.upserted))
	}
	if provider.upserted[0].Type != dnsv1alpha1.RecordTypeTXT {
		t.Fatalf("upserted record set = %#v, want TXT only", provider.upserted[0])
	}

	var gotUnit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &gotUnit); err != nil {
		t.Fatalf("ZoneUnit was not found: %v", err)
	}
	aliasIndex := slices.IndexFunc(gotUnit.Status.RecordSets, func(status dnsv1alpha1.ZoneUnitRecordSetStatus) bool {
		return status.RecordSetNamespace == "app" && status.RecordSetName == "bad-alias"
	})
	if aliasIndex < 0 {
		t.Fatalf("ZoneUnit status has no bad-alias entry: %#v", gotUnit.Status.RecordSets)
	}
	aliasProgrammed := assertCondition(t, gotUnit.Status.RecordSets[aliasIndex].Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ProviderInvalidRequest")
	if !strings.Contains(aliasProgrammed.Message, "https://not-a-dns-name") {
		t.Fatalf("alias Programmed message = %q, want provider detail", aliasProgrammed.Message)
	}
	txtIndex := slices.IndexFunc(gotUnit.Status.RecordSets, func(status dnsv1alpha1.ZoneUnitRecordSetStatus) bool {
		return status.RecordSetNamespace == "app" && status.RecordSetName == "acme-challenge"
	})
	if txtIndex < 0 {
		t.Fatalf("ZoneUnit status has no acme-challenge entry: %#v", gotUnit.Status.RecordSets)
	}
	txtProgrammed := assertCondition(t, gotUnit.Status.RecordSets[txtIndex].Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ProviderChangePending")
	if strings.Contains(txtProgrammed.Message, "https://not-a-dns-name") {
		t.Fatalf("TXT Programmed message leaked alias error: %q", txtProgrammed.Message)
	}
}

func TestZoneReconcilerCreatesCNAMERecordFromZoneReconcile(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	recordSet := route53CNAMERecordSet("app", "www-cname")
	objects := route53RecordSetObjects(t, recordSet)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	result, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("Reconcile did not request a requeue for a pending Route 53 record change")
	}
	if len(provider.upserted) != 1 {
		t.Fatalf("upserted record sets = %d, want 1", len(provider.upserted))
	}
	upserted := provider.upserted[0]
	if upserted.HostedZoneID != "Z000001" || upserted.Name != "www.apps.example.com." || upserted.Type != dnsv1alpha1.RecordTypeCNAME {
		t.Fatalf("upserted identity = %#v", upserted)
	}
	if upserted.TTL == nil || *upserted.TTL != 300 {
		t.Fatalf("upserted ttl = %#v, want 300", upserted.TTL)
	}
	if !slices.Equal(upserted.Values, []string{"target.example.net."}) {
		t.Fatalf("upserted values = %#v", upserted.Values)
	}
}

func TestZoneReconcilerMatchesTXTRecordByParsedRoute53Chunks(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	ttl := int64(300)
	provider.records[recordKey("Z000001", "_acme-challenge.apps.example.com.", dnsv1alpha1.RecordTypeTXT)] = RecordSetResource{
		HostedZoneID: "Z000001",
		Name:         "_acme-challenge.apps.example.com.",
		Type:         dnsv1alpha1.RecordTypeTXT,
		TTL:          &ttl,
		Values:       []string{`"v=spf1 " "include:_spf.example.net ~all"`, `"challenge-token"`},
	}
	recordSet := route53TXTRecordSet("app", "acme-challenge")
	setRecordSetStatusData(t, recordSet, route53v1alpha1.Route53RecordSetStatusData{
		HostedZoneID: "Z000001",
		RecordName:   "_acme-challenge.apps.example.com.",
		RecordType:   string(dnsv1alpha1.RecordTypeTXT),
	})
	objects := route53RecordSetObjects(t, recordSet)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	result, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.Requeue {
		t.Fatalf("Reconcile result = %#v, want no immediate requeue", result)
	}
	if len(provider.upserted) != 0 {
		t.Fatalf("upserted record sets = %d, want 0", len(provider.upserted))
	}
}

func TestZoneReconcilerMatchesCNAMERecordByCanonicalRoute53Name(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	ttl := int64(300)
	provider.records[recordKey("Z000001", "www.apps.example.com.", dnsv1alpha1.RecordTypeCNAME)] = RecordSetResource{
		HostedZoneID: "Z000001",
		Name:         "www.apps.example.com.",
		Type:         dnsv1alpha1.RecordTypeCNAME,
		TTL:          &ttl,
		Values:       []string{"target.example.net."},
	}
	recordSet := route53CNAMERecordSet("app", "www-cname")
	setRecordSetStatusData(t, recordSet, route53v1alpha1.Route53RecordSetStatusData{
		HostedZoneID: "Z000001",
		RecordName:   "www.apps.example.com.",
		RecordType:   string(dnsv1alpha1.RecordTypeCNAME),
	})
	objects := route53RecordSetObjects(t, recordSet)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	result, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.Requeue {
		t.Fatalf("Reconcile result = %#v, want no immediate requeue", result)
	}
	if len(provider.upserted) != 0 {
		t.Fatalf("upserted record sets = %d, want 0", len(provider.upserted))
	}
}

func TestZoneReconcilerCreatesMXRecordFromZoneReconcile(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	recordSet := route53MXRecordSet("app", "mail")
	objects := route53RecordSetObjects(t, recordSet)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	result, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("Reconcile did not request a requeue for a pending Route 53 record change")
	}
	if len(provider.upserted) != 1 {
		t.Fatalf("upserted record sets = %d, want 1", len(provider.upserted))
	}
	upserted := provider.upserted[0]
	if upserted.HostedZoneID != "Z000001" || upserted.Name != "apps.example.com." || upserted.Type != dnsv1alpha1.RecordTypeMX {
		t.Fatalf("upserted identity = %#v", upserted)
	}
	if upserted.TTL == nil || *upserted.TTL != 300 {
		t.Fatalf("upserted ttl = %#v, want 300", upserted.TTL)
	}
	want := []string{"10 mail1.example.net.", "20 mail2.example.net."}
	if !slices.Equal(upserted.Values, want) {
		t.Fatalf("upserted values = %#v, want %#v", upserted.Values, want)
	}
}

func TestZoneReconcilerMatchesMXRecordByParsedRoute53Values(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	ttl := int64(300)
	provider.records[recordKey("Z000001", "apps.example.com.", dnsv1alpha1.RecordTypeMX)] = RecordSetResource{
		HostedZoneID: "Z000001",
		Name:         "apps.example.com.",
		Type:         dnsv1alpha1.RecordTypeMX,
		TTL:          &ttl,
		Values:       []string{"20 mail2.example.net.", "10 mail1.example.net."},
	}
	recordSet := route53MXRecordSet("app", "mail")
	setRecordSetStatusData(t, recordSet, route53v1alpha1.Route53RecordSetStatusData{
		HostedZoneID: "Z000001",
		RecordName:   "apps.example.com.",
		RecordType:   string(dnsv1alpha1.RecordTypeMX),
	})
	objects := route53RecordSetObjects(t, recordSet)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	result, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.Requeue {
		t.Fatalf("Reconcile result = %#v, want no immediate requeue", result)
	}
	if len(provider.upserted) != 0 {
		t.Fatalf("upserted record sets = %d, want 0", len(provider.upserted))
	}
}

func TestZoneReconcilerCreatesNullMXRecordFromZoneReconcile(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	recordSet := route53MXRecordSet("app", "no-mail")
	recordSet.Spec.MX.Records = []dnsv1alpha1.MXRecord{{Preference: 0, Exchange: "."}}
	objects := route53RecordSetObjects(t, recordSet)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	result, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("Reconcile did not request a requeue for a pending Route 53 record change")
	}
	if len(provider.upserted) != 1 {
		t.Fatalf("upserted record sets = %d, want 1", len(provider.upserted))
	}
	if !slices.Equal(provider.upserted[0].Values, []string{"0 ."}) {
		t.Fatalf("upserted values = %#v, want Null MX", provider.upserted[0].Values)
	}
}

func TestZoneReconcilerCreatesCAARecordFromZoneReconcile(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	recordSet := route53CAARecordSet("app", "ca-policy")
	objects := route53RecordSetObjects(t, recordSet)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	result, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("Reconcile did not request a requeue for a pending Route 53 record change")
	}
	if len(provider.upserted) != 1 {
		t.Fatalf("upserted record sets = %d, want 1", len(provider.upserted))
	}
	upserted := provider.upserted[0]
	if upserted.HostedZoneID != "Z000001" || upserted.Name != "apps.example.com." || upserted.Type != dnsv1alpha1.RecordTypeCAA {
		t.Fatalf("upserted identity = %#v", upserted)
	}
	if upserted.TTL == nil || *upserted.TTL != 300 {
		t.Fatalf("upserted ttl = %#v, want 300", upserted.TTL)
	}
	want := []string{`0 iodef "mailto:security@example.com"`, `0 issue "letsencrypt.org"`}
	if !slices.Equal(upserted.Values, want) {
		t.Fatalf("upserted values = %#v, want %#v", upserted.Values, want)
	}
}

func TestZoneReconcilerMatchesCAARecordByParsedRoute53Values(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	ttl := int64(300)
	provider.records[recordKey("Z000001", "apps.example.com.", dnsv1alpha1.RecordTypeCAA)] = RecordSetResource{
		HostedZoneID: "Z000001",
		Name:         "apps.example.com.",
		Type:         dnsv1alpha1.RecordTypeCAA,
		TTL:          &ttl,
		Values:       []string{`0 iodef "mailto:security@example.com"`, `0 issue "letsencrypt.org"`},
	}
	recordSet := route53CAARecordSet("app", "ca-policy")
	setRecordSetStatusData(t, recordSet, route53v1alpha1.Route53RecordSetStatusData{
		HostedZoneID: "Z000001",
		RecordName:   "apps.example.com.",
		RecordType:   string(dnsv1alpha1.RecordTypeCAA),
	})
	objects := route53RecordSetObjects(t, recordSet)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	result, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.Requeue {
		t.Fatalf("Reconcile result = %#v, want no immediate requeue", result)
	}
	if len(provider.upserted) != 0 {
		t.Fatalf("upserted record sets = %d, want 0", len(provider.upserted))
	}
}

func TestZoneReconcilerCreatesDelegatedNSRecordFromZoneReconcile(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	recordSet := route53NSRecordSet("app", "delegated")
	objects := route53RecordSetObjects(t, recordSet)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	result, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("Reconcile did not request a requeue for a pending Route 53 record change")
	}
	if len(provider.upserted) != 1 {
		t.Fatalf("upserted record sets = %d, want 1", len(provider.upserted))
	}
	upserted := provider.upserted[0]
	if upserted.HostedZoneID != "Z000001" || upserted.Name != "delegated.apps.example.com." || upserted.Type != dnsv1alpha1.RecordTypeNS {
		t.Fatalf("upserted identity = %#v", upserted)
	}
	if upserted.TTL == nil || *upserted.TTL != 300 {
		t.Fatalf("upserted ttl = %#v, want 300", upserted.TTL)
	}
	want := []string{"ns-111.example-dns.net.", "ns-222.example-dns.net."}
	if !slices.Equal(upserted.Values, want) {
		t.Fatalf("upserted values = %#v, want %#v", upserted.Values, want)
	}
}

func TestZoneReconcilerMatchesDelegatedNSRecordByParsedRoute53Values(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	ttl := int64(300)
	provider.records[recordKey("Z000001", "delegated.apps.example.com.", dnsv1alpha1.RecordTypeNS)] = RecordSetResource{
		HostedZoneID: "Z000001",
		Name:         "delegated.apps.example.com.",
		Type:         dnsv1alpha1.RecordTypeNS,
		TTL:          &ttl,
		Values:       []string{"ns-222.example-dns.net.", "ns-111.example-dns.net."},
	}
	recordSet := route53NSRecordSet("app", "delegated")
	setRecordSetStatusData(t, recordSet, route53v1alpha1.Route53RecordSetStatusData{
		HostedZoneID: "Z000001",
		RecordName:   "delegated.apps.example.com.",
		RecordType:   string(dnsv1alpha1.RecordTypeNS),
	})
	objects := route53RecordSetObjects(t, recordSet)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	result, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.Requeue {
		t.Fatalf("Reconcile result = %#v, want no immediate requeue", result)
	}
	if len(provider.upserted) != 0 {
		t.Fatalf("upserted record sets = %d, want 0", len(provider.upserted))
	}
}

func TestZoneReconcilerUpsertsRoute53AliasRecordSet(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	recordSet := route53AliasARecordSet("app", "alias")
	objects := route53RecordSetObjects(t, recordSet)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	result, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("Reconcile did not request a requeue for a pending Route 53 alias change")
	}
	if len(provider.upserted) != 1 {
		t.Fatalf("upserted record sets = %d, want 1", len(provider.upserted))
	}
	upserted := provider.upserted[0]
	if upserted.HostedZoneID != "Z000001" || upserted.Name != "alias.apps.example.com." || upserted.Type != dnsv1alpha1.RecordTypeA {
		t.Fatalf("upserted identity = %#v", upserted)
	}
	if upserted.TTL != nil {
		t.Fatalf("upserted ttl = %#v, want nil for alias", upserted.TTL)
	}
	if len(upserted.Values) != 0 {
		t.Fatalf("upserted resource records = %#v, want none for alias", upserted.Values)
	}
	if upserted.Alias == nil {
		t.Fatalf("upserted alias = nil, want AliasTarget")
	}
	if upserted.Alias.DNSName != "target.apps.example.com." ||
		upserted.Alias.HostedZoneID != "Z000001" ||
		upserted.Alias.EvaluateTargetHealth {
		t.Fatalf("upserted alias = %#v", upserted.Alias)
	}
}

func TestZoneReconcilerRejectsRecordSetWhenCNAMEZoneUnitItemOwnsSameName(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	recordSet := route53ARecordSet("app", "www")
	objects := []client.Object{
		route53Provider(),
		route53ZoneClass("app", "route53-public", nil),
		acceptedRoute53Identity("app", "route53-dev"),
		route53ReadyZone(t, "app", "apps-example-com"),
		route53ZoneUnitWithRecordSetItem("app", "apps-example-com", "other", "www-cname", "www", dnsv1alpha1.RecordTypeCNAME),
		recordSet,
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	_, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var gotRecordSet dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(recordSet), &gotRecordSet); err != nil {
		t.Fatalf("RecordSet was not found: %v", err)
	}
	assertCondition(t, gotRecordSet.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionFalse, "RecordSetConflict")
	if len(provider.upserted) != 0 {
		t.Fatalf("upserted record sets = %d, want 0", len(provider.upserted))
	}
}

func TestZoneReconcilerMatchesAAAARecordByParsedAddress(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	ttl := int64(300)
	provider.records[recordKey("Z000001", "www.apps.example.com.", dnsv1alpha1.RecordTypeAAAA)] = RecordSetResource{
		HostedZoneID: "Z000001",
		Name:         "www.apps.example.com.",
		Type:         dnsv1alpha1.RecordTypeAAAA,
		TTL:          &ttl,
		Values:       []string{"2001:0db8:0000::0010"},
	}
	recordSet := route53AAAARecordSet("app", "www-v6")
	setRecordSetStatusData(t, recordSet, route53v1alpha1.Route53RecordSetStatusData{
		HostedZoneID: "Z000001",
		RecordName:   "www.apps.example.com.",
		RecordType:   string(dnsv1alpha1.RecordTypeAAAA),
	})
	objects := route53RecordSetObjects(t, recordSet)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	result, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.Requeue {
		t.Fatalf("Reconcile result = %#v, want no immediate requeue", result)
	}
	if len(provider.upserted) != 0 {
		t.Fatalf("upserted record sets = %d, want 0", len(provider.upserted))
	}
}

func TestZoneReconcilerMatchesEscapedRoute53WildcardRecordName(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	ttl := int64(300)
	provider.records[recordKey("Z000001", "\\052.apps.example.com.", dnsv1alpha1.RecordTypeA)] = RecordSetResource{
		HostedZoneID: "Z000001",
		Name:         "\\052.apps.example.com.",
		Type:         dnsv1alpha1.RecordTypeA,
		TTL:          &ttl,
		Values:       []string{"192.0.2.10"},
	}
	recordSet := route53ARecordSet("app", "wildcard")
	recordSet.Spec.Name = "*"
	setRecordSetStatusData(t, recordSet, route53v1alpha1.Route53RecordSetStatusData{
		HostedZoneID: "Z000001",
		RecordName:   "*.apps.example.com.",
		RecordType:   string(dnsv1alpha1.RecordTypeA),
	})
	objects := route53RecordSetObjects(t, recordSet)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	result, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.Requeue {
		t.Fatalf("Reconcile result = %#v, want no immediate requeue", result)
	}
	if len(provider.upserted) != 0 {
		t.Fatalf("upserted record sets = %d, want 0", len(provider.upserted))
	}

	var gotRecordSet dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(recordSet), &gotRecordSet); err != nil {
		t.Fatalf("RecordSet was not found: %v", err)
	}
	assertCondition(t, gotRecordSet.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionTrue, "Programmed")
	if gotRecordSet.Status.Provider != nil {
		t.Fatalf("status.provider = %#v, want no Route 53 RecordSet provider data", gotRecordSet.Status.Provider)
	}
}

func TestZoneReconcilerRecordsRoute53RecordSetEventsOnRecordSets(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	recorder := &capturingRecorder{}
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	recordSet := route53ARecordSet("app", "www")
	objects := route53RecordSetObjects(t, recordSet)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider, Recorder: recorder}

	if _, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if !recorder.has("RecordSet", "Route53RecordSetChangeSubmitted") {
		t.Fatalf("Route53RecordSetChangeSubmitted was not recorded on a RecordSet: %#v", recorder.events)
	}
	if recorder.has("Zone", "Route53RecordSetChangeSubmitted") {
		t.Fatalf("Route53RecordSetChangeSubmitted was recorded on a Zone: %#v", recorder.events)
	}

	var zone dnsv1alpha1.Zone
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &zone); err != nil {
		t.Fatalf("Zone was not found: %v", err)
	}
	pending := mustZoneUnitStatusData(t, ctx, k8sClient, zone.Namespace, zone.Name).PendingRecordSetChange
	if pending == nil || pending.ID == "" {
		t.Fatalf("pendingRecordSetChange = %#v, want pending record set change", pending)
	}
	provider.changes[pending.ID].Status = route53v1alpha1.Route53ChangeStatusInSync

	if _, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}}); err != nil {
		t.Fatalf("second Reconcile returned error: %v", err)
	}
	if !recorder.has("RecordSet", "Route53RecordSetChangeInSync") {
		t.Fatalf("Route53RecordSetChangeInSync was not recorded on a RecordSet: %#v", recorder.events)
	}
	if recorder.has("Zone", "Route53RecordSetChangeInSync") {
		t.Fatalf("Route53RecordSetChangeInSync was recorded on a Zone: %#v", recorder.events)
	}
}

func TestZoneReconcilerDeletesRecordSetFromSpecIdentity(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
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
	recordSet := deletingRoute53ARecordSet(t)
	setRecordSetStatusData(t, recordSet, route53v1alpha1.Route53RecordSetStatusData{
		HostedZoneID: "ZSTALE",
		RecordName:   "stale.apps.example.com.",
		RecordType:   string(dnsv1alpha1.RecordTypeA),
	})
	objects := route53RecordSetObjects(t, recordSet)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	result, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("Reconcile did not request a requeue for a pending Route 53 deletion")
	}
	if len(provider.deletedRRs) != 1 {
		t.Fatalf("deleted record sets = %d, want 1", len(provider.deletedRRs))
	}
	deleted := provider.deletedRRs[0]
	if deleted.HostedZoneID != "Z000001" || deleted.Name != "www.apps.example.com." || deleted.Type != dnsv1alpha1.RecordTypeA {
		t.Fatalf("deleted identity = %#v", deleted)
	}

	var zone dnsv1alpha1.Zone
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &zone); err != nil {
		t.Fatalf("Zone was not found: %v", err)
	}
	pending := mustZoneUnitStatusData(t, ctx, k8sClient, zone.Namespace, zone.Name).PendingRecordSetChange
	if pending == nil || pending.ID == "" {
		t.Fatalf("pendingRecordSetChange = %#v, want pending delete", pending)
	}
	provider.changes[pending.ID].Status = route53v1alpha1.Route53ChangeStatusInSync

	if _, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}}); err != nil {
		t.Fatalf("second Reconcile returned error: %v", err)
	}
	if _, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}}); err != nil {
		t.Fatalf("third Reconcile returned error: %v", err)
	}

	var unit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &unit); err != nil {
		t.Fatalf("ZoneUnit was not found: %v", err)
	}
	statusIndex := slices.IndexFunc(unit.Status.RecordSets, func(status dnsv1alpha1.ZoneUnitRecordSetStatus) bool {
		return status.RecordSetNamespace == recordSet.Namespace && status.RecordSetName == recordSet.Name
	})
	if statusIndex < 0 || !unit.Status.RecordSets[statusIndex].DeletionCompleted {
		t.Fatalf("ZoneUnit recordSet deletionCompleted = %#v, want true", unit.Status.RecordSets)
	}
}

func TestZoneReconcilerIgnoresCompletedDeletionStatusForActiveZoneUnitItem(t *testing.T) {
	ctx := context.Background()
	recordSet := route53ARecordSet("app", "www-a")
	unit := route53ZoneUnit("app", "apps-example-com", "app", "route53-public")
	unit.Spec.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetSpec{route53ZoneUnitRecordSetSpec(recordSet)}
	unit.Status.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetStatus{
		{
			RecordSetNamespace: "app",
			RecordSetName:      "www-a",
			ObservedGeneration: 2,
			DeletionCompleted:  true,
			Provider: &dnsv1alpha1.ProviderStatus{
				Data: runtime.RawExtension{Raw: []byte(`{"hostedZoneID":"Z000001","recordName":"www.apps.example.com.","recordType":"A"}`)},
			},
			Conditions: []metav1.Condition{
				{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted"},
				{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionTrue, Reason: "Programmed"},
			},
		},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(unit).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient}

	recordSets, err := reconciler.recordSetsForZone(ctx, route53ReadyZone(t, "app", "apps-example-com"))
	if err != nil {
		t.Fatalf("recordSetsForZone returned error: %v", err)
	}
	if len(recordSets) != 1 {
		t.Fatalf("recordSets = %#v, want one record set", recordSets)
	}
	got := recordSets[0]
	if got.Status.Provider != nil {
		t.Fatalf("provider status = %#v, want nil", got.Status.Provider)
	}
	if len(got.Status.Conditions) != 0 {
		t.Fatalf("conditions = %#v, want empty", got.Status.Conditions)
	}
	if got.Status.ObservedGeneration != recordSet.Generation {
		t.Fatalf("observedGeneration = %d, want %d", got.Status.ObservedGeneration, recordSet.Generation)
	}
}

func TestProviderErrorConditionSanitizesAWSAPIError(t *testing.T) {
	err := &smithy.OperationError{
		ServiceID:     "STS",
		OperationName: "GetCallerIdentity",
		Err: fmt.Errorf(
			"https response error StatusCode: 400, RequestID: req-1, %w",
			&smithy.GenericAPIError{
				Code:    "InvalidGrantException",
				Message: "SSO token could not be refreshed",
			},
		),
	}

	reason, message := providerErrorCondition(err)

	if reason != "ProviderAccessDenied" {
		t.Fatalf("reason = %q, want ProviderAccessDenied", reason)
	}
	if message != "InvalidGrantException: SSO token could not be refreshed" {
		t.Fatalf("message = %q", message)
	}
}

func TestNormalizeRoute53RecordName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "adds trailing dot and lowercases",
			in:   "WWW.Apps.Example.Com",
			want: "www.apps.example.com.",
		},
		{
			name: "unescapes route53 wildcard octal",
			in:   "\\052.Platform2.Test.",
			want: "*.platform2.test.",
		},
		{
			name: "keeps non octal escape literal",
			in:   "\\x52.platform2.test.",
			want: "\\x52.platform2.test.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeRoute53RecordName(tt.in); got != tt.want {
				t.Fatalf("normalizeRoute53RecordName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("corev1 AddToScheme: %v", err)
	}
	if err := dnsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("dns AddToScheme: %v", err)
	}
	if err := route53v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("route53 AddToScheme: %v", err)
	}
	return scheme
}

func sameNamespaceClassObjects(namespace string, tags map[string]string) []client.Object {
	return []client.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}},
		route53Provider(),
		route53ZoneClass(namespace, "route53-public", tags),
		acceptedRoute53Identity(namespace, "route53-dev"),
		route53ZoneUnit(namespace, "apps-example-com", namespace, "route53-public"),
		&dnsv1alpha1.Zone{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      "apps-example-com",
				UID:       types.UID("11111111-2222-3333-4444-555555555555"),
			},
			Spec: dnsv1alpha1.ZoneSpec{
				DomainName:   "apps.example.com",
				ZoneClassRef: dnsv1alpha1.ZoneClassReference{Name: "route53-public"},
				Provider:     route53v1alpha1.ProviderRef,
			},
		},
	}
}

func route53ZoneClass(namespace, name string, tags map[string]string) *dnsv1alpha1.ZoneClass {
	parameters := route53v1alpha1.Route53ZoneClassParameters{
		Tags: tags,
	}
	raw, err := json.Marshal(parameters)
	if err != nil {
		panic(err)
	}
	return &dnsv1alpha1.ZoneClass{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Generation: 1},
		Spec: dnsv1alpha1.ZoneClassSpec{
			Provider:       route53v1alpha1.ProviderRef,
			ControllerName: DefaultControllerName,
			IdentityRef:    dnsv1alpha1.LocalObjectReference{Name: "route53-dev"},
			Parameters:     runtime.RawExtension{Raw: raw},
			AllowedZones: dnsv1alpha1.ZoneClassAllowedZones{
				Namespaces: dnsv1alpha1.NamespacePolicy{From: dnsv1alpha1.NamespacesFromSame},
			},
		},
		Status: dnsv1alpha1.ZoneClassStatus{
			Conditions: []metav1.Condition{
				{
					Type:               string(dnsv1alpha1.ConditionAccepted),
					Status:             metav1.ConditionTrue,
					Reason:             "Accepted",
					ObservedGeneration: 1,
				},
			},
		},
	}
}

func route53Provider() *dnsv1alpha1.Provider {
	return &dnsv1alpha1.Provider{
		ObjectMeta: metav1.ObjectMeta{Name: route53v1alpha1.ProviderName},
		Spec: dnsv1alpha1.ProviderSpec{
			Display: dnsv1alpha1.ProviderDisplay{Name: "Amazon Route 53"},
			Versions: []dnsv1alpha1.ProviderVersion{
				{
					Name:    route53v1alpha1.ProviderVersion,
					Served:  true,
					Storage: true,
					Identity: &dnsv1alpha1.ProviderIdentity{
						Resource: dnsv1alpha1.ProviderIdentityResource{
							Group: "route53.dns.appthrust.io",
							Kind:  "Route53Identity",
							Scope: "Namespaced",
						},
					},
					RecordSet: dnsv1alpha1.ProviderRecordSet{
						SupportedTypes: []dnsv1alpha1.RecordType{
							dnsv1alpha1.RecordTypeA,
							dnsv1alpha1.RecordTypeAAAA,
							dnsv1alpha1.RecordTypeTXT,
							dnsv1alpha1.RecordTypeCNAME,
							dnsv1alpha1.RecordTypeMX,
							dnsv1alpha1.RecordTypeCAA,
							dnsv1alpha1.RecordTypeNS,
						},
					},
				},
			},
		},
	}
}

func route53RecordSetObjects(t *testing.T, recordSet *dnsv1alpha1.RecordSet) []client.Object {
	t.Helper()
	zone := route53ReadyZone(t, "app", "apps-example-com")
	unit := route53ZoneUnit("app", "apps-example-com", "app", "route53-public")
	unit.Spec.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetSpec{route53ZoneUnitRecordSetSpec(recordSet)}
	if recordSet.Status.Provider != nil || len(recordSet.Status.Conditions) > 0 {
		unit.Status.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetStatus{
			{
				RecordSetNamespace: recordSet.Namespace,
				RecordSetName:      recordSet.Name,
				ObservedGeneration: recordSet.Status.ObservedGeneration,
				Provider:           recordSet.Status.Provider,
				Conditions:         slices.Clone(recordSet.Status.Conditions),
			},
		}
	}
	return []client.Object{
		route53Provider(),
		route53ZoneClass("app", "route53-public", nil),
		acceptedRoute53Identity("app", "route53-dev"),
		zone,
		unit,
		recordSet,
	}
}

func route53ZoneUnit(zoneNamespace, zoneName, zoneClassNamespace, zoneClassName string) *dnsv1alpha1.ZoneUnit {
	return &dnsv1alpha1.ZoneUnit{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: zoneNamespace,
			Name:      zoneName,
			UID:       types.UID("11111111-2222-3333-4444-555555555555"),
		},
		Spec: dnsv1alpha1.ZoneUnitSpec{
			Provider: dnsv1alpha1.ProviderReference{Name: route53v1alpha1.ProviderName, Version: route53v1alpha1.ProviderVersion},
			Zone: dnsv1alpha1.ZoneUnitZoneSpec{
				Ref:                dnsv1alpha1.ObjectReference{Namespace: zoneNamespace, Name: zoneName},
				ObservedGeneration: 1,
				DomainName:         "apps.example.com",
				ZoneClassRef:       dnsv1alpha1.ObjectReference{Namespace: zoneClassNamespace, Name: zoneClassName},
			},
		},
	}
}

func markRoute53ZoneUnitDeleting(unit *dnsv1alpha1.ZoneUnit, deletionTime metav1.Time) {
	unit.DeletionTimestamp = &deletionTime
	if !slices.Contains(unit.Finalizers, ZoneFinalizer) {
		unit.Finalizers = append(unit.Finalizers, ZoneFinalizer)
	}
}

func route53ZoneUnitWithRecordSetItem(zoneNamespace, zoneName, recordSetNamespace, recordSetName, recordName string, recordType dnsv1alpha1.RecordType) *dnsv1alpha1.ZoneUnit {
	return &dnsv1alpha1.ZoneUnit{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: zoneNamespace,
			Name:      zoneName,
			UID:       types.UID("11111111-2222-3333-4444-555555555555"),
		},
		Spec: dnsv1alpha1.ZoneUnitSpec{
			Provider: dnsv1alpha1.ProviderReference{Name: route53v1alpha1.ProviderName, Version: route53v1alpha1.ProviderVersion},
			Zone: dnsv1alpha1.ZoneUnitZoneSpec{
				Ref:                dnsv1alpha1.ObjectReference{Namespace: zoneNamespace, Name: zoneName},
				ObservedGeneration: 1,
				DomainName:         "apps.example.com",
				ZoneClassRef:       dnsv1alpha1.ObjectReference{Namespace: zoneNamespace, Name: "route53-public"},
			},
			RecordSets: []dnsv1alpha1.ZoneUnitRecordSetSpec{
				{
					RecordSetNamespace: recordSetNamespace,
					RecordSetName:      recordSetName,
					Name:               recordName,
					Type:               recordType,
				},
			},
		},
	}
}

func route53ZoneUnitRecordSetSpec(recordSet *dnsv1alpha1.RecordSet) dnsv1alpha1.ZoneUnitRecordSetSpec {
	return dnsv1alpha1.ZoneUnitRecordSetSpec{
		RecordSetNamespace: recordSet.Namespace,
		RecordSetName:      recordSet.Name,
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

func route53ReadyZone(t *testing.T, namespace, name string) *dnsv1alpha1.Zone {
	t.Helper()
	zone := &dnsv1alpha1.Zone{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:  namespace,
			Name:       name,
			UID:        types.UID("11111111-2222-3333-4444-555555555555"),
			Generation: 1,
		},
		Spec: dnsv1alpha1.ZoneSpec{
			DomainName:   "apps.example.com",
			ZoneClassRef: dnsv1alpha1.ZoneClassReference{Name: "route53-public"},
			Provider:     route53v1alpha1.ProviderRef,
		},
		Status: dnsv1alpha1.ZoneStatus{
			Conditions: []metav1.Condition{
				{
					Type:               string(dnsv1alpha1.ConditionAccepted),
					Status:             metav1.ConditionTrue,
					Reason:             "Accepted",
					ObservedGeneration: 1,
				},
				{
					Type:               string(dnsv1alpha1.ConditionProgrammed),
					Status:             metav1.ConditionTrue,
					Reason:             "Programmed",
					ObservedGeneration: 1,
				},
			},
		},
	}
	setZoneStatusData(t, zone, route53v1alpha1.Route53ZoneStatusData{HostedZoneID: "Z000001"})
	return zone
}

func route53ARecordSet(namespace, name string) *dnsv1alpha1.RecordSet {
	return &dnsv1alpha1.RecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Generation: 1},
		Spec: dnsv1alpha1.RecordSetSpec{
			ZoneRef:  dnsv1alpha1.ZoneReference{Name: "apps-example-com"},
			Provider: route53v1alpha1.ProviderRef,
			Type:     dnsv1alpha1.RecordTypeA,
			Name:     "www",
			TTL:      ptr(int32(300)),
			A: &dnsv1alpha1.ARecordSet{
				Addresses: []string{"192.0.2.10"},
			},
		},
	}
}

func route53AAAARecordSet(namespace, name string) *dnsv1alpha1.RecordSet {
	return &dnsv1alpha1.RecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Generation: 1},
		Spec: dnsv1alpha1.RecordSetSpec{
			ZoneRef:  dnsv1alpha1.ZoneReference{Name: "apps-example-com"},
			Provider: route53v1alpha1.ProviderRef,
			Type:     dnsv1alpha1.RecordTypeAAAA,
			Name:     "www",
			TTL:      ptr(int32(300)),
			AAAA: &dnsv1alpha1.AAAARecordSet{
				Addresses: []string{"2001:db8::10"},
			},
		},
	}
}

func route53TXTRecordSet(namespace, name string) *dnsv1alpha1.RecordSet {
	return &dnsv1alpha1.RecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Generation: 1},
		Spec: dnsv1alpha1.RecordSetSpec{
			ZoneRef:  dnsv1alpha1.ZoneReference{Name: "apps-example-com"},
			Provider: route53v1alpha1.ProviderRef,
			Type:     dnsv1alpha1.RecordTypeTXT,
			Name:     "_acme-challenge",
			TTL:      ptr(int32(300)),
			TXT: &dnsv1alpha1.TXTRecordSet{
				Values: []string{"challenge-token", "v=spf1 include:_spf.example.net ~all"},
			},
		},
	}
}

func route53CNAMERecordSet(namespace, name string) *dnsv1alpha1.RecordSet {
	return &dnsv1alpha1.RecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Generation: 1},
		Spec: dnsv1alpha1.RecordSetSpec{
			ZoneRef:  dnsv1alpha1.ZoneReference{Name: "apps-example-com"},
			Provider: route53v1alpha1.ProviderRef,
			Type:     dnsv1alpha1.RecordTypeCNAME,
			Name:     "www",
			TTL:      ptr(int32(300)),
			CNAME: &dnsv1alpha1.CNAMERecordSet{
				Target: "target.example.net",
			},
		},
	}
}

func route53MXRecordSet(namespace, name string) *dnsv1alpha1.RecordSet {
	return &dnsv1alpha1.RecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Generation: 1},
		Spec: dnsv1alpha1.RecordSetSpec{
			ZoneRef:  dnsv1alpha1.ZoneReference{Name: "apps-example-com"},
			Provider: route53v1alpha1.ProviderRef,
			Type:     dnsv1alpha1.RecordTypeMX,
			Name:     "@",
			TTL:      ptr(int32(300)),
			MX: &dnsv1alpha1.MXRecordSet{
				Records: []dnsv1alpha1.MXRecord{
					{Preference: 10, Exchange: "mail1.example.net"},
					{Preference: 20, Exchange: "mail2.example.net"},
				},
			},
		},
	}
}

func route53CAARecordSet(namespace, name string) *dnsv1alpha1.RecordSet {
	return &dnsv1alpha1.RecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Generation: 1},
		Spec: dnsv1alpha1.RecordSetSpec{
			ZoneRef:  dnsv1alpha1.ZoneReference{Name: "apps-example-com"},
			Provider: route53v1alpha1.ProviderRef,
			Type:     dnsv1alpha1.RecordTypeCAA,
			Name:     "@",
			TTL:      ptr(int32(300)),
			CAA: &dnsv1alpha1.CAARecordSet{
				Records: []dnsv1alpha1.CAARecord{
					{Flags: 0, Tag: "issue", Value: "letsencrypt.org"},
					{Flags: 0, Tag: "iodef", Value: "mailto:security@example.com"},
				},
			},
		},
	}
}

func route53NSRecordSet(namespace, name string) *dnsv1alpha1.RecordSet {
	return &dnsv1alpha1.RecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Generation: 1},
		Spec: dnsv1alpha1.RecordSetSpec{
			ZoneRef:  dnsv1alpha1.ZoneReference{Name: "apps-example-com"},
			Provider: route53v1alpha1.ProviderRef,
			Type:     dnsv1alpha1.RecordTypeNS,
			Name:     "delegated",
			TTL:      ptr(int32(300)),
			NS: &dnsv1alpha1.NSRecordSet{
				NameServers: []string{"ns-111.example-dns.net", "ns-222.example-dns.net"},
			},
		},
	}
}

func route53AliasARecordSet(namespace, name string) *dnsv1alpha1.RecordSet {
	return &dnsv1alpha1.RecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Generation: 1},
		Spec: dnsv1alpha1.RecordSetSpec{
			ZoneRef:  dnsv1alpha1.ZoneReference{Name: "apps-example-com"},
			Provider: route53v1alpha1.ProviderRef,
			Type:     dnsv1alpha1.RecordTypeA,
			Name:     "alias",
			Options:  runtime.RawExtension{Raw: []byte(`{"alias":{"dnsName":"target.apps.example.com.","hostedZoneID":"Z000001","evaluateTargetHealth":false}}`)},
		},
	}
}

func deletingRoute53ARecordSet(t *testing.T) *dnsv1alpha1.RecordSet {
	t.Helper()
	recordSet := route53ARecordSet("app", "www")
	deletionTime := metav1.Now()
	recordSet.DeletionTimestamp = &deletionTime
	recordSet.Finalizers = []string{RecordSetFinalizer}
	return recordSet
}

func setZoneStatusData(t *testing.T, zone *dnsv1alpha1.Zone, data route53v1alpha1.Route53ZoneStatusData) {
	t.Helper()
	stateRaw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("json.Marshal returned error: %v", err)
	}
	publicData := route53v1alpha1.Route53ZoneStatusData{}
	if data.HostedZoneID != "" {
		publicData.HostedZoneID = data.HostedZoneID
	}
	publicRaw, err := json.Marshal(publicData)
	if err != nil {
		t.Fatalf("json.Marshal returned error: %v", err)
	}
	zone.Status.Provider = &dnsv1alpha1.ProviderStatus{
		Data:  runtime.RawExtension{Raw: publicRaw},
		State: runtime.RawExtension{Raw: stateRaw},
	}
}

func setRecordSetStatusData(t *testing.T, recordSet *dnsv1alpha1.RecordSet, data route53v1alpha1.Route53RecordSetStatusData) {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("json.Marshal returned error: %v", err)
	}
	recordSet.Status.Provider = &dnsv1alpha1.ProviderStatus{Data: runtime.RawExtension{Raw: raw}}
}

func mustZoneUnitRecordSetStatusState(t *testing.T, ctx context.Context, k8sClient client.Client, zoneNamespace, zoneName, recordSetNamespace, recordSetName string) route53v1alpha1.Route53RecordSetStatusData {
	t.Helper()
	var unit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: zoneNamespace, Name: zoneName}, &unit); err != nil {
		t.Fatalf("ZoneUnit was not found: %v", err)
	}
	index := slices.IndexFunc(unit.Status.RecordSets, func(status dnsv1alpha1.ZoneUnitRecordSetStatus) bool {
		return status.RecordSetNamespace == recordSetNamespace && status.RecordSetName == recordSetName
	})
	if index < 0 {
		t.Fatalf("ZoneUnit status.recordSets has no entry for %s/%s: %#v", recordSetNamespace, recordSetName, unit.Status.RecordSets)
	}
	provider := unit.Status.RecordSets[index].Provider
	if provider == nil || len(provider.State.Raw) == 0 {
		t.Fatalf("ZoneUnit status.recordSets[%d].provider.state is empty", index)
	}
	var data route53v1alpha1.Route53RecordSetStatusData
	if err := json.Unmarshal(provider.State.Raw, &data); err != nil {
		t.Fatalf("ZoneUnit status.recordSets[%d].provider.state must match Route 53 schema: %v", index, err)
	}
	return data
}

func acceptedRoute53Identity(namespace, name string) *route53v1alpha1.Route53Identity {
	return &route53v1alpha1.Route53Identity{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Generation: 1},
		Spec: route53v1alpha1.Route53IdentitySpec{
			AccountID: "123456789012",
			Region:    "ap-northeast-1",
			Credentials: route53v1alpha1.Route53Credentials{
				Runtime: &route53v1alpha1.Route53RuntimeCredentials{},
			},
		},
		Status: route53v1alpha1.Route53IdentityStatus{
			Conditions: []metav1.Condition{
				{
					Type:               string(dnsv1alpha1.ConditionAccepted),
					Status:             metav1.ConditionTrue,
					Reason:             "Accepted",
					ObservedGeneration: 1,
				},
				{
					Type:               "Ready",
					Status:             metav1.ConditionTrue,
					Reason:             "Ready",
					ObservedGeneration: 1,
				},
			},
		},
	}
}

func assertCondition(t *testing.T, conditions []metav1.Condition, conditionType string, status metav1.ConditionStatus, reason string) metav1.Condition {
	t.Helper()
	condition := meta.FindStatusCondition(conditions, conditionType)
	if condition == nil {
		t.Fatalf("condition %s was not found in %#v", conditionType, conditions)
	}
	if condition.Status != status || condition.Reason != reason {
		t.Fatalf("condition %s = (%s, %s), want (%s, %s)", conditionType, condition.Status, condition.Reason, status, reason)
	}
	return *condition
}

func mustZoneStatusData(t *testing.T, zone *dnsv1alpha1.Zone) route53v1alpha1.Route53ZoneStatusData {
	t.Helper()
	data, err := route53ZoneStatusData(zone)
	if err != nil {
		t.Fatalf("route53ZoneStatusData returned error: %v", err)
	}
	return data
}

func mustZoneUnitStatusData(t *testing.T, ctx context.Context, k8sClient client.Client, namespace, name string) route53v1alpha1.Route53ZoneStatusData {
	t.Helper()
	var unit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &unit); err != nil {
		t.Fatalf("ZoneUnit was not found: %v", err)
	}
	data, err := route53ZoneUnitStatusData(&unit)
	if err != nil {
		t.Fatalf("route53ZoneUnitStatusData returned error: %v", err)
	}
	return data
}

func ptr[T any](value T) *T {
	return &value
}

type recordedEvent struct {
	Object string
	Reason string
}

type capturingRecorder struct {
	events []recordedEvent
}

func (r *capturingRecorder) Event(object runtime.Object, _ string, reason, _ string) {
	r.events = append(r.events, recordedEvent{Object: eventObjectName(object), Reason: reason})
}

func (r *capturingRecorder) Eventf(object runtime.Object, eventType, reason, messageFmt string, args ...interface{}) {
	r.Event(object, eventType, reason, fmt.Sprintf(messageFmt, args...))
}

func (r *capturingRecorder) AnnotatedEventf(object runtime.Object, _ map[string]string, eventType, reason, messageFmt string, args ...interface{}) {
	r.Event(object, eventType, reason, fmt.Sprintf(messageFmt, args...))
}

func (r *capturingRecorder) has(object, reason string) bool {
	for _, event := range r.events {
		if event.Object == object && event.Reason == reason {
			return true
		}
	}
	return false
}

func eventObjectName(object runtime.Object) string {
	switch object.(type) {
	case *dnsv1alpha1.RecordSet:
		return "RecordSet"
	case *dnsv1alpha1.Zone:
		return "Zone"
	case *route53v1alpha1.Route53Identity:
		return "Route53Identity"
	default:
		return fmt.Sprintf("%T", object)
	}
}

type createHostedZoneRequest struct {
	domainName      string
	callerReference string
}

type fakeProvider struct {
	zones      map[string]HostedZone
	tags       map[string]map[string]string
	changes    map[string]*route53v1alpha1.Route53Change
	records    map[string]RecordSetResource
	created    []createHostedZoneRequest
	upserted   []RecordSetResource
	deletedRRs []RecordSetResource

	changeRecordSetsErr func(hostedZoneID string, changes []RecordSetChange) error
}

func newFakeProvider() *fakeProvider {
	return &fakeProvider{
		zones:   map[string]HostedZone{},
		tags:    map[string]map[string]string{},
		changes: map[string]*route53v1alpha1.Route53Change{},
		records: map[string]RecordSetResource{},
	}
}

func (p *fakeProvider) GetHostedZone(_ context.Context, id string) (HostedZone, error) {
	zone, ok := p.zones[normalizeHostedZoneID(id)]
	if !ok {
		return HostedZone{}, &smithy.GenericAPIError{
			Code:    "NoSuchHostedZone",
			Message: fmt.Sprintf("hosted zone %s was not found", id),
		}
	}
	return zone, nil
}

func (p *fakeProvider) ListHostedZonesByName(_ context.Context, domainName string) ([]HostedZone, error) {
	var zones []HostedZone
	for _, zone := range p.zones {
		if normalizeDomainName(zone.Name) == domainName {
			zones = append(zones, zone)
		}
	}
	return zones, nil
}

func (p *fakeProvider) CreateHostedZone(_ context.Context, domainName, callerReference string) (CreatedHostedZone, error) {
	p.created = append(p.created, createHostedZoneRequest{domainName: domainName, callerReference: callerReference})
	id := fmt.Sprintf("Z%06d", len(p.created))
	changeID := fmt.Sprintf("C%06d", len(p.created))
	hostedZone := HostedZone{
		ID:              id,
		Name:            domainName,
		CallerReference: callerReference,
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	p.zones[id] = hostedZone
	p.changes[changeID] = &route53v1alpha1.Route53Change{
		ID:     changeID,
		Status: route53v1alpha1.Route53ChangeStatusPending,
	}
	return CreatedHostedZone{
		HostedZone: hostedZone,
		Change:     p.changes[changeID],
	}, nil
}

func (p *fakeProvider) GetChange(_ context.Context, id string) (*route53v1alpha1.Route53Change, error) {
	change, ok := p.changes[id]
	if !ok {
		return nil, fmt.Errorf("change %s was not found", id)
	}
	return change, nil
}

func (p *fakeProvider) DeleteHostedZone(_ context.Context, id string) (*route53v1alpha1.Route53Change, error) {
	delete(p.zones, normalizeHostedZoneID(id))
	change := &route53v1alpha1.Route53Change{
		ID:     "CDELETE",
		Status: route53v1alpha1.Route53ChangeStatusPending,
	}
	p.changes[change.ID] = change
	return change, nil
}

func (p *fakeProvider) TagHostedZone(_ context.Context, id string, tags map[string]string) error {
	copied := make(map[string]string, len(tags))
	for key, value := range tags {
		copied[key] = value
	}
	p.tags[normalizeHostedZoneID(id)] = copied
	return nil
}

func (p *fakeProvider) GetRecordSet(_ context.Context, hostedZoneID, recordName string, recordType dnsv1alpha1.RecordType) (*RecordSetResource, error) {
	record, ok := p.records[recordKey(hostedZoneID, recordName, recordType)]
	if !ok {
		return nil, nil
	}
	copied := copyRecordSetResource(record)
	return &copied, nil
}

func (p *fakeProvider) ListRecordSets(_ context.Context, hostedZoneID string) ([]RecordSetResource, error) {
	var records []RecordSetResource
	for _, record := range p.records {
		if normalizeHostedZoneID(record.HostedZoneID) != normalizeHostedZoneID(hostedZoneID) {
			continue
		}
		records = append(records, copyRecordSetResource(record))
	}
	return records, nil
}

func (p *fakeProvider) ChangeRecordSets(_ context.Context, hostedZoneID string, changes []RecordSetChange) (*route53v1alpha1.Route53Change, error) {
	if p.changeRecordSetsErr != nil {
		if err := p.changeRecordSetsErr(hostedZoneID, changes); err != nil {
			return nil, err
		}
	}
	for _, change := range changes {
		recordSet := copyRecordSetResource(change.RecordSet)
		recordSet.HostedZoneID = normalizeHostedZoneID(hostedZoneID)
		switch change.Action {
		case RecordSetChangeActionDelete:
			p.deletedRRs = append(p.deletedRRs, recordSet)
			delete(p.records, recordKey(recordSet.HostedZoneID, recordSet.Name, recordSet.Type))
		default:
			p.upserted = append(p.upserted, recordSet)
			p.records[recordKey(recordSet.HostedZoneID, recordSet.Name, recordSet.Type)] = recordSet
		}
	}
	changeID := fmt.Sprintf("CRR%06d", len(p.upserted)+len(p.deletedRRs))
	p.changes[changeID] = &route53v1alpha1.Route53Change{ID: changeID, Status: route53v1alpha1.Route53ChangeStatusPending}
	return p.changes[changeID], nil
}

func (p *fakeProvider) UpsertRecordSet(_ context.Context, recordSet RecordSetResource) (*route53v1alpha1.Route53Change, error) {
	copied := copyRecordSetResource(recordSet)
	p.upserted = append(p.upserted, copied)
	p.records[recordKey(recordSet.HostedZoneID, recordSet.Name, recordSet.Type)] = copied
	changeID := fmt.Sprintf("CRR%06d", len(p.upserted))
	p.changes[changeID] = &route53v1alpha1.Route53Change{ID: changeID, Status: route53v1alpha1.Route53ChangeStatusPending}
	return p.changes[changeID], nil
}

func (p *fakeProvider) DeleteRecordSet(_ context.Context, recordSet RecordSetResource) (*route53v1alpha1.Route53Change, error) {
	copied := copyRecordSetResource(recordSet)
	p.deletedRRs = append(p.deletedRRs, copied)
	delete(p.records, recordKey(recordSet.HostedZoneID, recordSet.Name, recordSet.Type))
	p.changes["CRRDELETE"] = &route53v1alpha1.Route53Change{ID: "CRRDELETE", Status: route53v1alpha1.Route53ChangeStatusPending}
	return p.changes["CRRDELETE"], nil
}

func recordKey(hostedZoneID, recordName string, recordType dnsv1alpha1.RecordType) string {
	return normalizeHostedZoneID(hostedZoneID) + "|" + normalizeRoute53RecordName(recordName) + "|" + string(recordType)
}

func copyRecordSetResource(recordSet RecordSetResource) RecordSetResource {
	copied := recordSet
	copied.Values = append([]string(nil), recordSet.Values...)
	if recordSet.TTL != nil {
		ttl := *recordSet.TTL
		copied.TTL = &ttl
	}
	if recordSet.Alias != nil {
		alias := *recordSet.Alias
		copied.Alias = &alias
	}
	return copied
}
