package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// Zone represents a provider hosted zone or zone.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Domain",type=string,JSONPath=`.spec.domainName`
// +kubebuilder:printcolumn:name="Class",type=string,JSONPath=`.spec.zoneClassRef.name`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Zone struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ZoneSpec   `json:"spec,omitempty"`
	Status ZoneStatus `json:"status,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="!has(oldSelf.provider) || self.provider == oldSelf.provider",message="provider is immutable"
type ZoneSpec struct {
	// DomainName is the normalized ASCII zone apex without a trailing root dot.
	// +kubebuilder:validation:Pattern=`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)(\.([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?))*$`
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="domainName is immutable"
	DomainName string `json:"domainName"`

	// ZoneClassRef selects the provider implementation and parameters.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="zoneClassRef is immutable"
	ZoneClassRef ZoneClassReference `json:"zoneClassRef"`

	// Provider references a Provider version.
	Provider ProviderReference `json:"provider"`

	// Adoption selects an existing external zone to manage.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	Adoption runtime.RawExtension `json:"adoption,omitempty"`

	// AllowedRecordSets grants cross-namespace RecordSet attachment.
	// +optional
	AllowedRecordSets []AllowedRecordSet `json:"allowedRecordSets,omitempty"`
}

type ZoneStatus struct {
	// ObservedGeneration is the Zone generation reflected by this status.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// NameServers contains provider-assigned authoritative name servers.
	// +optional
	NameServers []string `json:"nameServers,omitempty"`

	// Provider contains provider-specific public status.
	// +optional
	Provider *ProviderStatus `json:"provider,omitempty"`

	// Conditions include Accepted and Programmed.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ZoneList contains a list of Zone.
// +kubebuilder:object:root=true
type ZoneList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Zone `json:"items"`
}
