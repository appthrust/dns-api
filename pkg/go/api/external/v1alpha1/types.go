package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

const (
	ProviderName    = "external.dns.appthrust.io"
	ProviderVersion = "v1alpha1"
	ControllerName  = ProviderName + "/controller"
)

type ResolverMode string

const (
	ResolverModeCluster   ResolverMode = "Cluster"
	ResolverModeRecursive ResolverMode = "Recursive"
)

// ExternalZoneClassParameters is an inline payload, not a Kubernetes resource.
type ExternalZoneClassParameters struct {
	Resolver            ResolverPolicy `json:"resolver,omitempty"`
	ExpectedNameservers []string       `json:"expectedNameservers,omitempty"`
}

// ResolverPolicy selects independent recursive views; servers are canonical IP:port endpoints.
type ResolverPolicy struct {
	Mode    ResolverMode `json:"mode,omitempty"`
	Servers []string     `json:"servers,omitempty"`
}

// VerificationValues is a bounded preview. Comparisons always use the complete observation.
type VerificationValues struct {
	Values    []string `json:"values,omitempty"`
	Total     int32    `json:"total"`
	Truncated bool     `json:"truncated"`
}

// ExternalVerificationData is public, non-secret evidence shared by Zone and RecordSet status.
// Values are typed (for example "A 192.0.2.1"); chain and views are bounded diagnostics.
type ExternalVerificationData struct {
	ResolverMode       ResolverMode       `json:"resolverMode"`
	CheckedName        string             `json:"checkedName"`
	SampledWildcard    bool               `json:"sampledWildcard,omitempty"`
	Actual             VerificationValues `json:"actual"`
	Missing            VerificationValues `json:"missing"`
	Extra              VerificationValues `json:"extra"`
	CNAMEChain         VerificationValues `json:"cnameChain"`
	Views              VerificationValues `json:"views"`
	ObservedTTLSeconds *uint32            `json:"observedTTLSeconds,omitempty"`
	NegativeTTLSeconds *uint32            `json:"negativeTTLSeconds,omitempty"`
	NegativeExpiryTime *metav1.Time       `json:"negativeExpiryTime,omitempty"`
	LastProbeTime      *metav1.Time       `json:"lastProbeTime,omitempty"`
	NextProbeTime      *metav1.Time       `json:"nextProbeTime,omitempty"`
}
