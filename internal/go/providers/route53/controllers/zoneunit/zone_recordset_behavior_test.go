package route53

import (
	"context"
	"fmt"
	"slices"
	"testing"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	"github.com/aws/smithy-go"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestZoneReconcilerCreatesNullMXRecordFromZoneReconcile(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	recordSet := route53MXRecordSet("app", "no-mail")
	recordSet.Spec.MX.Records = []dnsv1alpha1.MXRecord{{Preference: 0, Exchange: "."}}
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
	if !slices.Equal(provider.upserted[0].Values, []string{"0 ."}) {
		t.Fatalf("upserted values = %#v, want Null MX", provider.upserted[0].Values)
	}
}
func TestZoneReconcilerCreatesCAARecordFromZoneReconcile(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	recordSet := route53CAARecordSet("app", "ca-policy")
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
	if upserted.HostedZoneID != "Z000001" || upserted.Name != "apps.example.com." || upserted.Type != dnsv1alpha1.RecordTypeCAA {
		t.Fatalf("upserted identity = %#v", upserted)
	}
	if upserted.TTL == nil || *upserted.TTL != 300 {
		t.Fatalf("upserted ttl = %#v, want 300", upserted.TTL)
	}
	want := []string{`0 iodef "mailto:security@example.com"`, `0 issue "letsencrypt.org"`}
	if !slices.Equal(upserted.Values, want) {
		t.Fatalf("upserted values = %#v, want %#v", upserted.Values, want)
	}
}
func TestZoneReconcilerMatchesCAARecordByParsedRoute53Values(t *testing.T) {
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
	provider.records[recordKey("Z000001", "apps.example.com.", dnsv1alpha1.RecordTypeCAA)] = RecordSetResource{
		HostedZoneID: "Z000001",
		Name:         "apps.example.com.",
		Type:         dnsv1alpha1.RecordTypeCAA,
		TTL:          &ttl,
		Values:       []string{`0 iodef "mailto:security@example.com"`, `0 issue "letsencrypt.org"`},
	}
	recordSet := route53CAARecordSet("app", "ca-policy")
	setRecordSetStatusData(t, recordSet, route53v1alpha1.Route53RecordSetStatusData{
		HostedZoneID: "Z000001",
		RecordName:   "apps.example.com.",
		RecordType:   string(dnsv1alpha1.RecordTypeCAA),
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
func TestZoneReconcilerCreatesDelegatedNSRecordFromZoneReconcile(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	recordSet := route53NSRecordSet("app", "delegated")
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
	if upserted.HostedZoneID != "Z000001" || upserted.Name != "delegated.apps.example.com." || upserted.Type != dnsv1alpha1.RecordTypeNS {
		t.Fatalf("upserted identity = %#v", upserted)
	}
	if upserted.TTL == nil || *upserted.TTL != 300 {
		t.Fatalf("upserted ttl = %#v, want 300", upserted.TTL)
	}
	want := []string{"ns-111.example-dns.net.", "ns-222.example-dns.net."}
	if !slices.Equal(upserted.Values, want) {
		t.Fatalf("upserted values = %#v, want %#v", upserted.Values, want)
	}
}
func TestZoneReconcilerMatchesDelegatedNSRecordByParsedRoute53Values(t *testing.T) {
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
	provider.records[recordKey("Z000001", "delegated.apps.example.com.", dnsv1alpha1.RecordTypeNS)] = RecordSetResource{
		HostedZoneID: "Z000001",
		Name:         "delegated.apps.example.com.",
		Type:         dnsv1alpha1.RecordTypeNS,
		TTL:          &ttl,
		Values:       []string{"ns-222.example-dns.net.", "ns-111.example-dns.net."},
	}
	recordSet := route53NSRecordSet("app", "delegated")
	setRecordSetStatusData(t, recordSet, route53v1alpha1.Route53RecordSetStatusData{
		HostedZoneID: "Z000001",
		RecordName:   "delegated.apps.example.com.",
		RecordType:   string(dnsv1alpha1.RecordTypeNS),
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
func TestZoneReconcilerUpsertsRoute53AliasRecordSet(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	recordSet := route53AliasARecordSet("app", "alias")
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
		t.Fatalf("Reconcile did not request a requeue for a pending Route 53 alias change")
	}
	if len(provider.upserted) != 1 {
		t.Fatalf("upserted record sets = %d, want 1", len(provider.upserted))
	}
	upserted := provider.upserted[0]
	if upserted.HostedZoneID != "Z000001" || upserted.Name != "alias.apps.example.com." || upserted.Type != dnsv1alpha1.RecordTypeA {
		t.Fatalf("upserted identity = %#v", upserted)
	}
	if upserted.TTL != nil {
		t.Fatalf("upserted ttl = %#v, want nil for alias", upserted.TTL)
	}
	if len(upserted.Values) != 0 {
		t.Fatalf("upserted resource records = %#v, want none for alias", upserted.Values)
	}
	if upserted.Alias == nil {
		t.Fatalf("upserted alias = nil, want AliasTarget")
	}
	if upserted.Alias.DNSName != "target.apps.example.com." ||
		upserted.Alias.HostedZoneID != "Z000001" ||
		upserted.Alias.EvaluateTargetHealth {
		t.Fatalf("upserted alias = %#v", upserted.Alias)
	}
}
func TestZoneReconcilerAdoptsMatchingRoute53AliasRecordSet(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	provider.records[recordKey("Z000001", "alias.apps.example.com.", dnsv1alpha1.RecordTypeA)] = RecordSetResource{
		HostedZoneID: "Z000001",
		Name:         "alias.apps.example.com.",
		Type:         dnsv1alpha1.RecordTypeA,
		Alias: &route53v1alpha1.Route53AliasTarget{
			DNSName:              "k8s-public-123456.ap-northeast-1.elb.amazonaws.com.",
			HostedZoneID:         "/hostedzone/Z14GRHDCWA56QT",
			EvaluateTargetHealth: true,
		},
	}
	recordSet := route53AliasARecordSet("app", "alias")
	recordSet.Spec.Adoption = runtime.RawExtension{Raw: []byte(`{"enabled":true}`)}
	recordSet.Spec.Options = runtime.RawExtension{Raw: []byte(`{"alias":{"dnsName":"dualstack.k8s-public-123456.ap-northeast-1.elb.amazonaws.com.","hostedZoneID":"Z14GRHDCWA56QT","evaluateTargetHealth":true}}`)}
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
	assertCondition(t, unit.Status.RecordSets[index].Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionTrue, "Programmed")
}
func TestZoneReconcilerRejectsRecordSetWhenCNAMEZoneUnitItemOwnsSameName(t *testing.T) {
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
	objects := []client.Object{
		route53Provider(),
		route53ZoneClass("app", "route53-public", nil),
		acceptedRoute53Identity("app", "route53-dev"),
		route53ReadyZone(t, "app", "apps-example-com"),
		route53ZoneUnitWithRecordSetItem("app", "apps-example-com", "other", "www-cname", "www", dnsv1alpha1.RecordTypeCNAME),
		recordSet,
	}
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
	assertCondition(t, gotRecordSet.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionFalse, "RecordSetConflict")
	if len(provider.upserted) != 0 {
		t.Fatalf("upserted record sets = %d, want 0", len(provider.upserted))
	}
}
func TestZoneReconcilerMatchesAAAARecordByParsedAddress(t *testing.T) {
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
	provider.records[recordKey("Z000001", "www.apps.example.com.", dnsv1alpha1.RecordTypeAAAA)] = RecordSetResource{
		HostedZoneID: "Z000001",
		Name:         "www.apps.example.com.",
		Type:         dnsv1alpha1.RecordTypeAAAA,
		TTL:          &ttl,
		Values:       []string{"2001:0db8:0000::0010"},
	}
	recordSet := route53AAAARecordSet("app", "www-v6")
	setRecordSetStatusData(t, recordSet, route53v1alpha1.Route53RecordSetStatusData{
		HostedZoneID: "Z000001",
		RecordName:   "www.apps.example.com.",
		RecordType:   string(dnsv1alpha1.RecordTypeAAAA),
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
func TestZoneReconcilerMatchesEscapedRoute53WildcardRecordName(t *testing.T) {
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
	provider.records[recordKey("Z000001", "\\052.apps.example.com.", dnsv1alpha1.RecordTypeA)] = RecordSetResource{
		HostedZoneID: "Z000001",
		Name:         "\\052.apps.example.com.",
		Type:         dnsv1alpha1.RecordTypeA,
		TTL:          &ttl,
		Values:       []string{"192.0.2.10"},
	}
	recordSet := route53ARecordSet("app", "wildcard")
	recordSet.Spec.Name = "*"
	setRecordSetStatusData(t, recordSet, route53v1alpha1.Route53RecordSetStatusData{
		HostedZoneID: "Z000001",
		RecordName:   "*.apps.example.com.",
		RecordType:   string(dnsv1alpha1.RecordTypeA),
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

	var gotRecordSet dnsv1alpha1.RecordSet
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(recordSet), &gotRecordSet); err != nil {
		t.Fatalf("RecordSet was not found: %v", err)
	}
	assertCondition(t, gotRecordSet.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionTrue, "Programmed")
	if gotRecordSet.Status.Provider != nil {
		t.Fatalf("status.provider = %#v, want no Route 53 RecordSet provider data", gotRecordSet.Status.Provider)
	}
}
func TestZoneReconcilerRecordsRoute53RecordSetEventsOnRecordSets(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	recorder := &capturingRecorder{}
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
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider, Recorder: recorder}

	if _, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if !recorder.has("RecordSet", "Route53RecordSetChangeSubmitted") {
		t.Fatalf("Route53RecordSetChangeSubmitted was not recorded on a RecordSet: %#v", recorder.events)
	}
	if recorder.has("Zone", "Route53RecordSetChangeSubmitted") {
		t.Fatalf("Route53RecordSetChangeSubmitted was recorded on a Zone: %#v", recorder.events)
	}

	var zone dnsv1alpha1.Zone
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &zone); err != nil {
		t.Fatalf("Zone was not found: %v", err)
	}
	pending := mustZoneUnitStatusData(t, ctx, k8sClient, zone.Namespace, zone.Name).PendingRecordSetChange
	if pending == nil || pending.ID == "" {
		t.Fatalf("pendingRecordSetChange = %#v, want pending record set change", pending)
	}
	// Core projection reads this receipt to update the child claim; it does not
	// clear the provider-owned receipt while Route 53 still reports the change.
	provider.changes[pending.ID].Status = route53v1alpha1.Route53ChangeStatusInSync

	if _, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}}); err != nil {
		t.Fatalf("second Reconcile returned error: %v", err)
	}
	if !recorder.has("RecordSet", "Route53RecordSetChangeInSync") {
		t.Fatalf("Route53RecordSetChangeInSync was not recorded on a RecordSet: %#v", recorder.events)
	}
	if recorder.has("Zone", "Route53RecordSetChangeInSync") {
		t.Fatalf("Route53RecordSetChangeInSync was recorded on a Zone: %#v", recorder.events)
	}
	statusData := mustZoneUnitRecordSetStatusState(t, ctx, k8sClient, zone.Namespace, zone.Name, recordSet.Namespace, recordSet.Name)
	if statusData.HostedZoneID != "Z000001" || statusData.RecordName != "www.apps.example.com." || statusData.RecordType != string(dnsv1alpha1.RecordTypeA) {
		t.Fatalf("ZoneUnit recordSet provider state = %#v, want Route 53 record identity after INSYNC", statusData)
	}
	var gotUnit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: zone.Namespace, Name: zone.Name}, &gotUnit); err != nil {
		t.Fatalf("ZoneUnit was not found after INSYNC: %v", err)
	}
	statusIndex := slices.IndexFunc(gotUnit.Status.RecordSets, func(status dnsv1alpha1.ZoneUnitRecordSetStatus) bool {
		return status.RecordSetNamespace == recordSet.Namespace && status.RecordSetName == recordSet.Name
	})
	if statusIndex < 0 {
		t.Fatalf("ZoneUnit status.recordSets has no entry after INSYNC for %s/%s: %#v", recordSet.Namespace, recordSet.Name, gotUnit.Status.RecordSets)
	}
	assertCondition(t, gotUnit.Status.RecordSets[statusIndex].Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionTrue, "Programmed")
}
func TestZoneReconcilerDeletesRecordSetFromSpecIdentity(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	provider := newFakeProvider()
	provider.zones["Z000001"] = HostedZone{
		ID:              "Z000001",
		Name:            "apps.example.com",
		CallerReference: "dns-api:11111111-2222-3333-4444-555555555555",
	}
	ttl := int64(300)
	provider.records[recordKey("Z000001", "www.apps.example.com.", dnsv1alpha1.RecordTypeA)] = RecordSetResource{
		HostedZoneID: "Z000001",
		Name:         "www.apps.example.com.",
		Type:         dnsv1alpha1.RecordTypeA,
		TTL:          &ttl,
		Values:       []string{"192.0.2.10"},
	}
	recordSet := deletingRoute53ARecordSet(t)
	setRecordSetStatusData(t, recordSet, route53v1alpha1.Route53RecordSetStatusData{
		HostedZoneID: "ZSTALE",
		RecordName:   "stale.apps.example.com.",
		RecordType:   string(dnsv1alpha1.RecordTypeA),
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
	if result.RequeueAfter == 0 {
		t.Fatalf("Reconcile did not request a requeue for a pending Route 53 deletion")
	}
	if len(provider.deletedRRs) != 1 {
		t.Fatalf("deleted record sets = %d, want 1", len(provider.deletedRRs))
	}
	deleted := provider.deletedRRs[0]
	if deleted.HostedZoneID != "Z000001" || deleted.Name != "www.apps.example.com." || deleted.Type != dnsv1alpha1.RecordTypeA {
		t.Fatalf("deleted identity = %#v", deleted)
	}

	var zone dnsv1alpha1.Zone
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &zone); err != nil {
		t.Fatalf("Zone was not found: %v", err)
	}
	pending := mustZoneUnitStatusData(t, ctx, k8sClient, zone.Namespace, zone.Name).PendingRecordSetChange
	if pending == nil || pending.ID == "" {
		t.Fatalf("pendingRecordSetChange = %#v, want pending delete", pending)
	}
	provider.changes[pending.ID].Status = route53v1alpha1.Route53ChangeStatusInSync

	if _, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}}); err != nil {
		t.Fatalf("second Reconcile returned error: %v", err)
	}
	var unit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &unit); err != nil {
		t.Fatalf("ZoneUnit was not found: %v", err)
	}
	statusIndex := slices.IndexFunc(unit.Status.RecordSets, func(status dnsv1alpha1.ZoneUnitRecordSetStatus) bool {
		return status.RecordSetNamespace == recordSet.Namespace && status.RecordSetName == recordSet.Name
	})
	if statusIndex < 0 ||
		!unit.Status.RecordSets[statusIndex].DeletionCompleted ||
		unit.Status.RecordSets[statusIndex].RecordSetUID != recordSet.UID {
		t.Fatalf("ZoneUnit recordSet deletion completion = %#v, want current UID completed", unit.Status.RecordSets)
	}
	if len(unit.Spec.RecordSets) != 0 {
		t.Fatalf("core retained spec item after completed deletion: %#v", unit.Spec.RecordSets)
	}

	// Once core consumed the completion and dropped the spec item, the ledger
	// entry is orphaned and the provider prunes it.
	if _, err := reconcileRoute53AndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}}); err != nil {
		t.Fatalf("third Reconcile returned error: %v", err)
	}
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &unit); err != nil {
		t.Fatalf("ZoneUnit was not found: %v", err)
	}
	if len(unit.Status.RecordSets) != 0 {
		t.Fatalf("orphaned ledger entry was not pruned: %#v", unit.Status.RecordSets)
	}
}
func TestZoneReconcilerIgnoresCompletedDeletionStatusForActiveZoneUnitItem(t *testing.T) {
	recordSet := route53ARecordSet("app", "www-a")
	unit := route53ZoneUnit("app", "apps-example-com", "app", "route53-public")
	unit.Spec.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetSpec{route53ZoneUnitRecordSetSpec(recordSet)}
	unit.Status.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetStatus{
		{
			RecordSetNamespace: "app",
			RecordSetName:      "www-a",
			RecordSetUID:       recordSet.UID,
			ObservedGeneration: 2,
			DeletionCompleted:  true,
			Provider: &dnsv1alpha1.ProviderStatus{
				Data: runtime.RawExtension{Raw: []byte(`{"hostedZoneID":"Z000001","recordName":"www.apps.example.com.","recordType":"A"}`)},
			},
			Conditions: []metav1.Condition{
				{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted"},
				{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionTrue, Reason: "Programmed"},
			},
		},
	}
	recordSets, _ := zoneUnitRecordSets(unit)
	if len(recordSets) != 1 {
		t.Fatalf("recordSets = %#v, want one record set", recordSets)
	}
	got := recordSets[0]
	if got.Status.Provider != nil {
		t.Fatalf("provider status = %#v, want nil", got.Status.Provider)
	}
	if len(got.Status.Conditions) != 0 {
		t.Fatalf("conditions = %#v, want empty", got.Status.Conditions)
	}
	if got.Status.ObservedGeneration != recordSet.Generation {
		t.Fatalf("observedGeneration = %d, want %d", got.Status.ObservedGeneration, recordSet.Generation)
	}
}
func TestProviderErrorConditionSanitizesAWSAPIError(t *testing.T) {
	err := &smithy.OperationError{
		ServiceID:     "STS",
		OperationName: "GetCallerIdentity",
		Err: fmt.Errorf(
			"https response error StatusCode: 400, RequestID: req-1, %w",
			&smithy.GenericAPIError{
				Code:    "InvalidGrantException",
				Message: "SSO token could not be refreshed",
			},
		),
	}

	reason, message := providerErrorCondition(err)

	if reason != "ProviderAccessDenied" {
		t.Fatalf("reason = %q, want ProviderAccessDenied", reason)
	}
	if message != "InvalidGrantException: SSO token could not be refreshed" {
		t.Fatalf("message = %q", message)
	}
}
func TestNormalizeRoute53RecordName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "adds trailing dot and lowercases",
			in:   "WWW.Apps.Example.Com",
			want: "www.apps.example.com.",
		},
		{
			name: "unescapes route53 wildcard octal",
			in:   "\\052.Platform2.Test.",
			want: "*.platform2.test.",
		},
		{
			name: "keeps non octal escape literal",
			in:   "\\x52.platform2.test.",
			want: "\\x52.platform2.test.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeRoute53RecordName(tt.in); got != tt.want {
				t.Fatalf("normalizeRoute53RecordName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
