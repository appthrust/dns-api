package endpointrecordset

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestSetAggregateConditionsRequiresEveryChildConditionToBeCurrent(t *testing.T) {
	status := endpointv1alpha1.EndpointRecordSetStatus{
		GeneratedRecordSetCount: 2,
		Conditions:              []metav1.Condition{{Type: "Resolved", Status: metav1.ConditionTrue, ObservedGeneration: 7, Reason: "Resolved"}},
		Hostnames: []endpointv1alpha1.EndpointRecordSetHostnameStatus{{
			Hostname: "reo.appthrust.app",
			RecordSets: []endpointv1alpha1.EndpointRecordSetGeneratedRecordSetStatus{
				readyGeneratedRecordSetStatus("apex", 3),
				readyGeneratedRecordSetStatus("wildcard", 4),
			},
		}},
	}

	setAggregateConditions(&status, 7)
	assertEndpointCondition(t, status.Conditions, "Accepted", metav1.ConditionTrue, 7)
	assertEndpointCondition(t, status.Conditions, "Programmed", metav1.ConditionTrue, 7)
	assertEndpointCondition(t, status.Conditions, "Ready", metav1.ConditionTrue, 7)

	status.Hostnames[0].RecordSets[1].Conditions[1].ObservedGeneration = 3
	setAggregateConditions(&status, 7)
	assertEndpointCondition(t, status.Conditions, "Programmed", metav1.ConditionFalse, 7)
	assertEndpointCondition(t, status.Conditions, "Ready", metav1.ConditionFalse, 7)

	status.Hostnames[0].RecordSets[1] = readyGeneratedRecordSetStatus("wildcard", 4)
	status.Hostnames[0].RecordSets[1].ObservedGeneration = 3
	setAggregateConditions(&status, 7)
	assertEndpointCondition(t, status.Conditions, "Accepted", metav1.ConditionFalse, 7)
	assertEndpointCondition(t, status.Conditions, "Programmed", metav1.ConditionFalse, 7)
	assertEndpointCondition(t, status.Conditions, "Ready", metav1.ConditionFalse, 7)
}

func TestSetAggregateConditionsDoesNotTreatEmptyChildrenAsReady(t *testing.T) {
	status := endpointv1alpha1.EndpointRecordSetStatus{
		Conditions: []metav1.Condition{{Type: "Resolved", Status: metav1.ConditionTrue, ObservedGeneration: 2, Reason: "Resolved"}},
	}

	setAggregateConditions(&status, 2)
	assertEndpointCondition(t, status.Conditions, "Accepted", metav1.ConditionFalse, 2)
	assertEndpointCondition(t, status.Conditions, "Programmed", metav1.ConditionFalse, 2)
	assertEndpointCondition(t, status.Conditions, "Ready", metav1.ConditionFalse, 2)
}

func TestReconcileDeletionRetainsFinalizerUntilGeneratedRecordSetsAreGone(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := endpointv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add endpoint scheme: %v", err)
	}
	if err := dnsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add dns scheme: %v", err)
	}
	endpointRecordSet := &endpointv1alpha1.EndpointRecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "dns-system", Name: "reo", Finalizers: []string{generatedRecordSetsFinalizer}},
	}
	generated := &dnsv1alpha1.RecordSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:  "dns-system",
			Name:       "reo-apex",
			Finalizers: []string{"dns.appthrust.io/provider-cleanup"},
			Labels: map[string]string{
				managedByLabel:                           managedByValue,
				generatedLabelEndpointRecordSetNamespace: endpointRecordSet.Namespace,
				generatedLabelEndpointRecordSetName:      endpointRecordSet.Name,
			},
		},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(endpointRecordSet).WithObjects(endpointRecordSet, generated).Build()
	reconciler := &Reconciler{Client: k8sClient, Scheme: scheme, RecordSetNamespace: "dns-system"}

	if err := k8sClient.Delete(ctx, endpointRecordSet); err != nil {
		t.Fatalf("delete EndpointRecordSet: %v", err)
	}
	var deleting endpointv1alpha1.EndpointRecordSet
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(endpointRecordSet), &deleting); err != nil {
		t.Fatalf("get deleting EndpointRecordSet: %v", err)
	}
	if err := reconciler.reconcileDeletion(ctx, &deleting, "dns-system"); err != nil {
		t.Fatalf("reconcile deletion with child: %v", err)
	}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(endpointRecordSet), &deleting); err != nil {
		t.Fatalf("get retained EndpointRecordSet: %v", err)
	}
	if !slices.Contains(deleting.Finalizers, generatedRecordSetsFinalizer) {
		t.Fatal("generated RecordSet finalizer was removed before child terminal deletion")
	}

	var deletingChild dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(generated), &deletingChild); err != nil {
		t.Fatalf("get deleting child: %v", err)
	}
	deletingChild.Finalizers = nil
	if err := k8sClient.Update(ctx, &deletingChild); err != nil {
		t.Fatalf("remove child finalizer: %v", err)
	}
	if err := reconciler.reconcileDeletion(ctx, &deleting, "dns-system"); err != nil {
		t.Fatalf("reconcile deletion after child removal: %v", err)
	}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(endpointRecordSet), &deleting); err == nil {
		t.Fatal("EndpointRecordSet still exists after generated children reached terminal deletion")
	}
}

