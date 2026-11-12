package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"reflect"
	"testing"

	endpointcontroller "github.com/appthrust/dns-api/internal/go/apps/endpoint/controllers/endpointrecordset"
	corezoneunit "github.com/appthrust/dns-api/internal/go/core/controllers/zoneunit"
	route53 "github.com/appthrust/dns-api/internal/go/providers/route53/controllers/zoneunit"
	route53conversion "github.com/appthrust/dns-api/internal/go/providers/route53/conversion"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const testAdmissionReceiptAnnotation = "gateway.endpoint.dns.appthrust.io/admission-receipt"

func TestGatewayEndpointRecordSetRetainsAdmittedSiblingDuringUnknownAndAddressLoss(t *testing.T) {
	r, paymentKey, haKey, gatewayKey, recordKey := retentionFixture(t, true)
	assertRetentionRecord(t, r, recordKey, []string{"ha.example.com", "payment.example.com"}, "192.0.2.1")

	setRouteAcceptance(t, r, haKey, metav1.ConditionUnknown)
	setGatewayAddresses(t, r, gatewayKey, nil)
	deleteRoute(t, r, paymentKey)

	assertRetentionRecord(t, r, recordKey, []string{"ha.example.com"}, "192.0.2.1")

	setRouteAcceptance(t, r, haKey, metav1.ConditionTrue)
	setGatewayAddresses(t, r, gatewayKey, []gatewayv1.GatewayStatusAddress{{Value: "192.0.2.2"}})
	reconcileRetentionRoute(t, r, haKey)
	assertRetentionRecord(t, r, recordKey, []string{"ha.example.com"}, "192.0.2.2")
}

func TestGatewayEndpointRecordSetRetainsReceiptAcrossObservedTransientStates(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, r *Reconciler, routeKey, gatewayKey client.ObjectKey)
	}{
		{
			name: "unknown gateway acceptance with empty addresses",
			run: func(t *testing.T, r *Reconciler, routeKey, gatewayKey client.ObjectKey) {
				setGatewayAcceptance(t, r, gatewayKey, metav1.ConditionUnknown)
				setGatewayAddresses(t, r, gatewayKey, nil)
				reconcileRetentionRoute(t, r, routeKey)
			},
		},
		{
			name: "missing route parent status",
			run: func(t *testing.T, r *Reconciler, routeKey, gatewayKey client.ObjectKey) {
				setGatewayAddresses(t, r, gatewayKey, nil)
				var route gatewayv1.HTTPRoute
				if err := r.Get(context.Background(), routeKey, &route); err != nil {
					t.Fatal(err)
				}
				route.Status.Parents = nil
				if err := r.Status().Update(context.Background(), &route); err != nil {
					t.Fatal(err)
				}
				reconcileRetentionRoute(t, r, routeKey)
			},
		},
		{
			name: "route generation advanced before its status is current",
			run: func(t *testing.T, r *Reconciler, routeKey, gatewayKey client.ObjectKey) {
				setGatewayAddresses(t, r, gatewayKey, nil)
				var route gatewayv1.HTTPRoute
				if err := r.Get(context.Background(), routeKey, &route); err != nil {
					t.Fatal(err)
				}
				// A spec change bumps the generation; the fake client does not
				// re-evaluate status, so the parent conditions now carry a stale
				// observedGeneration exactly as a leaf lagging its hub does.
				route.Spec.Rules = append(route.Spec.Rules, gatewayv1.HTTPRouteRule{})
				route.Generation++
				if err := r.Update(context.Background(), &route); err != nil {
					t.Fatal(err)
				}
				reconcileRetentionRoute(t, r, routeKey)
			},
		},
		{
			name: "route replacement with new UID",
			run: func(t *testing.T, r *Reconciler, routeKey, gatewayKey client.ObjectKey) {
				setGatewayAddresses(t, r, gatewayKey, nil)
				var oldRoute gatewayv1.HTTPRoute
				if err := r.Get(context.Background(), routeKey, &oldRoute); err != nil {
					t.Fatal(err)
				}
				oldRoute.Finalizers = nil
				if err := r.Update(context.Background(), &oldRoute); err != nil {
					t.Fatal(err)
				}
				if err := r.Delete(context.Background(), &oldRoute); err != nil {
					t.Fatal(err)
				}
				replacement := retentionRoute(
					oldRoute.Namespace,
					oldRoute.Name,
					"replacement-route",
					"ha.example.com",
					metav1.ConditionTrue,
				)
				if err := r.Create(context.Background(), replacement); err != nil {
					t.Fatal(err)
				}
				reconcileRetentionRoute(t, r, routeKey)
				var record endpointv1alpha1.EndpointRecordSet
				if err := r.Get(context.Background(), client.ObjectKey{
					Namespace: "dns-system",
					Name:      generatedGatewayEndpointRecordSetName("platform", "public"),
				}, &record); err != nil {
					t.Fatal(err)
				}
				receipt := existingAdmissionReceipt(&record)
				if len(receipt.Bindings) != 1 || receipt.Bindings[0].RouteUID != "replacement-route" {
					t.Fatalf("receipt was not rebound to replacement route: %#v", receipt.Bindings)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r, _, routeKey, gatewayKey, recordKey := retentionFixture(t, false)
			test.run(t, r, routeKey, gatewayKey)
			assertRetentionRecord(t, r, recordKey, []string{"ha.example.com"}, "192.0.2.1")
		})
	}
}

