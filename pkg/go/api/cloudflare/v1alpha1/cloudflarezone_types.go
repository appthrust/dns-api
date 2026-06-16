package v1alpha1

import dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"

type ZoneCreationPolicy string

const (
	ZoneCreationPolicyCreate ZoneCreationPolicy = "Create"
	ZoneCreationPolicyDeny   ZoneCreationPolicy = "Deny"
)

type ZoneDeletionPolicy string

const (
	ZoneDeletionPolicyDelete ZoneDeletionPolicy = "Delete"
	ZoneDeletionPolicyRetain ZoneDeletionPolicy = "Retain"
)

const (
	ProviderName    = "cloudflare.dns.appthrust.io"
	ProviderVersion = "v1alpha1"
)

var ProviderRef = dnsv1alpha1.ProviderReference{Name: ProviderName, Version: ProviderVersion}

type CloudflareZoneClassParameters struct {
	// ZoneCreationPolicy controls whether the controller may create Cloudflare zones.
	ZoneCreationPolicy ZoneCreationPolicy `json:"zoneCreationPolicy,omitempty"`

	// ZoneDeletionPolicy controls whether deleting a Zone deletes the Cloudflare zone.
	ZoneDeletionPolicy ZoneDeletionPolicy `json:"zoneDeletionPolicy,omitempty"`
}

type CloudflareZoneAdoption struct {
	// ZoneID is the Cloudflare zone ID.
	ZoneID string `json:"zoneID"`
}

type CloudflareZoneStatusData struct {
	// Zone is the observed Cloudflare zone metadata required for reconciliation.
	Zone CloudflareZoneStatus `json:"zone,omitempty"`
}

type CloudflareZoneStatus struct {
	// ID is the Cloudflare zone ID.
	ID string `json:"id,omitempty"`

	// Status is the Cloudflare zone status.
	Status string `json:"status,omitempty"`

	// Type is the Cloudflare zone type.
	Type string `json:"type,omitempty"`
}

type CloudflareZoneState struct {
	// CreationIntent records the local intent persisted before creating a Cloudflare zone.
	CreationIntent *CloudflareZoneCreationIntent `json:"creationIntent,omitempty"`
}

type CloudflareZoneCreationIntent struct {
	AccountID string `json:"accountID,omitempty"`
	Name      string `json:"name,omitempty"`
}

type CloudflareRecordSetTTLMode string

const CloudflareRecordSetTTLModeAuto CloudflareRecordSetTTLMode = "Auto"

type CloudflareRecordSetOptions struct {
	// TTL selects Cloudflare automatic TTL when set to Auto.
	// +optional
	TTL CloudflareRecordSetTTLMode `json:"ttl,omitempty"`

	// Proxied controls Cloudflare proxying for A, AAAA, and CNAME records.
	// +optional
	Proxied *bool `json:"proxied,omitempty"`

	// Comment is copied to each Cloudflare DNS record.
	// +optional
	Comment string `json:"comment,omitempty"`

	// Tags are copied to each Cloudflare DNS record as name:value strings.
	// +optional
	Tags []string `json:"tags,omitempty"`
}

type CloudflareRecordSetAdoption struct {
	// RecordIDs is the complete unordered set of Cloudflare DNS record IDs.
	RecordIDs []string `json:"recordIDs"`
}

type CloudflareRecordSetStatusData struct {
	// Records are the Cloudflare DNS record IDs managed by this RecordSet.
	Records []CloudflareDNSRecordStatus `json:"records,omitempty"`
}

type CloudflareDNSRecordStatus struct {
	// ID is the Cloudflare DNS record ID.
	ID string `json:"id"`
}

type CloudflareRecordSetState struct {
	// Records are provider-internal observed Cloudflare DNS record details.
	Records []CloudflareDNSRecordState `json:"records,omitempty"`
}

type CloudflareDNSRecordState struct {
	ID        string   `json:"id"`
	Type      string   `json:"type,omitempty"`
	Name      string   `json:"name,omitempty"`
	Content   string   `json:"content,omitempty"`
	Priority  *int32   `json:"priority,omitempty"`
	TTL       *int32   `json:"ttl,omitempty"`
	Proxied   *bool    `json:"proxied,omitempty"`
	Proxiable *bool    `json:"proxiable,omitempty"`
	Comment   string   `json:"comment,omitempty"`
	Tags      []string `json:"tags,omitempty"`
}
