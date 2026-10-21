package manifest

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/appthrust/dns-api/internal/go/core/celruntime"
	"github.com/appthrust/dns-api/internal/go/core/providercontract"
	externalmanifest "github.com/appthrust/dns-api/internal/go/providers/external/manifest"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	hostedv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/hosted/v1alpha1"
	"github.com/google/cel-go/cel"
	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiservervalidation "k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"
)

func TestHostedDeclaration(t *testing.T) {
	provider, err := Provider()
	if err != nil {
		t.Fatal(err)
	}
	if provider.Name != hostedv1alpha1.ProviderName || provider.Kind != "Provider" || provider.APIVersion != "dns.appthrust.io/v1alpha1" || len(provider.Spec.Versions) != 1 {
		t.Fatalf("provider=%#v", provider)
	}
	version := &provider.Spec.Versions[0]
	if version.Name != hostedv1alpha1.ProviderVersion || version.Identity != nil || !version.Storage || !version.Served {
		t.Fatalf("version=%#v", version)
	}
	wantTypes := []dnsv1alpha1.RecordType{dnsv1alpha1.RecordTypeA, dnsv1alpha1.RecordTypeAAAA, dnsv1alpha1.RecordTypeCNAME, dnsv1alpha1.RecordTypeTXT, dnsv1alpha1.RecordTypeMX, dnsv1alpha1.RecordTypeCAA, dnsv1alpha1.RecordTypeNS}
	if !reflect.DeepEqual(version.RecordSet.SupportedTypes, wantTypes) {
		t.Fatalf("supported types=%v want=%v", version.RecordSet.SupportedTypes, wantTypes)
	}
	assertReasons(t, version.Zone.AdditionalConditionReasons, []reasonKey{
		{dnsv1alpha1.ConditionType("Programmed"), metav1.ConditionFalse, hostedv1alpha1.ReasonServingPending},
		{dnsv1alpha1.ConditionType("Programmed"), metav1.ConditionFalse, hostedv1alpha1.ReasonSnapshotRejected},
	})
	assertReasons(t, version.RecordSet.AdditionalConditionReasons, []reasonKey{
		{dnsv1alpha1.ConditionType("Programmed"), metav1.ConditionFalse, hostedv1alpha1.ReasonServingPending},
	})
	if version.RecordWithdrawal == nil || !version.RecordWithdrawal.Supported || version.RecordWithdrawal.Schemas.StatusProviderData == nil {
		t.Fatal("withdrawal capability and status schema must ship together")
	}
	assertReasons(t, version.RecordWithdrawal.AdditionalConditionReasons, []reasonKey{
		{"Withdrawn", metav1.ConditionFalse, hostedv1alpha1.ReasonDNSStillResolves},
		{"Ready", metav1.ConditionFalse, hostedv1alpha1.ReasonDNSStillResolves},
		{"Withdrawn", metav1.ConditionFalse, hostedv1alpha1.ReasonNegativeTTLNotElapsed},
		{"Ready", metav1.ConditionFalse, hostedv1alpha1.ReasonNegativeTTLNotElapsed},
		{"Withdrawn", metav1.ConditionUnknown, hostedv1alpha1.ReasonDNSProbeFailed},
		{"Ready", metav1.ConditionFalse, hostedv1alpha1.ReasonDNSProbeFailed},
		{"Withdrawn", metav1.ConditionUnknown, hostedv1alpha1.ReasonPolicyChanged},
		{"Ready", metav1.ConditionFalse, hostedv1alpha1.ReasonPolicyChanged},
		{"Withdrawn", metav1.ConditionTrue, hostedv1alpha1.ReasonWithdrawalVerified},
		{"Ready", metav1.ConditionTrue, hostedv1alpha1.ReasonWithdrawalVerified},
		{"OverrideAccepted", metav1.ConditionTrue, hostedv1alpha1.ReasonOperatorAuthorizedRelease},
	})
	external, err := externalmanifest.Provider()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(version.RecordWithdrawal, external.Spec.Versions[0].RecordWithdrawal) {
		t.Fatal("withdrawal schema and closed reason table differ from external")
	}
	if len(version.RecordSet.DisableValidations) != 0 || len(version.ZoneUnit.DisableValidations) != 0 {
		t.Fatal("hosted must retain all core validations")
	}

	var capability endpointv1alpha1.EndpointProviderCapability
	if err := yaml.UnmarshalStrict(CapabilityYAML, &capability); err != nil {
		t.Fatal(err)
	}
	if capability.Kind != "EndpointProviderCapability" || capability.APIVersion != "endpoint.dns.appthrust.io/v1alpha1" || capability.Name != "hosted-v1alpha1" || capability.Spec.Provider.Name != hostedv1alpha1.ProviderName || capability.Spec.Provider.Version != hostedv1alpha1.ProviderVersion || capability.Spec.Conversion.Group != "endpoint."+hostedv1alpha1.ProviderName || capability.Spec.Conversion.Version != hostedv1alpha1.ProviderVersion || capability.Spec.Conversion.Resource != "endpointrecordsetconversions" {
		t.Fatalf("capability=%#v", capability)
	}
}

