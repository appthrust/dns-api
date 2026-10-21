// Package render builds the complete, deterministic hosted serving snapshot.
package render

import (
	"encoding/json"
	"fmt"
	"math"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	hostedv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/hosted/v1alpha1"
	"github.com/miekg/dns"
)

// Render omits cleanup ledger entries. The previous digest, when available, is
// read from the unit's public zone receipt; the controller also compares the
// authoritative snapshot so a crash between snapshot and receipt cannot bump it.
func Render(unit *dnsv1alpha1.ZoneUnit, params hostedv1alpha1.HostedZoneClassParameters, previousSerial uint32, now time.Time) (hostedv1alpha1.HostedZoneSpec, error) {
	var spec hostedv1alpha1.HostedZoneSpec
	if unit == nil || len(params.NameServers) == 0 {
		return spec, fmt.Errorf("hosted rendering requires a ZoneUnit and at least one name server")
	}
	spec.DomainName = canonicalName(unit.Spec.Zone.DomainName)
	spec.SOA = hostedv1alpha1.SOA{MName: canonicalName(params.NameServers[0].Name), RName: canonicalName(params.SOA.RName), Refresh: defaultNumber(params.SOA.Refresh, hostedv1alpha1.DefaultSOARefresh), Retry: defaultNumber(params.SOA.Retry, hostedv1alpha1.DefaultSOARetry), Expire: defaultNumber(params.SOA.Expire, hostedv1alpha1.DefaultSOAExpire), Minimum: defaultNumber(params.SOA.Minimum, hostedv1alpha1.DefaultSOAMinimum)}
	if spec.SOA.RName == "" {
		spec.SOA.RName = "hostmaster." + spec.DomainName
	}
	ttl := defaultNumber(params.DefaultTTL, hostedv1alpha1.DefaultTTL)
	records := make(map[string]hostedv1alpha1.HostedZoneRecord, len(unit.Spec.RecordSets)+1)
	add := func(record hostedv1alpha1.HostedZoneRecord) error {
		key := record.Name + "/" + string(record.Type)
		if old, found := records[key]; found {
			if old.TTL != record.TTL {
				return fmt.Errorf("conflicting TTLs for %s", key)
			}
			record.Values = append(old.Values, record.Values...)
		}
		slices.Sort(record.Values)
		record.Values = slices.Compact(record.Values)
		if len(record.Values) > 64 {
			return fmt.Errorf("record %s exceeds 64 values", key)
		}
		records[key] = record
		return nil
	}
	for _, item := range unit.Spec.RecordSets {
		if !item.IsAllowed() || item.DeletionRequested {
			continue
		}
		if item.Name == "@" && item.Type == dnsv1alpha1.RecordTypeNS {
			return spec, fmt.Errorf("apex NS is owned by nameServers")
		}
		record, err := renderRecord(item, ttl)
		if err != nil {
			return spec, err
		}
		if err = add(record); err != nil {
			return spec, err
		}
	}
	ns := hostedv1alpha1.HostedZoneRecord{Name: "@", Type: dnsv1alpha1.RecordTypeNS, TTL: ttl}
	for _, server := range params.NameServers {
		server.Name = canonicalName(server.Name)
		server.Addresses = slices.Clone(server.Addresses)
		for i, address := range server.Addresses {
			ip, err := netip.ParseAddr(address)
			if err != nil {
				return spec, fmt.Errorf("invalid name server address %q", address)
			}
			server.Addresses[i] = ip.String()
			if server.Name != spec.DomainName && !strings.HasSuffix(server.Name, "."+spec.DomainName) {
				continue
			}
			name := strings.TrimSuffix(server.Name, "."+spec.DomainName)
			if server.Name == spec.DomainName {
				name = "@"
			}
			kind := dnsv1alpha1.RecordTypeAAAA
			if ip.Is4() {
				kind = dnsv1alpha1.RecordTypeA
			}
			if err := add(hostedv1alpha1.HostedZoneRecord{Name: name, Type: kind, TTL: ttl, Values: []string{ip.String()}}); err != nil {
				return spec, err
			}
		}
		slices.Sort(server.Addresses)
		server.Addresses = slices.Compact(server.Addresses)
		spec.NameServers = append(spec.NameServers, server)
		ns.Values = append(ns.Values, dns.Fqdn(server.Name))
	}
	slices.SortFunc(spec.NameServers, func(a, b hostedv1alpha1.NameServer) int { return strings.Compare(a.Name, b.Name) })
	if err := add(ns); err != nil {
		return spec, err
	}
	if len(records) > 10000 {
		return spec, fmt.Errorf("snapshot exceeds 10000 RRsets")
	}
	spec.Records = make([]hostedv1alpha1.HostedZoneRecord, 0, len(records))
	for _, record := range records {
		spec.Records = append(spec.Records, record)
	}
	slices.SortFunc(spec.Records, func(a, b hostedv1alpha1.HostedZoneRecord) int {
		if n := strings.Compare(a.Name, b.Name); n != 0 {
			return n
		}
		return strings.Compare(string(a.Type), string(b.Type))
	})
	spec.ContentDigest = ContentDigest(spec)
	if spec.ContentDigest == "" {
		return spec, fmt.Errorf("snapshot contains invalid DNS wire data")
	}
	var previous struct {
		ContentDigest string `json:"contentDigest"`
	}
	if unit.Status.Zone != nil && unit.Status.Zone.Provider != nil {
		_ = json.Unmarshal(unit.Status.Zone.Provider.Data.Raw, &previous)
	}
	if previousSerial != 0 && previous.ContentDigest == spec.ContentDigest {
		spec.Serial = previousSerial
		return spec, nil
	}
	if previousSerial == math.MaxUint32 {
		return spec, fmt.Errorf("hosted snapshot serial exhausted")
	}
	spec.Serial = max(previousSerial+1, uint32(now.Unix()))
	return spec, nil
}

