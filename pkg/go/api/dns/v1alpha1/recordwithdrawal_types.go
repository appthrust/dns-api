package v1alpha1

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"net/netip"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	AddressLabelKey = "endpoint.dns.appthrust.io/address"
	ConditionWithdrawn ConditionType = "Withdrawn"
	ConditionOverrideAccepted ConditionType = "OverrideAccepted"
)

// AddressLabelValue encodes a normalized address as a Kubernetes label value.
// Invalid addresses return an empty value; callers must validate before creation.
func AddressLabelValue(ip netip.Addr) string {
	if !ip.IsValid() || ip.Zone() != "" { return "" }
	ip = ip.Unmap()
	if ip.Is4() { return ip.String() }
	bytes := ip.As16()
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(bytes[:]))
}

// WithdrawalSpecDigest binds the exact immutable wire spec. The digest is SHA-256
// of encoding/json's struct encoding, prefixed with sha256:.
func WithdrawalSpecDigest(spec RecordWithdrawalSpec) string {
	data, _ := json.Marshal(spec)
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:])
}

// RecordWithdrawal is a retained request to verify exclusion of one DNS address.
// It deliberately has no owner reference or automatic cleanup finalizer.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Address",type=string,JSONPath=`.spec.address`
// +kubebuilder:printcolumn:name="Withdrawn",type=string,JSONPath=`.status.conditions[?(@.type=="Withdrawn")].status`
type RecordWithdrawal struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec RecordWithdrawalSpec `json:"spec"`
	// +optional
	Status RecordWithdrawalStatus `json:"status,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable"
type RecordWithdrawalSpec struct {
	ZoneRef ZoneReference `json:"zoneRef"`
	// Name is a canonical zone-relative owner, including @ or a leftmost wildcard.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
	// Address is a canonical, unscoped and unmapped IP literal.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=39
	Address string `json:"address"`
}

// WithdrawalObjectReference binds one immutable Kubernetes object incarnation.
type WithdrawalObjectReference struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Namespace string `json:"namespace"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	UID types.UID `json:"uid"`
}

type RecordWithdrawalStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Zone and Unit are the exact binding used to interpret the provider result.
	// +optional
	Zone *WithdrawalObjectReference `json:"zone,omitempty"`
	// +optional
	Unit *WithdrawalObjectReference `json:"unit,omitempty"`
	// +optional
	ProviderRef *ProviderReference `json:"providerRef,omitempty"`
	// +optional
	PolicyFingerprint string `json:"policyFingerprint,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxItems=256
	// +listType=set
	CheckedNames []string `json:"checkedNames,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxItems=256
	// +listType=set
	RequiredViews []string `json:"requiredViews,omitempty"`
	// +optional
	SampledWildcard bool `json:"sampledWildcard,omitempty"`
	// +optional
	AbsentSince *metav1.Time `json:"absentSince,omitempty"`
	// +optional
	NotBefore *metav1.Time `json:"notBefore,omitempty"`
	// +optional
	LastProbeTime *metav1.Time `json:"lastProbeTime,omitempty"`
	// +optional
	NextProbeTime *metav1.Time `json:"nextProbeTime,omitempty"`
	// +optional
	VerifiedAt *metav1.Time `json:"verifiedAt,omitempty"`
	// +optional
	ResultDigest string `json:"resultDigest,omitempty"`
	// Provider contains bounded public evidence, never private provider state.
	// +optional
	Provider *ProviderStatus `json:"provider,omitempty"`
	// +optional
	WaiverRef *RecordWithdrawalWaiverReference `json:"waiverRef,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxItems=4
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type RecordWithdrawalList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items []RecordWithdrawal `json:"items"`
}

// RecordWithdrawalWaiver records separately RBAC-authorized acceptance of stale
// DNS risk. Kubernetes audit, not client-supplied identity fields, is authority.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type RecordWithdrawalWaiver struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec RecordWithdrawalWaiverSpec `json:"spec"`
	// +optional
	Status RecordWithdrawalWaiverStatus `json:"status,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable"
