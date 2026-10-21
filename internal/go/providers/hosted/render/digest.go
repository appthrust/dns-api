package render

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/netip"
	"slices"

	hostedv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/hosted/v1alpha1"
	"github.com/miekg/dns"
)

// ContentDigest hashes sorted, uncompressed canonical DNS wire records. Serial
// is zeroed, so only content changes create a revision. An invalid wire record
// returns an empty digest and cannot be published by Render.
func ContentDigest(spec hostedv1alpha1.HostedZoneSpec) string {
	wire := make([][]byte, 0, len(spec.Records)+1)
	add := func(rr dns.RR) bool {
		data := make([]byte, dns.Len(rr))
		n, err := dns.PackRR(rr, data, 0, nil, false)
		if err != nil {
			return false
		}
		wire = append(wire, data[:n])
		return true
	}
	soa := &dns.SOA{Hdr: dns.RR_Header{Name: dns.Fqdn(canonicalName(spec.DomainName)), Rrtype: dns.TypeSOA, Class: dns.ClassINET}, Ns: dns.Fqdn(canonicalName(spec.SOA.MName)), Mbox: dns.Fqdn(canonicalName(spec.SOA.RName)), Refresh: spec.SOA.Refresh, Retry: spec.SOA.Retry, Expire: spec.SOA.Expire, Minttl: spec.SOA.Minimum}
	if !add(soa) {
		return ""
	}
	// Name-server addresses outside the zone are not served glue, but they remain
	// declared snapshot content and must be covered by the digest as well.
	for _, server := range spec.NameServers {
		if !add(&dns.NS{Hdr: dns.RR_Header{Name: soa.Hdr.Name, Rrtype: dns.TypeNS, Class: dns.ClassINET}, Ns: dns.Fqdn(canonicalName(server.Name))}) {
			return ""
		}
		for _, address := range server.Addresses {
			ip, err := netip.ParseAddr(address)
			if err != nil {
				return ""
			}
			kind := "AAAA"
			if ip.Is4() {
				kind = "A"
			}
			rr, err := dns.NewRR(fmt.Sprintf("%s 0 IN %s %s", dns.Fqdn(canonicalName(server.Name)), kind, ip.String()))
			if err != nil || !add(rr) {
				return ""
			}
		}
	}
	for _, record := range spec.Records {
		name := canonicalName(record.Name) + "." + canonicalName(spec.DomainName)
		if record.Name == "@" {
			name = canonicalName(spec.DomainName)
		}
		for _, value := range record.Values {
			rr, err := dns.NewRR(fmt.Sprintf("%s %d IN %s %s", dns.Fqdn(name), record.TTL, record.Type, value))
			if err != nil {
				return ""
			}
			switch typed := rr.(type) {
			case *dns.NS:
				typed.Ns = dns.Fqdn(canonicalName(typed.Ns))
			case *dns.CNAME:
				typed.Target = dns.Fqdn(canonicalName(typed.Target))
			case *dns.MX:
				typed.Mx = dns.Fqdn(canonicalName(typed.Mx))
			}
			if !add(rr) {
				return ""
			}
		}
	}
	slices.SortFunc(wire, bytes.Compare)
	wire = slices.CompactFunc(wire, bytes.Equal)
	hash := sha256.New()
	var size [4]byte
	for _, rr := range wire {
		binary.BigEndian.PutUint32(size[:], uint32(len(rr)))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write(rr)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}