func TestGatewayRouteAdmissionRetainsPublishedRecordsWithoutProviderDelete(t *testing.T) {
	ctx := t.Context()
	r, _, routeKey, gatewayKey, recordKey := retentionFixture(t, false)
	scheme := r.Client.Scheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, dnsv1alpha1.AddToScheme, route53v1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	var intent endpointv1alpha1.EndpointRecordSet
	if err := r.Get(ctx, recordKey, &intent); err != nil {
		t.Fatal(err)
	}
	intent.UID, intent.Generation = "publication-intent", 1
	if err := r.Update(ctx, &intent); err != nil {
		t.Fatal(err)
	}
	var route gatewayv1.HTTPRoute
	if err := r.Get(ctx, routeKey, &route); err != nil {
		t.Fatal(err)
	}
	var gateway gatewayv1.Gateway
	if err := r.Get(ctx, gatewayKey, &gateway); err != nil {
		t.Fatal(err)
	}
	// Fake clients do not allocate UIDs. Assign distinct incarnations at the
	// create boundary, as the API server does, so replacement cannot pass.
	incarnation := 0
	r.Client = fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&route, &gateway, &intent, &dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneUnit{}, &dnsv1alpha1.RecordSet{}).
		WithObjects(&route, &gateway, &intent).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				incarnation++
				obj.SetUID(types.UID(fmt.Sprintf("publication-%d", incarnation)))
				obj.SetGeneration(1)
				return cl.Create(ctx, obj, opts...)
			},
		}).Build()
	zone := &dnsv1alpha1.Zone{
		ObjectMeta: metav1.ObjectMeta{Namespace: "dns-system", Name: "example", UID: "publication-zone", Generation: 1},
		Spec: dnsv1alpha1.ZoneSpec{
			DomainName: "example.com", Provider: route53v1alpha1.ProviderRef,
			ZoneClassRef: dnsv1alpha1.ZoneClassReference{Name: "route53"},
			Adoption:     runtime.RawExtension{Raw: []byte(`{"hostedZoneId":"ZPUBLICATION"}`)},
			AllowedRecordSets: []dnsv1alpha1.AllowedRecordSet{{
				Namespaces: dnsv1alpha1.AllowedRecordSetNamespaces{Selector: metav1.LabelSelector{}},
				Records: []dnsv1alpha1.AllowedRecord{{
					Name: dnsv1alpha1.RecordNamePolicy{Pattern: ".*"}, Types: []dnsv1alpha1.RecordType{dnsv1alpha1.RecordTypeA},
				}},
			}},
		},
	}
	objects := []client.Object{
		zone,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: zone.Namespace}},
		&dnsv1alpha1.Provider{
			ObjectMeta: metav1.ObjectMeta{Name: route53v1alpha1.ProviderName},
			Spec: dnsv1alpha1.ProviderSpec{Versions: []dnsv1alpha1.ProviderVersion{{
				Name: route53v1alpha1.ProviderVersion, Served: true, Storage: true,
				RecordSet: dnsv1alpha1.ProviderRecordSet{SupportedTypes: []dnsv1alpha1.RecordType{dnsv1alpha1.RecordTypeA}},
			}}},
		},
		&dnsv1alpha1.ZoneClass{
			ObjectMeta: metav1.ObjectMeta{Namespace: zone.Namespace, Name: "route53", Generation: 1},
			Spec: dnsv1alpha1.ZoneClassSpec{
				Provider: route53v1alpha1.ProviderRef, ControllerName: route53.DefaultControllerName,
				IdentityRef: &dnsv1alpha1.LocalObjectReference{Name: "route53"},
				Parameters:  runtime.RawExtension{Raw: []byte(`{}`)},
				AllowedZones: dnsv1alpha1.ZoneClassAllowedZones{
					Namespaces: dnsv1alpha1.NamespacePolicy{From: dnsv1alpha1.NamespacesFromSame},
				},
			},
			Status: dnsv1alpha1.ZoneClassStatus{Conditions: []metav1.Condition{{
				Type: "Accepted", Status: metav1.ConditionTrue, ObservedGeneration: 1,
			}}},
		},
		&route53v1alpha1.Route53Identity{
			ObjectMeta: metav1.ObjectMeta{Namespace: zone.Namespace, Name: "route53", Generation: 1},
			Status: route53v1alpha1.Route53IdentityStatus{Conditions: []metav1.Condition{
				{Type: "Accepted", Status: metav1.ConditionTrue, ObservedGeneration: 1},
				{Type: "Ready", Status: metav1.ConditionTrue, ObservedGeneration: 1},
			}},
		},
		&endpointv1alpha1.EndpointProviderCapability{
			ObjectMeta: metav1.ObjectMeta{Name: "route53"},
			Spec: endpointv1alpha1.EndpointProviderCapabilitySpec{
				Provider: route53v1alpha1.ProviderRef,
				Conversion: endpointv1alpha1.EndpointRecordSetConversionAPI{
					Group: route53conversion.GroupName, Version: route53conversion.Version, Resource: route53conversion.Resource,
				},
			},
		},
	}
	for _, obj := range objects {
		if err := r.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(route53conversion.NewHandler())
	t.Cleanup(server.Close)
	endpoint := &endpointcontroller.Reconciler{Client: r.Client, RecordSetNamespace: recordKey.Namespace, RESTConfig: &rest.Config{Host: server.URL}}
	composition := &corezoneunit.ZoneUnitCompositionReconciler{Client: r.Client}
	provider := &publicationProvider{records: map[string]route53.RecordSetResource{}}
	dns := &route53.ZoneReconciler{Client: r.Client, Provider: provider}
	advance := func() {
		t.Helper()
		for range 8 {
			for _, step := range []struct {
				reconciler reconcile.Reconciler
				key        client.ObjectKey
			}{{endpoint, recordKey}, {composition, client.ObjectKeyFromObject(zone)}, {dns, client.ObjectKeyFromObject(zone)}} {
				if _, err := step.reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: step.key}); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	advance()
	var before endpointv1alpha1.EndpointRecordSet
	if err := r.Get(ctx, recordKey, &before); err != nil {
		t.Fatal(err)
	}
	var children dnsv1alpha1.RecordSetList
	if err := r.List(ctx, &children); err != nil {
		t.Fatal(err)
	}
	if len(children.Items) != 1 || children.Items[0].UID == "" || provider.upserts != 1 || len(provider.records) != 1 {
		var unit dnsv1alpha1.ZoneUnit
		if err := r.Get(ctx, client.ObjectKeyFromObject(zone), &unit); err != nil {
			t.Fatal(err)
		}
		t.Fatalf("publication did not reach provider: children=%d upserts=%d records=%#v zone conditions=%#v", len(children.Items), provider.upserts, provider.records, unit.Status.Zone)
	}
	child := children.Items[0]
	for _, status := range []metav1.ConditionStatus{metav1.ConditionFalse, metav1.ConditionUnknown} {
		for _, emptyAddresses := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/empty-addresses=%t", status, emptyAddresses), func(t *testing.T) {
				setRouteAcceptance(t, r, routeKey, status)
				addresses := []gatewayv1.GatewayStatusAddress{{Value: "192.0.2.1"}}
				if emptyAddresses {
					addresses = nil
				}
				setGatewayAddresses(t, r, gatewayKey, addresses)
				// Reconstruct the source reconciler: no in-memory admission
				// observation is available after a controller restart.
				r = &Reconciler{Client: r.Client, EndpointRecordSetNamespace: recordKey.Namespace}
				reconcileRetentionRoute(t, r, routeKey)
				advance()
				var after endpointv1alpha1.EndpointRecordSet
				if err := r.Get(ctx, recordKey, &after); err != nil {
					t.Fatal(err)
				}
				if after.UID != before.UID || !after.DeletionTimestamp.IsZero() || !reflect.DeepEqual(after.Spec, before.Spec) {
					t.Fatalf("established intent changed: before=%#v after=%#v", before, after)
				}
				var current dnsv1alpha1.RecordSet
				if err := r.Get(ctx, client.ObjectKeyFromObject(&child), &current); err != nil {
					t.Fatal(err)
				}
				if current.UID != child.UID || !current.DeletionTimestamp.IsZero() || !reflect.DeepEqual(current.Spec, child.Spec) {
					t.Fatalf("established child changed: before=%#v after=%#v", child, current)
				}
				if provider.deletes != 0 || provider.upserts != 1 || len(provider.records) != 1 {
					t.Fatalf("admission churn changed provider records: deletes=%d upserts=%d records=%#v", provider.deletes, provider.upserts, provider.records)
				}
			})
		}
	}
	// Positive control: the same consumer chain really deletes the provider
	// record when publication intent ends.
	deleteRoute(t, r, routeKey)
	advance()
	if provider.deletes != 1 || len(provider.records) != 0 {
		t.Fatalf("route deletion did not reach provider: deletes=%d records=%#v", provider.deletes, provider.records)
	}
}