type reasonKey struct {
	condition dnsv1alpha1.ConditionType
	status    metav1.ConditionStatus
	reason    string
}

func assertReasons(t *testing.T, actual []dnsv1alpha1.ProviderConditionReason, want []reasonKey) {
	t.Helper()
	got := make([]reasonKey, len(actual))
	for i, reason := range actual {
		got[i] = reasonKey{reason.ConditionType, reason.Status, reason.Reason}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reasons=%v want=%v", got, want)
	}
}

func TestHostedParametersAndCEL(t *testing.T) {
	provider, err := Provider()
	if err != nil {
		t.Fatal(err)
	}
	version := &provider.Spec.Versions[0]
	for _, rule := range version.ZoneClass.ValidationRules {
		if err := celruntime.CompileBool(rule.Rule, cel.Variable("self", cel.DynType)); err != nil {
			t.Fatalf("compile %q: %v", rule.Rule, err)
		}
	}
	const ns = `"nameServers":[{"name":"ns.example.test","addresses":["192.0.2.53","2001:db8::53"]}]`
	cases := []struct {
		name, parameters string
		valid            bool
	}{
		{"minimal", `{` + ns + `}`, true},
		{"full", `{` + ns + `,"soa":{"rname":"hostmaster.example.test","refresh":3600,"retry":900,"expire":1209600,"minimum":300},"defaultTTL":60,"verification":{"views":["192.0.2.53:53","[2001:db8::53]:5353"],"delegation":{"resolver":{"mode":"Recursive","servers":["192.0.2.54:53"]}}}}`, true},
		{"empty optional objects", `{` + ns + `,"soa":{},"verification":{"delegation":{}}}`, true},
		{"empty delegation resolver", `{` + ns + `,"verification":{"delegation":{"resolver":{}}}}`, true},
		{"cluster delegation", `{` + ns + `,"verification":{"delegation":{"resolver":{"mode":"Cluster"}}}}`, true},
		{"cluster empty servers", `{` + ns + `,"verification":{"delegation":{"resolver":{"mode":"Cluster","servers":[]}}}}`, true},
		{"cluster explicit servers", `{` + ns + `,"verification":{"delegation":{"resolver":{"mode":"Cluster","servers":["192.0.2.54:53"]}}}}`, false},
		{"default mode explicit servers", `{` + ns + `,"verification":{"delegation":{"resolver":{"servers":["192.0.2.54:53"]}}}}`, false},
		{"recursive missing servers", `{` + ns + `,"verification":{"delegation":{"resolver":{"mode":"Recursive"}}}}`, false},
		{"invalid delegation mode", `{` + ns + `,"verification":{"delegation":{"resolver":{"mode":"Authoritative"}}}}`, false},
		{"obsolete resolvers rejected", `{` + ns + `,"verification":{"delegation":{"resolvers":["192.0.2.54:53"]}}}`, false},
		{"unnested resolver rejected", `{` + ns + `,"verification":{"delegation":{"mode":"Recursive","servers":["192.0.2.54:53"]}}}`, false},
		{"missing nameservers", `{}`, false},
		{"empty nameservers", `{"nameServers":[]}`, false},
		{"too many nameservers", `{"nameServers":[` + strings.Repeat(`{"name":"ns.example.test","addresses":["192.0.2.53"]},`, 8) + `{"name":"ns.example.test","addresses":["192.0.2.53"]}]}`, false},
		{"missing addresses", `{"nameServers":[{"name":"ns.example.test"}]}`, false},
		{"empty addresses", `{"nameServers":[{"name":"ns.example.test","addresses":[]}]}`, false},
		{"too many addresses", `{"nameServers":[{"name":"ns.example.test","addresses":[` + strings.Repeat(`"192.0.2.53",`, 8) + `"192.0.2.53"]}]}`, false},
		{"NS IP", strings.ReplaceAll(`{`+ns+`}`, "ns.example.test", "192.0.2.53"), false},
		{"uppercase NS", strings.ReplaceAll(`{`+ns+`}`, "ns.example.test", "NS.example.test"), false},
		{"root dot NS", strings.ReplaceAll(`{`+ns+`}`, "ns.example.test", "ns.example.test."), false},
		{"invalid address", strings.ReplaceAll(`{`+ns+`}`, "192.0.2.53", "999.0.2.53"), false},
		{"expanded IPv6", strings.ReplaceAll(`{`+ns+`}`, "2001:db8::53", "2001:db8:0:0:0:0:0:53"), false},
		{"uppercase IPv6", strings.ReplaceAll(`{`+ns+`}`, "2001:db8::53", "2001:DB8::53"), false},
		{"RName IP", `{` + ns + `,"soa":{"rname":"192.0.2.53"}}`, false},
		{"RName root dot", `{` + ns + `,"soa":{"rname":"hostmaster.example.test."}}`, false},
		{"TTL zero", `{` + ns + `,"defaultTTL":0}`, false},
		{"TTL ceiling", `{` + ns + `,"defaultTTL":86400}`, true},
		{"TTL overflow", `{` + ns + `,"defaultTTL":86401}`, false},
		{"unknown parameter", `{` + ns + `,"identityRef":{}}`, false},
	}
	for _, key := range []string{"refresh", "retry", "expire", "minimum"} {
		for _, boundary := range []struct {
			value string
			valid bool
		}{{"0", false}, {"1", true}, {"2419200", true}, {"2419201", false}} {
			cases = append(cases, struct {
				name, parameters string
				valid            bool
			}{key + boundary.value, `{` + ns + `,"soa":{"` + key + `":` + boundary.value + `}}`, boundary.valid})
		}
	}
	for _, field := range []string{"views", "delegation"} {
		for _, endpoints := range []struct {
			name, value string
			valid       bool
		}{
			{"empty", `[]`, true},
			{"three", `["192.0.2.1:53","192.0.2.2:53","[2001:db8::53]:65535"]`, true},
			{"four", `["192.0.2.1:53","192.0.2.2:53","192.0.2.3:53","192.0.2.4:53"]`, false},
			{"bad IP", `["999.0.2.53:53"]`, false},
			{"hostname", `["dns.example.test:53"]`, false},
			{"port overflow", `["192.0.2.53:65536"]`, false},
			{"port zero", `["192.0.2.53:0"]`, false},
			{"leading zero port", `["192.0.2.53:053"]`, false},
			{"duplicate", `["192.0.2.53:53","192.0.2.53:53"]`, false},
			{"noncanonical IPv6", `["[2001:db8:0:0:0:0:0:53]:53"]`, false},
			{"bracketed IPv4", `["[192.0.2.53]:53"]`, false},
		} {
			value := endpoints.value
			valid := endpoints.valid
			if field == "delegation" {
				value = `{"resolver":{"mode":"Recursive","servers":` + value + `}}`
				valid = valid && endpoints.name != "empty"
			}
			cases = append(cases, struct {
				name, parameters string
				valid            bool
			}{field + " " + endpoints.name, `{` + ns + `,"verification":{"` + field + `":` + value + `}}`, valid})
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			class := &dnsv1alpha1.ZoneClass{Spec: dnsv1alpha1.ZoneClassSpec{Parameters: runtime.RawExtension{Raw: []byte(tc.parameters)}}}
			_, schemaErr := providercontract.ConvertZoneClassParametersToStorage(provider, version, class)
			valid := schemaErr == nil
			if valid {
				var params map[string]interface{}
				if err := json.Unmarshal([]byte(tc.parameters), &params); err != nil {
					t.Fatal(err)
				}
				self := map[string]interface{}{"spec": map[string]interface{}{"parameters": params}}
				for _, rule := range version.ZoneClass.ValidationRules {
					ok, err := celruntime.EvalBool(rule.Rule, map[string]interface{}{"self": self}, cel.Variable("self", cel.DynType))
					if err != nil || !ok {
						valid = false
						break
					}
				}
			}
			if valid != tc.valid {
				t.Fatalf("valid=%v want=%v schemaError=%v", valid, tc.valid, schemaErr)
			}
		})
	}
}

