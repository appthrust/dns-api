package server

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"

	hostedv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/hosted/v1alpha1"
	"github.com/miekg/dns"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// zone is immutable after publication. Readers retain one snapshot throughout a
// query, including CNAME traversal, so an informer update cannot mix serials.
type zone struct {
	key         types.NamespacedName
	uid         types.UID
	apex        string
	serial      uint32
	digest      string
	owners      map[string]map[uint16][]dns.RR
	wildcards   map[string]map[uint16][]dns.RR
	soa         *dns.SOA
	negativeSOA *dns.SOA
	glue        []dns.RR
}

func compileZone(snapshot *hostedv1alpha1.HostedZone) (*zone, error) {
	spec := &snapshot.Spec
	apex := dns.CanonicalName(spec.DomainName)
	if _, valid := dns.IsDomainName(apex); !valid || spec.DomainName == "" || apex == "." || spec.Serial == 0 {
		return nil, fmt.Errorf("invalid zone apex or serial")
	}
	z := &zone{key: types.NamespacedName{Namespace: snapshot.Namespace, Name: snapshot.Name}, uid: snapshot.UID,
		apex: apex, serial: spec.Serial, digest: spec.ContentDigest,
		owners: make(map[string]map[uint16][]dns.RR), wildcards: make(map[string]map[uint16][]dns.RR)}
	z.owners[apex] = make(map[uint16][]dns.RR)
	glueTTL := hostedv1alpha1.DefaultTTL
	for _, record := range spec.Records {
		owner := apex
		if record.Name != "@" {
			owner = dns.CanonicalName(record.Name + "." + apex)
		}
		if _, valid := dns.IsDomainName(owner); !valid || !dns.IsSubDomain(apex, owner) {
			return nil, fmt.Errorf("invalid record owner %q", record.Name)
		}
		kind, ok := dns.StringToType[string(record.Type)]
		if !ok || !supportedType(kind) {
			return nil, fmt.Errorf("unsupported record type %q", record.Type)
		}
		if len(record.Values) == 0 {
			return nil, fmt.Errorf("empty RRset at %s", owner)
		}
		if z.owners[owner] == nil {
			z.owners[owner] = make(map[uint16][]dns.RR)
		}
		if len(z.owners[owner][kind]) != 0 {
			return nil, fmt.Errorf("duplicate RRset at %s type %s", owner, record.Type)
		}
		rrs := make([]dns.RR, 0, len(record.Values))
		prefix := owner + " " + strconv.FormatUint(uint64(record.TTL), 10) + " IN " + string(record.Type) + " "
		for _, value := range record.Values {
			rr, err := dns.NewRR(prefix + value)
			if err != nil {
				return nil, fmt.Errorf("record %s %s: %w", owner, record.Type, err)
			}
			rrs = append(rrs, rr)
		}
		z.owners[owner][kind] = rrs
		if owner == apex && kind == dns.TypeNS {
			glueTTL = record.TTL
		}
		if strings.HasPrefix(owner, "*.") {
			z.wildcards[owner[2:]] = z.owners[owner]
		}
		// Empty non-terminals exist for RFC 4592 closest-encloser matching.
		for ancestor := parentName(owner); ancestor != "" && dns.IsSubDomain(apex, ancestor); ancestor = parentName(ancestor) {
			if _, exists := z.owners[ancestor]; !exists {
				z.owners[ancestor] = nil
			}
			if ancestor == apex {
				break
			}
		}
	}
	for owner, rrsets := range z.owners {
		if cnames := rrsets[dns.TypeCNAME]; len(cnames) != 0 && (len(cnames) != 1 || len(rrsets) != 1 || owner == apex) {
			return nil, fmt.Errorf("CNAME must be exclusive at %s", owner)
		}
	}
	for _, ns := range spec.NameServers {
		name := dns.CanonicalName(ns.Name)
		if _, valid := dns.IsDomainName(name); !valid || name == "." {
			return nil, fmt.Errorf("invalid nameserver %q", ns.Name)
		}
		for _, address := range ns.Addresses {
			ip := net.ParseIP(address)
			if ip == nil {
				return nil, fmt.Errorf("invalid glue address %q", address)
			}
			header := dns.RR_Header{Name: name, Class: dns.ClassINET, Ttl: glueTTL}
			if v4 := ip.To4(); v4 != nil {
				header.Rrtype = dns.TypeA
				z.glue = append(z.glue, &dns.A{Hdr: header, A: v4})
			} else {
				header.Rrtype = dns.TypeAAAA
				z.glue = append(z.glue, &dns.AAAA{Hdr: header, AAAA: ip})
			}
		}
	}
	if len(z.owners[apex][dns.TypeNS]) == 0 {
		for _, ns := range spec.NameServers {
			z.owners[apex][dns.TypeNS] = append(z.owners[apex][dns.TypeNS], &dns.NS{Hdr: dns.RR_Header{Name: apex, Rrtype: dns.TypeNS, Class: dns.ClassINET, Ttl: glueTTL}, Ns: dns.CanonicalName(ns.Name)})
		}
	}
	z.soa = &dns.SOA{Hdr: dns.RR_Header{Name: apex, Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: spec.SOA.Minimum},
		Ns: dns.CanonicalName(spec.SOA.MName), Mbox: dns.CanonicalName(spec.SOA.RName), Serial: spec.Serial,
		Refresh: spec.SOA.Refresh, Retry: spec.SOA.Retry, Expire: spec.SOA.Expire, Minttl: spec.SOA.Minimum}
	z.owners[apex][dns.TypeSOA] = []dns.RR{z.soa}
	negative := *z.soa
	negative.Hdr.Ttl = min(z.soa.Hdr.Ttl, spec.SOA.Minimum)
	z.negativeSOA = &negative
	return z, nil
}

