package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// RecordSet represents one DNS record set attached to one Zone.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Zone",type=string,JSONPath=`.spec.zoneRef.name`
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Record",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type RecordSet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RecordSetSpec   `json:"spec,omitempty"`
	Status RecordSetStatus `json:"status,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="self.zoneRef == oldSelf.zoneRef",message="zoneRef is immutable"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.provider) || self.provider == oldSelf.provider",message="provider is immutable"
// +kubebuilder:validation:XValidation:rule="self.type == oldSelf.type",message="type is immutable"
// +kubebuilder:validation:XValidation:rule="self.name == oldSelf.name",message="name is immutable"
type RecordSetSpec struct {
	// ZoneRef selects the Zone that owns this record set.
	ZoneRef ZoneReference `json:"zoneRef"`

	// Provider references a Provider version.
	Provider ProviderReference `json:"provider"`

	// Type is the DNS record type.
	// +kubebuilder:validation:Enum=A;AAAA;CNAME;NS;TXT;MX;CAA
	Type RecordType `json:"type"`

	// Name is the relative DNS owner name in the Zone.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// TTL is the record TTL in seconds.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=2147483647
	// +optional
	TTL *int32 `json:"ttl,omitempty"`

	// A defines a standard A record body.
	// +optional
	A *ARecordSet `json:"a,omitempty"`

	// AAAA defines a standard AAAA record body.
	// +optional
	AAAA *AAAARecordSet `json:"aaaa,omitempty"`

	// TXT defines a standard TXT record body.
	// +optional
	TXT *TXTRecordSet `json:"txt,omitempty"`

	// CNAME defines a standard CNAME record body.
	// +optional
	CNAME *CNAMERecordSet `json:"cname,omitempty"`

	// MX defines a standard MX record body.
	// +optional
	MX *MXRecordSet `json:"mx,omitempty"`

	// CAA defines a standard CAA record body.
	// +optional
	CAA *CAARecordSet `json:"caa,omitempty"`

	// NS defines a standard delegated NS record body.
	// +optional
	NS *NSRecordSet `json:"ns,omitempty"`

	// Options contains provider-specific record options.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	Options runtime.RawExtension `json:"options,omitempty"`

	// Adoption selects an existing external record set to manage.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	Adoption runtime.RawExtension `json:"adoption,omitempty"`
}

type ARecordSet struct {
	// Addresses contains IPv4 addresses.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:items:Pattern=`^((25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])\.){3}(25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])$`
	Addresses []string `json:"addresses"`
}

type AAAARecordSet struct {
	// Addresses contains IPv6 addresses.
	// +kubebuilder:validation:MinItems=1
	Addresses []string `json:"addresses"`
}

type TXTRecordSet struct {
	// Values contains logical TXT resource record values.
	// +kubebuilder:validation:MinItems=1
	Values []string `json:"values"`
}

type CNAMERecordSet struct {
	// Target contains the canonical target DNS name without a trailing root dot.
	// +kubebuilder:validation:MinLength=1
	Target string `json:"target"`
}

type MXRecordSet struct {
	// Records contains mail exchange entries.
	// +kubebuilder:validation:MinItems=1
	Records []MXRecord `json:"records"`
}

type MXRecord struct {
	// Preference is the MX priority. Lower values have higher priority.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=65535
	Preference int32 `json:"preference"`

	// Exchange contains the canonical mail server DNS name without a trailing root dot.
	// Use "." with preference 0 for Null MX.
	// +kubebuilder:validation:MinLength=1
	Exchange string `json:"exchange"`
}

type CAARecordSet struct {
	// Records contains Certification Authority Authorization entries.
	// +kubebuilder:validation:MinItems=1
	Records []CAARecord `json:"records"`
}

type CAARecord struct {
	// Flags is the CAA flags field.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=255
	Flags int32 `json:"flags"`

	// Tag is the lowercase ASCII CAA property tag.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[a-z0-9]+$`
	Tag string `json:"tag"`

	// Value is the CAA property value.
	// +kubebuilder:validation:MinLength=1
	Value string `json:"value"`
}

type NSRecordSet struct {
	// NameServers contains delegated name server DNS names without trailing root dots.
	// +kubebuilder:validation:MinItems=1
	NameServers []string `json:"nameServers"`
}

type RecordSetStatus struct {
	// ObservedGeneration is the RecordSet generation reflected by this status.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Zone describes the resolved Zone relationship.
	// +optional
	Zone *RecordSetZoneStatus `json:"zone,omitempty"`

	// Provider contains provider-specific public status.
	// +optional
	Provider *ProviderStatus `json:"provider,omitempty"`

	// Conditions include Accepted and Programmed.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type RecordSetZoneStatus struct {
	// Ref is the Zone that this RecordSet is attached to.
	Ref ObjectReference `json:"ref"`
}

// RecordSetList contains a list of RecordSet.
// +kubebuilder:object:root=true
type RecordSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RecordSet `json:"items"`
}