func TestDefaultsMatchAPIConstants(t *testing.T) {
	provider, err := Provider()
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]struct {
			Default    uint32 `json:"default"`
			Properties map[string]struct {
				Default uint32 `json:"default"`
			} `json:"properties"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(provider.Spec.Versions[0].ZoneClass.Schemas.Parameters.OpenAPIV3Schema.Raw, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Properties["defaultTTL"].Default != hostedv1alpha1.DefaultTTL {
		t.Fatal("defaultTTL differs from API default")
	}
	for key, want := range map[string]uint32{"refresh": hostedv1alpha1.DefaultSOARefresh, "retry": hostedv1alpha1.DefaultSOARetry, "expire": hostedv1alpha1.DefaultSOAExpire, "minimum": hostedv1alpha1.DefaultSOAMinimum} {
		if schema.Properties["soa"].Properties[key].Default != want {
			t.Fatalf("soa.%s default differs from %d", key, want)
		}
	}
}

func TestApexNSRules(t *testing.T) {
	provider, err := Provider()
	if err != nil {
		t.Fatal(err)
	}
	version := &provider.Spec.Versions[0]
	if len(version.RecordSet.ValidationRules) != 1 || len(version.ZoneUnit.ValidationRules) != 1 {
		t.Fatal("both admission and composition must reject apex NS claims")
	}
	for _, tc := range []struct {
		name, recordType string
		valid            bool
	}{{"@", "NS", false}, {"sub", "NS", true}, {"@", "A", true}} {
		item := map[string]interface{}{"name": tc.name, "type": tc.recordType}
		ok, err := celruntime.EvalBool(version.RecordSet.ValidationRules[0].Rule, map[string]interface{}{"self": map[string]interface{}{"spec": item}}, cel.Variable("self", cel.DynType))
		if err != nil || ok != tc.valid {
			t.Fatalf("admission %v valid=%v error=%v", item, ok, err)
		}
		for _, reverse := range []bool{false, true} {
			self, other := item, map[string]interface{}{"name": "@", "type": "TXT"}
			if reverse {
				self, other = other, self
			}
			ok, err := celruntime.EvalBool(version.ZoneUnit.ValidationRules[0].Rule, map[string]interface{}{"self": self, "other": other}, cel.Variable("self", cel.DynType), cel.Variable("other", cel.DynType))
			if err != nil || ok != tc.valid {
				t.Fatalf("composition %v valid=%v error=%v", item, ok, err)
			}
		}
	}
}

func TestNonemptyProviderPayloadsRejected(t *testing.T) {
	provider, err := Provider()
	if err != nil {
		t.Fatal(err)
	}
	version := &provider.Spec.Versions[0]
	for _, target := range []providercontract.PayloadTarget{providercontract.TargetZone, providercontract.TargetRecordSet} {
		input := providercontract.ConversionInput{Provider: provider, FromVersion: version, ToVersion: version, Target: target, Payload: providercontract.Payload{Adoption: runtime.RawExtension{Raw: []byte(`{"enabled":true}`)}}}
		if _, err := providercontract.ConvertProviderPayloadToStorage(input); err == nil {
			t.Fatalf("nonempty adoption accepted for %s", target)
		}
	}
	input := providercontract.ConversionInput{Provider: provider, FromVersion: version, ToVersion: version, Target: providercontract.TargetRecordSet, Payload: providercontract.Payload{Options: runtime.RawExtension{Raw: []byte(`{"alias":{}}`)}}}
	if _, err := providercontract.ConvertProviderPayloadToStorage(input); err == nil {
		t.Fatal("nonempty options accepted")
	}
}

func TestStatusProviderDataSchemas(t *testing.T) {
	provider, err := Provider()
	if err != nil {
		t.Fatal(err)
	}
	version := &provider.Spec.Versions[0]
	const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const server = `{"endpoint":"dns/server","serial":1,"contentDigest":"` + digest + `","observedAt":"2026-10-21T00:00:00Z"}`
	const delegation = `"observedNameServers":["ns.example.test"],"matches":true,"lastProbeTime":"2026-10-21T00:00:00Z"`
	cases := []struct {
		name, payload string
		zone          bool
		valid         bool
	}{
		{"zone complete", `{"serial":1,"contentDigest":"` + digest + `","servers":[` + server + `],"delegation":{` + delegation + `}}`, true, true},
		{"zone without delegation", `{"serial":1,"contentDigest":"` + digest + `","servers":[]}`, true, true},
		{"zone max serial", `{"serial":4294967295}`, true, true},
		{"zone serial overflow", `{"serial":4294967296}`, true, false},
		{"zone zero serial", `{"serial":0}`, true, false},
		{"zone malformed digest", `{"contentDigest":"bad"}`, true, false},
		{"server malformed digest", `{"servers":[` + strings.Replace(server, digest, "bad", 1) + `]}`, true, false},
		{"server missing digest", `{"servers":[{"endpoint":"dns/server","serial":1,"observedAt":"2026-10-21T00:00:00Z"}]}`, true, false},
		{"64 servers", `{"servers":[` + strings.Repeat(server+",", 63) + server + `]}`, true, true},
		{"65 servers", `{"servers":[` + strings.Repeat(server+",", 64) + server + `]}`, true, false},
		{"delegation error", `{"delegation":{` + delegation + `,"error":"DNSProbeFailed"}}`, true, true},
		{"512 error", `{"delegation":{` + delegation + `,"error":"` + strings.Repeat("x", 512) + `"}}`, true, true},
		{"513 error", `{"delegation":{` + delegation + `,"error":"` + strings.Repeat("x", 513) + `"}}`, true, false},
		{"16 nameservers", `{"delegation":{"observedNameServers":[` + strings.Repeat(`"ns.example.test",`, 15) + `"ns.example.test"],"matches":true,"lastProbeTime":"2026-10-21T00:00:00Z"}}`, true, true},
		{"17 nameservers", `{"delegation":{"observedNameServers":[` + strings.Repeat(`"ns.example.test",`, 16) + `"ns.example.test"],"matches":true,"lastProbeTime":"2026-10-21T00:00:00Z"}}`, true, false},
		{"obsolete delegation failure", `{"delegation":{` + delegation + `,"failure":"DNSProbeFailed"}}`, true, false},
		{"zone unknown data", `{"unexpected":true}`, true, false},
		{"record pending", `{"serial":1}`, false, true},
		{"record served", `{"serial":1,"servedAt":"2026-10-21T00:00:00Z"}`, false, true},
		{"record max serial", `{"serial":4294967295}`, false, true},
		{"record missing serial", `{}`, false, false},
		{"record zero serial", `{"serial":0}`, false, false},
		{"record serial overflow", `{"serial":4294967296}`, false, false},
		{"record invalid servedAt", `{"serial":1,"servedAt":"not-a-timestamp"}`, false, false},
		{"record unknown data", `{"serial":1,"unexpected":true}`, false, false},
	}
	validators := make(map[bool]apiservervalidation.SchemaValidator, 2)
	for zone, schema := range map[bool]*dnsv1alpha1.ProviderOpenAPISchema{
		true:  version.Zone.Schemas.StatusProviderData,
		false: version.RecordSet.Schemas.StatusProviderData,
	} {
		if schema == nil {
			t.Fatalf("statusProviderData schema missing: zone=%v", zone)
		}
		var schemaV1 apiextensionsv1.JSONSchemaProps
		if err := json.Unmarshal(schema.OpenAPIV3Schema.Raw, &schemaV1); err != nil {
			t.Fatal(err)
		}
		assertSchemaDescriptions(t, schemaV1)
		if zone && schemaV1.Properties["delegation"].Properties["matches"].Description != "observed apex NS name set (lowercase, no trailing dot) equals the configured nameServers[].name set" {
			t.Fatal("delegation.matches must describe normalized apex NS set equality")
		}
		var internal apiextensions.JSONSchemaProps
		if err := apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(&schemaV1, &internal, nil); err != nil {
			t.Fatal(err)
		}
		validator, _, err := apiservervalidation.NewSchemaValidator(&internal)
		if err != nil {
			t.Fatal(err)
		}
		validators[zone] = validator
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var payload map[string]interface{}
			if err := json.Unmarshal([]byte(tc.payload), &payload); err != nil {
				t.Fatal(err)
			}
			result := validators[tc.zone].Validate(payload)
			if result.IsValid() != tc.valid {
				t.Fatalf("valid=%v want=%v errors=%v", result.IsValid(), tc.valid, result.Errors)
			}
		})
	}
}

func assertSchemaDescriptions(t *testing.T, schema apiextensionsv1.JSONSchemaProps) {
	t.Helper()
	if schema.Description == "" {
		t.Fatalf("public status schema is missing a description: %#v", schema)
	}
	for name, property := range schema.Properties {
		t.Run(name, func(t *testing.T) { assertSchemaDescriptions(t, property) })
	}
	if schema.Items != nil && schema.Items.Schema != nil {
		assertSchemaDescriptions(t, *schema.Items.Schema)
	}
}
