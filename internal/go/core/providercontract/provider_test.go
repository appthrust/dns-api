package providercontract

import (
	"encoding/json"
	"testing"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestConvertProviderPayloadToStorageAppliesSchemaDefaults(t *testing.T) {
	provider := &dnsv1alpha1.Provider{
		ObjectMeta: metav1.ObjectMeta{Name: "example.dns.appthrust.io"},
		Spec: dnsv1alpha1.ProviderSpec{
			Versions: []dnsv1alpha1.ProviderVersion{
				{
					Name:    "v1alpha1",
					Served:  true,
					Storage: true,
					RecordSet: dnsv1alpha1.ProviderRecordSet{
						Schemas: dnsv1alpha1.ProviderRecordSetSchemas{
							Options: &dnsv1alpha1.ProviderOpenAPISchema{OpenAPIV3Schema: raw(t, map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"alias": map[string]interface{}{
										"type": "object",
										"properties": map[string]interface{}{
											"evaluateTargetHealth": map[string]interface{}{
												"type":    "boolean",
												"default": false,
											},
										},
									},
								},
							})},
						},
					},
				},
			},
		},
	}
	storage := &provider.Spec.Versions[0]

	got, payloadErr := ConvertProviderPayloadToStorage(ConversionInput{
		Provider:    provider,
		Target:      TargetRecordSet,
		FromVersion: storage,
		ToVersion:   storage,
		Payload: Payload{
			Options: raw(t, map[string]interface{}{"alias": map[string]interface{}{}}),
		},
	})
	if payloadErr != nil {
		t.Fatalf("ConvertProviderPayloadToStorage error = %v", payloadErr)
	}
	assertRawEqual(t, got.Options, map[string]interface{}{
		"alias": map[string]interface{}{
			"evaluateTargetHealth": false,
		},
	})
}

func TestConvertProviderPayloadToStorageAppliesStorageSchemaDefaultsAfterConversion(t *testing.T) {
	provider := &dnsv1alpha1.Provider{
		ObjectMeta: metav1.ObjectMeta{Name: "example.dns.appthrust.io"},
		Spec: dnsv1alpha1.ProviderSpec{
			Versions: []dnsv1alpha1.ProviderVersion{
				{
					Name:    "v1alpha1",
					Served:  true,
					Storage: true,
					RecordSet: dnsv1alpha1.ProviderRecordSet{
						Schemas: dnsv1alpha1.ProviderRecordSetSchemas{
							Options: &dnsv1alpha1.ProviderOpenAPISchema{OpenAPIV3Schema: raw(t, map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"storageTTL": map[string]interface{}{
										"type":    "integer",
										"default": 300,
									},
								},
							})},
						},
					},
				},
				{
					Name:   "v1beta1",
					Served: true,
					RecordSet: dnsv1alpha1.ProviderRecordSet{
						Conversion: dnsv1alpha1.ProviderConversion{
							ToStorage: dnsv1alpha1.ProviderConversionTarget{
								CEL: "{'options': {}}",
							},
						},
					},
				},
			},
		},
	}
	storage := &provider.Spec.Versions[0]
	served := &provider.Spec.Versions[1]

	got, payloadErr := ConvertProviderPayloadToStorage(ConversionInput{
		Provider:    provider,
		Target:      TargetRecordSet,
		FromVersion: served,
		ToVersion:   storage,
		Payload:     Payload{},
	})
	if payloadErr != nil {
		t.Fatalf("ConvertProviderPayloadToStorage error = %v", payloadErr)
	}
	assertRawEqual(t, got.Options, map[string]interface{}{"storageTTL": float64(300)})
}

func TestConvertProviderPayloadToStorageAllowsMissingPayloadWithRequiredSchema(t *testing.T) {
	provider := &dnsv1alpha1.Provider{
		ObjectMeta: metav1.ObjectMeta{Name: "example.dns.appthrust.io"},
		Spec: dnsv1alpha1.ProviderSpec{
			Versions: []dnsv1alpha1.ProviderVersion{
				{
					Name:    "v1alpha1",
					Served:  true,
					Storage: true,
					Zone: dnsv1alpha1.ProviderZone{
						Schemas: dnsv1alpha1.ProviderZoneSchemas{
							Adoption: &dnsv1alpha1.ProviderOpenAPISchema{OpenAPIV3Schema: raw(t, map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"externalRef": map[string]interface{}{
										"type": "object",
										"properties": map[string]interface{}{
											"zoneID": map[string]interface{}{
												"type": "string",
											},
										},
										"required": []interface{}{"zoneID"},
									},
								},
								"required": []interface{}{"externalRef"},
							})},
						},
					},
				},
			},
		},
	}
	storage := &provider.Spec.Versions[0]

	got, payloadErr := ConvertProviderPayloadToStorage(ConversionInput{
		Provider:    provider,
		Target:      TargetZone,
		FromVersion: storage,
		ToVersion:   storage,
		Payload:     Payload{},
	})
	if payloadErr != nil {
		t.Fatalf("ConvertProviderPayloadToStorage error = %v", payloadErr)
	}
	if len(got.Adoption.Raw) != 0 {
		t.Fatalf("adoption raw = %s, want empty", string(got.Adoption.Raw))
	}
}