type publicationProvider struct {
	route53.Provider
	records map[string]route53.RecordSetResource
	tags    map[string]string
	upserts int
	deletes int
}

func (p *publicationProvider) GetHostedZone(context.Context, string) (route53.HostedZone, error) {
	return route53.HostedZone{ID: "ZPUBLICATION", Name: "example.com"}, nil
}

func (p *publicationProvider) TagHostedZone(_ context.Context, _ string, tags map[string]string) error {
	p.tags = tags
	return nil
}

func (p *publicationProvider) ListRecordSets(context.Context, string) ([]route53.RecordSetResource, error) {
	records := make([]route53.RecordSetResource, 0, len(p.records))
	for _, record := range p.records {
		records = append(records, record)
	}
	return records, nil
}

func (p *publicationProvider) ChangeRecordSets(_ context.Context, _ string, changes []route53.RecordSetChange) (*route53v1alpha1.Route53Change, error) {
	for _, change := range changes {
		switch change.Action {
		case route53.RecordSetChangeActionDelete:
			p.deletes++
			delete(p.records, change.RecordSet.Name)
		case route53.RecordSetChangeActionUpsert:
			p.upserts++
			p.records[change.RecordSet.Name] = change.RecordSet
		default:
			return nil, fmt.Errorf("unexpected provider action %q", change.Action)
		}
	}
	return &route53v1alpha1.Route53Change{ID: "publication-change", Status: route53v1alpha1.Route53ChangeStatusPending}, nil
}

