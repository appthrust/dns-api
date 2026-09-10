package cloudflare

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	cloudflarev1alpha1 "github.com/appthrust/dns-api/pkg/go/api/cloudflare/v1alpha1"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
)

var cloudflareTagPattern = regexp.MustCompile(`^([A-Za-z0-9_-]{1,32}):(.*)$`)

func cloudflareRecordSetOptions(recordSet *dnsv1alpha1.RecordSet) (cloudflarev1alpha1.CloudflareRecordSetOptions, error) {
	if len(recordSet.Spec.Options.Raw) == 0 {
		return cloudflarev1alpha1.CloudflareRecordSetOptions{}, nil
	}
	var options cloudflarev1alpha1.CloudflareRecordSetOptions
	if err := json.Unmarshal(recordSet.Spec.Options.Raw, &options); err != nil {
		return cloudflarev1alpha1.CloudflareRecordSetOptions{}, fmt.Errorf("options must match Cloudflare RecordSet schema: %w", err)
	}
	return options, nil
}

func validateCloudflareRecordSetOptions(recordSet *dnsv1alpha1.RecordSet, options cloudflarev1alpha1.CloudflareRecordSetOptions) string {
	if options.TTL != "" && options.TTL != cloudflarev1alpha1.CloudflareRecordSetTTLModeAuto {
		return "options.ttl must be Auto"
	}
	if options.TTL != "" && recordSet.Spec.TTL != nil {
		return "ttl must not be specified when cloudflare automatic ttl is used"
	}
	if options.TTL != cloudflarev1alpha1.CloudflareRecordSetTTLModeAuto {
		if recordSet.Spec.TTL == nil {
			return "ttl is required for Cloudflare records unless options.ttl is Auto"
		}
		if !cloudflareFixedTTLAllowed(*recordSet.Spec.TTL) {
			return "cloudflare fixed ttl must be between 60 and 86400 seconds"
		}
	}
	if options.Proxied != nil && *options.Proxied {
		if recordSet.Spec.Type != dnsv1alpha1.RecordTypeA && recordSet.Spec.Type != dnsv1alpha1.RecordTypeAAAA && recordSet.Spec.Type != dnsv1alpha1.RecordTypeCNAME {
			return "cloudflare proxied is supported only for A, AAAA, and CNAME records"
		}
		if options.TTL != cloudflarev1alpha1.CloudflareRecordSetTTLModeAuto || recordSet.Spec.TTL != nil {
			return "cloudflare proxied records must use automatic ttl"
		}
	}
	for _, tag := range options.Tags {
		matches := cloudflareTagPattern.FindStringSubmatch(tag)
		if matches == nil || strings.ContainsAny(matches[2], "\r\n") || len(matches[2]) > 100 {
			return "cloudflare tags must use name:value format"
		}
		if strings.HasPrefix(strings.ToLower(matches[1]), "cf-") {
			return "cloudflare tag names starting with cf- are reserved"
		}
	}
	seenTags := map[string]struct{}{}
	for _, tag := range options.Tags {
		matches := cloudflareTagPattern.FindStringSubmatch(tag)
		if matches == nil {
			continue
		}
		key := strings.ToLower(matches[1]) + ":" + matches[2]
		if _, ok := seenTags[key]; ok {
			return "cloudflare tags must not contain duplicate name:value pairs"
		}
		seenTags[key] = struct{}{}
	}
	return ""
}

