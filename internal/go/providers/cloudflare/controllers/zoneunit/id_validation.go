package cloudflare

import (
	"fmt"
	"regexp"

	cloudflarev1alpha1 "github.com/appthrust/dns-api/pkg/go/api/cloudflare/v1alpha1"
)

var cloudflareIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func validateCloudflareID(id, path string) error {
	if !cloudflareIDPattern.MatchString(id) {
		return fmt.Errorf("%s must be a 32-character lowercase hexadecimal Cloudflare ID", path)
	}
	return nil
}

func validateCloudflareUniqueIDs(ids []string, path string) error {
	seen := make(map[string]struct{}, len(ids))
	for index, id := range ids {
		itemPath := fmt.Sprintf("%s[%d]", path, index)
		if err := validateCloudflareID(id, itemPath); err != nil {
			return err
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("%s must not contain duplicate Cloudflare IDs", path)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func validateCloudflareDNSRecordIDs(records []CloudflareDNSRecord, path string) error {
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.ID)
	}
	return validateCloudflareUniqueIDs(ids, path)
}

func validateCloudflareRecordStatusIDs(records []cloudflarev1alpha1.CloudflareDNSRecordStatus, path string) error {
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.ID)
	}
	return validateCloudflareUniqueIDs(ids, path)
}
