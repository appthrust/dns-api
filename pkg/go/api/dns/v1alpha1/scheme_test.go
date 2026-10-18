package v1alpha1_test

import (
	"encoding/json"
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

func TestZoneClassOptionalIdentityWireAndDeepCopy(t *testing.T) {
	zoneClass := &dnsv1alpha1.ZoneClass{}
	payload, err := json.Marshal(zoneClass.Spec)
	if err != nil {
		t.Fatalf("marshal ZoneClass spec: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatalf("decode ZoneClass spec: %v", err)
	}
	if _, present := fields["identityRef"]; present {
		t.Fatalf("credentialless ZoneClass serialized identityRef: %s", payload)
	}
	if copied := zoneClass.DeepCopy(); copied.Spec.IdentityRef != nil {
		t.Fatalf("DeepCopy manufactured identityRef: %#v", copied.Spec.IdentityRef)
	}
	zoneClass.Spec.IdentityRef = &dnsv1alpha1.LocalObjectReference{Name: "original"}
	copied := zoneClass.DeepCopy()
	copied.Spec.IdentityRef.Name = "changed"
	if zoneClass.Spec.IdentityRef.Name != "original" {
		t.Fatal("DeepCopy aliased the original identityRef")
	}
}