func desiredCloudflareDNSRecords(recordSet *dnsv1alpha1.RecordSet, fullName string, options cloudflarev1alpha1.CloudflareRecordSetOptions) ([]CloudflareDNSRecord, error) {
	ttl := int32(1)
	if options.TTL != cloudflarev1alpha1.CloudflareRecordSetTTLModeAuto {
		if recordSet.Spec.TTL == nil {
			return nil, errors.New("ttl is required for Cloudflare records unless options.ttl is Auto")
		}
		if !cloudflareFixedTTLAllowed(*recordSet.Spec.TTL) {
			return nil, errors.New("cloudflare fixed ttl must be between 60 and 86400 seconds")
		}
		ttl = *recordSet.Spec.TTL
	}
	base := CloudflareDNSRecord{
		Type:    string(recordSet.Spec.Type),
		Name:    fullName,
		TTL:     &ttl,
		Comment: options.Comment,
		Tags:    slices.Clone(options.Tags),
	}
	if recordSet.Spec.Type == dnsv1alpha1.RecordTypeA || recordSet.Spec.Type == dnsv1alpha1.RecordTypeAAAA || recordSet.Spec.Type == dnsv1alpha1.RecordTypeCNAME {
		proxied := false
		if options.Proxied != nil {
			proxied = *options.Proxied
		}
		base.Proxied = &proxied
	}
	var records []CloudflareDNSRecord
	switch recordSet.Spec.Type {
	case dnsv1alpha1.RecordTypeA:
		for _, address := range recordSet.Spec.A.Addresses {
			record := base
			record.Content = address
			records = append(records, record)
		}
	case dnsv1alpha1.RecordTypeAAAA:
		for _, address := range recordSet.Spec.AAAA.Addresses {
			record := base
			record.Content = address
			records = append(records, record)
		}
	case dnsv1alpha1.RecordTypeTXT:
		for _, value := range recordSet.Spec.TXT.Values {
			record := base
			record.Content = value
			records = append(records, record)
		}
	case dnsv1alpha1.RecordTypeCNAME:
		record := base
		record.Content = recordSet.Spec.CNAME.Target
		records = append(records, record)
	case dnsv1alpha1.RecordTypeMX:
		for _, item := range recordSet.Spec.MX.Records {
			record := base
			record.Content = item.Exchange
			record.Priority = &item.Preference
			records = append(records, record)
		}
	case dnsv1alpha1.RecordTypeCAA:
		for _, item := range recordSet.Spec.CAA.Records {
			record := base
			record.Content = item.Value
			record.CAA = &CloudflareCAAData{Flags: item.Flags, Tag: item.Tag, Value: item.Value}
			records = append(records, record)
		}
	case dnsv1alpha1.RecordTypeNS:
		for _, nameServer := range recordSet.Spec.NS.NameServers {
			record := base
			record.Content = nameServer
			records = append(records, record)
		}
	default:
		return nil, errors.New("Cloudflare supports A, AAAA, TXT, CNAME, MX, CAA, and delegated NS records")
	}
	slices.SortFunc(records, compareCloudflareDNSRecord)
	return records, nil
}

func cloudflareFixedTTLAllowed(ttl int32) bool {
	return ttl >= cloudflareFixedTTLMin && ttl <= cloudflareFixedTTLMax
}

func cloudflareDNSRecordSetEqual(a, b []CloudflareDNSRecord) bool {
	left := slices.Clone(a)
	right := slices.Clone(b)
	slices.SortFunc(left, compareCloudflareDNSRecord)
	slices.SortFunc(right, compareCloudflareDNSRecord)
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !cloudflareDNSRecordEqual(left[index], right[index]) {
			return false
		}
	}
	return true
}

func cloudflareDNSRecordEqual(a, b CloudflareDNSRecord) bool {
	return a.Type == b.Type &&
		normalizeCloudflareName(a.Name) == normalizeCloudflareName(b.Name) &&
		(a.Type == string(dnsv1alpha1.RecordTypeCAA) || a.Content == b.Content) &&
		int32PtrValue(a.Priority) == int32PtrValue(b.Priority) &&
		int32PtrValue(a.TTL) == int32PtrValue(b.TTL) &&
		boolPtrValue(a.Proxied) == boolPtrValue(b.Proxied) &&
		a.Comment == b.Comment &&
		slices.Equal(cloudflareSortedTags(a.Tags), cloudflareSortedTags(b.Tags)) &&
		cloudflareCAAEqual(a.CAA, b.CAA)
}

