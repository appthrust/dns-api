package route53

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	zoneunitcontroller "github.com/appthrust/dns-api/internal/go/core/controllers/zoneunit"
	zoneclass "github.com/appthrust/dns-api/internal/go/providers/route53/controllers/zoneclass"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
		&dnsv1alpha1.Zone{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-a-app", Name: "apps-example-com", UID: types.UID("11111111-2222-3333-4444-555555555555"), Generation: 1}, Spec: dnsv1alpha1.ZoneSpec{
			DomainName: "apps.example.com",
			Provider:   route53v1alpha1.ProviderRef,
			ZoneClassRef: dnsv1alpha1.ZoneClassReference{
				Namespace: ptr("tenant-a-platform"),
				Name:      "route53-public",
			},
		}},
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