func (p *publicationProvider) GetChange(context.Context, string) (*route53v1alpha1.Route53Change, error) {
	return &route53v1alpha1.Route53Change{ID: "publication-change", Status: route53v1alpha1.Route53ChangeStatusInSync}, nil
}

func TestGatewayEndpointRecordSetRetainsReceiptOnRouteAcceptedFalse(t *testing.T) {
	for _, status := range []metav1.ConditionStatus{metav1.ConditionFalse, metav1.ConditionUnknown} {
		for _, policy := range []gatewayv1.FromNamespaces{gatewayv1.NamespacesFromAll, gatewayv1.NamespacesFromSame, gatewayv1.NamespacesFromSelector} {
			t.Run(string(status)+"/"+string(policy), func(t *testing.T) {
				ctx := context.Background()
				r, _, routeKey, _, recordKey := retentionFixtureWithNamespacePolicy(t, false, policy)
				var before endpointv1alpha1.EndpointRecordSet
				if err := r.Get(ctx, recordKey, &before); err != nil {
					t.Fatal(err)
				}
				before.UID = "retained-endpoint"
				if err := r.Update(ctx, &before); err != nil {
					t.Fatal(err)
				}
				setRouteAcceptance(t, r, routeKey, status)
				reconcileRetentionRoute(t, r, routeKey)

				var after endpointv1alpha1.EndpointRecordSet
				if err := r.Get(ctx, recordKey, &after); err != nil {
					t.Fatal(err)
				}
				if policy != gatewayv1.NamespacesFromAll {
					// The fixture route is in a different namespace. Same and
					// Selector cannot re-prove its attachment while unaccepted.
					if after.DeletionTimestamp.IsZero() {
						t.Fatal("listener without retention permission retained DNS intent")
					}
					return
				}
				assertRetentionRecord(t, r, recordKey, []string{"ha.example.com"}, "192.0.2.1")
				if after.UID != before.UID || after.ResourceVersion != before.ResourceVersion {
					t.Fatal("route admission changed the established DNS intent")
				}
			})
		}
	}
}

