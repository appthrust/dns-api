package v1alpha1

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	structuralcel "k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel"
	apiservervalidation "k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sjson "k8s.io/apimachinery/pkg/util/json"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/yaml"
)

func TestSchemeAndDeepcopy(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"HostedZone", "HostedZoneList"} {
		if !scheme.Recognizes(SchemeGroupVersion.WithKind(kind)) {
			t.Fatalf("missing CRD kind %s", kind)
		}
	}
	for _, kind := range []string{"HostedIdentity", "HostedRecordSet", "HostedZoneClassParameters"} {
		if scheme.Recognizes(SchemeGroupVersion.WithKind(kind)) {
			t.Fatalf("unexpected CRD kind %s", kind)
		}
	}
	params := &HostedZoneClassParameters{
		NameServers: []NameServer{{Name: "ns.example.test", Addresses: []string{"192.0.2.53"}}},
		Verification: VerificationPolicy{
			Views:      []string{"192.0.2.53:53"},
			Delegation: DelegationPolicy{Resolver: ResolverPolicy{Mode: "Recursive", Servers: []string{"[2001:db8::53]:53"}}},
		},
	}
	copy := params.DeepCopy()
	copy.NameServers[0].Addresses[0] = "192.0.2.54"
	copy.Verification.Views[0] = "192.0.2.54:53"
	copy.Verification.Delegation.Resolver.Mode = "Cluster"
	copy.Verification.Delegation.Resolver.Servers[0] = "[2001:db8::54]:53"
	if params.NameServers[0].Addresses[0] != "192.0.2.53" || params.Verification.Views[0] != "192.0.2.53:53" || params.Verification.Delegation.Resolver.Mode != "Recursive" || params.Verification.Delegation.Resolver.Servers[0] != "[2001:db8::53]:53" {
		t.Fatal("parameter deepcopy aliases source")
	}
	zone := &HostedZone{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"owner": "controller"}},
		Spec: HostedZoneSpec{
			NameServers: params.NameServers,
			Records:     []HostedZoneRecord{{Name: "@", Type: "A", TTL: 60, Values: []string{"192.0.2.1"}}},
		},
		Status: HostedZoneStatus{Servers: []HostedZoneServerStatus{{Endpoint: "dns/server", Serial: 1}}},
	}
	zoneCopy := zone.DeepCopyObject().(*HostedZone)
	zoneCopy.Labels["owner"] = "other"
	zoneCopy.Spec.NameServers[0].Addresses[0] = "192.0.2.54"
	zoneCopy.Spec.Records[0].Values[0] = "192.0.2.2"
	zoneCopy.Status.Servers[0].Serial = 2
	if zone.Labels["owner"] != "controller" || zone.Spec.NameServers[0].Addresses[0] != "192.0.2.53" || zone.Spec.Records[0].Values[0] != "192.0.2.1" || zone.Status.Servers[0].Serial != 1 {
		t.Fatal("snapshot deepcopy aliases source")
	}
	list := &HostedZoneList{Items: []HostedZone{*zone}}
	listCopy := list.DeepCopyObject().(*HostedZoneList)
	listCopy.Items[0].Spec.Records[0].Values[0] = "192.0.2.3"
	if list.Items[0].Spec.Records[0].Values[0] != "192.0.2.1" {
		t.Fatal("list deepcopy aliases source")
	}
}

func TestSnapshotJSONContract(t *testing.T) {
	const input = `{"spec":{"domainName":"example.test","serial":4294967295,"soa":{"mname":"ns.example.test","rname":"hostmaster.example.test","refresh":3600,"retry":900,"expire":1209600,"minimum":300},"nameServers":[{"name":"ns.example.test","addresses":["192.0.2.53"]}],"records":[{"name":"@","type":"A","ttl":60,"values":["192.0.2.1"]}],"contentDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"status":{"servers":[{"endpoint":"dns/server","serial":4294967295,"contentDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","observedAt":"2026-10-21T00:00:00Z"}]}}`
	var zone HostedZone
	if err := json.Unmarshal([]byte(input), &zone); err != nil {
		t.Fatal(err)
	}
	if zone.Spec.DomainName != "example.test" || zone.Spec.Serial != ^uint32(0) || zone.Spec.SOA.MName != "ns.example.test" || zone.Spec.SOA.RName != "hostmaster.example.test" || zone.Spec.SOA.Minimum != 300 || zone.Spec.Records[0].TTL != 60 || zone.Status.Servers[0].Serial != zone.Spec.Serial || zone.Status.Servers[0].ObservedAt.IsZero() {
		t.Fatalf("snapshot JSON fields were lost: %#v", zone)
	}
}

