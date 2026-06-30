package gateway

import (
	"context"
	"testing"

	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestReconcilerCreatesEndpointRecordSetForAcceptedHTTPRouteParent(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	section := gatewayv1.SectionName("https")
	gatewayNamespace := gatewayv1.Namespace("platform")
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "web"},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Namespace:   &gatewayNamespace,
					Name:        "public",
					SectionName: &section,
				}},
			},
			Hostnames: []gatewayv1.Hostname{"api.example.com"},
		},
		Status: gatewayv1.HTTPRouteStatus{
			RouteStatus: gatewayv1.RouteStatus{
				Parents: []gatewayv1.RouteParentStatus{{
					ParentRef: gatewayv1.ParentReference{
						Namespace:   &gatewayNamespace,
						Name:        "public",
						SectionName: &section,
					},
					Conditions: []metav1.Condition{{Type: string(gatewayv1.RouteConditionAccepted), Status: metav1.ConditionTrue}},
				}},
			},
		},
	}
	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Namespace: "platform", Name: "public"},
		Spec: gatewayv1.GatewaySpec{
			Listeners: []gatewayv1.Listener{{Name: section}},
		},
		Status: gatewayv1.GatewayStatus{
			Addresses: []gatewayv1.GatewayStatusAddress{{Value: "k8s-public-123.ap-northeast-1.elb.amazonaws.com"}},
		},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(route, gateway).
		Build()

	reconciler := &Reconciler{Client: k8sClient, Scheme: scheme, EndpointRecordSetNamespace: "dns-api-system"}
	if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(route)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var list endpointv1alpha1.EndpointRecordSetList
	if err := k8sClient.List(ctx, &list, client.InNamespace("dns-api-system")); err != nil {
		t.Fatalf("list EndpointRecordSets: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("EndpointRecordSets = %d, want 1", len(list.Items))
	}
	got := list.Items[0]
	if len(got.Spec.Hostnames) != 1 || got.Spec.Hostnames[0] != "api.example.com" {
		t.Fatalf("hostnames = %#v", got.Spec.Hostnames)
	}
	if len(got.Spec.Targets) != 1 || got.Spec.Targets[0].Type != endpointv1alpha1.EndpointTargetTypeHostname || got.Spec.Targets[0].Value != "k8s-public-123.ap-northeast-1.elb.amazonaws.com" {
		t.Fatalf("targets = %#v", got.Spec.Targets)
	}
}

func TestReconcilerDeduplicatesEndpointRecordSetAcrossRoutesOnSameGateway(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	section := gatewayv1.SectionName("https")
	gatewayNamespace := gatewayv1.Namespace("platform")
	routeA := acceptedHTTPRoute("app", "web", gatewayNamespace, section, "app.example.com")
	routeB := acceptedHTTPRoute("app", "redirect", gatewayNamespace, section, "app.example.com")
	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Namespace: "platform", Name: "public"},
		Spec: gatewayv1.GatewaySpec{
			Listeners: []gatewayv1.Listener{{Name: section}},
		},
		Status: gatewayv1.GatewayStatus{
			Addresses: []gatewayv1.GatewayStatusAddress{{Value: "k8s-public-123.ap-northeast-1.elb.amazonaws.com"}},
		},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(routeA, routeB, gateway).
		Build()

	reconciler := &Reconciler{Client: k8sClient, Scheme: scheme, EndpointRecordSetNamespace: "dns-api-system"}
	if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(routeA)}); err != nil {
		t.Fatalf("Reconcile routeA returned error: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(routeB)}); err != nil {
		t.Fatalf("Reconcile routeB returned error: %v", err)
	}

	var list endpointv1alpha1.EndpointRecordSetList
	if err := k8sClient.List(ctx, &list, client.InNamespace("dns-api-system")); err != nil {
		t.Fatalf("list EndpointRecordSets: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("EndpointRecordSets = %d, want 1", len(list.Items))
	}
	got := list.Items[0]
	if len(got.Spec.Hostnames) != 1 || got.Spec.Hostnames[0] != "app.example.com" {
		t.Fatalf("hostnames = %#v", got.Spec.Hostnames)
	}
	if got.Labels[generatedLabelRouteName] != "" {
		t.Fatalf("route label = %q, want gateway-owned EndpointRecordSet", got.Labels[generatedLabelRouteName])
	}
}

func TestReconcilerWaitsUntilAllHTTPRouteParentsAreAccepted(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	section := gatewayv1.SectionName("https")
	gatewayNamespace := gatewayv1.Namespace("platform")
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "web"},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Namespace:   &gatewayNamespace,
					Name:        "public",
					SectionName: &section,
				}},
			},
			Hostnames: []gatewayv1.Hostname{"api.example.com"},
		},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(route).
		Build()

	reconciler := &Reconciler{Client: k8sClient, Scheme: scheme, EndpointRecordSetNamespace: "dns-api-system"}
	if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(route)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var list endpointv1alpha1.EndpointRecordSetList
	if err := k8sClient.List(ctx, &list, client.InNamespace("dns-api-system")); err != nil {
		t.Fatalf("list EndpointRecordSets: %v", err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("EndpointRecordSets = %d, want 0", len(list.Items))
	}
}

func acceptedHTTPRoute(namespace, name string, gatewayNamespace gatewayv1.Namespace, section gatewayv1.SectionName, hostname gatewayv1.Hostname) *gatewayv1.HTTPRoute {
	return &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Namespace:   &gatewayNamespace,
					Name:        "public",
					SectionName: &section,
				}},
			},
			Hostnames: []gatewayv1.Hostname{hostname},
		},
		Status: gatewayv1.HTTPRouteStatus{
			RouteStatus: gatewayv1.RouteStatus{
				Parents: []gatewayv1.RouteParentStatus{{
					ParentRef: gatewayv1.ParentReference{
						Namespace:   &gatewayNamespace,
						Name:        "public",
						SectionName: &section,
					},
					Conditions: []metav1.Condition{{Type: string(gatewayv1.RouteConditionAccepted), Status: metav1.ConditionTrue}},
				}},
			},
		},
	}
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := endpointv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add endpoint scheme: %v", err)
	}
	if err := gatewayv1.Install(scheme); err != nil {
		t.Fatalf("add gateway scheme: %v", err)
	}
	return scheme
}
