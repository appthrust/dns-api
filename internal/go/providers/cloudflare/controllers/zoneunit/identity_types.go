package cloudflare

import cloudflarev1alpha1 "github.com/appthrust/dns-api/pkg/go/api/cloudflare/v1alpha1"

type IdentityResolution struct {
	Account     cloudflarev1alpha1.CloudflareAccountStatus
	AccessToken cloudflarev1alpha1.CloudflareAccessTokenStatus
}

type identityReasonError struct {
	reason  string
	message string
}

func (e *identityReasonError) Error() string {
	return e.message
}
