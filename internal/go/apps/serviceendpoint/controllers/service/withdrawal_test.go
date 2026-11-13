package service

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
)

func TestWithdrawalServiceUsesCreateRetireCaptureContract(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithStatusSubresource(&endpointv1alpha1.EndpointRecordSet{}).Build()
	recorder := record.NewFakeRecorder(4)
	r := &Reconciler{Client: c, Recorder: recorder}
	ers := &endpointv1alpha1.EndpointRecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "dns", Name: "service", UID: "service-ers", Generation: 4, Labels: map[string]string{managedByLabel: managedByValue, generatedLabelServiceNamespace: "app", generatedLabelServiceName: "web"}},
		Spec:       endpointv1alpha1.EndpointRecordSetSpec{Hostnames: []string{"h1.example.com", "h2.example.com"}, Targets: []endpointv1alpha1.EndpointTarget{{Type: endpointv1alpha1.EndpointTargetTypeIPAddress, Value: "192.0.2.1"}}},
	}
	if err := r.applyEndpointRecordSet(ctx, ers); err != nil {
		t.Fatal(err)
	}
	if !containsString(ers.Finalizers, endpointv1alpha1.GeneratedRecordSetsFinalizer) {
		t.Fatal("create omitted finalizer")
	}
	desired := ers.DeepCopy()
	desired.Spec.Hostnames = []string{"h2.example.com"}
	desired.Spec.Targets[0].Value = "192.0.2.2"
	if err := r.applyEndpointRecordSet(ctx, desired); err != nil {
		t.Fatal(err)
	}
	var current endpointv1alpha1.EndpointRecordSet
	if err := c.Get(ctx, client.ObjectKeyFromObject(ers), &current); err != nil {
		t.Fatal(err)
	}
	if len(current.Spec.RetiredTargets) != 1 || len(current.Spec.RetiredHostnames) != 1 || current.Spec.RetiredHostnames[0].Addresses[0] != "192.0.2.1" {
		t.Fatal("service lost old pair")
	}
	// Real API updates advance generation; fake client does not.
	current.Generation++
	if err := c.Update(ctx, &current); err != nil {
		t.Fatal(err)
	}
	if err := r.cleanupForService(ctx, "dns", "app", "web"); !errors.Is(err, retirement.ErrCapturePending) {
		t.Fatalf("cleanup = %v", err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(ers), &current); err != nil {
		t.Fatal(err)
	}
	if !current.DeletionTimestamp.IsZero() || len(current.Spec.Hostnames) != 0 || len(current.Spec.RetiredTargets) != 2 {
		t.Fatal("cleanup discarded uncaptured state")
	}
	if err := r.cleanupForService(ctx, "dns", "app", "web"); !errors.Is(err, retirement.ErrCapturePending) {
		t.Fatalf("repeat cleanup = %v", err)
	}
	assertServiceTransitionEvent(t, recorder, retirement.Patched)
	current.Status.Conditions = []metav1.Condition{{Type: retirement.CapturedCondition, Status: metav1.ConditionTrue, ObservedGeneration: current.Generation, Reason: "Captured"}}
	if err := c.Status().Update(ctx, &current); err != nil {
		t.Fatal(err)
	}
	if err := r.cleanupForService(ctx, "dns", "app", "web"); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(ers), &current); err != nil || current.DeletionTimestamp.IsZero() {
		t.Fatalf("capture did not permit cleanup: %v", err)
	}
	if err := r.cleanupForService(ctx, "dns", "app", "web"); err != nil {
		t.Fatal(err)
	}
	assertServiceTransitionEvent(t, recorder, retirement.Deleted)
}

func TestWithdrawalServiceNonExternalUpdateUnchanged(t *testing.T) {
	ctx := context.Background()
	ers := &endpointv1alpha1.EndpointRecordSet{ObjectMeta: metav1.ObjectMeta{Namespace: "dns", Name: "ers", Generation: 1},
		Spec:   endpointv1alpha1.EndpointRecordSetSpec{Hostnames: []string{"h1.example.com"}, Targets: []endpointv1alpha1.EndpointTarget{{Type: endpointv1alpha1.EndpointTargetTypeIPAddress, Value: "192.0.2.1"}}},
		Status: endpointv1alpha1.EndpointRecordSetStatus{Conditions: []metav1.Condition{{Type: retirement.ProtectedCondition, Status: metav1.ConditionFalse, ObservedGeneration: 1}}},
	}
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithStatusSubresource(ers).WithObjects(ers).Build()
	r := &Reconciler{Client: c}
	desired := ers.DeepCopy()
	desired.Spec.Targets[0].Value = "192.0.2.2"
	if err := r.applyEndpointRecordSet(ctx, desired); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(ers), ers); err != nil {
		t.Fatal(err)
	}
	if len(ers.Spec.RetiredTargets)+len(ers.Spec.RetiredHostnames) != 0 || ers.Spec.Targets[0].Value != "192.0.2.2" {
		t.Fatal("non-external source behavior changed")
	}
}

func assertServiceTransitionEvent(t *testing.T, recorder *record.FakeRecorder, outcome retirement.Outcome) {
	t.Helper()
	select {
	case event := <-recorder.Events:
		if !strings.HasPrefix(event, "Normal "+string(outcome)+" ") {
			t.Fatalf("event = %q, want %s", event, outcome)
		}
	default:
		t.Fatalf("missing %s Event", outcome)
	}
	select {
	case event := <-recorder.Events:
		t.Fatalf("repeated transition Event: %s", event)
	default:
	}
}