func compareCloudflareDNSRecord(a, b CloudflareDNSRecord) int {
	for _, pair := range [][2]string{
		{a.Type, b.Type},
		{normalizeCloudflareName(a.Name), normalizeCloudflareName(b.Name)},
		{a.Content, b.Content},
		{fmt.Sprintf("%05d", int32PtrValue(a.Priority)), fmt.Sprintf("%05d", int32PtrValue(b.Priority))},
		{cloudflareCAAKey(a.CAA), cloudflareCAAKey(b.CAA)},
	} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	return 0
}

func cloudflareCAAEqual(a, b *CloudflareCAAData) bool {
	return cloudflareCAAKey(a) == cloudflareCAAKey(b)
}

func cloudflareCAAKey(value *CloudflareCAAData) string {
	if value == nil {
		return ""
	}
	return fmt.Sprintf("%d\x00%s\x00%s", value.Flags, value.Tag, value.Value)
}

func cloudflareSortedTags(tags []string) []string {
	out := slices.Clone(tags)
	slices.Sort(out)
	return out
}

func cloudflareSameNameConflict(recordType dnsv1alpha1.RecordType, records []CloudflareDNSRecord) string {
	for _, record := range records {
		currentType := dnsv1alpha1.RecordType(record.Type)
		if currentType == recordType {
			continue
		}
		if recordType == dnsv1alpha1.RecordTypeCNAME || currentType == dnsv1alpha1.RecordTypeCNAME {
			return "Cloudflare DNS record conflicts with same-name CNAME exclusivity"
		}
		if recordType == dnsv1alpha1.RecordTypeNS || currentType == dnsv1alpha1.RecordTypeNS {
			return "Cloudflare DNS record conflicts with same-name delegated NS exclusivity"
		}
	}
	return ""
}

func cloudflareRecordsByType(records []CloudflareDNSRecord, recordType string) []CloudflareDNSRecord {
	var out []CloudflareDNSRecord
	for _, record := range records {
		if record.Type == recordType {
			out = append(out, record)
		}
	}
	return out
}

func cloudflareRecordsByIDs(records []CloudflareDNSRecord, ids map[string]struct{}) ([]CloudflareDNSRecord, []string) {
	var out []CloudflareDNSRecord
	found := make(map[string]struct{}, len(ids))
	for _, record := range records {
		if _, ok := ids[record.ID]; ok {
			out = append(out, record)
			found[record.ID] = struct{}{}
		}
	}
	var missing []string
	for id := range ids {
		if _, ok := found[id]; !ok {
			missing = append(missing, id)
		}
	}
	return out, missing
}

func cloudflareRecordsExcludingIDs(records []CloudflareDNSRecord, ids map[string]struct{}) []CloudflareDNSRecord {
	var out []CloudflareDNSRecord
	for _, record := range records {
		if _, ok := ids[record.ID]; !ok {
			out = append(out, record)
		}
	}
	return out
}

func cloudflareStatusRecordIDs(recordSet *dnsv1alpha1.RecordSet) map[string]struct{} {
	statusData, err := cloudflareRecordSetStatusData(recordSet)
	if err != nil {
		return nil
	}
	out := make(map[string]struct{}, len(statusData.Records))
	for _, record := range statusData.Records {
		if record.ID != "" {
			out[record.ID] = struct{}{}
		}
	}
	return out
}

func cloudflareDNSRecordStatuses(records []CloudflareDNSRecord) []cloudflarev1alpha1.CloudflareDNSRecordStatus {
	statuses := make([]cloudflarev1alpha1.CloudflareDNSRecordStatus, 0, len(records))
	for _, record := range records {
		statuses = append(statuses, cloudflareDNSRecordStatus(record))
	}
	return statuses
}

