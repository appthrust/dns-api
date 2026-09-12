package route53

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
)

type route53RecordSetIdentity struct {
	recordType dnsv1alpha1.RecordType
	recordName string
}

func recordSetIdentity(recordType dnsv1alpha1.RecordType, recordName string) route53RecordSetIdentity {
	return route53RecordSetIdentity{
		recordType: recordType,
		recordName: normalizeRoute53RecordName(recordName),
	}
}

func (i route53RecordSetIdentity) key() string {
	return string(i.recordType) + "\x00" + i.recordName
}

func indexRecordSets(recordSets []RecordSetResource) map[string]RecordSetResource {
	index := make(map[string]RecordSetResource, len(recordSets))
	for _, recordSet := range recordSets {
		identity := recordSetIdentity(recordSet.Type, recordSet.Name)
		index[identity.key()] = recordSet
	}
	return index
}

func desiredRoute53RecordSet(recordSet *dnsv1alpha1.RecordSet, hostedZoneID, recordName string, options route53v1alpha1.Route53RecordSetOptions) RecordSetResource {
	desired := RecordSetResource{
		HostedZoneID: normalizeHostedZoneID(hostedZoneID),
		Name:         normalizeRoute53RecordName(recordName),
		Type:         recordSet.Spec.Type,
	}
	if options.Alias != nil {
		desired.Alias = options.Alias
		return desired
	}
	ttl := int64(*recordSet.Spec.TTL)
	desired.TTL = &ttl
	switch recordSet.Spec.Type {
	case dnsv1alpha1.RecordTypeA:
		desired.Values = slices.Clone(recordSet.Spec.A.Addresses)
	case dnsv1alpha1.RecordTypeAAAA:
		desired.Values = slices.Clone(recordSet.Spec.AAAA.Addresses)
	case dnsv1alpha1.RecordTypeTXT:
		for _, value := range recordSet.Spec.TXT.Values {
			desired.Values = append(desired.Values, quoteRoute53TXTValue(value))
		}
	case dnsv1alpha1.RecordTypeCNAME:
		desired.Values = []string{normalizeRoute53RecordName(recordSet.Spec.CNAME.Target)}
	case dnsv1alpha1.RecordTypeMX:
		for _, record := range recordSet.Spec.MX.Records {
			desired.Values = append(desired.Values, formatRoute53MXValue(record))
		}
	case dnsv1alpha1.RecordTypeCAA:
		for _, record := range recordSet.Spec.CAA.Records {
			desired.Values = append(desired.Values, formatRoute53CAAValue(record))
		}
	case dnsv1alpha1.RecordTypeNS:
		for _, nameServer := range recordSet.Spec.NS.NameServers {
			desired.Values = append(desired.Values, normalizeRoute53RecordName(nameServer))
		}
	}
	slices.Sort(desired.Values)
	return desired
}

func route53RecordSetEqual(a, b RecordSetResource) bool {
	if normalizeHostedZoneID(a.HostedZoneID) != normalizeHostedZoneID(b.HostedZoneID) ||
		normalizeRoute53RecordName(a.Name) != normalizeRoute53RecordName(b.Name) ||
		a.Type != b.Type {
		return false
	}
	if (a.TTL == nil) != (b.TTL == nil) {
		return false
	}
	if a.TTL != nil && b.TTL != nil && *a.TTL != *b.TTL {
		return false
	}
	if (a.Alias == nil) != (b.Alias == nil) {
		return false
	}
	if a.Alias != nil && b.Alias != nil {
		if normalizeRoute53AliasDNSNameForCompare(a.Alias.DNSName) != normalizeRoute53AliasDNSNameForCompare(b.Alias.DNSName) ||
			normalizeHostedZoneID(a.Alias.HostedZoneID) != normalizeHostedZoneID(b.Alias.HostedZoneID) ||
			a.Alias.EvaluateTargetHealth != b.Alias.EvaluateTargetHealth {
			return false
		}
	}
	aValues, ok := canonicalRecordSetValues(a.Type, a.Values)
	if !ok {
		return false
	}
	bValues, ok := canonicalRecordSetValues(b.Type, b.Values)
	if !ok {
		return false
	}
	slices.Sort(aValues)
	slices.Sort(bValues)
	return slices.Equal(aValues, bValues)
}

func normalizeRoute53AliasDNSNameForCompare(name string) string {
	normalized := normalizeRoute53RecordName(name)
	withoutDualstack := strings.TrimPrefix(normalized, "dualstack.")
	if withoutDualstack != normalized && isRoute53ELBAliasDNSName(withoutDualstack) {
		return withoutDualstack
	}
	return normalized
}

