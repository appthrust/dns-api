package route53

import (
	"encoding/json"
	"testing"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := dnsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add dns scheme: %v", err)
	}
	if err := route53v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add route53 scheme: %v", err)
	}
	return scheme
}

func route53ZoneClass(namespace, name string, tags map[string]string) *dnsv1alpha1.ZoneClass {
	raw, err := json.Marshal(route53v1alpha1.Route53ZoneClassParameters{
		ZoneCreationPolicy: route53v1alpha1.ZoneCreationPolicyCreate,
		ZoneDeletionPolicy: route53v1alpha1.ZoneDeletionPolicyDelete,
		Tags:               tags,
	})
	if err != nil {
		panic(err)
	}
	return &dnsv1alpha1.ZoneClass{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Generation: 1},
		Spec: dnsv1alpha1.ZoneClassSpec{
			ControllerName: DefaultControllerName,
			Provider:       route53v1alpha1.ProviderRef,
			IdentityRef:    dnsv1alpha1.LocalObjectReference{Name: "route53-dev"},
			Parameters:     runtime.RawExtension{Raw: raw},
		},
	}
}

func route53Provider() *dnsv1alpha1.Provider {
	return &dnsv1alpha1.Provider{
		ObjectMeta: metav1.ObjectMeta{Name: route53v1alpha1.ProviderName},
		Spec: dnsv1alpha1.ProviderSpec{
			Display: dnsv1alpha1.ProviderDisplay{Name: "Amazon Route 53"},
			Versions: []dnsv1alpha1.ProviderVersion{
				{
					Name:    route53v1alpha1.ProviderVersion,
					Served:  true,
					Storage: true,
					Identity: &dnsv1alpha1.ProviderIdentity{
						Resource: dnsv1alpha1.ProviderIdentityResource{
							Group: "route53.dns.appthrust.io",
							Kind:  "Route53Identity",
							Scope: "Namespaced",
						},
					},
					RecordSet: dnsv1alpha1.ProviderRecordSet{
						SupportedTypes: []dnsv1alpha1.RecordType{
							dnsv1alpha1.RecordTypeA,
							dnsv1alpha1.RecordTypeAAAA,
							dnsv1alpha1.RecordTypeTXT,
							dnsv1alpha1.RecordTypeCNAME,
							dnsv1alpha1.RecordTypeMX,
							dnsv1alpha1.RecordTypeCAA,
							dnsv1alpha1.RecordTypeNS,
						},
					},
				},
			},
		},
	}
}

func acceptedRoute53Identity(namespace, name string) *route53v1alpha1.Route53Identity {
	return &route53v1alpha1.Route53Identity{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Generation: 1},
		Spec: route53v1alpha1.Route53IdentitySpec{
			AccountID: "123456789012",
			Region:    "ap-northeast-1",
			Credentials: route53v1alpha1.Route53Credentials{
				Runtime: &route53v1alpha1.Route53RuntimeCredentials{},
			},
		},
		Status: route53v1alpha1.Route53IdentityStatus{
			Conditions: []metav1.Condition{
				{
					Type:               string(dnsv1alpha1.ConditionAccepted),
					Status:             metav1.ConditionTrue,
					Reason:             "Accepted",
					ObservedGeneration: 1,
				},
				{
					Type:               "Ready",
					Status:             metav1.ConditionTrue,
					Reason:             "Ready",
					ObservedGeneration: 1,
				},
			},
		},
	}
}

func assertCondition(t *testing.T, conditions []metav1.Condition, conditionType string, status metav1.ConditionStatus, reason string) metav1.Condition {
	t.Helper()
	condition := meta.FindStatusCondition(conditions, conditionType)
	if condition == nil {
		t.Fatalf("condition %s was not found in %#v", conditionType, conditions)
	}
	if condition.Status != status || condition.Reason != reason {
		t.Fatalf("condition %s = (%s, %s), want (%s, %s)", conditionType, condition.Status, condition.Reason, status, reason)
	}
	return *condition
}
