package declaration

import (
	"context"
	"testing"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestSharedParentZonePolicyAllowsOnlyOrganizationAliasRecords(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	writerNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   "organization-endpoints",
		Labels: map[string]string{"appthrust.io/dns-writer": "organization-endpoints"},
	}}
	tenantNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-project"}}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(writerNamespace, tenantNamespace).Build()
	zone := &dnsv1alpha1.Zone{
		ObjectMeta: metav1.ObjectMeta{Namespace: "platform-root-dns", Name: "appthrust-app"},
		Spec: dnsv1alpha1.ZoneSpec{
			DomainName: "appthrust.app",
			AllowedRecordSets: []dnsv1alpha1.AllowedRecordSet{{
				Namespaces: dnsv1alpha1.AllowedRecordSetNamespaces{Selector: metav1.LabelSelector{MatchLabels: map[string]string{"appthrust.io/dns-writer": "organization-endpoints"}}},
				Records: []dnsv1alpha1.AllowedRecord{{
					Name:  dnsv1alpha1.RecordNamePolicy{Pattern: `([a-z0-9]([-a-z0-9]*[a-z0-9])?|\*\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)`},
					Types: []dnsv1alpha1.RecordType{dnsv1alpha1.RecordTypeA, dnsv1alpha1.RecordTypeAAAA},
				}},
			}},
		},
	}

	tests := []struct {
		name       string
		namespace  string
		recordName string
		recordType dnsv1alpha1.RecordType
		allowed    bool
	}{
		{name: "organization apex alias A", namespace: writerNamespace.Name, recordName: "reo", recordType: dnsv1alpha1.RecordTypeA, allowed: true},
		{name: "organization wildcard alias AAAA", namespace: writerNamespace.Name, recordName: "*.reo", recordType: dnsv1alpha1.RecordTypeAAAA, allowed: true},
		{name: "tenant cannot forge organization apex", namespace: tenantNamespace.Name, recordName: "reo", recordType: dnsv1alpha1.RecordTypeA},
		{name: "parent apex", recordName: "@", recordType: dnsv1alpha1.RecordTypeA},
		{name: "root CAA", recordName: "@", recordType: dnsv1alpha1.RecordTypeCAA},
		{name: "root TXT", recordName: "@", recordType: dnsv1alpha1.RecordTypeTXT},
		{name: "delegated NS", recordName: "reo", recordType: dnsv1alpha1.RecordTypeNS},
		{name: "deeper record", recordName: "api.reo", recordType: dnsv1alpha1.RecordTypeA},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			namespace := tt.namespace
			if namespace == "" {
				namespace = writerNamespace.Name
			}
			recordSet := &dnsv1alpha1.RecordSet{
				ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "candidate"},
				Spec:       dnsv1alpha1.RecordSetSpec{Name: tt.recordName, Type: tt.recordType},
			}
			allowed, _ := RecordSetAllowedByZone(context.Background(), client, recordSet, zone)
			if allowed != tt.allowed {
				t.Fatalf("RecordSetAllowedByZone(%q, %q) = %t, want %t", tt.recordName, tt.recordType, allowed, tt.allowed)
			}
		})
	}
}

func TestSharedParentZoneRootOwnerUsesSeparateNamespaceTrustBoundary(t *testing.T) {
	zone := &dnsv1alpha1.Zone{ObjectMeta: metav1.ObjectMeta{Namespace: "platform-root-dns", Name: "appthrust-app"}}
	rootCAA := &dnsv1alpha1.RecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: zone.Namespace, Name: "root-caa"},
		Spec:       dnsv1alpha1.RecordSetSpec{Name: "@", Type: dnsv1alpha1.RecordTypeCAA},
	}
	allowed, _ := RecordSetAllowedByZone(context.Background(), fake.NewClientBuilder().Build(), rootCAA, zone)
	if !allowed {
		t.Fatal("same-namespace root owner must remain independent from cross-namespace Organization endpoint grants")
	}
}
