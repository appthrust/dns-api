package v1alpha1

import (
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	ProviderName    = "hosted.dns.appthrust.io"
	ProviderVersion = "v1alpha1"
	ControllerName  = ProviderName + "/controller"

	ReasonServingPending            = "ServingPending"
	ReasonSnapshotRejected          = "SnapshotRejected"
	ReasonDNSStillResolves          = "DNSStillResolves"
	ReasonNegativeTTLNotElapsed     = "NegativeTTLNotElapsed"
	ReasonDNSProbeFailed            = "DNSProbeFailed"
	ReasonPolicyChanged             = "PolicyChanged"
	ReasonWithdrawalVerified        = "WithdrawalVerified"
	ReasonOperatorAuthorizedRelease = "OperatorAuthorizedRelease"

	DefaultTTL        uint32 = 60
	DefaultSOARefresh uint32 = 3600
	DefaultSOARetry   uint32 = 900
	DefaultSOAExpire  uint32 = 1209600
	DefaultSOAMinimum uint32 = 300
)

// HostedZoneClassParameters is an inline payload, not a Kubernetes resource.
// The Provider declaration validates it. Omitted numeric fields use the defaults
// above; an omitted SOA RName is rendered as hostmaster.<domainName>.
type HostedZoneClassParameters struct {
	NameServers  []NameServer       `json:"nameServers"`
	SOA          SOAParameters      `json:"soa,omitempty"`
	DefaultTTL   uint32             `json:"defaultTTL,omitempty"`
	Verification VerificationPolicy `json:"verification,omitempty"`
}

// NameServer declares an authoritative name and its canonical IP glue addresses.
// +kubebuilder:validation:XValidation:rule="!isIP(self.name)",message="name must be a DNS name, not an IP address"
// +kubebuilder:validation:XValidation:rule="self.addresses.all(a, isIP(a) && ip.isCanonical(a))",message="addresses must contain canonical IP addresses"
type NameServer struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`
	Name string `json:"name"`

	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:items:MaxLength=45
	Addresses []string `json:"addresses"`
}

// SOAParameters is the class-level SOA policy; MName is nameServers[0].name.
type SOAParameters struct {
	RName   string `json:"rname,omitempty"`
	Refresh uint32 `json:"refresh,omitempty"`
	Retry   uint32 `json:"retry,omitempty"`
	Expire  uint32 `json:"expire,omitempty"`
	Minimum uint32 `json:"minimum,omitempty"`
}

// VerificationPolicy selects independent canonical IP:port endpoints. Empty
// Views selects the hosted server Service; delegation uses its resolver policy.
type VerificationPolicy struct {
	Views      []string         `json:"views,omitempty"`
	Delegation DelegationPolicy `json:"delegation,omitempty"`
}

// DelegationPolicy selects the resolver view for parent NS observation.
type DelegationPolicy struct {
	Resolver ResolverPolicy `json:"resolver,omitempty"`
}

// ResolverPolicy uses pod DNS in Cluster mode (the default). Recursive mode
// requires one to three canonical IP:port servers; Cluster leaves servers empty.
type ResolverPolicy struct {
	Mode    string   `json:"mode,omitempty"`
	Servers []string `json:"servers,omitempty"`
}

// HostedZone is the complete authoritative serving snapshot of one ZoneUnit.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Domain",type=string,JSONPath=`.spec.domainName`
// +kubebuilder:printcolumn:name="Serial",type=integer,JSONPath=`.spec.serial`
// +kubebuilder:printcolumn:name="Servers",type=string,JSONPath=`.status.servers[*].endpoint`
type HostedZone struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HostedZoneSpec   `json:"spec"`
	Status HostedZoneStatus `json:"status,omitempty"`
}

// HostedZoneSpec is written only by the hosted controller.
// +kubebuilder:validation:XValidation:rule="self.serial > 0",message="serial must be greater than zero"
type HostedZoneSpec struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`
	DomainName string `json:"domainName"`

	// Serial increases on each content change.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4294967295
	// +kubebuilder:validation:Format=int64
	Serial uint32 `json:"serial"`

	SOA SOA `json:"soa"`

	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=8
	NameServers []NameServer `json:"nameServers"`

	// Records includes apex NS and glue as well as accepted record claims.
	// +kubebuilder:validation:MaxItems=10000
	// +listType=map
	// +listMapKey=name
	// +listMapKey=type
	Records []HostedZoneRecord `json:"records"`

	// +kubebuilder:validation:Pattern=`^sha256:[0-9a-f]{64}$`
	ContentDigest string `json:"contentDigest"`
}

// SOA is fully rendered and contains no implicit defaults.
type SOA struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`
	MName string `json:"mname"`

	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`
	RName string `json:"rname"`

	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=2419200
	Refresh uint32 `json:"refresh"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=2419200
	Retry uint32 `json:"retry"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=2419200
	Expire uint32 `json:"expire"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=2419200
	Minimum uint32 `json:"minimum"`
}

// HostedZoneRecord is one complete canonical presentation-format RRset.
type HostedZoneRecord struct {
	// Name is @ or a relative name; a wildcard is allowed only as the first label.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^(@|\*|((\*\.)?[a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?(\.[a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?)*))$`
	Name string `json:"name"`

	// +kubebuilder:validation:Enum=A;AAAA;CNAME;TXT;MX;CAA;NS
	Type dnsv1alpha1.RecordType `json:"type"`

	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=2147483647
	TTL uint32 `json:"ttl"`

	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=64
	Values []string `json:"values"`
}

type HostedZoneStatus struct {
	// Servers is independently applied by servers using hosted-server/<pod>.
	// +optional
	// +listType=map
	// +listMapKey=endpoint
	Servers []HostedZoneServerStatus `json:"servers,omitempty"`
}

// HostedZoneServerStatus acknowledges the exact content loaded by one server.
type HostedZoneServerStatus struct {
	// Endpoint is the server pod identity, namespace/name, not an IP endpoint.
	// +kubebuilder:validation:MinLength=1
	Endpoint string `json:"endpoint"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4294967295
	// +kubebuilder:validation:Format=int64
	Serial uint32 `json:"serial"`
	// +kubebuilder:validation:Pattern=`^sha256:[0-9a-f]{64}$`
	ContentDigest string      `json:"contentDigest"`
	ObservedAt    metav1.Time `json:"observedAt"`
}

// HostedZoneList contains a list of HostedZone snapshots.
// +kubebuilder:object:root=true
type HostedZoneList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HostedZone `json:"items"`
}
