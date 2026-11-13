package gateway

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/appthrust/dns-api/internal/go/apps/endpoint/retirement"
	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func assertTransitionEvents(t *testing.T, recorder *record.FakeRecorder, reasons ...string) {
	t.Helper()
	for _, reason := range reasons {
		select {
		case event := <-recorder.Events:
			if !strings.HasPrefix(event, "Normal "+reason+" ") {
				t.Fatalf("event=%q, want %s", event, reason)
			}
		default:
			t.Fatalf("missing %s Event", reason)
		}
	}
	select {
	case event := <-recorder.Events:
		t.Fatalf("unexpected duplicate event: %s", event)
	default:
	}
}

func TestHostnameRemovedEventOnceAfterSuccessfulUpdate(t *testing.T) {
	ers := gatewayWithdrawalERS()
	base := fake.NewClientBuilder().WithScheme(testScheme(t)).WithStatusSubresource(ers).WithObjects(ers).Build()
	recorder := record.NewFakeRecorder(10)
	r := &Reconciler{Client: base, Recorder: recorder}
	desired := ers.DeepCopy()
	desired.Spec.Hostnames = []string{"h2.example.com"}
	for range 2 {
		if err := r.applyGatewayEndpointRecordSets(t.Context(), ers.Namespace, client.ObjectKey{Namespace: "app", Name: "public"}, []endpointv1alpha1.EndpointRecordSet{*desired}); err != nil {
			t.Fatal(err)
		}
	}
	assertTransitionEvents(t, recorder, string(reasonHostnameRemoved))
	var current endpointv1alpha1.EndpointRecordSet
	if err := base.Get(t.Context(), client.ObjectKeyFromObject(ers), &current); err != nil {
		t.Fatal(err)
	}
	if len(current.Spec.Hostnames) != 1 || current.Spec.Hostnames[0] != "h2.example.com" {
		t.Fatalf("hostname was not removed: %+v", current.Spec)
	}
}

func TestEndpointRetirementAndDeletionEventsOnlyOnTransitions(t *testing.T) {
	for _, cleanup := range []string{"gateway", "route"} {
		t.Run(cleanup, func(t *testing.T) {
			ers := gatewayWithdrawalERS()
			base := fake.NewClientBuilder().WithScheme(testScheme(t)).WithStatusSubresource(ers).WithObjects(ers).Build()
			recorder := record.NewFakeRecorder(10)
			r := &Reconciler{Client: base, Recorder: recorder}
			run := func() error {
				if cleanup == "route" {
					return r.cleanupForRoute(t.Context(), ers.Namespace, "app", "route")
				}
				return r.applyGatewayEndpointRecordSets(t.Context(), ers.Namespace, client.ObjectKey{Namespace: "app", Name: "public"}, nil)
			}
			for range 2 {
				if err := run(); !errors.Is(err, retirement.ErrCapturePending) {
					t.Fatalf("retirement=%v", err)
				}
			}
			assertTransitionEvents(t, recorder, string(reasonHostnameRemoved), string(reasonHostnameRemoved), string(retirement.Patched))
			var current endpointv1alpha1.EndpointRecordSet
			if err := base.Get(t.Context(), client.ObjectKeyFromObject(ers), &current); err != nil {
				t.Fatal(err)
			}
			current.Status.Conditions = []metav1.Condition{{Type: retirement.CapturedCondition, Status: metav1.ConditionTrue, ObservedGeneration: current.Generation, Reason: "Captured"}}
			if err := base.Status().Update(t.Context(), &current); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := run(); err != nil {
					t.Fatal(err)
				}
			}
			assertTransitionEvents(t, recorder, string(retirement.Deleted))
			if err := base.Get(t.Context(), client.ObjectKeyFromObject(ers), &current); err != nil || current.DeletionTimestamp.IsZero() {
				t.Fatalf("deletion not persisted: %v", err)
			}
		})
	}
}

func TestFailedPublicationMutationDoesNotEmitTransition(t *testing.T) {
	for _, mutation := range []string{"update", "retire", "delete"} {
		t.Run(mutation, func(t *testing.T) {
			ers := gatewayWithdrawalERS()
			if mutation == "delete" {
				ers.Status.Conditions = []metav1.Condition{{Type: retirement.ProtectedCondition, Status: metav1.ConditionFalse, ObservedGeneration: ers.Generation}}
			}
			failure := errors.New("write unavailable")
			base := fake.NewClientBuilder().WithScheme(testScheme(t)).WithStatusSubresource(ers).WithObjects(ers).Build()
			c := interceptor.NewClient(base, interceptor.Funcs{
				Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error { return failure },
				Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
					return failure
				},
				Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error { return failure },
			})
			recorder := record.NewFakeRecorder(10)
			r := &Reconciler{Client: c, Recorder: recorder}
			var desired []endpointv1alpha1.EndpointRecordSet
			if mutation == "update" {
				item := ers.DeepCopy()
				item.Spec.Hostnames = item.Spec.Hostnames[1:]
				desired = append(desired, *item)
			}
			if err := r.applyGatewayEndpointRecordSets(t.Context(), ers.Namespace, client.ObjectKey{Namespace: "app", Name: "public"}, desired); !errors.Is(err, failure) {
				t.Fatalf("mutation error=%v", err)
			}
			assertTransitionEvents(t, recorder)
		})
	}
}
