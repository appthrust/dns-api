package gateway

import (
	"context"
	"testing"

	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestGatewayEndpointRecordSetReconcilesReceiptGatewaysAfterParentChange(t *testing.T) {
	t.Run("parent removal preserves the remaining Gateway", func(t *testing.T) {
		ctx := context.Background()
		r, routeKey, gatewayARecordKey, gatewayBRecordKey := receiptSyncFixture(t)
		detachReceiptSyncGatewayA(t, r, routeKey)
		reconcileRetentionRoute(t, r, routeKey)

		var removed endpointv1alpha1.EndpointRecordSet
		if err := r.Get(ctx, gatewayARecordKey, &removed); !apierrors.IsNotFound(err) {
			t.Fatalf("detached Gateway retained DNS intent: %v", err)
		}
		assertRetentionRecord(t, r, gatewayBRecordKey, []string{"shared.example.com"}, "192.0.2.20")
	})
	t.Run("route deletion cleans a detached Gateway not yet reconciled", func(t *testing.T) {
		ctx := context.Background()
		r, routeKey, gatewayARecordKey, gatewayBRecordKey := receiptSyncFixture(t)
		detachReceiptSyncGatewayA(t, r, routeKey)
		deleteRoute(t, r, routeKey)

		for _, key := range []client.ObjectKey{gatewayARecordKey, gatewayBRecordKey} {
			var record endpointv1alpha1.EndpointRecordSet
			if err := r.Get(ctx, key, &record); !apierrors.IsNotFound(err) {
				t.Fatalf("deleted route left prior Gateway DNS intent %s: %v", key, err)
			}
		}
	})
}

func TestGatewayEndpointRecordSetDoesNotMintReceiptWithoutCurrentAddresses(t *testing.T) {
	ctx := context.Background()
	r, _, haKey, gatewayKey, recordKey := retentionFixture(t, false)
	setGatewayAddresses(t, r, gatewayKey, nil)

	fresh := retentionRoute("app", "fresh", "fresh-route", "ha.example.com", metav1.ConditionTrue)
	if err := r.Create(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	freshKey := client.ObjectKeyFromObject(fresh)
	reconcileRetentionRoute(t, r, freshKey)
	setRouteAcceptance(t, r, freshKey, metav1.ConditionUnknown)
	deleteRoute(t, r, haKey)

	var record endpointv1alpha1.EndpointRecordSet
	if err := r.Get(ctx, recordKey, &record); !apierrors.IsNotFound(err) {
		t.Fatalf("fresh route inherited a receipt while addresses were absent: %v", err)
	}
}

func TestGatewayEndpointRecordSetKeepsReceiptAcrossRepeatedAddressLoss(t *testing.T) {
	r, _, haKey, gatewayKey, recordKey := retentionFixture(t, false)
	setGatewayAddresses(t, r, gatewayKey, nil)

	for range 2 {
		reconcileRetentionRoute(t, r, haKey)
		assertRetentionRecord(t, r, recordKey, []string{"ha.example.com"}, "192.0.2.1")
	}

	setRouteAcceptance(t, r, haKey, metav1.ConditionUnknown)
	reconcileRetentionRoute(t, r, haKey)
	assertRetentionRecord(t, r, recordKey, []string{"ha.example.com"}, "192.0.2.1")
}

func receiptSyncFixture(t *testing.T) (*Reconciler, client.ObjectKey, client.ObjectKey, client.ObjectKey) {
	t.Helper()
	gatewayNamespace := gatewayv1.Namespace("platform")
	section := gatewayv1.SectionName("https")
	parentA := gatewayv1.ParentReference{Namespace: &gatewayNamespace, Name: "gateway-a", SectionName: &section}
	parentB := gatewayv1.ParentReference{Namespace: &gatewayNamespace, Name: "gateway-b", SectionName: &section}
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "web", UID: types.UID("shared-route"), Generation: 1},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: []gatewayv1.ParentReference{parentA, parentB}},
			Hostnames:       []gatewayv1.Hostname{"shared.example.com"},
		},
		Status: gatewayv1.HTTPRouteStatus{RouteStatus: gatewayv1.RouteStatus{Parents: []gatewayv1.RouteParentStatus{
			receiptSyncAcceptedParent(parentA, 1),
			receiptSyncAcceptedParent(parentB, 1),
		}}},
	}
	gatewayA := receiptSyncGateway("gateway-a", "gateway-a-uid", "192.0.2.10", section)
	gatewayB := receiptSyncGateway("gateway-b", "gateway-b-uid", "192.0.2.20", section)
	k8sClient := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithStatusSubresource(route, gatewayA, gatewayB).
		WithObjects(route, gatewayA, gatewayB).Build()
	r := &Reconciler{Client: k8sClient, EndpointRecordSetNamespace: "dns-system"}
	routeKey := client.ObjectKeyFromObject(route)
	reconcileRetentionRoute(t, r, routeKey)
	gatewayARecordKey := client.ObjectKey{Namespace: "dns-system", Name: generatedGatewayEndpointRecordSetName(gatewayA.Namespace, gatewayA.Name)}
	gatewayBRecordKey := client.ObjectKey{Namespace: "dns-system", Name: generatedGatewayEndpointRecordSetName(gatewayB.Namespace, gatewayB.Name)}
	assertRetentionRecord(t, r, gatewayARecordKey, []string{"shared.example.com"}, "192.0.2.10")
	assertRetentionRecord(t, r, gatewayBRecordKey, []string{"shared.example.com"}, "192.0.2.20")
	return r, routeKey, gatewayARecordKey, gatewayBRecordKey
}

func receiptSyncGateway(name, uid, address string, section gatewayv1.SectionName) *gatewayv1.Gateway {
	allNamespaces := gatewayv1.NamespacesFromAll
	return &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Namespace: "platform", Name: name, UID: types.UID(uid), Generation: 1},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "test-gateway",
			Listeners: []gatewayv1.Listener{{
				Name: section,
				AllowedRoutes: &gatewayv1.AllowedRoutes{Namespaces: &gatewayv1.RouteNamespaces{
					From: &allNamespaces,
				}},
			}},
		},
		Status: gatewayv1.GatewayStatus{Addresses: []gatewayv1.GatewayStatusAddress{{Value: address}}},
	}
}

func receiptSyncAcceptedParent(parent gatewayv1.ParentReference, generation int64) gatewayv1.RouteParentStatus {
	return gatewayv1.RouteParentStatus{
		ParentRef: parent,
		Conditions: []metav1.Condition{{
			Type:               string(gatewayv1.RouteConditionAccepted),
			Status:             metav1.ConditionTrue,
			ObservedGeneration: generation,
		}},
	}
}

func detachReceiptSyncGatewayA(t *testing.T, r *Reconciler, routeKey client.ObjectKey) {
	t.Helper()
	ctx := context.Background()
	var route gatewayv1.HTTPRoute
	if err := r.Get(ctx, routeKey, &route); err != nil {
		t.Fatal(err)
	}
	route.Spec.ParentRefs = route.Spec.ParentRefs[1:]
	route.Generation++
	if err := r.Update(ctx, &route); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, routeKey, &route); err != nil {
		t.Fatal(err)
	}
	route.Status.Parents = []gatewayv1.RouteParentStatus{receiptSyncAcceptedParent(route.Spec.ParentRefs[0], route.Generation)}
	if err := r.Status().Update(ctx, &route); err != nil {
		t.Fatal(err)
	}
}
