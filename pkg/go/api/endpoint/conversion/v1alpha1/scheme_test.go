package v1alpha1_test

import (
	"testing"

	endpointconversionv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/conversion/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestAddToSchemeRegistersEndpointRecordSetConversion(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := endpointconversionv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme returned error: %v", err)
	}
	obj, err := scheme.New(endpointconversionv1alpha1.SchemeGroupVersion.WithKind("EndpointRecordSetConversion"))
	if err != nil {
		t.Fatalf("EndpointRecordSetConversion is not registered: %v", err)
	}
	if _, ok := obj.(*endpointconversionv1alpha1.EndpointRecordSetConversion); !ok {
		t.Fatalf("kind EndpointRecordSetConversion created %T", obj)
	}
}
