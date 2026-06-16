package cloudflare

import (
	"encoding/json"
	"errors"

	cloudflarev1alpha1 "github.com/appthrust/dns-api/pkg/go/api/cloudflare/v1alpha1"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
)

func cloudflareZoneClassParameters(zoneClass *dnsv1alpha1.ZoneClass) (*cloudflarev1alpha1.CloudflareZoneClassParameters, error) {
	if len(zoneClass.Spec.Parameters.Raw) == 0 {
		return nil, errors.New("parameters must be an object")
	}
	var params cloudflarev1alpha1.CloudflareZoneClassParameters
	if err := json.Unmarshal(zoneClass.Spec.Parameters.Raw, &params); err != nil {
		return nil, err
	}
	return &params, nil
}

func zoneCreationPolicy(params *cloudflarev1alpha1.CloudflareZoneClassParameters) cloudflarev1alpha1.ZoneCreationPolicy {
	if params == nil || params.ZoneCreationPolicy == "" {
		return cloudflarev1alpha1.ZoneCreationPolicyCreate
	}
	return params.ZoneCreationPolicy
}

func zoneDeletionPolicy(params *cloudflarev1alpha1.CloudflareZoneClassParameters) cloudflarev1alpha1.ZoneDeletionPolicy {
	if params == nil || params.ZoneDeletionPolicy == "" {
		return cloudflarev1alpha1.ZoneDeletionPolicyRetain
	}
	return params.ZoneDeletionPolicy
}
