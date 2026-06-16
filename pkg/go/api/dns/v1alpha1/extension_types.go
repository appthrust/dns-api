package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// Provider defines provider-specific capability, schemas, and versions.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Provider struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ProviderSpec `json:"spec,omitempty"`
}

type ProviderSpec struct {
	// Display contains UI-facing provider metadata.
	Display ProviderDisplay `json:"display"`

	// Versions contains provider schema versions.
	// +kubebuilder:validation:MinItems=1
	Versions []ProviderVersion `json:"versions"`
}

type ProviderDisplay struct {
	// Name is the provider name shown in UI.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Description is an optional short provider description.
	// +optional
	Description string `json:"description,omitempty"`

	// Logo optionally points to provider logo metadata.
	// +optional
	Logo *ProviderLogo `json:"logo,omitempty"`
}

type ProviderLogo struct {
	// URL is an optional provider logo URL.
	// +optional
	URL string `json:"url,omitempty"`
}

type ProviderVersion struct {
	// Name is the version segment used in spec.provider.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[^/]+$`
	Name string `json:"name"`

	// Served controls whether new or updated specs may reference this version.
	Served bool `json:"served"`

	// Storage marks the provider version used inside ZoneUnit.
	// +optional
	Storage bool `json:"storage,omitempty"`

	// Deprecated marks this version as deprecated for UI and validation messages.
	// +optional
	Deprecated bool `json:"deprecated,omitempty"`

	// Identity declares the provider identity resource selected by ZoneClass.spec.identityRef.
	// +optional
	Identity *ProviderIdentity `json:"identity,omitempty"`

	// ZoneClass defines schemas and validation for ZoneClass resources.
	// +optional
	ZoneClass ProviderZoneClass `json:"zoneClass,omitempty"`

	// Zone defines schemas and validation for Zone resources.
	// +optional
	Zone ProviderZone `json:"zone,omitempty"`

	// RecordSet defines supported types, schemas, and validation for RecordSet resources.
	// +optional
	RecordSet ProviderRecordSet `json:"recordSet,omitempty"`

	// ZoneUnit defines provider-specific composition validations evaluated when core builds ZoneUnit.
	// +optional
	ZoneUnit ProviderZoneUnit `json:"zoneUnit,omitempty"`
}

type ProviderIdentity struct {
	// Resource declares the provider identity resource.
	Resource ProviderIdentityResource `json:"resource"`
}

type ProviderIdentityResource struct {
	// Group is the provider identity API group.
	// +kubebuilder:validation:MinLength=1
	Group string `json:"group"`

	// Kind is the provider identity resource kind.
	// +kubebuilder:validation:MinLength=1
	Kind string `json:"kind"`

	// Scope is the provider identity resource scope.
	// +kubebuilder:validation:Enum=Namespaced
	Scope string `json:"scope"`
}

type ProviderZoneClass struct {
	// Schemas defines OpenAPI v3 schemas for provider-specific ZoneClass inline objects.
	// +optional
	Schemas ProviderZoneClassSchemas `json:"schemas,omitempty"`

	// ValidationRules contains CEL rules evaluated against ZoneClass objects.
	// +optional
	ValidationRules []ProviderValidationRule `json:"validationRules,omitempty"`

	// Conversion defines conversion from this version to the Provider storage version.
	// +optional
	Conversion ProviderConversion `json:"conversion,omitempty"`
}

type ProviderZoneClassSchemas struct {
	// Parameters validates ZoneClass.spec.parameters.
	// +optional
	Parameters *ProviderOpenAPISchema `json:"parameters,omitempty"`
}

type ProviderZone struct {
	// Schemas defines OpenAPI v3 schemas for provider-specific Zone inline objects.
	// +optional
	Schemas ProviderZoneSchemas `json:"schemas,omitempty"`

	// AdditionalConditionReasons declares provider-specific Zone status condition reasons.
	// +optional
	AdditionalConditionReasons []ProviderConditionReason `json:"additionalConditionReasons,omitempty"`

	// ValidationRules contains CEL rules evaluated against Zone objects.
	// +optional
	ValidationRules []ProviderValidationRule `json:"validationRules,omitempty"`

	// Conversion defines conversion from this version to the Provider storage version.
	// +optional
	Conversion ProviderConversion `json:"conversion,omitempty"`
}

type ProviderZoneSchemas struct {
	// Adoption validates Zone.spec.adoption.
	// +optional
	Adoption *ProviderOpenAPISchema `json:"adoption,omitempty"`

	// StatusProviderData validates Zone.status.provider.data.
	// +optional
	StatusProviderData *ProviderOpenAPISchema `json:"statusProviderData,omitempty"`
}

type ProviderRecordSet struct {
	// SupportedTypes lists DNS record types handled by this schema.
	// +kubebuilder:validation:items:Enum=A;AAAA;CNAME;NS;TXT;MX;CAA
	// +optional
	SupportedTypes []RecordType `json:"supportedTypes,omitempty"`

	// Schemas defines OpenAPI v3 schemas for provider-specific RecordSet inline objects.
	// +optional
	Schemas ProviderRecordSetSchemas `json:"schemas,omitempty"`

	// ValidationRules contains CEL rules evaluated against RecordSet objects.
	// +optional
	ValidationRules []ProviderValidationRule `json:"validationRules,omitempty"`

	// AdditionalConditionReasons declares provider-specific RecordSet status condition reasons.
	// +optional
	AdditionalConditionReasons []ProviderConditionReason `json:"additionalConditionReasons,omitempty"`

	// DisableValidations declares named core validations disabled by a CEL condition.
	// +optional
	DisableValidations []RecordSetValidationToggle `json:"disableValidations,omitempty"`

	// Conversion defines conversion from this version to the Provider storage version.
	// +optional
	Conversion ProviderConversion `json:"conversion,omitempty"`
}