func isRoute53ELBAliasDNSName(name string) bool {
	trimmed := strings.TrimSuffix(name, ".")
	return strings.HasSuffix(trimmed, ".elb.amazonaws.com") ||
		strings.Contains(trimmed, ".elb.") && strings.HasSuffix(trimmed, ".amazonaws.com") ||
		strings.HasSuffix(trimmed, ".elb.amazonaws.com.cn") ||
		strings.Contains(trimmed, ".elb.") && strings.HasSuffix(trimmed, ".amazonaws.com.cn")
}

func route53RecordSetOptions(recordSet *dnsv1alpha1.RecordSet) (route53v1alpha1.Route53RecordSetOptions, error) {
	if len(recordSet.Spec.Options.Raw) == 0 {
		return route53v1alpha1.Route53RecordSetOptions{}, nil
	}
	var options route53v1alpha1.Route53RecordSetOptions
	if err := json.Unmarshal(recordSet.Spec.Options.Raw, &options); err != nil {
		return route53v1alpha1.Route53RecordSetOptions{}, fmt.Errorf("options must match Route 53 RecordSet schema: %w", err)
	}
	return options, nil
}

func validateRoute53RecordSetBody(recordSet *dnsv1alpha1.RecordSet, options route53v1alpha1.Route53RecordSetOptions) string {
	if options.Alias != nil {
		if recordSet.Spec.Type != dnsv1alpha1.RecordTypeA && recordSet.Spec.Type != dnsv1alpha1.RecordTypeAAAA {
			return "Route 53 alias supports only A and AAAA record types"
		}
		if recordSet.Spec.TTL != nil {
			return "ttl must not be specified when route53 alias is used"
		}
		if recordSet.Spec.A != nil && len(recordSet.Spec.A.Addresses) > 0 {
			return "a.addresses must not be specified when route53 alias is used"
		}
		if recordSet.Spec.AAAA != nil && len(recordSet.Spec.AAAA.Addresses) > 0 {
			return "aaaa.addresses must not be specified when route53 alias is used"
		}
		if recordSet.Spec.TXT != nil && len(recordSet.Spec.TXT.Values) > 0 {
			return "txt.values must not be specified when route53 alias is used"
		}
		if recordSet.Spec.CNAME != nil && recordSet.Spec.CNAME.Target != "" {
			return "cname.target must not be specified when route53 alias is used"
		}
		if recordSet.Spec.MX != nil && len(recordSet.Spec.MX.Records) > 0 {
			return "mx.records must not be specified when route53 alias is used"
		}
		if recordSet.Spec.CAA != nil && len(recordSet.Spec.CAA.Records) > 0 {
			return "caa.records must not be specified when route53 alias is used"
		}
		if recordSet.Spec.NS != nil && len(recordSet.Spec.NS.NameServers) > 0 {
			return "ns.nameServers must not be specified when route53 alias is used"
		}
		if options.Alias.DNSName == "" || options.Alias.HostedZoneID == "" {
			return "options.alias.dnsName and options.alias.hostedZoneID are required"
		}
		if err := validateAliasDNSName(options.Alias.DNSName); err != nil {
			return "options.alias.dnsName " + err.Error()
		}
		return ""
	}
	if recordSet.Spec.TTL == nil {
		return "ttl is required for standard records"
	}
	switch recordSet.Spec.Type {
	case dnsv1alpha1.RecordTypeA:
		if recordSet.Spec.A == nil || len(recordSet.Spec.A.Addresses) == 0 {
			return "a.addresses is required for standard A records"
		}
		if err := validateRecordSetIPAddresses(recordSet.Spec.A.Addresses, false); err != nil {
			return "a.addresses " + err.Error()
		}
		if recordSet.Spec.AAAA != nil {
			return "aaaa must not be specified for standard A records"
		}
		if recordSet.Spec.TXT != nil {
			return "txt must not be specified for standard A records"
		}
		if recordSet.Spec.CNAME != nil {
			return "cname must not be specified for standard A records"
		}
		if recordSet.Spec.MX != nil {
			return "mx must not be specified for standard A records"
		}
		if recordSet.Spec.CAA != nil {
			return "caa must not be specified for standard A records"
		}
		if recordSet.Spec.NS != nil {
			return "ns must not be specified for standard A records"
		}
	case dnsv1alpha1.RecordTypeAAAA:
		if recordSet.Spec.AAAA == nil || len(recordSet.Spec.AAAA.Addresses) == 0 {
			return "aaaa.addresses is required for standard AAAA records"
		}
		if err := validateRecordSetIPAddresses(recordSet.Spec.AAAA.Addresses, true); err != nil {
			return "aaaa.addresses " + err.Error()
		}
		if recordSet.Spec.A != nil {
			return "a must not be specified for standard AAAA records"
		}
		if recordSet.Spec.TXT != nil {
			return "txt must not be specified for standard AAAA records"
		}
		if recordSet.Spec.CNAME != nil {
			return "cname must not be specified for standard AAAA records"
		}
		if recordSet.Spec.MX != nil {
			return "mx must not be specified for standard AAAA records"
		}
		if recordSet.Spec.CAA != nil {
			return "caa must not be specified for standard AAAA records"
		}
		if recordSet.Spec.NS != nil {
			return "ns must not be specified for standard AAAA records"
		}
	case dnsv1alpha1.RecordTypeTXT:
		if recordSet.Spec.TXT == nil || len(recordSet.Spec.TXT.Values) == 0 {
			return "txt.values is required for standard TXT records"
		}
		if err := validateRecordSetTXTValues(recordSet.Spec.TXT.Values); err != nil {
			return "txt.values " + err.Error()
		}
		if recordSet.Spec.A != nil {
			return "a must not be specified for standard TXT records"
		}
		if recordSet.Spec.AAAA != nil {
			return "aaaa must not be specified for standard TXT records"
		}
		if recordSet.Spec.CNAME != nil {
			return "cname must not be specified for standard TXT records"
		}
		if recordSet.Spec.MX != nil {
			return "mx must not be specified for standard TXT records"
		}
		if recordSet.Spec.CAA != nil {
			return "caa must not be specified for standard TXT records"
		}
		if recordSet.Spec.NS != nil {
			return "ns must not be specified for standard TXT records"
		}
	case dnsv1alpha1.RecordTypeCNAME:
		if recordSet.Spec.CNAME == nil || recordSet.Spec.CNAME.Target == "" {
			return "cname.target is required for standard CNAME records"
		}
		if err := validateCNAMETarget(recordSet.Spec.CNAME.Target); err != nil {
			return "cname.target " + err.Error()
		}
		if recordSet.Spec.A != nil {
			return "a must not be specified for standard CNAME records"
		}
		if recordSet.Spec.AAAA != nil {
			return "aaaa must not be specified for standard CNAME records"
		}
		if recordSet.Spec.TXT != nil {
			return "txt must not be specified for standard CNAME records"
		}
		if recordSet.Spec.MX != nil {
			return "mx must not be specified for standard CNAME records"
		}
		if recordSet.Spec.CAA != nil {
			return "caa must not be specified for standard CNAME records"
		}
		if recordSet.Spec.NS != nil {
			return "ns must not be specified for standard CNAME records"
		}
	case dnsv1alpha1.RecordTypeMX:
		if recordSet.Spec.MX == nil || len(recordSet.Spec.MX.Records) == 0 {
			return "mx.records is required for standard MX records"
		}
		if err := validateRecordSetMXRecords(recordSet.Spec.MX.Records); err != nil {
			return "mx.records " + err.Error()
		}
		if recordSet.Spec.A != nil {
			return "a must not be specified for standard MX records"
		}
		if recordSet.Spec.AAAA != nil {
			return "aaaa must not be specified for standard MX records"
		}
		if recordSet.Spec.TXT != nil {
			return "txt must not be specified for standard MX records"
		}
		if recordSet.Spec.CNAME != nil {
			return "cname must not be specified for standard MX records"
		}
		if recordSet.Spec.CAA != nil {
			return "caa must not be specified for standard MX records"
		}
		if recordSet.Spec.NS != nil {
			return "ns must not be specified for standard MX records"
		}
	case dnsv1alpha1.RecordTypeCAA:
		if recordSet.Spec.CAA == nil || len(recordSet.Spec.CAA.Records) == 0 {
			return "caa.records is required for standard CAA records"
		}
		if err := validateRecordSetCAARecords(recordSet.Spec.CAA.Records); err != nil {
			return "caa.records " + err.Error()
		}
		if recordSet.Spec.A != nil {
			return "a must not be specified for standard CAA records"
		}
		if recordSet.Spec.AAAA != nil {
			return "aaaa must not be specified for standard CAA records"
		}
		if recordSet.Spec.TXT != nil {
			return "txt must not be specified for standard CAA records"
		}
		if recordSet.Spec.CNAME != nil {
			return "cname must not be specified for standard CAA records"
		}
		if recordSet.Spec.MX != nil {
			return "mx must not be specified for standard CAA records"
		}
		if recordSet.Spec.NS != nil {
			return "ns must not be specified for standard CAA records"
		}
	case dnsv1alpha1.RecordTypeNS:
		if recordSet.Spec.Name == "@" {
			return "record name @ is not allowed for delegated NS records"
		}
		if strings.HasPrefix(recordSet.Spec.Name, "*") {
			return "wildcard record names are not allowed for delegated NS records"
		}
		if recordSet.Spec.NS == nil || len(recordSet.Spec.NS.NameServers) == 0 {
			return "ns.nameServers is required for standard NS records"
		}
		if err := validateRecordSetNSNameServers(recordSet.Spec.NS.NameServers); err != nil {
			return "ns.nameServers " + err.Error()
		}
		if recordSet.Spec.A != nil {
			return "a must not be specified for standard NS records"
		}
		if recordSet.Spec.AAAA != nil {
			return "aaaa must not be specified for standard NS records"
		}
		if recordSet.Spec.TXT != nil {
			return "txt must not be specified for standard NS records"
		}
		if recordSet.Spec.CNAME != nil {
			return "cname must not be specified for standard NS records"
		}
		if recordSet.Spec.MX != nil {
			return "mx must not be specified for standard NS records"
		}
		if recordSet.Spec.CAA != nil {
			return "caa must not be specified for standard NS records"
		}
	default:
		return "only A, AAAA, TXT, CNAME, MX, CAA, and delegated NS records are supported without a Route 53 alias option"
	}
	return ""
}

