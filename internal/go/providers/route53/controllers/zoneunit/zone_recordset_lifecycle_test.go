package route53

import (
	"context"
	"slices"
	"strings"
	"testing"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	"github.com/aws/smithy-go"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestZoneReconcilerCreatesARecordFromZoneReconcile(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	recordSet := route53ARecordSet("app", "www")
	objects := route53RecordSetObjects(t, recordSet)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	result, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("Reconcile did not request a requeue for a pending Route 53 record change")
	}
	if len(provider.upserted) != 1 {
		t.Fatalf("upserted record sets = %d, want 1", len(provider.upserted))
	}
	upserted := provider.upserted[0]
	if upserted.HostedZoneID != "Z000001" || upserted.Name != "www.apps.example.com." || upserted.Type != dnsv1alpha1.RecordTypeA {
		t.Fatalf("upserted identity = %#v", upserted)
	}
	if upserted.TTL == nil || *upserted.TTL != 300 {
		t.Fatalf("upserted ttl = %#v, want 300", upserted.TTL)
	}
	if !slices.Equal(upserted.Values, []string{"192.0.2.10"}) {
		t.Fatalf("upserted values = %#v", upserted.Values)
	}

	var gotRecordSet dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(recordSet), &gotRecordSet); err != nil {
		t.Fatalf("RecordSet was not found: %v", err)
	}
	assertCondition(t, gotRecordSet.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted")
	assertCondition(t, gotRecordSet.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ProviderChangePending")
	if !slices.Contains(gotRecordSet.Finalizers, "dns.appthrust.io/recordset-finalizer") {
		t.Fatalf("finalizers = %#v, want core RecordSet finalizer", gotRecordSet.Finalizers)
	}
	statusData := mustZoneUnitRecordSetStatusState(t, ctx, k8sClient, "app", "apps-example-com", "app", "www")
	if statusData.HostedZoneID != "Z000001" || statusData.RecordName != "www.apps.example.com." || statusData.RecordType != string(dnsv1alpha1.RecordTypeA) {
		t.Fatalf("ZoneUnit recordSet provider state = %#v, want Route 53 record identity", statusData)
	}
	if gotRecordSet.Status.Provider != nil && len(gotRecordSet.Status.Provider.Data.Raw) > 0 {
		t.Fatalf("status.provider.data = %s, want Route 53 record identity only in state", string(gotRecordSet.Status.Provider.Data.Raw))
	}

	var gotZone dnsv1alpha1.Zone
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &gotZone); err != nil {
		t.Fatalf("Zone was not found: %v", err)
	}
	unitData := mustZoneUnitStatusData(t, ctx, k8sClient, gotZone.Namespace, gotZone.Name)
	if unitData.PendingRecordSetChange == nil || unitData.PendingRecordSetChange.Operation != "UPSERT_BATCH" {
		t.Fatalf("pendingRecordSetChange = %#v, want UPSERT batch", unitData.PendingRecordSetChange)
	}
}
func TestZoneReconcilerUpsertsRecordSetSelectedByZoneUnitAcrossNamespaces(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	recordSet := route53ARecordSet("record-ns", "www")
	objects := route53RecordSetObjects(t, recordSet)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	_, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if len(provider.upserted) != 1 {
		t.Fatalf("upserted record sets = %d, want 1", len(provider.upserted))
	}

	var unit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &unit); err != nil {
		t.Fatalf("ZoneUnit was not found: %v", err)
	}
	index := slices.IndexFunc(unit.Status.RecordSets, func(status dnsv1alpha1.ZoneUnitRecordSetStatus) bool {
		return status.RecordSetNamespace == recordSet.Namespace && status.RecordSetName == recordSet.Name
	})
	if index < 0 {
		t.Fatalf("ZoneUnit status.recordSets has no entry for %s/%s: %#v", recordSet.Namespace, recordSet.Name, unit.Status.RecordSets)
	}
	assertCondition(t, unit.Status.RecordSets[index].Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted")

}
func TestZoneReconcilerAcceptsRecordSetAdoptionFromDerivedIdentity(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	recordSet := route53ARecordSet("app", "www")
	recordSet.Spec.Adoption = runtime.RawExtension{Raw: []byte(`{"enabled":true}`)}
	setRecordSetStatusData(t, recordSet, route53v1alpha1.Route53RecordSetStatusData{
		HostedZoneID: "Z000001",
		RecordName:   "www.apps.example.com.",
		RecordType:   string(dnsv1alpha1.RecordTypeA),
	})
	objects := route53RecordSetObjects(t, recordSet)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	_, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var gotRecordSet dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(recordSet), &gotRecordSet); err != nil {
		t.Fatalf("RecordSet was not found: %v", err)
	}
	assertCondition(t, gotRecordSet.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted")
	if len(provider.deletedRRs) != 0 {
		t.Fatalf("deleted record sets = %d, want 0", len(provider.deletedRRs))
	}
}
func TestZoneReconcilerClassifiesNotAllowedByZoneMessage(t *testing.T) {
	ctx := context.Background()
	zone := &dnsv1alpha1.Zone{
		ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "apps-example-com"},
		Spec: dnsv1alpha1.ZoneSpec{
			Provider: route53v1alpha1.ProviderRef,
			AllowedRecordSets: []dnsv1alpha1.AllowedRecordSet{
				{
					Namespaces: dnsv1alpha1.AllowedRecordSetNamespaces{
						Selector: metav1.LabelSelector{MatchLabels: map[string]string{"access": "dns"}},
					},
					Records: []dnsv1alpha1.AllowedRecord{
						{
							Name:  dnsv1alpha1.RecordNamePolicy{Pattern: "www"},
							Types: []dnsv1alpha1.RecordType{dnsv1alpha1.RecordTypeA},
						},
					},
				},
			},
		},
	}

	tests := []struct {
		name            string
		namespaceLabels map[string]string
		recordName      string
		recordType      dnsv1alpha1.RecordType
		want            string
	}{
		{
			name:            "namespace",
			namespaceLabels: nil,
			recordName:      "www",
			recordType:      dnsv1alpha1.RecordTypeA,
			want:            "RecordSet namespace is not allowed by the referenced Zone.",
		},
		{
			name:            "record-name",
			namespaceLabels: map[string]string{"access": "dns"},
			recordName:      "api",
			recordType:      dnsv1alpha1.RecordTypeA,
			want:            "RecordSet name is not allowed by the referenced Zone.",
		},
		{
			name:            "record-type",
			namespaceLabels: map[string]string{"access": "dns"},
			recordName:      "www",
			recordType:      dnsv1alpha1.RecordTypeAAAA,
			want:            "RecordSet type is not allowed for this RecordSet name by the referenced Zone.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recordSet := route53ARecordSet("record-ns", "www")
			recordSet.Spec.Name = tt.recordName
			recordSet.Spec.Type = tt.recordType
			reconciler := &ZoneReconciler{
				Client: fake.NewClientBuilder().
					WithScheme(testScheme(t)).
					WithObjects(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "record-ns", Labels: tt.namespaceLabels}}).
					Build(),
			}

			allowed, message := reconciler.recordSetAllowedByZoneMessage(ctx, recordSet, zone)
			if allowed {
				t.Fatalf("recordSetAllowedByZoneMessage returned allowed, want denied")
			}
			if message != tt.want {
				t.Fatalf("message = %q, want %q", message, tt.want)
			}
		})
	}
}
func TestValidateRecordSetTXTValuesRejectsNonPrintableASCII(t *testing.T) {
	tests := map[string]string{
		"newline":   "line1\nline2",
		"tab":       "one\ttwo",
		"control":   "prefix\x7fsuffix",
		"non-ascii": "exämple",
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			err := validateRecordSetTXTValues([]string{value})
			if err == nil || !strings.Contains(err.Error(), "printable ASCII") {
				t.Fatalf("validateRecordSetTXTValues error = %v, want printable ASCII failure", err)
			}
		})
	}
}
func TestZoneReconcilerCreatesAAAARecordFromZoneReconcile(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	recordSet := route53AAAARecordSet("app", "www-v6")
	objects := route53RecordSetObjects(t, recordSet)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	result, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("Reconcile did not request a requeue for a pending Route 53 record change")
	}
	if len(provider.upserted) != 1 {
		t.Fatalf("upserted record sets = %d, want 1", len(provider.upserted))
	}
	upserted := provider.upserted[0]
	if upserted.HostedZoneID != "Z000001" || upserted.Name != "www.apps.example.com." || upserted.Type != dnsv1alpha1.RecordTypeAAAA {
		t.Fatalf("upserted identity = %#v", upserted)
	}
	if upserted.TTL == nil || *upserted.TTL != 300 {
		t.Fatalf("upserted ttl = %#v, want 300", upserted.TTL)
	}
	if !slices.Equal(upserted.Values, []string{"2001:db8::10"}) {
		t.Fatalf("upserted values = %#v", upserted.Values)
	}
}
func TestZoneReconcilerCreatesTXTRecordFromZoneReconcile(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	recordSet := route53TXTRecordSet("app", "acme-challenge")
	objects := route53RecordSetObjects(t, recordSet)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	result, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("Reconcile did not request a requeue for a pending Route 53 record change")
	}
	if len(provider.upserted) != 1 {
		t.Fatalf("upserted record sets = %d, want 1", len(provider.upserted))
	}
	upserted := provider.upserted[0]
	if upserted.HostedZoneID != "Z000001" || upserted.Name != "_acme-challenge.apps.example.com." || upserted.Type != dnsv1alpha1.RecordTypeTXT {
		t.Fatalf("upserted identity = %#v", upserted)
	}
	if upserted.TTL == nil || *upserted.TTL != 300 {
		t.Fatalf("upserted ttl = %#v, want 300", upserted.TTL)
	}
	want := []string{`"challenge-token"`, `"v=spf1 include:_spf.example.net ~all"`}
	if !slices.Equal(upserted.Values, want) {
		t.Fatalf("upserted values = %#v, want %#v", upserted.Values, want)
	}
}
func TestZoneReconcilerDoesNotPropagateInvalidBatchErrorToUnrelatedRecordSet(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	provider.changeRecordSetsErr = func(_ string, changes []RecordSetChange) error {
		if len(changes) > 1 || changes[0].RecordSet.Name == "alias.apps.example.com." {
			return &smithy.GenericAPIError{
				Code:    "InvalidChangeBatch",
				Message: "Tried to create an alias that targets https://not-a-dns-name.example.com.",
			}
		}
		return nil
	}
	alias := route53AliasARecordSet("app", "bad-alias")
	alias.Spec.Name = "alias"
	txt := route53TXTRecordSet("app", "acme-challenge")
	unit := route53ZoneUnit("app", "apps-example-com", "app", "route53-public")
	unit.Spec.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetSpec{
		route53ZoneUnitRecordSetSpec(alias),
		route53ZoneUnitRecordSetSpec(txt),
	}
	objects := []client.Object{
		route53Provider(),
		route53ZoneClass("app", "route53-public", nil),
		acceptedRoute53Identity("app", "route53-dev"),
		route53ReadyZone(t, "app", "apps-example-com"),
		unit,
		alias,
		txt,
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	result, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("Reconcile did not request a requeue for the retried Route 53 record change")
	}
	if len(provider.upserted) != 1 {
		t.Fatalf("upserted record sets = %d, want 1", len(provider.upserted))
	}
	if provider.upserted[0].Type != dnsv1alpha1.RecordTypeTXT {
		t.Fatalf("upserted record set = %#v, want TXT only", provider.upserted[0])
	}

	var gotUnit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &gotUnit); err != nil {
		t.Fatalf("ZoneUnit was not found: %v", err)
	}
	aliasIndex := slices.IndexFunc(gotUnit.Status.RecordSets, func(status dnsv1alpha1.ZoneUnitRecordSetStatus) bool {
		return status.RecordSetNamespace == "app" && status.RecordSetName == "bad-alias"
	})
	if aliasIndex < 0 {
		t.Fatalf("ZoneUnit status has no bad-alias entry: %#v", gotUnit.Status.RecordSets)
	}
	aliasProgrammed := assertCondition(t, gotUnit.Status.RecordSets[aliasIndex].Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ProviderInvalidRequest")
	if !strings.Contains(aliasProgrammed.Message, "https://not-a-dns-name") {
		t.Fatalf("alias Programmed message = %q, want provider detail", aliasProgrammed.Message)
	}
	txtIndex := slices.IndexFunc(gotUnit.Status.RecordSets, func(status dnsv1alpha1.ZoneUnitRecordSetStatus) bool {
		return status.RecordSetNamespace == "app" && status.RecordSetName == "acme-challenge"
	})
	if txtIndex < 0 {
		t.Fatalf("ZoneUnit status has no acme-challenge entry: %#v", gotUnit.Status.RecordSets)
	}
	txtProgrammed := assertCondition(t, gotUnit.Status.RecordSets[txtIndex].Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ProviderChangePending")
	if strings.Contains(txtProgrammed.Message, "https://not-a-dns-name") {
		t.Fatalf("TXT Programmed message leaked alias error: %q", txtProgrammed.Message)
	}
}
func TestZoneReconcilerCreatesCNAMERecordFromZoneReconcile(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	recordSet := route53CNAMERecordSet("app", "www-cname")
	objects := route53RecordSetObjects(t, recordSet)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	result, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("Reconcile did not request a requeue for a pending Route 53 record change")
	}
	if len(provider.upserted) != 1 {
		t.Fatalf("upserted record sets = %d, want 1", len(provider.upserted))
	}
	upserted := provider.upserted[0]
	if upserted.HostedZoneID != "Z000001" || upserted.Name != "www.apps.example.com." || upserted.Type != dnsv1alpha1.RecordTypeCNAME {
		t.Fatalf("upserted identity = %#v", upserted)
	}
	if upserted.TTL == nil || *upserted.TTL != 300 {
		t.Fatalf("upserted ttl = %#v, want 300", upserted.TTL)
	}
	if !slices.Equal(upserted.Values, []string{"target.example.net."}) {
		t.Fatalf("upserted values = %#v", upserted.Values)
	}
}
func TestZoneReconcilerMatchesTXTRecordByParsedRoute53Chunks(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	ttl := int64(300)
	provider.records[recordKey("Z000001", "_acme-challenge.apps.example.com.", dnsv1alpha1.RecordTypeTXT)] = RecordSetResource{
		HostedZoneID: "Z000001",
		Name:         "_acme-challenge.apps.example.com.",
		Type:         dnsv1alpha1.RecordTypeTXT,
		TTL:          &ttl,
		Values:       []string{`"v=spf1 " "include:_spf.example.net ~all"`, `"challenge-token"`},
	}
	recordSet := route53TXTRecordSet("app", "acme-challenge")
	setRecordSetStatusData(t, recordSet, route53v1alpha1.Route53RecordSetStatusData{
		HostedZoneID: "Z000001",
		RecordName:   "_acme-challenge.apps.example.com.",
		RecordType:   string(dnsv1alpha1.RecordTypeTXT),
	})
	objects := route53RecordSetObjects(t, recordSet)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	result, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.Requeue {
		t.Fatalf("Reconcile result = %#v, want no immediate requeue", result)
	}
	if len(provider.upserted) != 0 {
		t.Fatalf("upserted record sets = %d, want 0", len(provider.upserted))
	}
}
func TestZoneReconcilerMatchesCNAMERecordByCanonicalRoute53Name(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	ttl := int64(300)
	provider.records[recordKey("Z000001", "www.apps.example.com.", dnsv1alpha1.RecordTypeCNAME)] = RecordSetResource{
		HostedZoneID: "Z000001",
		Name:         "www.apps.example.com.",
		Type:         dnsv1alpha1.RecordTypeCNAME,
		TTL:          &ttl,
		Values:       []string{"target.example.net."},
	}
	recordSet := route53CNAMERecordSet("app", "www-cname")
	setRecordSetStatusData(t, recordSet, route53v1alpha1.Route53RecordSetStatusData{
		HostedZoneID: "Z000001",
		RecordName:   "www.apps.example.com.",
		RecordType:   string(dnsv1alpha1.RecordTypeCNAME),
	})
	objects := route53RecordSetObjects(t, recordSet)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	result, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.Requeue {
		t.Fatalf("Reconcile result = %#v, want no immediate requeue", result)
	}
	if len(provider.upserted) != 0 {
		t.Fatalf("upserted record sets = %d, want 0", len(provider.upserted))
	}
}
func TestZoneReconcilerCreatesMXRecordFromZoneReconcile(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	recordSet := route53MXRecordSet("app", "mail")
	objects := route53RecordSetObjects(t, recordSet)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	result, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("Reconcile did not request a requeue for a pending Route 53 record change")
	}
	if len(provider.upserted) != 1 {
		t.Fatalf("upserted record sets = %d, want 1", len(provider.upserted))
	}
	upserted := provider.upserted[0]
	if upserted.HostedZoneID != "Z000001" || upserted.Name != "apps.example.com." || upserted.Type != dnsv1alpha1.RecordTypeMX {
		t.Fatalf("upserted identity = %#v", upserted)
	}
	if upserted.TTL == nil || *upserted.TTL != 300 {
		t.Fatalf("upserted ttl = %#v, want 300", upserted.TTL)
	}
	want := []string{"10 mail1.example.net.", "20 mail2.example.net."}
	if !slices.Equal(upserted.Values, want) {
		t.Fatalf("upserted values = %#v, want %#v", upserted.Values, want)
	}
}
func TestZoneReconcilerMatchesMXRecordByParsedRoute53Values(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	ttl := int64(300)
	provider.records[recordKey("Z000001", "apps.example.com.", dnsv1alpha1.RecordTypeMX)] = RecordSetResource{
		HostedZoneID: "Z000001",
		Name:         "apps.example.com.",
		Type:         dnsv1alpha1.RecordTypeMX,
		TTL:          &ttl,
		Values:       []string{"20 mail2.example.net.", "10 mail1.example.net."},
	}
	recordSet := route53MXRecordSet("app", "mail")
	setRecordSetStatusData(t, recordSet, route53v1alpha1.Route53RecordSetStatusData{
		HostedZoneID: "Z000001",
		RecordName:   "apps.example.com.",
		RecordType:   string(dnsv1alpha1.RecordTypeMX),
	})
	objects := route53RecordSetObjects(t, recordSet)
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.RecordSet{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}, &dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}

	result, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.Requeue {
		t.Fatalf("Reconcile result = %#v, want no immediate requeue", result)
	}
	if len(provider.upserted) != 0 {
		t.Fatalf("upserted record sets = %d, want 0", len(provider.upserted))
	}
}