func supportedType(kind uint16) bool {
	switch kind {
	case dns.TypeA, dns.TypeAAAA, dns.TypeCNAME, dns.TypeTXT, dns.TypeMX, dns.TypeCAA, dns.TypeNS:
		return true
	}
	return false
}

func parentName(name string) string {
	if name == "." {
		return ""
	}
	index, end := dns.NextLabel(name, 0)
	if end {
		return "."
	}
	return name[index:]
}

func (z *zone) lookup(name string) (map[uint16][]dns.RR, bool) {
	if rrsets, exists := z.owners[name]; exists {
		return rrsets, true
	}
	for ancestor := parentName(name); ancestor != "" && dns.IsSubDomain(z.apex, ancestor); ancestor = parentName(ancestor) {
		if _, exists := z.owners[ancestor]; exists {
			rrsets, wildcard := z.wildcards[ancestor]
			return rrsets, wildcard
		}
	}
	return nil, false
}

// sync.Map publishes each compiled zone atomically without copying unrelated
// zones. mu serializes only index mutations and capacity/ownership bookkeeping.
type zoneIndex struct {
	zones    sync.Map // canonical apex -> *zone
	blocked  sync.Map // conflicting apexes must not fall through to a parent zone
	mu       sync.Mutex
	keys     map[types.NamespacedName]string
	maxZones int
	metrics  *serverMetrics
}

func newZoneIndex(maxZones int, metrics *serverMetrics) *zoneIndex {
	return &zoneIndex{keys: make(map[types.NamespacedName]string), maxZones: maxZones, metrics: metrics}
}

func (index *zoneIndex) replace(snapshot *hostedv1alpha1.HostedZone) error {
	index.mu.Lock()
	defer index.mu.Unlock()
	key := types.NamespacedName{Namespace: snapshot.Namespace, Name: snapshot.Name}
	if !hasZoneUnitOwner(snapshot.OwnerReferences) {
		index.metrics.rejected.WithLabelValues("unowned").Inc()
		if apex, exists := index.keys[key]; exists {
			index.removeApexLocked(apex)
		}
		return fmt.Errorf("snapshot has no ZoneUnit owner reference")
	}
	oldApex, exists := index.keys[key]
	apex := dns.CanonicalName(snapshot.Spec.DomainName)
	if current, exists := index.zones.Load(apex); exists && current.(*zone).key != key {
		index.blocked.Store(apex, struct{}{})
		index.removeApexLocked(apex)
		index.metrics.rejected.WithLabelValues("apex_conflict").Inc()
		return fmt.Errorf("zone apex %s has conflicting snapshots", apex)
	}
	if !exists && len(index.keys) >= index.maxZones {
		return fmt.Errorf("hosted zone limit %d reached", index.maxZones)
	}
	compiled, err := compileZone(snapshot)
	if err != nil {
		return err
	}
	if exists && oldApex != apex {
		index.removeApexLocked(oldApex)
	}
	index.zones.Store(apex, compiled)
	index.keys[key] = apex
	index.blocked.Delete(apex)
	index.metrics.serial.WithLabelValues(apex).Set(float64(compiled.serial))
	index.metrics.loaded.Set(float64(len(index.keys)))
	return nil
}

func (index *zoneIndex) remove(key types.NamespacedName, uid types.UID) {
	index.mu.Lock()
	defer index.mu.Unlock()
	apex, exists := index.keys[key]
	if !exists {
		return
	}
	if current, ok := index.zones.Load(apex); ok && current.(*zone).uid != uid {
		return
	}
	index.removeApexLocked(apex)
}

func hasZoneUnitOwner(owners []metav1.OwnerReference) bool {
	for _, owner := range owners {
		if owner.APIVersion == "dns.appthrust.io/v1alpha1" && owner.Kind == "ZoneUnit" && owner.UID != "" {
			return true
		}
	}
	return false
}

func (index *zoneIndex) removeApexLocked(apex string) {
	if current, exists := index.zones.LoadAndDelete(apex); exists {
		delete(index.keys, current.(*zone).key)
		index.metrics.serial.DeleteLabelValues(apex)
		index.metrics.queries.DeletePartialMatch(map[string]string{"zone": apex})
		index.metrics.loaded.Set(float64(len(index.keys)))
	}
}

func (index *zoneIndex) reconcileAbsentApex(apex string, conflict bool) {
	index.mu.Lock()
	defer index.mu.Unlock()
	if conflict {
		index.blocked.Store(apex, struct{}{})
		index.metrics.rejected.WithLabelValues("apex_conflict").Inc()
	} else {
		index.blocked.Delete(apex)
	}
	index.removeApexLocked(apex)
}

func (index *zoneIndex) lookup(name string) *zone {
	for candidate := name; candidate != ""; candidate = parentName(candidate) {
		if _, blocked := index.blocked.Load(candidate); blocked {
			return nil
		}
		if value, ok := index.zones.Load(candidate); ok {
			return value.(*zone)
		}
	}
	return nil
}