type RecordWithdrawalWaiverSpec struct {
	WithdrawalRef WithdrawalObjectReference `json:"withdrawalRef"`
	// +kubebuilder:validation:Pattern=`^sha256:[0-9a-f]{64}$`
	ClaimSpecDigest string `json:"claimSpecDigest"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=39
	Address string `json:"address"`
	ReservationRef WithdrawalObjectReference `json:"reservationRef"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=2048
	Reason string `json:"reason"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1024
	TicketRef string `json:"ticketRef"`
	// +kubebuilder:validation:XValidation:rule="self == true",message="acceptStaleDNSRisk must be true"
	AcceptStaleDNSRisk bool `json:"acceptStaleDNSRisk"`
}

func WithdrawalWaiverDigest(spec RecordWithdrawalWaiverSpec) string {
	data, _ := json.Marshal(spec)
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:])
}

type RecordWithdrawalWaiverStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxItems=1
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type RecordWithdrawalWaiverReference struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	UID types.UID `json:"uid"`
	// +kubebuilder:validation:Pattern=`^sha256:[0-9a-f]{64}$`
	Digest string `json:"digest"`
	ReservationRef WithdrawalObjectReference `json:"reservationRef"`
}

// +kubebuilder:object:root=true
type RecordWithdrawalWaiverList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items []RecordWithdrawalWaiver `json:"items"`
}

// Scalar keys duplicate the nested identity because Kubernetes map-list keys
// cannot address nested fields. CEL prevents either representation diverging.
// +kubebuilder:validation:XValidation:rule="self.claimNamespace == self.claim.namespace && self.claimName == self.claim.name",message="scalar claim keys must match claim"
type ZoneUnitWithdrawal struct {
	// +kubebuilder:validation:MinLength=1
	ClaimNamespace string `json:"claimNamespace"`
	// +kubebuilder:validation:MinLength=1
	ClaimName string `json:"claimName"`
	Claim WithdrawalObjectReference `json:"claim"`
	// +kubebuilder:validation:Minimum=1
	ObservedGeneration int64 `json:"observedGeneration"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=39
	Address string `json:"address"`
	Allowed bool `json:"allowed"`
	// +optional
	Waiver *ZoneUnitWithdrawalWaiver `json:"waiver,omitempty"`
}

type ZoneUnitWithdrawalWaiver struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	UID types.UID `json:"uid"`
	// +kubebuilder:validation:Pattern=`^sha256:[0-9a-f]{64}$`
	Digest string `json:"digest"`
	// ReservationRef is immutable waiver scope, not platform lookup authority.
	ReservationRef WithdrawalObjectReference `json:"reservationRef"`
}

// +kubebuilder:validation:XValidation:rule="self.claimNamespace == self.claim.namespace && self.claimName == self.claim.name",message="scalar claim keys must match claim"
type ZoneUnitWithdrawalStatus struct {
	// +kubebuilder:validation:MinLength=1
	ClaimNamespace string `json:"claimNamespace"`
	// +kubebuilder:validation:MinLength=1
	ClaimName string `json:"claimName"`
	Claim WithdrawalObjectReference `json:"claim"`
	// +kubebuilder:validation:Minimum=1
	ObservedGeneration int64 `json:"observedGeneration"`
	// +optional
	PolicyFingerprint string `json:"policyFingerprint,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxItems=256
	// +listType=set
	CheckedNames []string `json:"checkedNames,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxItems=256
	// +listType=set
	RequiredViews []string `json:"requiredViews,omitempty"`
	// +optional
	SampledWildcard bool `json:"sampledWildcard,omitempty"`
	// +optional
	AbsentSince *metav1.Time `json:"absentSince,omitempty"`
	// +optional
	NotBefore *metav1.Time `json:"notBefore,omitempty"`
	// +optional
	LastProbeTime *metav1.Time `json:"lastProbeTime,omitempty"`
	// +optional
	NextProbeTime *metav1.Time `json:"nextProbeTime,omitempty"`
	// +optional
	VerifiedAt *metav1.Time `json:"verifiedAt,omitempty"`
	// +optional
	ResultDigest string `json:"resultDigest,omitempty"`
	// +optional
	Provider *ProviderStatus `json:"provider,omitempty"`
	// +optional
	WaiverRef *RecordWithdrawalWaiverReference `json:"waiverRef,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxItems=4
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