func cloudflareDNSRecordStatus(record CloudflareDNSRecord) cloudflarev1alpha1.CloudflareDNSRecordStatus {
	return cloudflarev1alpha1.CloudflareDNSRecordStatus{
		ID: record.ID,
	}
}

func cloudflareDNSRecordStates(records []CloudflareDNSRecord) []cloudflarev1alpha1.CloudflareDNSRecordState {
	states := make([]cloudflarev1alpha1.CloudflareDNSRecordState, 0, len(records))
	for _, record := range records {
		states = append(states, cloudflareDNSRecordState(record))
	}
	return states
}

func cloudflareDNSRecordState(record CloudflareDNSRecord) cloudflarev1alpha1.CloudflareDNSRecordState {
	return cloudflarev1alpha1.CloudflareDNSRecordState{
		ID:        record.ID,
		Type:      record.Type,
		Name:      record.Name,
		Content:   record.Content,
		Priority:  record.Priority,
		TTL:       record.TTL,
		Proxied:   record.Proxied,
		Proxiable: record.Proxiable,
		Comment:   record.Comment,
		Tags:      slices.Clone(record.Tags),
	}
}

func cloudflareRecordStatusIDs(records []cloudflarev1alpha1.CloudflareDNSRecordStatus) []string {
	ids := make([]string, 0, len(records))
	for _, record := range records {
		if record.ID != "" {
			ids = append(ids, record.ID)
		}
	}
	return ids
}

func cloudflareRecordStatusIDsMatchAdoption(statuses []cloudflarev1alpha1.CloudflareDNSRecordStatus, adoptionIDs []string) bool {
	statusIDs := cloudflareRecordStatusIDs(statuses)
	if len(statusIDs) != len(adoptionIDs) {
		return false
	}
	expected := make(map[string]struct{}, len(adoptionIDs))
	for _, id := range adoptionIDs {
		expected[id] = struct{}{}
	}
	for _, id := range statusIDs {
		if _, ok := expected[id]; !ok {
			return false
		}
	}
	return true
}

func cloudflareRecordMatchesDeletingRecordSet(record CloudflareDNSRecord, recordSet *dnsv1alpha1.RecordSet, fullName string) bool {
	return record.Type == string(recordSet.Spec.Type) &&
		normalizeCloudflareName(record.Name) == normalizeCloudflareName(fullName)
}

func cloudflareRecordSetStatusData(recordSet *dnsv1alpha1.RecordSet) (cloudflarev1alpha1.CloudflareRecordSetStatusData, error) {
	return cloudflareRecordSetStatusDataFromProvider(recordSet.Status.Provider)
}

func cloudflareRecordSetStatusDataFromProvider(provider *dnsv1alpha1.ProviderStatus) (cloudflarev1alpha1.CloudflareRecordSetStatusData, error) {
	if provider == nil || len(provider.Data.Raw) == 0 {
		return cloudflarev1alpha1.CloudflareRecordSetStatusData{}, nil
	}
	var data cloudflarev1alpha1.CloudflareRecordSetStatusData
	if err := json.Unmarshal(provider.Data.Raw, &data); err != nil {
		return cloudflarev1alpha1.CloudflareRecordSetStatusData{}, fmt.Errorf("RecordSet status.provider.data must match Cloudflare schema: %w", err)
	}
	if len(data.Records) > 0 {
		if err := validateCloudflareRecordStatusIDs(data.Records, "RecordSet status.provider.data.records.id"); err != nil {
			return cloudflarev1alpha1.CloudflareRecordSetStatusData{}, err
		}
	}
	return data, nil
}

