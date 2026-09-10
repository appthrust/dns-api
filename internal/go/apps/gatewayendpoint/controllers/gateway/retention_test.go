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

func TestGatewayEndpointRecordSetRevokesExplicitFalseAndForeignGateway(t *testing.T) {
	t.Run("explicit false", func(t *testing.T) {
		ctx := context.Background()
		r, _, haKey, _, recordKey := retentionFixture(t, false)
		setRouteAcceptance(t, r, haKey, metav1.ConditionFalse)
		reconcileRetentionRoute(t, r, haKey)

		var record endpointv1alpha1.EndpointRecordSet
		if err := r.Get(ctx, recordKey, &record); !apierrors.IsNotFound(err) {
			t.Fatalf("explicitly rejected route retained DNS intent: %v", err)
		}
	})
	t.Run("gateway accepted false", func(t *testing.T) {
		ctx := context.Background()
		r, _, haKey, gatewayKey, recordKey := retentionFixture(t, false)
		setGatewayAcceptance(t, r, gatewayKey, metav1.ConditionFalse)
		setGatewayAddresses(t, r, gatewayKey, nil)
		reconcileRetentionRoute(t, r, haKey)

		var record endpointv1alpha1.EndpointRecordSet
		if err := r.Get(ctx, recordKey, &record); !apierrors.IsNotFound(err) {
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
		if err := r.Get(ctx, recordKey, &record); !apierrors.IsNotFound(err) {
			t.Fatalf("replacement Gateway inherited old targets: %v", err)
		}
		reconcileRetentionRoute(t, r, haKey)
		assertRetentionRecord(t, r, recordKey, []string{"ha.example.com"}, "192.0.2.2")
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
		if err := r.Get(ctx, recordKey, &record); !apierrors.IsNotFound(err) {
			t.Fatalf("changed listener ownership retained DNS intent: %v", err)
		}
	})
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
			if err := r.Get(ctx, recordKey, &record); !apierrors.IsNotFound(err) {
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
	k8sClient := fake.NewClientBuilder().WithScheme(testScheme(t)).WithStatusSubresource(ha, gateway).WithObjects(objects...).Build()
	r := &Reconciler{Client: k8sClient, EndpointRecordSetNamespace: "dns-system"}
	if includePayment {
		reconcileRetentionRoute(t, r, paymentKey)
	}
	reconcileRetentionRoute(t, r, client.ObjectKeyFromObject(ha))
	return r, paymentKey, client.ObjectKeyFromObject(ha), client.ObjectKeyFromObject(gateway), client.ObjectKey{Namespace: "dns-system", Name: generatedGatewayEndpointRecordSetName("platform", "public")}
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
