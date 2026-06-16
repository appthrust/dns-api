package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// ZoneClass is a namespaced class of DNS zones and provider parameters.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=`.spec.provider.name`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type ZoneClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ZoneClassSpec   `json:"spec,omitempty"`
	Status ZoneClassStatus `json:"status,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="!has(oldSelf.provider) || self.provider == oldSelf.provider",message="provider is immutable"
type ZoneClassSpec struct {
	// Provider references a Provider version.
	Provider ProviderReference `json:"provider"`

	// ControllerName selects the provider controller instance for ZoneUnits using this class.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/[^/]+$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="controllerName is immutable"
	ControllerName string `json:"controllerName"`

	// IdentityRef selects the provider identity resource in the ZoneClass namespace.
	IdentityRef LocalObjectReference `json:"identityRef"`

	// Parameters contains provider-specific ZoneClass settings.
	// +kubebuilder:pruning:PreserveUnknownFields
	Parameters runtime.RawExtension `json:"parameters"`

	// AllowedZones limits which Zone namespaces may use this class.
	AllowedZones ZoneClassAllowedZones `json:"allowedZones"`
}

type ZoneClassAllowedZones struct {
	// Namespaces selects namespaces allowed to create Zones for this class.
	Namespaces NamespacePolicy `json:"namespaces"`
}

type ZoneClassStatus struct {
	// Conditions describe whether this class can be used.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ZoneClassList contains a list of ZoneClass.
// +kubebuilder:object:root=true
type ZoneClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ZoneClass `json:"items"`
}
