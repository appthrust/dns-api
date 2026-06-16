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

type SameNameZonePolicy string

const (
	SameNameZonePolicyAllow SameNameZonePolicy = "Allow"
	SameNameZonePolicyDeny  SameNameZonePolicy = "Deny"
)

const (
	ProviderName    = "route53.dns.appthrust.io"
	ProviderVersion = "v1alpha1"
)

var ProviderRef = dnsv1alpha1.ProviderReference{Name: ProviderName, Version: ProviderVersion}

type Route53ZoneClassParameters struct {
	// ZoneCreationPolicy controls whether the controller may create hosted zones.
	ZoneCreationPolicy ZoneCreationPolicy `json:"zoneCreationPolicy,omitempty"`

	// ZoneDeletionPolicy controls whether deleting a Zone deletes the hosted zone.
	ZoneDeletionPolicy ZoneDeletionPolicy `json:"zoneDeletionPolicy,omitempty"`

	// SameNameZonePolicy controls whether multiple hosted zones may share a domain name.
	SameNameZonePolicy SameNameZonePolicy `json:"sameNameZonePolicy,omitempty"`

	// Tags are applied to Route 53 hosted zones created by this class.
	Tags map[string]string `json:"tags,omitempty"`
}
