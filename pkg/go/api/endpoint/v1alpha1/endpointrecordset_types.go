package v1alpha1

import (
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// EndpointTargetType is the endpoint target address type.
// +kubebuilder:validation:Enum=Hostname;IPAddress
type EndpointTargetType string

const (
	EndpointTargetTypeHostname  EndpointTargetType = "Hostname"
	EndpointTargetTypeIPAddress EndpointTargetType = "IPAddress"
)

// EndpointTarget is one provider-neutral endpoint address.
type EndpointTarget struct {
	// Type is the target address type.
	Type EndpointTargetType `json:"type"`

	// Value is the target address value.
	// +kubebuilder:validation:MinLength=1
	Value string `json:"value"`
}

// EndpointRecordSet is a provider-neutral endpoint publishing intent.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Hostnames",type=integer,JSONPath=`.status.hostnameCount`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type EndpointRecordSet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   EndpointRecordSetSpec   `json:"spec,omitempty"`
	Status EndpointRecordSetStatus `json:"status,omitempty"`
}

type EndpointRecordSetSpec struct {
	// Hostnames are fully qualified DNS hostnames to publish.
	// +kubebuilder:validation:MinItems=1
	// +listType=set
	Hostnames []string `json:"hostnames"`

	// Targets are endpoint addresses that the hostnames should publish.
	// +kubebuilder:validation:MinItems=1
	// +listType=atomic
	Targets []EndpointTarget `json:"targets"`
}

type EndpointRecordSetStatus struct {
	// ObservedGeneration is the generation observed by the endpoint controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// HostnameCount is the number of hostname status entries.
	// +optional
	HostnameCount int32 `json:"hostnameCount,omitempty"`

	// Hostnames contains per-hostname resolution and generated RecordSet status.
	// +optional
	// +listType=map
	// +listMapKey=hostname
	Hostnames []EndpointRecordSetHostnameStatus `json:"hostnames,omitempty"`

	// Conditions summarizes endpoint record set reconciliation.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type EndpointRecordSetHostnameStatus struct {
	// Hostname is the DNS hostname from spec.hostnames.
	// +kubebuilder:validation:MinLength=1
	Hostname string `json:"hostname"`

	// Zone is the selected Zone for this hostname.
	// +optional
	Zone *EndpointRecordSetZoneStatus `json:"zone,omitempty"`

	// RecordSets contains generated RecordSet outputs for this hostname.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +listMapKey=type
	RecordSets []EndpointRecordSetGeneratedRecordSetStatus `json:"recordSets,omitempty"`

	// Conditions describes per-hostname reconciliation.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type EndpointRecordSetZoneStatus struct {
	// Ref points to the selected Zone.
	Ref dnsv1alpha1.ObjectReference `json:"ref"`

	// DomainName is the selected Zone domain name.
	DomainName string `json:"domainName"`

	// Provider is the selected Zone provider.
	Provider dnsv1alpha1.ProviderReference `json:"provider"`
}

type EndpointRecordSetGeneratedRecordSetStatus struct {
	// Ref points to the generated Core RecordSet.
	Ref dnsv1alpha1.ObjectReference `json:"ref"`

	// Name is the zone-relative DNS owner name.
	Name string `json:"name"`

	// Type is the DNS record type.
	Type EndpointRecordSetType `json:"type"`

	// Fragment is the Provider-converted RecordSet.spec fragment.
	// +optional
	Fragment *RecordSetSpecFragment `json:"fragment,omitempty"`

	// Conditions mirrors generated RecordSet conditions.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// EndpointRecordSetList contains a list of EndpointRecordSet.
// +kubebuilder:object:root=true
type EndpointRecordSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []EndpointRecordSet `json:"items"`
}

// EndpointRecordSetType is the DNS record type supported by endpoint apps.
// +kubebuilder:validation:Enum=A;AAAA;CNAME
type EndpointRecordSetType string

const (
	EndpointRecordSetTypeA     EndpointRecordSetType = "A"
	EndpointRecordSetTypeAAAA  EndpointRecordSetType = "AAAA"
	EndpointRecordSetTypeCNAME EndpointRecordSetType = "CNAME"
)

func (t EndpointRecordSetType) DNSRecordType() dnsv1alpha1.RecordType {
	return dnsv1alpha1.RecordType(t)
}

// EndpointRecordSetConversionInput is the provider-neutral input passed to an
// endpoint Provider conversion API for one hostname and one selected Zone.
type EndpointRecordSetConversionInput struct {
	// Hostname is the fully qualified DNS hostname being converted.
	// +kubebuilder:validation:MinLength=1
	Hostname string `json:"hostname"`

	// Name is the zone-relative DNS owner name in the selected Zone.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Zone contains selected Zone context needed by Provider conversion.
	Zone EndpointRecordSetConversionZone `json:"zone"`

	// Targets are endpoint addresses that the hostname should publish.
	// +kubebuilder:validation:MinItems=1
	// +listType=atomic
	Targets []EndpointTarget `json:"targets"`
}

type EndpointRecordSetConversionZone struct {
	// DomainName is the selected Zone domain name.
	// +kubebuilder:validation:MinLength=1
	DomainName string `json:"domainName"`
}

type EndpointRecordSetConversionOutput struct {
	// Fragments are Provider-converted RecordSet.spec fragments.
	// +kubebuilder:validation:MinItems=1
	// +listType=atomic
	Fragments []RecordSetSpecFragment `json:"fragments"`
}

// RecordSetSpecFragment is the Provider-converted RecordSet.spec shape returned
// by Endpoint RecordSet Conversion APIs. The endpoint controller adds zoneRef
// and provider when it creates the final dns.appthrust.io RecordSet.
// +kubebuilder:validation:XValidation:rule="self.type != 'A' || has(self.a) || has(self.options)",message="a or options is required when type is A"
// +kubebuilder:validation:XValidation:rule="self.type != 'AAAA' || has(self.aaaa) || has(self.options)",message="aaaa or options is required when type is AAAA"
// +kubebuilder:validation:XValidation:rule="self.type != 'CNAME' || has(self.cname)",message="cname is required when type is CNAME"
type RecordSetSpecFragment struct {
	// Type is the DNS record type.
	Type EndpointRecordSetType `json:"type"`

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
	A *dnsv1alpha1.ARecordSet `json:"a,omitempty"`

	// AAAA defines a standard AAAA record body.
	// +optional
	AAAA *dnsv1alpha1.AAAARecordSet `json:"aaaa,omitempty"`

	// CNAME defines a standard CNAME record body.
	// +optional
	CNAME *dnsv1alpha1.CNAMERecordSet `json:"cname,omitempty"`

	// Options contains provider-specific record options.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	Options runtime.RawExtension `json:"options,omitempty"`
}