func cloudflareRecordSetStateFromProvider(provider *dnsv1alpha1.ProviderStatus) (cloudflarev1alpha1.CloudflareRecordSetState, error) {
	if provider == nil || len(provider.State.Raw) == 0 {
		return cloudflarev1alpha1.CloudflareRecordSetState{}, nil
	}
	var state cloudflarev1alpha1.CloudflareRecordSetState
	if err := json.Unmarshal(provider.State.Raw, &state); err != nil {
		return cloudflarev1alpha1.CloudflareRecordSetState{}, fmt.Errorf("RecordSet status.provider.state must match Cloudflare schema: %w", err)
	}
	return state, nil
}

func cloudflareRecordSetAdoptionRef(recordSet *dnsv1alpha1.RecordSet) (cloudflarev1alpha1.CloudflareRecordSetAdoption, bool, error) {
	if len(recordSet.Spec.Adoption.Raw) == 0 {
		return cloudflarev1alpha1.CloudflareRecordSetAdoption{}, false, nil
	}
	var externalRef cloudflarev1alpha1.CloudflareRecordSetAdoption
	if err := json.Unmarshal(recordSet.Spec.Adoption.Raw, &externalRef); err != nil {
		return cloudflarev1alpha1.CloudflareRecordSetAdoption{}, true, fmt.Errorf("adoption must be an object with recordIDs: %w", err)
	}
	if len(externalRef.RecordIDs) == 0 {
		return cloudflarev1alpha1.CloudflareRecordSetAdoption{}, true, errors.New("adoption.recordIDs must not be empty")
	}
	if err := validateCloudflareUniqueIDs(externalRef.RecordIDs, "adoption.recordIDs"); err != nil {
		return cloudflarev1alpha1.CloudflareRecordSetAdoption{}, true, err
	}
	return externalRef, true, nil
}

func cloudflareFullRecordName(ownerName, domainName string) string {
	switch ownerName {
	case "@":
		return domainName
	case "*":
		return "*." + domainName
	default:
		return ownerName + "." + domainName
	}
}

func cloudflareZoneUnitOwnsRecordSet(recordSet *dnsv1alpha1.RecordSet, unit *dnsv1alpha1.ZoneUnit) (bool, bool) {
	if unit == nil {
		return false, false
	}
	refKey := cloudflareRecordSetClaimKey(recordSet.Namespace, recordSet.Name)
	for _, item := range unit.Spec.RecordSets {
		if cloudflareRecordSetClaimKey(item.RecordSetNamespace, item.RecordSetName) == refKey {
			if item.RecordSetUID != "" &&
				item.RecordSetUID == recordSet.UID &&
				item.Name == recordSet.Spec.Name &&
				item.Type == recordSet.Spec.Type {
				return true, false
			}
			return false, true
		}
	}
	for _, item := range unit.Spec.RecordSets {
		if item.Name != recordSet.Spec.Name {
			continue
		}
		if item.Type == recordSet.Spec.Type || item.Type == dnsv1alpha1.RecordTypeCNAME || recordSet.Spec.Type == dnsv1alpha1.RecordTypeCNAME {
			return false, true
		}
	}
	return false, false
}

func cloudflareRecordSetClaimKey(namespace, name string) string {
	return namespace + "\x00" + name
}

func normalizeCloudflareName(value string) string {
	return strings.TrimSuffix(value, ".")
}

func recordSetZoneKey(recordSet *dnsv1alpha1.RecordSet) (string, string) {
	namespace := recordSet.Namespace
	if recordSet.Spec.ZoneRef.Namespace != nil && *recordSet.Spec.ZoneRef.Namespace != "" {
		namespace = *recordSet.Spec.ZoneRef.Namespace
	}
	return namespace, recordSet.Spec.ZoneRef.Name
}

func fullRecordNamePatternMatch(pattern, recordName string) (bool, error) {
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return false, err
	}
	match := compiled.FindStringIndex(recordName)
	return match != nil && match[0] == 0 && match[1] == len(recordName), nil
}

func int32PtrValue(value *int32) int32 {
	if value == nil {
		return 0
	}
	return *value
}

func boolPtrValue(value *bool) bool {
	return value != nil && *value
}