type route53RecordSetAdoption struct {
	Enabled bool `json:"enabled"`
}

func route53RecordSetAdoptionEnabled(recordSet *dnsv1alpha1.RecordSet) (bool, error) {
	if len(recordSet.Spec.Adoption.Raw) == 0 {
		return false, nil
	}
	var adoption route53RecordSetAdoption
	if err := json.Unmarshal(recordSet.Spec.Adoption.Raw, &adoption); err != nil {
		return true, fmt.Errorf("adoption must be an object with enabled: %w", err)
	}
	if !adoption.Enabled {
		return true, errors.New("adoption.enabled must be true")
	}
	return true, nil
}

func route53RecordSetManagedResourceMismatch(recordSet *dnsv1alpha1.RecordSet, statusData route53v1alpha1.Route53RecordSetStatusData) (string, bool, error) {
	adopting, err := route53RecordSetAdoptionEnabled(recordSet)
	if err != nil || !adopting {
		return "", false, err
	}
	statusZoneID := normalizeHostedZoneID(statusData.HostedZoneID)
	statusRecordName := normalizeRoute53RecordName(statusData.RecordName)
	if statusZoneID == "" || statusRecordName == "" || statusData.RecordType == "" {
		return "", false, nil
	}
	return "", false, nil
}

func route53RecordSetStatusData(recordSet *dnsv1alpha1.RecordSet) (route53v1alpha1.Route53RecordSetStatusData, error) {
	return route53RecordSetStatusDataFromProvider(recordSet.Status.Provider)
}

