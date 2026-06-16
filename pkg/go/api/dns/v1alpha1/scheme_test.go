package v1alpha1_test

import (
	"testing"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestAddToSchemeRegistersCoreTypes(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := dnsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme returned error: %v", err)
	}

	cases := map[string]func(runtime.Object) bool{
		"Provider": func(obj runtime.Object) bool {
			_, ok := obj.(*dnsv1alpha1.Provider)
			return ok
		},
		"RecordSet": func(obj runtime.Object) bool {
			_, ok := obj.(*dnsv1alpha1.RecordSet)
			return ok
		},
		"ZoneUnit": func(obj runtime.Object) bool {
			_, ok := obj.(*dnsv1alpha1.ZoneUnit)
			return ok
		},
	}

	for kind, matches := range cases {
		obj, err := scheme.New(dnsv1alpha1.SchemeGroupVersion.WithKind(kind))
		if err != nil {
			t.Fatalf("%s is not registered: %v", kind, err)
		}
		if !matches(obj) {
			t.Fatalf("kind %s created %T", kind, obj)
		}
	}
}
