package v1alpha1

import (
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EndpointProviderCapability declares endpoint app support for one Core Provider version.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=`.spec.provider.name`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.provider.version`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type EndpointProviderCapability struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec EndpointProviderCapabilitySpec `json:"spec,omitempty"`
}

type EndpointProviderCapabilitySpec struct {
	// Provider references a Core Provider version.
	Provider dnsv1alpha1.ProviderReference `json:"provider"`

	// Conversion declares the aggregated API resource for provider-specific
	// endpoint RecordSet conversion.
	Conversion EndpointRecordSetConversionAPI `json:"conversion"`
}

type EndpointRecordSetConversionAPI struct {
	// Group is the API group that serves EndpointRecordSetConversion.
	// +kubebuilder:validation:MinLength=1
	Group string `json:"group"`

	// Version defaults to spec.provider.version when omitted.
	// +optional
	Version string `json:"version,omitempty"`

	// Resource is the plural resource name, normally endpointrecordsetconversions.
	// +kubebuilder:validation:MinLength=1
	Resource string `json:"resource"`
}

// EndpointProviderCapabilityList contains a list of EndpointProviderCapability.
// +kubebuilder:object:root=true
type EndpointProviderCapabilityList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []EndpointProviderCapability `json:"items"`
}