func defaultNumber(value, fallback uint32) uint32 {
	if value == 0 {
		return fallback
	}
	return value
}
func canonicalName(value string) string { return strings.TrimSuffix(strings.ToLower(value), ".") }

func renderRecord(item dnsv1alpha1.ZoneUnitRecordSetSpec, defaultTTL uint32) (hostedv1alpha1.HostedZoneRecord, error) {
	record := hostedv1alpha1.HostedZoneRecord{Name: strings.ToLower(item.Name), Type: item.Type, TTL: defaultTTL}
	if item.TTL != nil {
		if *item.TTL < 1 {
			return record, fmt.Errorf("invalid TTL for %s", item.Name)
		}
		record.TTL = uint32(*item.TTL)
	}
	switch item.Type {
	case dnsv1alpha1.RecordTypeA:
		if item.A != nil {
			for _, value := range item.A.Addresses {
				ip, err := netip.ParseAddr(value)
				if err != nil || !ip.Is4() {
					return record, fmt.Errorf("invalid A address %q", value)
				}
				record.Values = append(record.Values, ip.String())
			}
		}
	case dnsv1alpha1.RecordTypeAAAA:
		if item.AAAA != nil {
			for _, value := range item.AAAA.Addresses {
				ip, err := netip.ParseAddr(value)
				if err != nil || !ip.Is6() {
					return record, fmt.Errorf("invalid AAAA address %q", value)
				}
				record.Values = append(record.Values, ip.String())
			}
		}
	case dnsv1alpha1.RecordTypeCNAME:
		if item.CNAME != nil {
			record.Values = []string{dns.Fqdn(canonicalName(item.CNAME.Target))}
		}
	case dnsv1alpha1.RecordTypeNS:
		if item.NS != nil {
			for _, value := range item.NS.NameServers {
				record.Values = append(record.Values, dns.Fqdn(canonicalName(value)))
			}
		}
	case dnsv1alpha1.RecordTypeTXT:
		if item.TXT != nil {
			for _, value := range item.TXT.Values {
				record.Values = append(record.Values, quoteTXT(value))
			}
		}
	case dnsv1alpha1.RecordTypeMX:
		if item.MX != nil {
			for _, value := range item.MX.Records {
				record.Values = append(record.Values, strconv.Itoa(int(value.Preference))+" "+dns.Fqdn(canonicalName(value.Exchange)))
			}
		}
	case dnsv1alpha1.RecordTypeCAA:
		if item.CAA != nil {
			for _, value := range item.CAA.Records {
				record.Values = append(record.Values, strconv.Itoa(int(value.Flags))+" "+strings.ToLower(value.Tag)+" "+quote(value.Value))
			}
		}
	default:
		return record, fmt.Errorf("unsupported hosted record type %q", item.Type)
	}
	if len(record.Values) == 0 {
		return record, fmt.Errorf("record %s/%s has no values", item.Name, item.Type)
	}
	return record, nil
}

// DNS character strings use decimal byte escapes, not Go's hex/unicode escapes.
func quote(value string) string {
	var out strings.Builder
	out.WriteByte('"')
	for i := range len(value) {
		c := value[i]
		switch {
		case c == '"' || c == '\\':
			out.WriteByte('\\')
			out.WriteByte(c)
		case c < 32 || c > 126:
			out.WriteByte('\\')
			out.WriteByte('0' + c/100)
			out.WriteByte('0' + c/10%10)
			out.WriteByte('0' + c%10)
		default:
			out.WriteByte(c)
		}
	}
	out.WriteByte('"')
	return out.String()
}
func quoteTXT(value string) string {
	if len(value) <= 255 {
		return quote(value)
	}
	var out strings.Builder
	for len(value) > 0 {
		n := min(255, len(value))
		if out.Len() > 0 {
			out.WriteByte(' ')
		}
		out.WriteString(quote(value[:n]))
		value = value[n:]
	}
	return out.String()
}