func route53RecordSetStatusDataFromProvider(provider *dnsv1alpha1.ProviderStatus) (route53v1alpha1.Route53RecordSetStatusData, error) {
	if provider != nil && len(provider.State.Raw) > 0 {
		var data route53v1alpha1.Route53RecordSetStatusData
		if err := json.Unmarshal(provider.State.Raw, &data); err != nil {
			return route53v1alpha1.Route53RecordSetStatusData{}, fmt.Errorf("RecordSet status.provider.state must match Route 53 schema: %w", err)
		}
		return data, nil
	}
	if provider != nil && len(provider.Data.Raw) > 0 {
		var data route53v1alpha1.Route53RecordSetStatusData
		if err := json.Unmarshal(provider.Data.Raw, &data); err != nil {
			return route53v1alpha1.Route53RecordSetStatusData{}, fmt.Errorf("RecordSet status.provider.data must match Route 53 schema: %w", err)
		}
		return data, nil
	}
	return route53v1alpha1.Route53RecordSetStatusData{}, nil
}

func providerStatusHasPayload(provider *dnsv1alpha1.ProviderStatus) bool {
	return provider != nil && (len(provider.Data.Raw) > 0 || len(provider.State.Raw) > 0)
}

func setRoute53RecordSetStatusData(data *route53v1alpha1.Route53RecordSetStatusData, recordSet RecordSetResource) {
	data.HostedZoneID = normalizeHostedZoneID(recordSet.HostedZoneID)
	data.RecordName = normalizeRoute53RecordName(recordSet.Name)
	data.RecordType = string(recordSet.Type)
}

