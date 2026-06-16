package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// CloudflareIdentity selects a Cloudflare API token used by the Cloudflare controller.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Account",type=string,JSONPath=`.status.account.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type CloudflareIdentity struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CloudflareIdentitySpec   `json:"spec,omitempty"`
	Status CloudflareIdentityStatus `json:"status,omitempty"`
}

type CloudflareIdentitySpec struct {
	// AccessToken points to the Cloudflare API token source.
	AccessToken CloudflareAccessTokenSource `json:"accessToken"`
}

type CloudflareAccessTokenSource struct {
	// SecretRef points to a Secret in the same namespace.
	SecretRef CloudflareSecretKeyReference `json:"secretRef"`
}

type CloudflareSecretKeyReference struct {
	// Name is the Secret name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Key is the Secret key containing the raw Cloudflare API token.
	// +kubebuilder:validation:MinLength=1
	Key string `json:"key"`
}

type CloudflareIdentityStatus struct {
	// ObservedGeneration is the last reconciled generation.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Account is the single Cloudflare account observed through the token.
	// +optional
	Account *CloudflareAccountStatus `json:"account,omitempty"`

	// AccessToken is non-secret token metadata observed through token verification.
	// +optional
	AccessToken *CloudflareAccessTokenStatus `json:"accessToken,omitempty"`

	// LastCredentialCheckTime is the last live token and account check time.
	// +optional
	LastCredentialCheckTime *metav1.Time `json:"lastCredentialCheckTime,omitempty"`

	// NextCredentialCheckTime is the controller-planned next live check time.
	// +optional
	NextCredentialCheckTime *metav1.Time `json:"nextCredentialCheckTime,omitempty"`

	// Conditions describe whether this identity can be referenced.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type CloudflareAccountStatus struct {
	// ID is the Cloudflare account ID.
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{32}$`
	// +optional
	ID string `json:"id,omitempty"`

	// Name is the Cloudflare account display name.
	// +optional
	Name string `json:"name,omitempty"`

	// Type is the Cloudflare account type.
	// +optional
	Type string `json:"type,omitempty"`
}

type CloudflareAccessTokenStatus struct {
	// ID is the Cloudflare token identifier.
	// +optional
	ID string `json:"id,omitempty"`

	// Status is the Cloudflare token status, such as active, disabled, or expired.
	// +optional
	Status string `json:"status,omitempty"`

	// ExpiresOn is the token expiration time when returned by Cloudflare.
	// +optional
	ExpiresOn *metav1.Time `json:"expiresOn,omitempty"`

	// NotBefore is the token not-before time when returned by Cloudflare.
	// +optional
	NotBefore *metav1.Time `json:"notBefore,omitempty"`
}

// CloudflareIdentityList contains a list of CloudflareIdentity.
// +kubebuilder:object:root=true
type CloudflareIdentityList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CloudflareIdentity `json:"items"`
}