func TestGatewayEndpointRecordSetWithdrawalBoundaries(t *testing.T) {
	t.Run("gateway accepted false", func(t *testing.T) {
		ctx := context.Background()
		r, _, haKey, gatewayKey, recordKey := retentionFixture(t, true)
		assertRetentionRecord(t, r, recordKey, []string{"ha.example.com", "payment.example.com"}, "192.0.2.1")
		setGatewayAcceptance(t, r, gatewayKey, metav1.ConditionFalse)
		setGatewayAddresses(t, r, gatewayKey, nil)
		reconcileRetentionRoute(t, r, haKey)

		var record endpointv1alpha1.EndpointRecordSet
		if err := r.Get(ctx, recordKey, &record); err != nil || record.DeletionTimestamp.IsZero() {
			t.Fatalf("explicitly rejected Gateway retained DNS intent: %v", err)
		}
	})
	t.Run("foreign gateway incarnation", func(t *testing.T) {
		ctx := context.Background()
		r, _, haKey, gatewayKey, recordKey := retentionFixture(t, false)
		var gateway gatewayv1.Gateway
		if err := r.Get(ctx, gatewayKey, &gateway); err != nil {
			t.Fatal(err)
		}
		if err := r.Delete(ctx, &gateway); err != nil {
			t.Fatal(err)
		}
		replacement := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: gateway.Namespace, Name: gateway.Name, UID: types.UID("gateway-recreated"), Generation: 1},
			Spec:       gateway.Spec,
			Status:     gatewayv1.GatewayStatus{Addresses: []gatewayv1.GatewayStatusAddress{{Value: "192.0.2.2"}}},
		}
		if err := r.Create(ctx, replacement); err != nil {
			t.Fatal(err)
		}
		reconcileRetentionRoute(t, r, haKey)

		var record endpointv1alpha1.EndpointRecordSet
		if err := r.Get(ctx, recordKey, &record); err != nil {
			t.Fatalf("replacement Gateway lost DNS intent: %v", err)
		}
		if record.Spec.Hostnames[0] != "ha.example.com" ||
			len(record.Spec.Targets) != 1 || record.Spec.Targets[0].Value != "192.0.2.2" {
			t.Fatalf("replacement Gateway did not publish current DNS intent: %#v", record.Spec)
		}
	})
	t.Run("changed listener ownership", func(t *testing.T) {
		ctx := context.Background()
		r, _, haKey, gatewayKey, recordKey := retentionFixture(t, false)
		var gateway gatewayv1.Gateway
		if err := r.Get(ctx, gatewayKey, &gateway); err != nil {
			t.Fatal(err)
		}
		sameNamespace := gatewayv1.NamespacesFromSame
		gateway.Spec.Listeners[0].AllowedRoutes.Namespaces.From = &sameNamespace
		if err := r.Update(ctx, &gateway); err != nil {
			t.Fatal(err)
		}
		reconcileRetentionRoute(t, r, haKey)

		var record endpointv1alpha1.EndpointRecordSet
		if err := r.Get(ctx, recordKey, &record); err != nil || record.DeletionTimestamp.IsZero() {
			t.Fatalf("changed listener ownership retained DNS intent: %v", err)
		}
	})
}

func TestGatewayEndpointRecordSetAddsUnreceiptedHostnameOnlyWhenAccepted(t *testing.T) {
	for _, status := range []metav1.ConditionStatus{metav1.ConditionFalse, metav1.ConditionUnknown} {
		t.Run(string(status), func(t *testing.T) {
			ctx := context.Background()
			r, _, routeKey, _, recordKey := retentionFixture(t, false)
			var route gatewayv1.HTTPRoute
			if err := r.Get(ctx, routeKey, &route); err != nil {
				t.Fatal(err)
			}
			route.Spec.Hostnames = append(route.Spec.Hostnames, "new.example.com")
			route.Generation++
			if err := r.Update(ctx, &route); err != nil {
				t.Fatal(err)
			}
			setRouteAcceptance(t, r, routeKey, status)
			reconcileRetentionRoute(t, r, routeKey)
			assertRetentionRecord(t, r, recordKey, []string{"ha.example.com"}, "192.0.2.1")

			setRouteAcceptance(t, r, routeKey, metav1.ConditionTrue)
			reconcileRetentionRoute(t, r, routeKey)
			assertRetentionRecord(t, r, recordKey, []string{"ha.example.com", "new.example.com"}, "192.0.2.1")
		})
	}
}

