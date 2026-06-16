package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// Route53Identity selects the AWS account and IAM identity used for Route 53.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Account",type=string,JSONPath=`.spec.accountID`
// +kubebuilder:printcolumn:name="Region",type=string,JSONPath=`.spec.region`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Route53Identity struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   Route53IdentitySpec   `json:"spec,omitempty"`
	Status Route53IdentityStatus `json:"status,omitempty"`
}

type Route53IdentitySpec struct {
	// AccountID is the AWS account ID expected after credential resolution.
	// +kubebuilder:validation:Pattern=`^[0-9]{12}$`
	AccountID string `json:"accountID"`

	// Region is the AWS region used for AWS SDK and STS endpoint resolution.
	// +kubebuilder:validation:Pattern=`^[a-z]{2}(-[a-z0-9]+)+-[0-9]+$`
	Region string `json:"region"`

	// Credentials selects the base credential source.
	Credentials Route53Credentials `json:"credentials"`

	// AssumeRoleChain contains IAM roles assumed in order from the base credential.
	// +optional
	AssumeRoleChain []Route53AssumeRole `json:"assumeRoleChain,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="has(self.runtime)",message="credentials.runtime must be specified"
type Route53Credentials struct {
	// Runtime uses the AWS SDK default credential chain from the controller runtime.
	// +optional
	Runtime *Route53RuntimeCredentials `json:"runtime,omitempty"`
}

type Route53RuntimeCredentials struct{}

type Route53AssumeRole struct {
	// RoleARN is the IAM role ARN to assume.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^arn:aws[a-zA-Z-]*:iam::[0-9]{12}:role/[A-Za-z0-9+=,.@_/-]+$`
	RoleARN string `json:"roleARN"`

	// ExternalID is passed to STS AssumeRole.
	// +optional
	ExternalID string `json:"externalID,omitempty"`

	// SessionName is passed to STS AssumeRole. The controller generates one when omitted.
	// +kubebuilder:validation:MaxLength=64
	// +optional
	SessionName string `json:"sessionName,omitempty"`
}

type Route53IdentityStatus struct {
	// ObservedGeneration is the last reconciled generation.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// AccountID is the AWS account ID returned by STS for the final credential.
	// +optional
	AccountID string `json:"accountID,omitempty"`

	// LastCredentialCheckTime is the last time the controller tried a live AWS identity check.
	// +optional
	LastCredentialCheckTime *metav1.Time `json:"lastCredentialCheckTime,omitempty"`

	// NextCredentialCheckTime is the controller-planned next live AWS identity check time.
	// +optional
	NextCredentialCheckTime *metav1.Time `json:"nextCredentialCheckTime,omitempty"`

	// Conditions describe whether this identity can be referenced.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Route53IdentityList contains a list of Route53Identity.
// +kubebuilder:object:root=true
type Route53IdentityList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Route53Identity `json:"items"`
}