type ProviderZoneUnit struct {
	// DisableValidations declares named core ZoneUnit composition validations disabled by a CEL condition.
	// +optional
	DisableValidations []ZoneUnitValidationToggle `json:"disableValidations,omitempty"`

	// ValidationRules contains CEL rules evaluated against pairs of ZoneUnit record set items.
	// +optional
	ValidationRules []ProviderValidationRule `json:"validationRules,omitempty"`
}

type ProviderConversion struct {
	// ToStorage converts payloads from this version to the Provider storage version.
	// +optional
	ToStorage ProviderConversionTarget `json:"toStorage,omitempty"`
}

type ProviderConversionTarget struct {
	// CEL is a KRO-compatible object-in/object-out expression.
	// +optional
	CEL string `json:"cel,omitempty"`

	// Webhook describes a provider conversion webhook for conversions that are not declarative.
	// +optional
	Webhook *ProviderConversionWebhook `json:"webhook,omitempty"`
}

type ProviderConversionWebhook struct {
	// ClientConfig describes how core reaches the conversion webhook.
	ClientConfig ProviderConversionWebhookClientConfig `json:"clientConfig"`

	// TimeoutSeconds is the conversion request timeout.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=30
	// +optional
	TimeoutSeconds *int32 `json:"timeoutSeconds,omitempty"`
}

type ProviderConversionWebhookClientConfig struct {
	// Service points to the in-cluster Service that receives conversion reviews.
	Service ProviderConversionWebhookServiceReference `json:"service"`

	// CABundle is the PEM CA bundle used to verify the Service certificate.
	// +optional
	CABundle []byte `json:"caBundle,omitempty"`
}

type ProviderConversionWebhookServiceReference struct {
	// Namespace is the Service namespace.
	// +kubebuilder:validation:MinLength=1
	Namespace string `json:"namespace"`

	// Name is the Service name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Path is the HTTPS path on the Service.
	// +kubebuilder:default=/convert
	// +optional
	Path string `json:"path,omitempty"`

	// Port is the Service port.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=443
	// +optional
	Port *int32 `json:"port,omitempty"`
}

type ProviderRecordSetSchemas struct {
	// Options validates RecordSet.spec.options.
	// +optional
	Options *ProviderOpenAPISchema `json:"options,omitempty"`

	// Adoption validates RecordSet.spec.adoption.
	// +optional
	Adoption *ProviderOpenAPISchema `json:"adoption,omitempty"`

	// StatusProviderData validates RecordSet.status.provider.data.
	// +optional
	StatusProviderData *ProviderOpenAPISchema `json:"statusProviderData,omitempty"`
}

type ProviderOpenAPISchema struct {
	// OpenAPIV3Schema is the schema for the corresponding inline object.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	OpenAPIV3Schema runtime.RawExtension `json:"openAPIV3Schema,omitempty"`
}

type ProviderValidationRule struct {
	// Rule is a CEL expression.
	// +kubebuilder:validation:MinLength=1
	Rule string `json:"rule"`

	// Message is shown when the rule fails.
	// +optional
	Message string `json:"message,omitempty"`
}

type ProviderConditionReason struct {
	// ConditionType is the status condition type this reason applies to.
	// +kubebuilder:validation:Enum=Accepted;Programmed
	ConditionType ConditionType `json:"conditionType"`

	// Status is the condition status this reason applies to.
	// +kubebuilder:validation:Enum=True;False;Unknown
	Status metav1.ConditionStatus `json:"status"`

	// Reason is the provider-specific condition reason stored in metav1.Condition.reason.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[A-Z][A-Za-z0-9]*$`
	Reason string `json:"reason"`

	// Description explains when the provider-specific reason is used.
	// +kubebuilder:validation:MinLength=1
	Description string `json:"description"`
}

type RecordSetValidationToggle struct {
	// Name is a core validation name.
	// +kubebuilder:validation:Enum=require-ttl;require-a-addresses;require-aaaa-addresses;require-txt-values;forbid-cname-apex;forbid-txt-non-printable-ascii
	Name string `json:"name"`

	// When is a CEL expression that disables this validation when true.
	// +kubebuilder:validation:MinLength=1
	When string `json:"when"`
}

type ZoneUnitValidationToggle struct {
	// Name is a core ZoneUnit composition validation name.
	// +kubebuilder:validation:Enum=forbid-cname-coexistence
	Name string `json:"name"`

	// When is a CEL expression evaluated with self and other ZoneUnit record set items.
	// +kubebuilder:validation:MinLength=1
	When string `json:"when"`
}

// ProviderList contains a list of Provider.
// +kubebuilder:object:root=true
type ProviderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Provider `json:"items"`
}