func readyGeneratedRecordSetStatus(name string, generation int64) endpointv1alpha1.EndpointRecordSetGeneratedRecordSetStatus {
	return endpointv1alpha1.EndpointRecordSetGeneratedRecordSetStatus{
		Ref:                dnsv1alpha1.ObjectReference{Namespace: "dns-system", Name: name},
		Generation:         generation,
		ObservedGeneration: generation,
		Conditions: []metav1.Condition{
			{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, ObservedGeneration: generation, Reason: "Accepted"},
			{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionTrue, ObservedGeneration: generation, Reason: "Programmed"},
		},
	}
}

func assertEndpointCondition(t *testing.T, conditions []metav1.Condition, conditionType string, status metav1.ConditionStatus, generation int64) {
	t.Helper()
	for _, condition := range conditions {
		if condition.Type == conditionType {
			if condition.Status != status || condition.ObservedGeneration != generation {
				t.Fatalf("condition %s = (%s, generation %d), want (%s, generation %d)", conditionType, condition.Status, condition.ObservedGeneration, status, generation)
			}
			return
		}
	}
	t.Fatalf("condition %s not found", conditionType)
}

func TestRecordSetFromFragmentAddsRoute53AdoptionWhenZoneOptedIn(t *testing.T) {
	reconciler := &Reconciler{}
	endpointRecordSet := &endpointv1alpha1.EndpointRecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "gateway-system", Name: "public"},
	}
	zone := &dnsv1alpha1.Zone{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "dns-system",
			Name:      "appthrust-dev",
			Annotations: map[string]string{
				route53RecordSetAdoptionAnnotation: "enabled",
			},
		},
		Spec: dnsv1alpha1.ZoneSpec{
			DomainName: "appthrust.dev",
			Provider:   route53v1alpha1.ProviderRef,
		},
	}

	recordSet := reconciler.recordSetFromFragment(context.Background(), endpointRecordSet, "dns-system", zone, endpointv1alpha1.RecordSetSpecFragment{
		Type: endpointv1alpha1.EndpointRecordSetTypeA,
		Name: "dashboard",
	})

	var adoption map[string]bool
	if err := json.Unmarshal(recordSet.Spec.Adoption.Raw, &adoption); err != nil {
		t.Fatalf("unmarshal adoption: %v", err)
	}
	if !adoption["enabled"] {
		t.Fatalf("adoption = %#v, want enabled", adoption)
	}
}

func TestRecordSetFromFragmentDoesNotAddRoute53AdoptionByDefault(t *testing.T) {
	reconciler := &Reconciler{}
	endpointRecordSet := &endpointv1alpha1.EndpointRecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "gateway-system", Name: "public"},
	}
	zone := &dnsv1alpha1.Zone{
		ObjectMeta: metav1.ObjectMeta{Namespace: "dns-system", Name: "appthrust-dev"},
		Spec: dnsv1alpha1.ZoneSpec{
			DomainName: "appthrust.dev",
			Provider:   route53v1alpha1.ProviderRef,
		},
	}

	recordSet := reconciler.recordSetFromFragment(context.Background(), endpointRecordSet, "dns-system", zone, endpointv1alpha1.RecordSetSpecFragment{
		Type: endpointv1alpha1.EndpointRecordSetTypeA,
		Name: "dashboard",
	})

	if len(recordSet.Spec.Adoption.Raw) != 0 {
		t.Fatalf("adoption = %s, want empty", string(recordSet.Spec.Adoption.Raw))
	}
}
