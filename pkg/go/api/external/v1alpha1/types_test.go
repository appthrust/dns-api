package v1alpha1

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
)

func TestInlineOnlySchemeAndDeepcopy(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"ExternalIdentity", "ExternalZone", "ExternalRecordSet"} {
		if scheme.Recognizes(SchemeGroupVersion.WithKind(kind)) {
			t.Fatalf("unexpected CRD kind %s", kind)
		}
	}
	params := &ExternalZoneClassParameters{Resolver: ResolverPolicy{Mode: ResolverModeRecursive, Servers: []string{"192.0.2.53:53"}}, ExpectedNameservers: []string{"ns.example.test"}}
	copy := params.DeepCopy()
	copy.Resolver.Servers[0] = "192.0.2.54:53"
	copy.ExpectedNameservers[0] = "other.example.test"
	if params.Resolver.Servers[0] != "192.0.2.53:53" || params.ExpectedNameservers[0] != "ns.example.test" {
		t.Fatal("deepcopy aliases source")
	}
}