func TestGatewayEndpointRecordSetWithdrawsUnacceptedIntentOrIdentityRemoval(t *testing.T) {
	for _, status := range []metav1.ConditionStatus{metav1.ConditionFalse, metav1.ConditionUnknown} {
		for _, removal := range []string{"hostname", "parentRef", "route", "route UID"} {
			t.Run(string(status)+"/"+removal, func(t *testing.T) {
				ctx := context.Background()
				r, _, routeKey, _, recordKey := retentionFixture(t, false)
				setRouteAcceptance(t, r, routeKey, status)
				var route gatewayv1.HTTPRoute
				if err := r.Get(ctx, routeKey, &route); err != nil {
					t.Fatal(err)
				}
				switch removal {
				case "hostname":
					route.Spec.Hostnames = nil
				case "parentRef":
					route.Spec.ParentRefs = nil
				case "route":
					deleteRoute(t, r, routeKey)
				case "route UID":
					route.Finalizers = nil
					if err := r.Update(ctx, &route); err != nil {
						t.Fatal(err)
					}
					if err := r.Delete(ctx, &route); err != nil {
						t.Fatal(err)
					}
					replacement := retentionRoute(route.Namespace, route.Name, "replacement-route", "ha.example.com", status)
					if err := r.Create(ctx, replacement); err != nil {
						t.Fatal(err)
					}
				}
				if removal == "hostname" || removal == "parentRef" {
					route.Generation++
					if err := r.Update(ctx, &route); err != nil {
						t.Fatal(err)
					}
					setRouteAcceptance(t, r, routeKey, status)
				}
				reconcileRetentionRoute(t, r, routeKey)
				var record endpointv1alpha1.EndpointRecordSet
				if err := r.Get(ctx, recordKey, &record); err != nil || record.DeletionTimestamp.IsZero() {
					t.Fatalf("removed %s retained DNS intent: %v", removal, err)
				}
			})
		}
	}
}

func TestGatewayEndpointRecordSetDoesNotPublishUnreceiptedHostnameDuringAddressLoss(t *testing.T) {
	ctx := context.Background()
	r, _, haKey, gatewayKey, recordKey := retentionFixture(t, false)
	setGatewayAddresses(t, r, gatewayKey, nil)

	pending := retentionRoute("app", "pending", "pending-route", "pending.example.com", metav1.ConditionUnknown)
	if err := r.Create(ctx, pending); err != nil {
		t.Fatal(err)
	}
	reconcileRetentionRoute(t, r, client.ObjectKeyFromObject(pending))

	assertRetentionRecord(t, r, recordKey, []string{"ha.example.com"}, "192.0.2.1")
	var record endpointv1alpha1.EndpointRecordSet
	if err := r.Get(ctx, recordKey, &record); err != nil {
		t.Fatal(err)
	}
	for _, hostname := range record.Spec.Hostnames {
		if hostname == "pending.example.com" {
			t.Fatal("unaccepted hostname inherited an existing EndpointRecordSet target")
		}
	}

	setRouteAcceptance(t, r, haKey, metav1.ConditionTrue)
	setGatewayAddresses(t, r, gatewayKey, []gatewayv1.GatewayStatusAddress{{Value: "192.0.2.2"}})

	reconcileRetentionRoute(t, r, haKey)
	assertRetentionRecord(t, r, recordKey, []string{"ha.example.com"}, "192.0.2.2")
}

type conflictOnceClient struct {
	client.Client
	fail bool
}

func (c *conflictOnceClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if c.fail {
		if _, ok := obj.(*endpointv1alpha1.EndpointRecordSet); ok {
			c.fail = false
			return apierrors.NewConflict(
				schema.GroupResource{Group: "endpoint.dns.appthrust.io", Resource: "endpointrecordsets"},
				obj.GetName(),
				errors.New("simulated concurrent update"),
			)
		}
	}
	return c.Client.Update(ctx, obj, opts...)
}

func TestGatewayEndpointRecordSetRetainsReceiptAfterUpdateConflictRetry(t *testing.T) {
	r, _, routeKey, gatewayKey, recordKey := retentionFixture(t, false)
	conflicting := &conflictOnceClient{Client: r.Client, fail: true}
	r.Client = conflicting
	setGatewayAcceptance(t, r, gatewayKey, metav1.ConditionUnknown)
	setGatewayAddresses(t, r, gatewayKey, []gatewayv1.GatewayStatusAddress{{Value: "192.0.2.2"}})

	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: routeKey}); err == nil ||
		!apierrors.IsConflict(err) {
		t.Fatalf("first reconcile error = %v, want conflict", err)
	}
	reconcileRetentionRoute(t, r, routeKey)
	assertRetentionRecord(t, r, recordKey, []string{"ha.example.com"}, "192.0.2.2")
}

