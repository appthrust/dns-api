package endpointrecordset

import (
	"context"
	"encoding/json"
	"testing"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

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
