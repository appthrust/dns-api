package webhook

import (
	"strings"
	"testing"

	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRoute53IdentityValidationRejectsInvalidRegionAndRoleARN(t *testing.T) {
	t.Run("invalid region", func(t *testing.T) {
		identity := validRoute53Identity()
		identity.Spec.Region = "not-a-region"

		err := validateRoute53Identity(identity)
		if err == nil || !strings.Contains(err.Error(), "spec.region") {
			t.Fatalf("validateRoute53Identity error = %v, want region failure", err)
		}
	})

	t.Run("non ARN role", func(t *testing.T) {
		identity := validRoute53Identity()
		identity.Spec.AssumeRoleChain = []route53v1alpha1.Route53AssumeRole{{RoleARN: "not-an-arn"}}

		err := validateRoute53Identity(identity)
		if err == nil || !strings.Contains(err.Error(), "roleARN") {
			t.Fatalf("validateRoute53Identity error = %v, want roleARN failure", err)
		}
	})
}

func validRoute53Identity() *route53v1alpha1.Route53Identity {
	return &route53v1alpha1.Route53Identity{
		ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "route53", Generation: 1},
		Spec: route53v1alpha1.Route53IdentitySpec{
			AccountID: "123456789012",
			Region:    "us-east-1",
			Credentials: route53v1alpha1.Route53Credentials{
				Runtime: &route53v1alpha1.Route53RuntimeCredentials{},
			},
		},
	}
}
