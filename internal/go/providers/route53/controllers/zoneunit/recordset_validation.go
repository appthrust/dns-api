package route53

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
)

var (
	cnameTargetPattern  = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?|_[a-z0-9]([a-z0-9-]{0,60}[a-z0-9])?)(\.([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?|_[a-z0-9]([a-z0-9-]{0,60}[a-z0-9])?))*$`)
	aliasDNSNamePattern = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)(\.([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?))*\.$`)
	mxExchangePattern   = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)(\.([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?))*$`)
	caaTagPattern       = regexp.MustCompile(`^[a-z0-9]+$`)
)

func validateRecordSetIPAddresses(values []string, wantIPv6 bool) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		address, err := netip.ParseAddr(value)
		if err != nil {
			return errors.New("must contain valid IP addresses")
		}
		if address.Is6() != wantIPv6 {
			if wantIPv6 {
				return errors.New("must contain only IPv6 addresses")
			}
			return errors.New("must contain only IPv4 addresses")
		}
		key := address.String()
		if _, ok := seen[key]; ok {
			return errors.New("must not contain duplicate IP addresses")
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateRecordSetTXTValues(values []string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		valueLen := len([]byte(value))
		if valueLen == 0 {
			return errors.New("must not contain empty values")
		}
		if valueLen > 4000 {
			return errors.New("must contain values of 4000 UTF-8 octets or fewer")
		}
		if !isPrintableASCII(value) {
			return errors.New("must contain only printable ASCII characters")
		}
		if _, ok := seen[value]; ok {
			return errors.New("must not contain duplicate values")
		}
		seen[value] = struct{}{}
	}
	return nil
}

func isPrintableASCII(value string) bool {
	for index := 0; index < len(value); index++ {
		if value[index] < 0x20 || value[index] > 0x7e {
			return false
		}
	}
	return true
}

func validateCNAMETarget(target string) error {
	if target == "" {
		return errors.New("must not be empty")
	}
	if len(target) > 253 {
		return errors.New("must be 253 octets or fewer")
	}
	if _, err := netip.ParseAddr(target); err == nil {
		return errors.New("must be a DNS name, not an IP address")
	}
	if !cnameTargetPattern.MatchString(target) {
		return errors.New("must be normalized lowercase ASCII without a trailing root dot")
	}
	return nil
}

func validateAliasDNSName(name string) error {
	if name == "" {
		return errors.New("must not be empty")
	}
	if len(name) > 254 {
		return errors.New("must be 254 octets or fewer")
	}
	if _, err := netip.ParseAddr(strings.TrimSuffix(name, ".")); err == nil {
		return errors.New("must be a DNS name, not an IP address")
	}
	if !aliasDNSNamePattern.MatchString(name) {
		return errors.New("must be normalized lowercase ASCII with a trailing root dot")
	}
	return nil
}

func validateRecordSetMXRecords(records []dnsv1alpha1.MXRecord) error {
	if len(records) == 0 {
		return errors.New("is required")
	}
	seen := make(map[string]struct{}, len(records))
	nullMXIndex := -1
	for index, record := range records {
		if record.Preference < 0 || record.Preference > 65535 {
			return fmt.Errorf("[%d].preference must be in range 0..65535", index)
		}
		if record.Exchange == "" {
			return fmt.Errorf("[%d].exchange is required", index)
		}
		if record.Exchange == "." {
			nullMXIndex = index
		} else if err := validateMXExchange(record.Exchange); err != nil {
			return fmt.Errorf("[%d].exchange %w", index, err)
		}
		key := fmt.Sprintf("%d\x00%s", record.Preference, record.Exchange)
		if _, ok := seen[key]; ok {
			return errors.New("must not contain duplicate preference and exchange pairs")
		}
		seen[key] = struct{}{}
	}
	if nullMXIndex >= 0 {
		if len(records) != 1 {
			return errors.New("must contain only one record when exchange is \".\"")
		}
		if records[nullMXIndex].Preference != 0 {
			return fmt.Errorf("[%d].preference must be 0 when exchange is \".\"", nullMXIndex)
		}
	}
	return nil
}

func validateRecordSetCAARecords(records []dnsv1alpha1.CAARecord) error {
	if len(records) == 0 {
		return errors.New("is required")
	}
	seen := make(map[string]struct{}, len(records))
	for index, record := range records {
		if record.Flags < 0 || record.Flags > 255 {
			return fmt.Errorf("[%d].flags must be in range 0..255", index)
		}
		if record.Tag == "" {
			return fmt.Errorf("[%d].tag is required", index)
		}
		if !caaTagPattern.MatchString(record.Tag) {
			return fmt.Errorf("[%d].tag must be lowercase ASCII alphanumeric", index)
		}
		if record.Value == "" {
			return fmt.Errorf("[%d].value is required", index)
		}
		key := fmt.Sprintf("%d\x00%s\x00%s", record.Flags, record.Tag, record.Value)
		if _, ok := seen[key]; ok {
			return errors.New("must not contain duplicate flags, tag, and value tuples")
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateRecordSetNSNameServers(nameServers []string) error {
	if len(nameServers) == 0 {
		return errors.New("is required")
	}
	seen := make(map[string]struct{}, len(nameServers))
	for index, nameServer := range nameServers {
		if err := validateNSNameServer(nameServer); err != nil {
			return fmt.Errorf("[%d] %w", index, err)
		}
		if _, ok := seen[nameServer]; ok {
			return errors.New("must not contain duplicate name servers")
		}
		seen[nameServer] = struct{}{}
	}
	return nil
}

func validateNSNameServer(nameServer string) error {
	if nameServer == "" {
		return errors.New("must not be empty")
	}
	if len(nameServer) > 253 {
		return errors.New("must be 253 octets or fewer")
	}
	if _, err := netip.ParseAddr(nameServer); err == nil {
		return errors.New("must be a DNS name, not an IP address")
	}
	if !mxExchangePattern.MatchString(nameServer) {
		return errors.New("must be normalized lowercase ASCII without a trailing root dot")
	}
	return nil
}

func validateMXExchange(exchange string) error {
	if exchange == "" {
		return errors.New("must not be empty")
	}
	if len(exchange) > 253 {
		return errors.New("must be 253 octets or fewer")
	}
	if _, err := netip.ParseAddr(exchange); err == nil {
		return errors.New("must be a DNS name, not an IP address")
	}
	if !mxExchangePattern.MatchString(exchange) {
		return errors.New("must be normalized lowercase ASCII without a trailing root dot")
	}
	return nil
}

func canonicalRecordSetValues(recordType dnsv1alpha1.RecordType, values []string) ([]string, bool) {
	switch recordType {
	case dnsv1alpha1.RecordTypeA, dnsv1alpha1.RecordTypeAAAA:
		canonical := make([]string, 0, len(values))
		for _, value := range values {
			address, err := netip.ParseAddr(value)
			if err != nil {
				return nil, false
			}
			if recordType == dnsv1alpha1.RecordTypeA && !address.Is4() {
				return nil, false
			}
			if recordType == dnsv1alpha1.RecordTypeAAAA && !address.Is6() {
				return nil, false
			}
			canonical = append(canonical, address.String())
		}
		return canonical, true
	case dnsv1alpha1.RecordTypeTXT:
		canonical := make([]string, 0, len(values))
		for _, value := range values {
			parsed, ok := parseRoute53TXTValue(value)
			if !ok {
				return nil, false
			}
			canonical = append(canonical, parsed)
		}
		return canonical, true
	case dnsv1alpha1.RecordTypeCNAME:
		canonical := make([]string, 0, len(values))
		for _, value := range values {
			canonical = append(canonical, normalizeRoute53RecordName(value))
		}
		return canonical, true
	case dnsv1alpha1.RecordTypeMX:
		canonical := make([]string, 0, len(values))
		for _, value := range values {
			record, ok := parseRoute53MXValue(value)
			if !ok {
				return nil, false
			}
			canonical = append(canonical, canonicalMXValue(record))
		}
		return canonical, true
	case dnsv1alpha1.RecordTypeCAA:
		canonical := make([]string, 0, len(values))
		for _, value := range values {
			record, ok := parseRoute53CAAValue(value)
			if !ok {
				return nil, false
			}
			canonical = append(canonical, canonicalCAAValue(record))
		}
		return canonical, true
	case dnsv1alpha1.RecordTypeNS:
		canonical := make([]string, 0, len(values))
		for _, value := range values {
			nameServer := strings.TrimSuffix(normalizeRoute53RecordName(value), ".")
			if err := validateNSNameServer(nameServer); err != nil {
				return nil, false
			}
			canonical = append(canonical, nameServer)
		}
		return canonical, true
	default:
		return slices.Clone(values), true
	}
}

func formatRoute53MXValue(record dnsv1alpha1.MXRecord) string {
	if record.Exchange == "." {
		return fmt.Sprintf("%d .", record.Preference)
	}
	return fmt.Sprintf("%d %s", record.Preference, normalizeRoute53RecordName(record.Exchange))
}

func canonicalMXValue(record dnsv1alpha1.MXRecord) string {
	if record.Exchange == "." {
		return fmt.Sprintf("%d .", record.Preference)
	}
	return fmt.Sprintf("%d %s", record.Preference, strings.TrimSuffix(normalizeRoute53RecordName(record.Exchange), "."))
}

func parseRoute53MXValue(value string) (dnsv1alpha1.MXRecord, bool) {
	parts := strings.Fields(value)
	if len(parts) != 2 {
		return dnsv1alpha1.MXRecord{}, false
	}
	preference, err := strconv.ParseInt(parts[0], 10, 32)
	if err != nil || preference < 0 || preference > 65535 {
		return dnsv1alpha1.MXRecord{}, false
	}
	exchange := parts[1]
	if exchange == "." {
		if preference != 0 {
			return dnsv1alpha1.MXRecord{}, false
		}
		return dnsv1alpha1.MXRecord{Preference: int32(preference), Exchange: "."}, true
	}
	normalized := strings.TrimSuffix(normalizeRoute53RecordName(exchange), ".")
	if err := validateMXExchange(normalized); err != nil {
		return dnsv1alpha1.MXRecord{}, false
	}
	return dnsv1alpha1.MXRecord{Preference: int32(preference), Exchange: normalized}, true
}

func formatRoute53CAAValue(record dnsv1alpha1.CAARecord) string {
	return fmt.Sprintf("%d %s %s", record.Flags, record.Tag, quoteRoute53TXTChunk(record.Value))
}

func canonicalCAAValue(record dnsv1alpha1.CAARecord) string {
	return fmt.Sprintf("%d %s %s", record.Flags, record.Tag, record.Value)
}

func parseRoute53CAAValue(value string) (dnsv1alpha1.CAARecord, bool) {
	parts := strings.Fields(value)
	if len(parts) < 3 {
		return dnsv1alpha1.CAARecord{}, false
	}
	flags, err := strconv.ParseInt(parts[0], 10, 32)
	if err != nil || flags < 0 || flags > 255 {
		return dnsv1alpha1.CAARecord{}, false
	}
	tag := parts[1]
	if !caaTagPattern.MatchString(tag) {
		return dnsv1alpha1.CAARecord{}, false
	}
	parsedValue, ok := parseRoute53TXTValue(strings.Join(parts[2:], " "))
	if !ok || parsedValue == "" {
		return dnsv1alpha1.CAARecord{}, false
	}
	return dnsv1alpha1.CAARecord{Flags: int32(flags), Tag: tag, Value: parsedValue}, true
}

func quoteRoute53TXTValue(value string) string {
	if value == "" {
		return `""`
	}
	chunks := make([]string, 0, len(value)/255+1)
	for len(value) > 0 {
		chunkLen := route53TXTChunkLen(value, 255)
		chunks = append(chunks, quoteRoute53TXTChunk(value[:chunkLen]))
		value = value[chunkLen:]
	}
	return strings.Join(chunks, " ")
}

func route53TXTChunkLen(value string, maxBytes int) int {
	if len(value) <= maxBytes {
		return len(value)
	}
	last := 0
	for index := range value {
		if index > maxBytes {
			break
		}
		last = index
	}
	if last == 0 {
		return maxBytes
	}
	return last
}

func quoteRoute53TXTChunk(chunk string) string {
	var out strings.Builder
	out.Grow(len(chunk) + 2)
	out.WriteByte('"')
	for _, r := range chunk {
		if r == '"' || r == '\\' {
			out.WriteByte('\\')
		}
		out.WriteRune(r)
	}
	out.WriteByte('"')
	return out.String()
}

func parseRoute53TXTValue(value string) (string, bool) {
	var out strings.Builder
	for index := 0; index < len(value); {
		for index < len(value) && (value[index] == ' ' || value[index] == '\t') {
			index++
		}
		if index >= len(value) {
			break
		}
		if value[index] != '"' {
			return "", false
		}
		index++
		for {
			if index >= len(value) {
				return "", false
			}
			if value[index] == '"' {
				index++
				break
			}
			if value[index] == '\\' {
				if index+3 < len(value) && isDecimalDigit(value[index+1]) && isDecimalDigit(value[index+2]) && isDecimalDigit(value[index+3]) {
					escaped := int(value[index+1]-'0')*100 + int(value[index+2]-'0')*10 + int(value[index+3]-'0')
					if escaped > 255 {
						return "", false
					}
					out.WriteByte(byte(escaped))
					index += 4
					continue
				}
				if index+1 >= len(value) {
					return "", false
				}
				index++
			}
			out.WriteByte(value[index])
			index++
		}
	}
	return out.String(), true
}

func isDecimalDigit(value byte) bool {
	return value >= '0' && value <= '9'
}