func TestGeneratedSnapshotSchema(t *testing.T) {
	data, err := os.ReadFile("../../../../../app/operator/config/crd/bases/hosted.dns.appthrust.io_hostedzones.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.UnmarshalStrict(data, &crd); err != nil {
		t.Fatal(err)
	}
	if crd.Spec.Group != GroupName || crd.Spec.Scope != apiextensionsv1.NamespaceScoped || len(crd.Spec.Versions) != 1 || crd.Spec.Versions[0].Name != ProviderVersion || crd.Spec.Versions[0].Subresources.Status == nil {
		t.Fatalf("unexpected CRD contract: %#v", crd.Spec)
	}
	columns := crd.Spec.Versions[0].AdditionalPrinterColumns
	if len(columns) != 3 || columns[0].Name != "Domain" || columns[1].Name != "Serial" || columns[2].Name != "Servers" {
		t.Fatalf("unexpected printer columns: %#v", columns)
	}
	var converted apiextensions.JSONSchemaProps
	if err := apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(crd.Spec.Versions[0].Schema.OpenAPIV3Schema, &converted, nil); err != nil {
		t.Fatal(err)
	}
	schema := &converted
	spec := schema.Properties["spec"]
	for _, entry := range []struct {
		schema apiextensions.JSONSchemaProps
		keys   []string
	}{{spec.Properties["records"], []string{"name", "type"}}, {schema.Properties["status"].Properties["servers"], []string{"endpoint"}}} {
		if entry.schema.XListType == nil || *entry.schema.XListType != "map" || strings.Join(entry.schema.XListMapKeys, ",") != strings.Join(entry.keys, ",") {
			t.Fatalf("map-list contract differs: %#v", entry.schema)
		}
	}
	validator, _, err := apiservervalidation.NewSchemaValidator(schema)
	if err != nil {
		t.Fatal(err)
	}
	structural, err := structuralschema.NewStructural(schema)
	if err != nil {
		t.Fatal(err)
	}
	celValidator := structuralcel.NewValidator(structural, true, 1000000)
	if celValidator == nil {
		t.Fatal("snapshot CEL validations are missing")
	}
	cases := []struct {
		name   string
		mutate func(*HostedZone)
		valid  bool
	}{
		{"valid", func(*HostedZone) {}, true},
		{"max serial", func(z *HostedZone) { z.Spec.Serial = ^uint32(0) }, true},
		{"zero serial", func(z *HostedZone) { z.Spec.Serial = 0 }, false},
		{"uppercase domain", func(z *HostedZone) { z.Spec.DomainName = "Example.test" }, false},
		{"root-dot domain", func(z *HostedZone) { z.Spec.DomainName = "example.test." }, false},
		{"bad digest", func(z *HostedZone) { z.Spec.ContentDigest = "sha256:bad" }, false},
		{"uppercase digest", func(z *HostedZone) { z.Spec.ContentDigest = "sha256:" + strings.Repeat("A", 64) }, false},
		{"missing nameservers", func(z *HostedZone) { z.Spec.NameServers = []NameServer{} }, false},
		{"invalid glue", func(z *HostedZone) { z.Spec.NameServers[0].Addresses[0] = "999.0.2.53" }, false},
		{"noncanonical glue", func(z *HostedZone) { z.Spec.NameServers[0].Addresses[0] = "2001:DB8::53" }, false},
		{"IP nameserver", func(z *HostedZone) { z.Spec.NameServers[0].Name = "192.0.2.53" }, false},
		{"wildcard", func(z *HostedZone) { z.Spec.Records[0].Name = "*.team" }, true},
		{"apex wildcard", func(z *HostedZone) { z.Spec.Records[0].Name = "*" }, true},
		{"TXT underscore", func(z *HostedZone) { z.Spec.Records[0].Name = "_acme-challenge"; z.Spec.Records[0].Type = "TXT" }, true},
		{"relative", func(z *HostedZone) { z.Spec.Records[0].Name = "www.team" }, true},
		{"nonleftmost wildcard", func(z *HostedZone) { z.Spec.Records[0].Name = "team.*" }, false},
		{"partial wildcard", func(z *HostedZone) { z.Spec.Records[0].Name = "*team" }, false},
		{"root-dot owner", func(z *HostedZone) { z.Spec.Records[0].Name = "www." }, false},
		{"uppercase owner", func(z *HostedZone) { z.Spec.Records[0].Name = "WWW" }, false},
		{"unsupported type", func(z *HostedZone) { z.Spec.Records[0].Type = "PTR" }, false},
		{"zero TTL", func(z *HostedZone) { z.Spec.Records[0].TTL = 0 }, false},
		{"empty values", func(z *HostedZone) { z.Spec.Records[0].Values = []string{} }, false},
		{"64 values", func(z *HostedZone) { z.Spec.Records[0].Values = make([]string, 64) }, true},
		{"65 values", func(z *HostedZone) { z.Spec.Records[0].Values = make([]string, 65) }, false},
		{"10000 records", func(z *HostedZone) { z.Spec.Records = snapshotRecords(10000) }, true},
		{"too many records", func(z *HostedZone) { z.Spec.Records = snapshotRecords(10001) }, false},
		{"zero refresh", func(z *HostedZone) { z.Spec.SOA.Refresh = 0 }, false},
		{"SOA overflow", func(z *HostedZone) { z.Spec.SOA.Minimum = 2419201 }, false},
		{"status digest", func(z *HostedZone) { z.Status.Servers[0].ContentDigest = "bad" }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			zone := &HostedZone{
				TypeMeta:   metav1.TypeMeta{APIVersion: SchemeGroupVersion.String(), Kind: "HostedZone"},
				ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "dns"},
				Spec: HostedZoneSpec{
					DomainName: "example.test", Serial: 1,
					SOA:           SOA{MName: "ns.example.test", RName: "hostmaster.example.test", Refresh: 3600, Retry: 900, Expire: 1209600, Minimum: 300},
					NameServers:   []NameServer{{Name: "ns.example.test", Addresses: []string{"192.0.2.53"}}},
					Records:       []HostedZoneRecord{{Name: "@", Type: "A", TTL: 60, Values: []string{"192.0.2.1"}}},
					ContentDigest: "sha256:" + strings.Repeat("a", 64),
				},
				Status: HostedZoneStatus{Servers: []HostedZoneServerStatus{{Endpoint: "dns/server", Serial: 1, ContentDigest: "sha256:" + strings.Repeat("a", 64), ObservedAt: metav1.Now()}}},
			}
			tc.mutate(zone)
			data, err := json.Marshal(zone)
			if err != nil {
				t.Fatal(err)
			}
			var object map[string]interface{}
			if err := k8sjson.Unmarshal(data, &object); err != nil {
				t.Fatal(err)
			}
			result := validator.Validate(object)
			valid := result.IsValid()
			var celErrors field.ErrorList
			if valid {
				celErrors, _ = celValidator.Validate(context.Background(), field.NewPath(""), structural, object, nil, 1000000)
				valid = len(celErrors) == 0
			}
			if valid != tc.valid {
				t.Fatalf("valid=%v want=%v schemaErrors=%v celErrors=%v", valid, tc.valid, result.Errors, celErrors)
			}
		})
	}
}

func snapshotRecords(count int) []HostedZoneRecord {
	records := make([]HostedZoneRecord, count)
	for i := range records {
		records[i] = HostedZoneRecord{Name: "record-" + strconv.Itoa(i), Type: "A", TTL: 60, Values: []string{"192.0.2.1"}}
	}
	return records
}

func TestDelegationPolicyJSONContract(t *testing.T) {
	const input = `{"resolver":{"mode":"Recursive","servers":["192.0.2.53:53","[2001:db8::53]:53"]}}`
	var policy DelegationPolicy
	if err := json.Unmarshal([]byte(input), &policy); err != nil {
		t.Fatal(err)
	}
	if policy.Resolver.Mode != "Recursive" || len(policy.Resolver.Servers) != 2 || policy.Resolver.Servers[1] != "[2001:db8::53]:53" {
		t.Fatalf("delegation resolver fields were lost: %#v", policy)
	}
	data, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != input {
		t.Fatalf("delegation JSON=%s want=%s", data, input)
	}
}
