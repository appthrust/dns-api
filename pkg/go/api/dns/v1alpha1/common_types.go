package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

type ZoneClassReference struct {
	// Namespace defaults to the Zone namespace when omitted.
	// +optional
	Namespace *string `json:"namespace,omitempty"`

	// Name is the ZoneClass name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

type ZoneReference struct {
	// Namespace defaults to the RecordSet namespace when omitted.
	// +optional
	Namespace *string `json:"namespace,omitempty"`

	// Name is the Zone name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

type ObjectReference struct {
	// Namespace is the object namespace.
	// +kubebuilder:validation:MinLength=1
	Namespace string `json:"namespace"`

	// Name is the object name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

type ProviderReference struct {
	// Name is the cluster-scoped Provider resource name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Version is the Provider version name.
	// +kubebuilder:validation:MinLength=1
	Version string `json:"version"`
}

type LocalObjectReference struct {
	// Name is the referenced object name in the same namespace as the owner resource.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

type TypedLocalObjectReference struct {
	// Group is the API group of the referenced resource.
	// +kubebuilder:validation:MinLength=1
	Group string `json:"group"`

	// Kind is the kind of the referenced resource.
	// +kubebuilder:validation:MinLength=1
	Kind string `json:"kind"`

	// Name is the object name in the same namespace as the owner resource.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

type ProviderStatus struct {
	// Data contains public provider-specific structured status.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	Data runtime.RawExtension `json:"data,omitempty"`

	// State contains provider-controller internal state.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	State runtime.RawExtension `json:"state,omitempty"`
}

type NamespacesFrom string

const (
	NamespacesFromSame     NamespacesFrom = "Same"
	NamespacesFromSelector NamespacesFrom = "Selector"
	NamespacesFromAll      NamespacesFrom = "All"
)

type NamespacePolicy struct {
	// From defines which namespaces are selected.
	// +kubebuilder:default=Same
	// +kubebuilder:validation:Enum=Same;Selector;All
	// +optional
	From NamespacesFrom `json:"from,omitempty"`

	// Selector selects namespaces when from is Selector.
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`
}

type RecordType string

const (
	RecordTypeA     RecordType = "A"
	RecordTypeAAAA  RecordType = "AAAA"
	RecordTypeCNAME RecordType = "CNAME"
	RecordTypeNS    RecordType = "NS"
	RecordTypeTXT   RecordType = "TXT"
	RecordTypeMX    RecordType = "MX"
	RecordTypeCAA   RecordType = "CAA"
)

type RecordNamePolicy struct {
	// Pattern is a regular expression matched against the relative record name.
	// +kubebuilder:validation:MinLength=1
	Pattern string `json:"pattern"`
}

type AllowedRecord struct {
	// Name limits the relative record owner name.
	Name RecordNamePolicy `json:"name"`

	// Types limits the record types.
	// +kubebuilder:validation:MinItems=1
	Types []RecordType `json:"types"`
}

type AllowedRecordSetNamespaces struct {
	// Selector selects namespaces allowed to attach RecordSets.
	Selector metav1.LabelSelector `json:"selector"`
}

type AllowedRecordSet struct {
	// Namespaces selects namespaces allowed to attach RecordSets.
	Namespaces AllowedRecordSetNamespaces `json:"namespaces"`

	// Records limits allowed record names and types.
	// +kubebuilder:validation:MinItems=1
	Records []AllowedRecord `json:"records"`
}

type ConditionType string

const (
	ConditionAccepted   ConditionType = "Accepted"
	ConditionProgrammed ConditionType = "Programmed"
	ConditionReady      ConditionType = "Ready"
)
