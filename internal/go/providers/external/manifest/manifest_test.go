package manifest

import (
	"encoding/json"
	"testing"

	"github.com/appthrust/dns-api/internal/go/core/celruntime"
	"github.com/appthrust/dns-api/internal/go/core/providercontract"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	"github.com/google/cel-go/cel"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestExternalDeclarationAndCEL(t *testing.T) {
	provider, err := Provider()
	if err != nil {
		t.Fatal(err)
	}
	version := &provider.Spec.Versions[0]
	if len(provider.Spec.Versions) != 1 || version.Identity != nil || !version.Storage || !version.Served || len(version.RecordSet.SupportedTypes) != 3 {
		t.Fatalf("provider=%#v", provider)
	}
	if len(version.Zone.AdditionalConditionReasons) != 5 || len(version.RecordSet.AdditionalConditionReasons) != 8 {
		t.Fatal("condition reason tables differ from design")
	}
	if version.RecordWithdrawal == nil || !version.RecordWithdrawal.Supported || version.RecordWithdrawal.Schemas.StatusProviderData == nil || len(version.RecordWithdrawal.AdditionalConditionReasons) != 11 {
		t.Fatal("withdrawal capability, schema and condition table must ship together")
	}
	for _, rule := range version.ZoneClass.ValidationRules {
		if err := celruntime.CompileBool(rule.Rule, cel.Variable("self", cel.DynType)); err != nil {
			t.Fatalf("compile %q: %v", rule.Rule, err)
		}
	}
	cases := []struct {
		name, parameters string
		valid            bool
	}{
		{"default", `{}`, true},
		{"empty resolver", `{"resolver":{}}`, true},
		{"recursive", `{"resolver":{"mode":"Recursive","servers":["192.0.2.53:53","[2001:db8::53]:5353"]},"expectedNameservers":["ns.example.test"]}`, true},
		{"missing servers", `{"resolver":{"mode":"Recursive"}}`, false},
		{"cluster servers", `{"resolver":{"mode":"Cluster","servers":["192.0.2.53:53"]}}`, false},
		{"bad IP", `{"resolver":{"mode":"Recursive","servers":["999.1.1.1:53"]}}`, false},
		{"port overflow", `{"resolver":{"mode":"Recursive","servers":["192.0.2.1:65536"]}}`, false},
		{"leading zero port", `{"resolver":{"mode":"Recursive","servers":["192.0.2.1:053"]}}`, false},
		{"duplicate", `{"resolver":{"mode":"Recursive","servers":["192.0.2.1:53","192.0.2.1:53"]}}`, false},
		{"NS IP", `{"expectedNameservers":["192.0.2.1"]}`, false},
		{"noncanonical NS", `{"expectedNameservers":["NS.example.test."]}`, false},
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
