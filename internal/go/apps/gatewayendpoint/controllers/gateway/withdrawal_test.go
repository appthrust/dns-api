package gateway

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/appthrust/dns-api/internal/go/apps/endpoint/retirement"
	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func gatewayWithdrawalERS() *endpointv1alpha1.EndpointRecordSet {
	return &endpointv1alpha1.EndpointRecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "dns", Name: generatedGatewayEndpointRecordSetName("app", "public"), UID: "ers-uid", Generation: 3,
			Finalizers: []string{endpointv1alpha1.GeneratedRecordSetsFinalizer}, Labels: map[string]string{
				managedByLabel: managedByValue, generatedLabelGatewayNamespace: "app", generatedLabelGatewayName: "public",
				generatedLabelRouteNamespace: "app", generatedLabelRouteName: "route",
			}},
		Spec: endpointv1alpha1.EndpointRecordSetSpec{Hostnames: []string{"h1.example.com", "h2.example.com"}, Targets: []endpointv1alpha1.EndpointTarget{{Type: endpointv1alpha1.EndpointTargetTypeIPAddress, Value: "192.0.2.1"}}},
	}
}

func TestWithdrawalGatewayCleanupPathsRequireCapture(t *testing.T) {
	for _, path := range []string{"missing gateway", "deleted gateway", "rejected admission", "cleanupForRoute"} {
		t.Run(path, func(t *testing.T) {
			ctx := context.Background()
			ers := gatewayWithdrawalERS()
			objects := []client.Object{ers}
			if path == "deleted gateway" || path == "rejected admission" {
				gateway := &gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "public"}, Status: gatewayv1.GatewayStatus{Conditions: []metav1.Condition{{Type: "Accepted", Status: metav1.ConditionFalse}}}}
				if path == "deleted gateway" {
					now := metav1.Now()
					gateway.DeletionTimestamp = &now
					gateway.Finalizers = []string{"test.example/hold"}
				}
				objects = append(objects, gateway)
			}
			c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithStatusSubresource(&endpointv1alpha1.EndpointRecordSet{}).WithObjects(objects...).Build()
			r := &Reconciler{Client: c}
			cleanup := func() error {
				if path == "cleanupForRoute" { return r.cleanupForRoute(ctx, "dns", "app", "route") }
				return r.syncGatewayEndpointRecordSet(ctx, "dns", client.ObjectKey{Namespace: "app", Name: "public"})
			}
			if err := cleanup(); !errors.Is(err, retirement.ErrCapturePending) { t.Fatalf("retire = %v", err) }
			var current endpointv1alpha1.EndpointRecordSet
			if err := c.Get(ctx, client.ObjectKeyFromObject(ers), &current); err != nil { t.Fatal(err) }
			if !current.DeletionTimestamp.IsZero() || len(current.Spec.Hostnames) != 0 || len(current.Spec.Targets) != 0 || len(current.Spec.RetiredTargets) != 1 || len(current.Spec.RetiredHostnames) != 2 {
				t.Fatalf("cleanup lost intent: %#v", current)
			}
			current.Status.Conditions = []metav1.Condition{{Type: retirement.CapturedCondition, Status: metav1.ConditionTrue, ObservedGeneration: current.Generation-1, Reason: "Captured"}}
			if err := c.Status().Update(ctx, &current); err != nil { t.Fatal(err) }
			if err := cleanup(); !errors.Is(err, retirement.ErrCapturePending) { t.Fatalf("stale acknowledgment permitted DELETE: %v", err) }
			current.Status.Conditions[0].ObservedGeneration = current.Generation
			if err := c.Status().Update(ctx, &current); err != nil { t.Fatal(err) }
			if err := cleanup(); err != nil { t.Fatal(err) }
			if err := c.Get(ctx, client.ObjectKeyFromObject(ers), &current); err != nil || current.DeletionTimestamp.IsZero() { t.Fatalf("capture did not permit deletion: %v", err) }
		})
	}
}

func TestWithdrawalGatewayUpdateSnapshotsAndOverflow(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		t.Run(map[bool]string{false:"snapshot", true:"overflow"}[overflow], func(t *testing.T) {
			ctx := context.Background()
			ers := gatewayWithdrawalERS()
			if overflow { ers.Spec.RetiredTargets = make([]endpointv1alpha1.RetiredTarget, retirement.MaxRetiredTargets) }
			c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithStatusSubresource(&endpointv1alpha1.EndpointRecordSet{}).WithObjects(ers).Build()
			r := &Reconciler{Client: c}
			desired := ers.DeepCopy()
			desired.Spec.Hostnames = []string{"h2.example.com"}
			desired.Spec.Targets[0].Value = "192.0.2.2"
			err := r.applyGatewayEndpointRecordSets(ctx, "dns", client.ObjectKey{Namespace:"app", Name:"public"}, []endpointv1alpha1.EndpointRecordSet{*desired})
			var current endpointv1alpha1.EndpointRecordSet
			if getErr := c.Get(ctx, client.ObjectKeyFromObject(ers), &current); getErr != nil { t.Fatal(getErr) }
			if overflow {
				if !errors.Is(err, retirement.ErrCapturePending) || !reflect.DeepEqual(current.Spec, ers.Spec) || meta.FindStatusCondition(current.Status.Conditions, "Ready").Reason != retirement.PendingCondition { t.Fatalf("overflow = %#v, %v", current, err) }
				return
			}
			if err != nil || len(current.Spec.RetiredTargets) != 1 || len(current.Spec.RetiredHostnames) != 1 || current.Spec.RetiredHostnames[0].Addresses[0] != "192.0.2.1" { t.Fatalf("snapshot = %#v, %v", current.Spec, err) }
		})
	}
}

func TestWithdrawalGatewayInstallsFinalizerAtCreate(t *testing.T) {
	ctx := context.Background()
	ers := gatewayWithdrawalERS()
	ers.Finalizers = nil
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).Build()
	r := &Reconciler{Client:c}
	if err := r.applyGatewayEndpointRecordSets(ctx, "dns", client.ObjectKey{Namespace:"app", Name:"public"}, []endpointv1alpha1.EndpointRecordSet{*ers}); err != nil { t.Fatal(err) }
	var current endpointv1alpha1.EndpointRecordSet
	if err := c.Get(ctx, client.ObjectKeyFromObject(ers), &current); err != nil { t.Fatal(err) }
	if !reflect.DeepEqual(current.Finalizers, []string{endpointv1alpha1.GeneratedRecordSetsFinalizer}) { t.Fatal("ERS create omitted endpoint finalizer") }
}
