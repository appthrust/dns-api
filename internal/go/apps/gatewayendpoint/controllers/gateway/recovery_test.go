package gateway

import (
	"context"
	"testing"

	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func recoveryFixture(t *testing.T) (*Reconciler, *gatewayv1.HTTPRoute, client.ObjectKey) {
	t.Helper()
	scheme := testScheme(t)
	section := gatewayv1.SectionName("https")
	route := acceptedHTTPRoute("app", "web", "platform", section, "app.example.com")
	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Namespace: "platform", Name: "public"},
		Spec:       gatewayv1.GatewaySpec{Listeners: []gatewayv1.Listener{{Name: section}}},
		Status:     gatewayv1.GatewayStatus{Addresses: []gatewayv1.GatewayStatusAddress{{Value: "192.0.2.1"}}},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(route).
		WithIndex(&gatewayv1.HTTPRoute{}, httpRouteGatewayIndex, routeGatewayIndexKeys).
		WithObjects(route, gateway).Build()
	r := &Reconciler{Client: c, Scheme: scheme, EndpointRecordSetNamespace: "dns-system"}
	reconcileRecoveryRoute(t, r, client.ObjectKeyFromObject(route))
	return r, route, client.ObjectKey{Namespace: "dns-system", Name: generatedGatewayEndpointRecordSetName("platform", "public")}
}

func reconcileRecoveryRoute(t *testing.T, r *Reconciler, key client.ObjectKey) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
}

func TestEndpointRecordSetReconcilePreservesUnchangedAndTerminatingObjects(t *testing.T) {
	ctx := context.Background()
	r, route, key := recoveryFixture(t)
	var record endpointv1alpha1.EndpointRecordSet
	if err := r.Get(ctx, key, &record); err != nil {
		t.Fatal(err)
	}
	before := record.ResourceVersion
	reconcileRecoveryRoute(t, r, client.ObjectKeyFromObject(route))
	if err := r.Get(ctx, key, &record); err != nil {
		t.Fatal(err)
	}
	if record.ResourceVersion != before {
		t.Fatal("unchanged DNS intent was rewritten, producing another watch event")
	}

	record.Finalizers = []string{"endpoint.dns.appthrust.io/generated-recordsets"}
	if err := r.Update(ctx, &record); err != nil {
		t.Fatal(err)
	}
	if err := r.Delete(ctx, &record); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, key, &record); err != nil {
		t.Fatal(err)
	}
	before = record.ResourceVersion
	reconcileRecoveryRoute(t, r, client.ObjectKeyFromObject(route))
	if err := r.Get(ctx, key, &record); err != nil {
		t.Fatal(err)
	}
	if record.DeletionTimestamp.IsZero() || record.ResourceVersion != before {
		t.Fatal("terminating DNS intent was mutated while provider cleanup was pending")
	}
}

func TestEndpointRecordSetDeletionRecoversOnlyCurrentPublication(t *testing.T) {
	for _, accepted := range []bool{true, false} {
		name := "accepted"
		if !accepted {
			name = "withdrawn"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			r, route, key := recoveryFixture(t)
			var record endpointv1alpha1.EndpointRecordSet
			if err := r.Get(ctx, key, &record); err != nil {
				t.Fatal(err)
			}
			record.Finalizers = []string{"endpoint.dns.appthrust.io/generated-recordsets"}
			if err := r.Update(ctx, &record); err != nil {
				t.Fatal(err)
			}
			if err := r.Delete(ctx, &record); err != nil {
				t.Fatal(err)
			}
			reconcileRecoveryRoute(t, r, client.ObjectKeyFromObject(route))
			if !accepted {
				if err := r.Get(ctx, client.ObjectKeyFromObject(route), route); err != nil {
					t.Fatal(err)
				}
				route.Status.Parents[0].Conditions[0].Status = metav1.ConditionFalse
				if err := r.Status().Update(ctx, route); err != nil {
					t.Fatal(err)
				}
			}
			if err := r.Get(ctx, key, &record); err != nil {
				t.Fatal(err)
			}
			record.Finalizers = nil
			if err := r.Update(ctx, &record); err != nil {
				t.Fatal(err)
			}
			// Only the child deletion wakes the parent; do not change the Route
			// or Gateway to rescue the publication as the old controller required.
			for _, req := range r.mapEndpointRecordSetToHTTPRoutes(ctx, &record) {
				reconcileRecoveryRoute(t, r, req.NamespacedName)
			}
			var recovered endpointv1alpha1.EndpointRecordSet
			err := r.Get(ctx, key, &recovered)
			if !accepted {
				if !apierrors.IsNotFound(err) {
					t.Fatalf("withdrawn publication was regenerated: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("accepted publication did not recover after deletion: %v", err)
			}
			if len(recovered.Spec.Hostnames) != 1 || recovered.Spec.Hostnames[0] != "app.example.com" ||
				len(recovered.Spec.Targets) != 1 || recovered.Spec.Targets[0].Value != "192.0.2.1" || !recovered.DeletionTimestamp.IsZero() {
				t.Fatalf("recovered DNS intent is not current: %+v", recovered)
			}
		})
	}
}