func recordSetZoneKey(recordSet *dnsv1alpha1.RecordSet) (string, string) {
	namespace := recordSet.Namespace
	if recordSet.Spec.ZoneRef.Namespace != nil && *recordSet.Spec.ZoneRef.Namespace != "" {
		namespace = *recordSet.Spec.ZoneRef.Namespace
	}
	return namespace, recordSet.Spec.ZoneRef.Name
}

type zoneUnitRecordSetOwnership struct {
	byRef      map[string]dnsv1alpha1.ZoneUnitRecordSetSpec
	byIdentity map[string]dnsv1alpha1.ZoneUnitRecordSetSpec
}

func newZoneUnitRecordSetOwnership(items []dnsv1alpha1.ZoneUnitRecordSetSpec) *zoneUnitRecordSetOwnership {
	ownership := &zoneUnitRecordSetOwnership{
		byRef:      make(map[string]dnsv1alpha1.ZoneUnitRecordSetSpec, len(items)),
		byIdentity: make(map[string]dnsv1alpha1.ZoneUnitRecordSetSpec, len(items)),
	}
	for _, item := range items {
		refKey := recordSetClaimKey(item.RecordSetNamespace, item.RecordSetName)
		ownership.byRef[refKey] = item
		ownership.byIdentity[recordIdentityKey(item.Name, item.Type)] = item
		if item.Type == dnsv1alpha1.RecordTypeCNAME {
			ownership.byIdentity[cnameExclusionKey(item.Name)] = item
		}
	}
	return ownership
}

func (o *zoneUnitRecordSetOwnership) acceptedOwner(recordSet *dnsv1alpha1.RecordSet) (bool, bool) {
	refKey := recordSetClaimKey(recordSet.Namespace, recordSet.Name)
	if item, ok := o.byRef[refKey]; ok {
		if item.Name != recordSet.Spec.Name || item.Type != recordSet.Spec.Type {
			return false, true
		}
		if item.RecordSetUID == "" || recordSet.UID == "" {
			return false, false
		}
		return item.RecordSetUID == recordSet.UID, item.RecordSetUID != recordSet.UID
	}
	for _, identityKey := range zoneUnitRecordSetIdentityKeys(recordSet.Spec.Name, recordSet.Spec.Type) {
		if owner, ok := o.byIdentity[identityKey]; ok && recordSetClaimKey(owner.RecordSetNamespace, owner.RecordSetName) != refKey {
			return false, true
		}
	}
	return false, false
}

func zoneUnitRecordSetIdentityKeys(recordName string, recordType dnsv1alpha1.RecordType) []string {
	keys := []string{recordIdentityKey(recordName, recordType)}
	keys = append(keys, cnameExclusionKey(recordName))
	return keys
}

func recordSetClaimKey(namespace, name string) string {
	return namespace + "\x00" + name
}
func zoneUnitRecordSetItemKey(item dnsv1alpha1.ZoneUnitRecordSetSpec) string {
	return recordSetClaimKey(item.RecordSetNamespace, item.RecordSetName) + "\x00" + string(item.RecordSetUID)
}

func zoneUnitRecordSetStatusKey(status dnsv1alpha1.ZoneUnitRecordSetStatus) string {
	return recordSetClaimKey(status.RecordSetNamespace, status.RecordSetName) + "\x00" + string(status.RecordSetUID)
}

func zoneUnitRecordSetStatusMatchesItem(status dnsv1alpha1.ZoneUnitRecordSetStatus, item dnsv1alpha1.ZoneUnitRecordSetSpec) bool {
	return item.RecordSetUID != "" &&
		status.RecordSetUID != "" &&
		status.RecordSetUID == item.RecordSetUID &&
		status.RecordSetNamespace == item.RecordSetNamespace &&
		status.RecordSetName == item.RecordSetName
}

func recordIdentityKey(name string, recordType dnsv1alpha1.RecordType) string {
	return name + "\x00" + string(recordType)
}

func cnameExclusionKey(name string) string {
	return name + "\x00" + string(dnsv1alpha1.RecordTypeCNAME) + "\x00exclusive"
}

func canonicalRecordName(ownerName, domainName string) string {
	switch ownerName {
	case "@":
		return domainName + "."
	case "*":
		return "*." + domainName + "."
	default:
		return ownerName + "." + domainName + "."
	}
}

func providerVersionSupportsType(providerVersion *dnsv1alpha1.ProviderVersion, recordType dnsv1alpha1.RecordType) bool {
	return providerVersion != nil && slices.Contains(providerVersion.RecordSet.SupportedTypes, recordType)
}

func fullRecordNamePatternMatch(pattern, recordName string) (bool, error) {
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return false, err
	}
	match := compiled.FindStringIndex(recordName)
	return match != nil && match[0] == 0 && match[1] == len(recordName), nil
}
