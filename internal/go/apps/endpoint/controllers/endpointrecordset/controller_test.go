package endpointrecordset

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestPreferredRecordTypeForEndpointRecordSet(t *testing.T) {
	recordSet := &endpointv1alpha1.EndpointRecordSet{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
			route53RecordTypeAnnotation: "cname",
		}},
	}
	got, err := preferredRecordTypeForEndpointRecordSet(recordSet)
	if err != nil || got != endpointv1alpha1.EndpointRecordSetTypeCNAME {
		t.Fatalf("preferred record type = (%q, %v)", got, err)
	}
	recordSet.Annotations[route53RecordTypeAnnotation] = "A"
	if _, err := preferredRecordTypeForEndpointRecordSet(recordSet); err == nil {
		t.Fatal("unsupported preferred record type was accepted")
	}
}

func TestEndpointRecordSetApexWildcardAliasLifecycleSurvivesControllerRestart(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	if err := endpointv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add endpoint scheme: %v", err)
	}
	if err := dnsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add dns scheme: %v", err)
	}
	endpointRecordSet := &endpointv1alpha1.EndpointRecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "allocation-system", Name: "reo"},
		Spec: endpointv1alpha1.EndpointRecordSetSpec{
			Hostnames: []string{"reo.appthrust.app", "*.reo.appthrust.app"},
			Targets:   []endpointv1alpha1.EndpointTarget{{Type: endpointv1alpha1.EndpointTargetTypeHostname, Value: "k8s-public-123456.ap-northeast-1.elb.amazonaws.com"}},
		},
	}
	zone := &dnsv1alpha1.Zone{
		ObjectMeta: metav1.ObjectMeta{Namespace: "platform-root-dns", Name: "appthrust-app"},
		Spec: dnsv1alpha1.ZoneSpec{
			DomainName: "appthrust.app",
			Provider:   route53v1alpha1.ProviderRef,
			AllowedRecordSets: []dnsv1alpha1.AllowedRecordSet{{
				Namespaces: dnsv1alpha1.AllowedRecordSetNamespaces{Selector: metav1.LabelSelector{MatchLabels: map[string]string{"appthrust.io/dns-writer": "organization-endpoints"}}},
				Records: []dnsv1alpha1.AllowedRecord{{
					Name:  dnsv1alpha1.RecordNamePolicy{Pattern: `(reo|\*\.reo)`},
					Types: []dnsv1alpha1.RecordType{dnsv1alpha1.RecordTypeA, dnsv1alpha1.RecordTypeAAAA},
				}},
			}},
		},
	}
	capability := &endpointv1alpha1.EndpointProviderCapability{
		ObjectMeta: metav1.ObjectMeta{Name: "route53-v1alpha1"},
		Spec: endpointv1alpha1.EndpointProviderCapabilitySpec{
			Provider: route53v1alpha1.ProviderRef,
			Conversion: endpointv1alpha1.EndpointRecordSetConversionAPI{
				Group: "endpoint.route53.dns.appthrust.io", Resource: "endpointrecordsetconversions",
			},
		},
	}
	writerNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "organization-endpoints", Labels: map[string]string{"appthrust.io/dns-writer": "organization-endpoints"}}}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&endpointv1alpha1.EndpointRecordSet{}, &dnsv1alpha1.RecordSet{}).
		WithObjects(endpointRecordSet, zone, capability, writerNamespace).
		Build()
	conversion := func(_ context.Context, _ *endpointv1alpha1.EndpointProviderCapability, input endpointv1alpha1.EndpointRecordSetConversionInput) ([]endpointv1alpha1.RecordSetSpecFragment, string, error) {
		options := runtime.RawExtension{Raw: []byte(`{"alias":{"dnsName":"dualstack.k8s-public-123456.ap-northeast-1.elb.amazonaws.com.","hostedZoneID":"Z14GRHDCWA56QT","evaluateTargetHealth":true}}`)}
		return []endpointv1alpha1.RecordSetSpecFragment{
			{Type: endpointv1alpha1.EndpointRecordSetTypeA, Name: input.Name, Options: options},
			{Type: endpointv1alpha1.EndpointRecordSetTypeAAAA, Name: input.Name, Options: options},
		}, "", nil
	}
	reconcile := func(reconciler *Reconciler) {
		t.Helper()
		if _, err := reconciler.Reconcile(ctx, ctrlRequest(endpointRecordSet)); err != nil {
			t.Fatalf("reconcile EndpointRecordSet: %v", err)
		}
	}
	reconciler := &Reconciler{Client: k8sClient, Scheme: scheme, RecordSetNamespace: writerNamespace.Name, conversionFunc: conversion}
	reconcile(reconciler) // establish lifecycle ownership before creating children
	reconcile(reconciler) // create apex and wildcard A/AAAA children

	var generated dnsv1alpha1.RecordSetList
	if err := k8sClient.List(ctx, &generated, client.InNamespace(writerNamespace.Name)); err != nil {
		t.Fatalf("list generated RecordSets: %v", err)
	}
	if len(generated.Items) != 4 {
		t.Fatalf("generated RecordSets = %d, want apex/wildcard A/AAAA", len(generated.Items))
	}
	wantNames := map[string]bool{"reo/A": false, "reo/AAAA": false, "*.reo/A": false, "*.reo/AAAA": false}
	for index := range generated.Items {
		item := &generated.Items[index]
		key := item.Spec.Name + "/" + string(item.Spec.Type)
		if _, ok := wantNames[key]; !ok {
			t.Fatalf("unexpected generated RecordSet %s", key)
		}
		wantNames[key] = true
		if item.Generation == 0 {
			item.Generation = 1 // fake client does not apply apiserver generation defaults
			if err := k8sClient.Update(ctx, item); err != nil {
				t.Fatalf("set generated RecordSet generation: %v", err)
			}
		}
		item.Status.ObservedGeneration = item.Generation
		item.Status.Conditions = []metav1.Condition{
			{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, ObservedGeneration: item.Generation, Reason: "Accepted"},
			{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionTrue, ObservedGeneration: item.Generation, Reason: "Programmed"},
		}
		if err := k8sClient.Status().Update(ctx, item); err != nil {
			t.Fatalf("set generated RecordSet status: %v", err)
		}
	}
	for key, found := range wantNames {
		if !found {
			t.Fatalf("generated RecordSet %s was not created", key)
		}
	}

	// A fresh reconciler represents a controller restart over persisted children.
	restarted := &Reconciler{Client: k8sClient, Scheme: scheme, RecordSetNamespace: writerNamespace.Name, conversionFunc: conversion}
	reconcile(restarted)
	var got endpointv1alpha1.EndpointRecordSet
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(endpointRecordSet), &got); err != nil {
		t.Fatalf("get reconciled EndpointRecordSet: %v", err)
	}
	assertEndpointCondition(t, got.Status.Conditions, "Accepted", metav1.ConditionTrue, got.Generation)
	assertEndpointCondition(t, got.Status.Conditions, "Programmed", metav1.ConditionTrue, got.Generation)
	assertEndpointCondition(t, got.Status.Conditions, "Ready", metav1.ConditionTrue, got.Generation)

	if err := k8sClient.Delete(ctx, &got); err != nil {
		t.Fatalf("delete EndpointRecordSet: %v", err)
	}
	reconcile(restarted)
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(endpointRecordSet), &got); !apierrors.IsNotFound(err) {
		t.Fatalf("EndpointRecordSet get after terminal child deletion = %v, want NotFound", err)
	}
	generated = dnsv1alpha1.RecordSetList{}
	if err := k8sClient.List(ctx, &generated, client.InNamespace(writerNamespace.Name)); err != nil {
		t.Fatalf("list generated RecordSets after deletion: %v", err)
	}
	if len(generated.Items) != 0 {
		t.Fatalf("generated RecordSets after deletion = %d, want 0", len(generated.Items))
	}
}

func TestSortedZonesUsesLongestSuffix(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := dnsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add dns scheme: %v", err)
	}
	parent := &dnsv1alpha1.Zone{ObjectMeta: metav1.ObjectMeta{Namespace: "dns", Name: "parent"}, Spec: dnsv1alpha1.ZoneSpec{DomainName: "appthrust.app"}}
	organization := &dnsv1alpha1.Zone{ObjectMeta: metav1.ObjectMeta{Namespace: "dns", Name: "organization"}, Spec: dnsv1alpha1.ZoneSpec{DomainName: "reo.appthrust.app"}}
	reconciler := &Reconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(parent, organization).Build()}

	zones, err := reconciler.sortedZones(ctx, "api.reo.appthrust.app")
	if err != nil {
		t.Fatalf("sortedZones: %v", err)
	}
	if len(zones) != 2 || zones[0].Name != organization.Name || zones[1].Name != parent.Name {
		t.Fatalf("sorted zones = %#v, want organization before parent", zones)
	}
}

func ctrlRequest(object client.Object) ctrl.Request {
	return ctrl.Request{NamespacedName: client.ObjectKeyFromObject(object)}
}

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
