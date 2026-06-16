package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// ZoneUnit is the zone-scoped desired-state and ownership ledger built from accepted Zone and RecordSet claims.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:validation:XValidation:rule="self.metadata.name == self.spec.zone.ref.name",message="metadata.name must match spec.zone.ref.name"
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=`.spec.provider.name`
// +kubebuilder:printcolumn:name="Zone",type=string,JSONPath=`.spec.zone.domainName`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type ZoneUnit struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ZoneUnitSpec   `json:"spec,omitempty"`
	Status ZoneUnitStatus `json:"status,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="!has(oldSelf.provider) || self.provider == oldSelf.provider",message="provider is immutable"
type ZoneUnitSpec struct {
	// Provider is the Provider storage version used inside this ZoneUnit.
	Provider ProviderReference `json:"provider"`

	// Zone is the accepted desired state for the owning Zone claim.
	Zone ZoneUnitZoneSpec `json:"zone"`

	// RecordSets contains accepted desired state and retained cleanup ledger state
	// for owner RecordSet claims.
	// +optional
	// +listType=map
	// +listMapKey=recordSetNamespace
	// +listMapKey=recordSetName
	RecordSets []ZoneUnitRecordSetSpec `json:"recordSets,omitempty"`
}

type ZoneUnitZoneSpec struct {
	// Ref points to the owning Zone claim.
	Ref ObjectReference `json:"ref"`

	// ObservedGeneration is the Zone generation used to build this item.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// DomainName is the accepted zone domain name.
	DomainName string `json:"domainName"`

	// ZoneClassRef is the resolved ZoneClass reference.
	ZoneClassRef ObjectReference `json:"zoneClassRef"`

	// Adoption is provider-specific adoption payload in storage-version form.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	Adoption runtime.RawExtension `json:"adoption,omitempty"`
}

type ZoneUnitRecordSetSpec struct {
	// RecordSetNamespace is the owner RecordSet namespace.
	RecordSetNamespace string `json:"recordSetNamespace"`

	// RecordSetName is the owner RecordSet name.
	RecordSetName string `json:"recordSetName"`

	// ObservedGeneration is the RecordSet generation used to build this item.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Name is the zone-relative DNS owner name.
	Name string `json:"name"`

	// Type is the DNS record type.
	Type RecordType `json:"type"`

	// TTL is the record TTL in seconds for standard records.
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

	// Options contains provider-specific record options in storage-version form.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	Options runtime.RawExtension `json:"options,omitempty"`

	// Adoption contains provider-specific adoption payload in storage-version form.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	Adoption runtime.RawExtension `json:"adoption,omitempty"`

	// Allowed indicates whether current composition policy allows normal provider
	// reconciliation for this item. Omitted means true. False keeps ownership and
	// cleanup state without allowing provider-side create or update.
	// +optional
	Allowed *bool `json:"allowed,omitempty"`

	// DeletionRequested asks the provider controller to delete the provider-side record target.
	// +optional
	DeletionRequested bool `json:"deletionRequested,omitempty"`
}

func (in ZoneUnitRecordSetSpec) IsAllowed() bool {
	return in.Allowed == nil || *in.Allowed
}

type ZoneUnitStatus struct {
	// ObservedGeneration is the ZoneUnit generation observed by the provider controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions contains resource-level conditions such as Programmed.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Provider contains provider-controller internal state for the whole unit.
	// +optional
	Provider *ProviderStatus `json:"provider,omitempty"`

	// Zone contains provider result for the owning Zone claim.
	// +optional
	Zone *ZoneUnitZoneStatus `json:"zone,omitempty"`

	// RecordSets contains provider results for accepted owner RecordSet claims.
	// +optional
	// +listType=map
	// +listMapKey=recordSetNamespace
	// +listMapKey=recordSetName
	RecordSets []ZoneUnitRecordSetStatus `json:"recordSets,omitempty"`
}

type ZoneUnitZoneStatus struct {
	// NameServers contains provider-assigned authoritative name servers.
	// +optional
	NameServers []string `json:"nameServers,omitempty"`

	// Provider contains public provider data and optional provider state for the zone target.
	// +optional
	Provider *ProviderStatus `json:"provider,omitempty"`

	// Conditions contains provider acceptance and programming result for the Zone claim.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type ZoneUnitRecordSetStatus struct {
	// RecordSetNamespace is the owner RecordSet namespace matching spec.recordSets[].
	RecordSetNamespace string `json:"recordSetNamespace"`

	// RecordSetName is the owner RecordSet name matching spec.recordSets[].
	RecordSetName string `json:"recordSetName"`

	// ObservedGeneration is the owner RecordSet generation observed in ZoneUnit.spec.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Provider contains public provider data and optional provider state for this record target.
	// +optional
	Provider *ProviderStatus `json:"provider,omitempty"`

	// DeletionCompleted is true after provider-side record cleanup has completed.
	// +optional
	DeletionCompleted bool `json:"deletionCompleted,omitempty"`

	// Conditions contains provider acceptance and programming result for the RecordSet claim.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ZoneUnitList contains a list of ZoneUnit.
// +kubebuilder:object:root=true
type ZoneUnitList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ZoneUnit `json:"items"`
}