func TestConvertProviderPayloadToStorageUsesKROCELLibrariesAndOmit(t *testing.T) {
	provider := &dnsv1alpha1.Provider{
		ObjectMeta: metav1.ObjectMeta{Name: "example.dns.appthrust.io"},
		Spec: dnsv1alpha1.ProviderSpec{
			Versions: []dnsv1alpha1.ProviderVersion{
				{
					Name:    "v1beta1",
					Served:  true,
					Storage: true,
					RecordSet: dnsv1alpha1.ProviderRecordSet{
						Schemas: dnsv1alpha1.ProviderRecordSetSchemas{
							Options: &dnsv1alpha1.ProviderOpenAPISchema{OpenAPIV3Schema: raw(t, map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"ttl":     map[string]interface{}{"type": "integer"},
									"comment": map[string]interface{}{"type": "string"},
								},
							})},
							Adoption: &dnsv1alpha1.ProviderOpenAPISchema{OpenAPIV3Schema: raw(t, map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"enabled": map[string]interface{}{"type": "boolean"},
								},
							})},
						},
					},
				},
				{
					Name:   "v1alpha1",
					Served: true,
					RecordSet: dnsv1alpha1.ProviderRecordSet{
						Schemas: dnsv1alpha1.ProviderRecordSetSchemas{
							Options: &dnsv1alpha1.ProviderOpenAPISchema{OpenAPIV3Schema: raw(t, map[string]interface{}{
								"type":                                 "object",
								"x-kubernetes-preserve-unknown-fields": true,
							})},
							Adoption: &dnsv1alpha1.ProviderOpenAPISchema{OpenAPIV3Schema: raw(t, map[string]interface{}{
								"type":                                 "object",
								"x-kubernetes-preserve-unknown-fields": true,
							})},
						},
						Conversion: dnsv1alpha1.ProviderConversion{
							ToStorage: dnsv1alpha1.ProviderConversionTarget{
								CEL: `{
									"options": self.options.merge({
										"ttl": self.options.?ttl.orValue(300),
										"comment": json.unmarshal('{"comment":"from-json"}').comment,
										"drop": omit()
									}),
									"adoption": has(self.adoption.externalRef) ? {"enabled": true} : omit()
								}`,
							},
						},
					},
				},
			},
		},
	}
	storage := &provider.Spec.Versions[0]
	served := &provider.Spec.Versions[1]

	got, payloadErr := ConvertProviderPayloadToStorage(ConversionInput{
		Provider:    provider,
		Target:      TargetRecordSet,
		FromVersion: served,
		ToVersion:   storage,
		Payload: Payload{
			Options: raw(t, map[string]interface{}{}),
		},
	})
	if payloadErr != nil {
		t.Fatalf("ConvertProviderPayloadToStorage error = %v", payloadErr)
	}
	assertRawEqual(t, got.Options, map[string]interface{}{
		"ttl":     float64(300),
		"comment": "from-json",
	})
	if len(got.Adoption.Raw) != 0 {
		t.Fatalf("adoption raw = %s, want empty", string(got.Adoption.Raw))
	}
}

func TestConvertZoneClassParametersToStorage(t *testing.T) {
	provider := &dnsv1alpha1.Provider{
		ObjectMeta: metav1.ObjectMeta{Name: "example.dns.appthrust.io"},
		Spec: dnsv1alpha1.ProviderSpec{
			Versions: []dnsv1alpha1.ProviderVersion{
				{
					Name:    "v1beta1",
					Served:  true,
					Storage: true,
					ZoneClass: dnsv1alpha1.ProviderZoneClass{
						Schemas: dnsv1alpha1.ProviderZoneClassSchemas{
							Parameters: &dnsv1alpha1.ProviderOpenAPISchema{OpenAPIV3Schema: raw(t, map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"zoneCreationPolicy": map[string]interface{}{"type": "string"},
								},
							})},
						},
					},
				},
				{
					Name:   "v1alpha1",
					Served: true,
					ZoneClass: dnsv1alpha1.ProviderZoneClass{
						Schemas: dnsv1alpha1.ProviderZoneClassSchemas{
							Parameters: &dnsv1alpha1.ProviderOpenAPISchema{OpenAPIV3Schema: raw(t, map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"create": map[string]interface{}{"type": "string"},
								},
							})},
						},
						Conversion: dnsv1alpha1.ProviderConversion{
							ToStorage: dnsv1alpha1.ProviderConversionTarget{
								CEL: `{"parameters": {"zoneCreationPolicy": self.parameters.?create.orValue("Create")}}`,
							},
						},
					},
				},
			},
		},
	}
	zoneClass := &dnsv1alpha1.ZoneClass{
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "default"},
		Spec: dnsv1alpha1.ZoneClassSpec{
			Parameters: raw(t, map[string]interface{}{"create": "Deny"}),
		},
	}

	got, payloadErr := ConvertZoneClassParametersToStorage(provider, &provider.Spec.Versions[1], zoneClass)
	if payloadErr != nil {
		t.Fatalf("ConvertZoneClassParametersToStorage error = %v", payloadErr)
	}
	assertRawEqual(t, got, map[string]interface{}{"zoneCreationPolicy": "Deny"})
}

func raw(t *testing.T, value interface{}) runtime.RawExtension {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json marshal: %v", err)
	}
	return runtime.RawExtension{Raw: data}
}

func assertRawEqual(t *testing.T, got runtime.RawExtension, want map[string]interface{}) {
	t.Helper()
	var gotObject map[string]interface{}
	if err := json.Unmarshal(got.Raw, &gotObject); err != nil {
		t.Fatalf("raw JSON = %s, decode error: %v", string(got.Raw), err)
	}
	if !equality.Semantic.DeepEqual(gotObject, want) {
		t.Fatalf("raw JSON = %#v, want %#v", gotObject, want)
	}
}
