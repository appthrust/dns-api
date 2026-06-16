package v1alpha1_test

import (
	"testing"

	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestAddToSchemeRegistersRoute53Types(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := route53v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme returned error: %v", err)
	}

	for _, tt := range []struct {
		kind string
		want func(any) bool
	}{
		{
			kind: "Route53Identity",
			want: func(obj any) bool {
				_, ok := obj.(*route53v1alpha1.Route53Identity)
				return ok
			},
		},
	} {
		obj, err := scheme.New(route53v1alpha1.SchemeGroupVersion.WithKind(tt.kind))
		if err != nil {
			t.Fatalf("%s is not registered: %v", tt.kind, err)
		}

		if !tt.want(obj) {
			t.Fatalf("got %T for %s", obj, tt.kind)
		}
	}
}
