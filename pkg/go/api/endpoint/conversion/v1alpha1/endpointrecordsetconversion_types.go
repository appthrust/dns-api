package v1alpha1

import (
	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EndpointRecordSetConversion is a create-only aggregated API request.
// It is served by app providers and is not installed as a CRD.
// +kubebuilder:object:root=true
type EndpointRecordSetConversion struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   EndpointRecordSetConversionSpec   `json:"spec,omitempty"`
	Status EndpointRecordSetConversionStatus `json:"status,omitempty"`
}

type EndpointRecordSetConversionSpec struct {
	// UID correlates request and response.
	// +kubebuilder:validation:MinLength=1
	UID string `json:"uid"`

	// Input is the provider-neutral endpoint conversion input.
	Input endpointv1alpha1.EndpointRecordSetConversionInput `json:"input"`
}

type EndpointRecordSetConversionStatus struct {
	// UID echoes spec.uid.
	// +kubebuilder:validation:MinLength=1
	UID string `json:"uid"`

	// Result is the provider conversion result.
	Result EndpointRecordSetConversionResult `json:"result"`

	// Output is the provider conversion output.
	// +optional
	Output *endpointv1alpha1.EndpointRecordSetConversionOutput `json:"output,omitempty"`

	// Conditions describe provider conversion.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type EndpointRecordSetConversionResult struct {
	// Status is Success or Failure.
	// +kubebuilder:validation:Enum=Success;Failure
	Status string `json:"status"`

	// Reason is a machine-readable result reason.
	// +kubebuilder:validation:MinLength=1
	Reason string `json:"reason"`

	// Message is a human-readable result message.
	// +optional
	Message string `json:"message,omitempty"`

	// Retryable tells callers whether a failed request may succeed later.
	// +optional
	Retryable bool `json:"retryable,omitempty"`
}

// EndpointRecordSetConversionList contains a list of EndpointRecordSetConversion.
// +kubebuilder:object:root=true
type EndpointRecordSetConversionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []EndpointRecordSetConversion `json:"items"`
}
