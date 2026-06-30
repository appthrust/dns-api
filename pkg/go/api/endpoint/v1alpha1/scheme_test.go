package v1alpha1_test

import (
	"testing"

	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestAddToSchemeRegistersEndpointTypes(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := endpointv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme returned error: %v", err)
	}

	cases := map[string]func(runtime.Object) bool{
		"EndpointProviderCapability": func(obj runtime.Object) bool {
			_, ok := obj.(*endpointv1alpha1.EndpointProviderCapability)
			return ok
		},
	}
	for kind, matches := range cases {
		obj, err := scheme.New(endpointv1alpha1.SchemeGroupVersion.WithKind(kind))
		if err != nil {
			t.Fatalf("%s is not registered: %v", kind, err)
		}
		if !matches(obj) {
			t.Fatalf("kind %s created %T", kind, obj)
		}
	}
}