func TestGatewayEndpointRecordSetDoesNotRetainWithoutReceiptOrSelectorProof(t *testing.T) {
	for _, test := range []struct {
		name   string
		policy gatewayv1.FromNamespaces
		legacy bool
	}{
		{name: "legacy receipt", policy: gatewayv1.NamespacesFromAll, legacy: true},
		{name: "dynamic namespace selector", policy: gatewayv1.NamespacesFromSelector},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			r, _, haKey, gatewayKey, recordKey := retentionFixtureWithNamespacePolicy(t, false, test.policy)
			if test.legacy {
				var record endpointv1alpha1.EndpointRecordSet
				if err := r.Get(ctx, recordKey, &record); err != nil {
					t.Fatal(err)
				}
				if record.Annotations[testAdmissionReceiptAnnotation] == "" {
					t.Fatal("EndpointRecordSet did not record its admitted ownership bindings")
				}
				delete(record.Annotations, testAdmissionReceiptAnnotation)
				if err := r.Update(ctx, &record); err != nil {
					t.Fatal(err)
				}
			}
			setRouteAcceptance(t, r, haKey, metav1.ConditionUnknown)
			setGatewayAddresses(t, r, gatewayKey, nil)
			reconcileRetentionRoute(t, r, haKey)

			var record endpointv1alpha1.EndpointRecordSet
			if err := r.Get(ctx, recordKey, &record); err != nil || record.DeletionTimestamp.IsZero() {
				t.Fatalf("unproven retained DNS intent: %v", err)
			}
		})
	}
}

func retentionFixture(t *testing.T, includePayment bool) (*Reconciler, client.ObjectKey, client.ObjectKey, client.ObjectKey, client.ObjectKey) {
	return retentionFixtureWithNamespacePolicy(t, includePayment, gatewayv1.NamespacesFromAll)
}

func retentionFixtureWithNamespacePolicy(t *testing.T, includePayment bool, namespacePolicy gatewayv1.FromNamespaces) (*Reconciler, client.ObjectKey, client.ObjectKey, client.ObjectKey, client.ObjectKey) {
	t.Helper()
	routeNamespaces := &gatewayv1.RouteNamespaces{From: &namespacePolicy}
	if namespacePolicy == gatewayv1.NamespacesFromSelector {
		routeNamespaces.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"dns-api.example/tenant": "app"}}
	}
	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Namespace: "platform", Name: "public", UID: types.UID("gateway-original"), Generation: 1},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "test-gateway",
			Listeners: []gatewayv1.Listener{{
				Name:          "https",
				AllowedRoutes: &gatewayv1.AllowedRoutes{Namespaces: routeNamespaces},
			}},
		},
		Status: gatewayv1.GatewayStatus{Addresses: []gatewayv1.GatewayStatusAddress{{Value: "192.0.2.1"}}},
	}
	ha := retentionRoute("app", "ha", "ha-route", "ha.example.com", metav1.ConditionTrue)
	objects := []client.Object{gateway, ha}
	paymentKey := client.ObjectKey{}
	if includePayment {
		payment := retentionRoute("app", "payment", "payment-route", "payment.example.com", metav1.ConditionTrue)
		objects = append(objects, payment)
		paymentKey = client.ObjectKeyFromObject(payment)
	}
	k8sClient := fake.NewClientBuilder().WithScheme(testScheme(t)).WithStatusSubresource(ha, gateway, &endpointv1alpha1.EndpointRecordSet{}).WithObjects(objects...).Build()
	r := &Reconciler{Client: k8sClient, EndpointRecordSetNamespace: "dns-system"}
	if includePayment {
		reconcileRetentionRoute(t, r, paymentKey)
	}
	reconcileRetentionRoute(t, r, client.ObjectKeyFromObject(ha))
	markUnprotected(t, r, client.ObjectKey{Namespace: "dns-system", Name: generatedGatewayEndpointRecordSetName("platform", "public")})
	return r, paymentKey, client.ObjectKeyFromObject(ha), client.ObjectKeyFromObject(gateway), client.ObjectKey{Namespace: "dns-system", Name: generatedGatewayEndpointRecordSetName("platform", "public")}
}

// These admission-receipt tests exercise ordinary, non-external publication.
// External protection and capture are covered by withdrawal_test.go.
func markUnprotected(t *testing.T, r *Reconciler, key client.ObjectKey) {
	t.Helper()
	var record endpointv1alpha1.EndpointRecordSet
	if err := r.Get(context.Background(), key, &record); err != nil {
		t.Fatal(err)
	}
	record.Status.Conditions = []metav1.Condition{{Type: "WithdrawalProtected", Status: metav1.ConditionFalse, ObservedGeneration: record.Generation, Reason: "ProviderCapability"}}
	if err := r.Status().Update(context.Background(), &record); err != nil {
		t.Fatal(err)
	}
}

func retentionRoute(namespace, name, uid, hostname string, status metav1.ConditionStatus) *gatewayv1.HTTPRoute {
	section := gatewayv1.SectionName("https")
	gatewayNamespace := gatewayv1.Namespace("platform")
	return &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, UID: types.UID(uid), Generation: 1},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: []gatewayv1.ParentReference{{
				Namespace:   &gatewayNamespace,
				Name:        "public",
				SectionName: &section,
			}}},
			Hostnames: []gatewayv1.Hostname{gatewayv1.Hostname(hostname)},
		},
		Status: gatewayv1.HTTPRouteStatus{RouteStatus: gatewayv1.RouteStatus{Parents: []gatewayv1.RouteParentStatus{{
			ParentRef: gatewayv1.ParentReference{Namespace: &gatewayNamespace, Name: "public", SectionName: &section},
			Conditions: []metav1.Condition{{
				Type:               string(gatewayv1.RouteConditionAccepted),
				Status:             status,
				ObservedGeneration: 1,
			}},
		}}}},
	}
}

func reconcileRetentionRoute(t *testing.T, r *Reconciler, key client.ObjectKey) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
}

func setRouteAcceptance(t *testing.T, r *Reconciler, key client.ObjectKey, status metav1.ConditionStatus) {
	t.Helper()
	ctx := context.Background()
	var route gatewayv1.HTTPRoute
	if err := r.Get(ctx, key, &route); err != nil {
		t.Fatal(err)
	}
	route.Status.Parents[0].Conditions[0].Status = status
	route.Status.Parents[0].Conditions[0].ObservedGeneration = route.Generation
	if err := r.Status().Update(ctx, &route); err != nil {
		t.Fatal(err)
	}
}
func setGatewayAcceptance(t *testing.T, r *Reconciler, key client.ObjectKey, status metav1.ConditionStatus) {
	t.Helper()
	ctx := context.Background()
	var gateway gatewayv1.Gateway
	if err := r.Get(ctx, key, &gateway); err != nil {
		t.Fatal(err)
	}
	gateway.Status.Conditions = []metav1.Condition{{
		Type:               string(gatewayv1.GatewayConditionAccepted),
		Status:             status,
		ObservedGeneration: gateway.Generation,
	}}
	if err := r.Status().Update(ctx, &gateway); err != nil {
		t.Fatal(err)
	}
}

func setGatewayAddresses(t *testing.T, r *Reconciler, key client.ObjectKey, addresses []gatewayv1.GatewayStatusAddress) {
	t.Helper()
	ctx := context.Background()
	var gateway gatewayv1.Gateway
	if err := r.Get(ctx, key, &gateway); err != nil {
		t.Fatal(err)
	}
	gateway.Status.Addresses = addresses
	if err := r.Status().Update(ctx, &gateway); err != nil {
		t.Fatal(err)
	}
}

func deleteRoute(t *testing.T, r *Reconciler, key client.ObjectKey) {
	t.Helper()
	ctx := context.Background()
	var route gatewayv1.HTTPRoute
	if err := r.Get(ctx, key, &route); err != nil {
		t.Fatal(err)
	}
	if err := r.Delete(ctx, &route); err != nil {
		t.Fatal(err)
	}
	reconcileRetentionRoute(t, r, key)
}

func assertRetentionRecord(t *testing.T, r *Reconciler, key client.ObjectKey, hostnames []string, target string) {
	t.Helper()
	var record endpointv1alpha1.EndpointRecordSet
	if err := r.Get(context.Background(), key, &record); err != nil {
		t.Fatal(err)
	}
	if !record.DeletionTimestamp.IsZero() {
		t.Fatal("retained EndpointRecordSet is being deleted")
	}
	if len(record.Spec.Hostnames) != len(hostnames) {
		t.Fatalf("hostnames = %#v, want %#v", record.Spec.Hostnames, hostnames)
	}
	for i, hostname := range hostnames {
		if record.Spec.Hostnames[i] != hostname {
			t.Fatalf("hostnames = %#v, want %#v", record.Spec.Hostnames, hostnames)
		}
	}
	if len(record.Spec.Targets) != 1 || record.Spec.Targets[0].Value != target {
		t.Fatalf("targets = %#v, want %q", record.Spec.Targets, target)
	}
}
