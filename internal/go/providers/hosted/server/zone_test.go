package server

import (
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"k8s.io/apimachinery/pkg/types"
)

func TestZoneIndexBoundsAndReplacement(t *testing.T) {
	index := newZoneIndex(1, newMetrics())
	original := testZone()
	if err := index.replace(original); err != nil {
		t.Fatal(err)
	}
	first := index.lookup("www.example.test.")
	original.Spec.Records[3].Values[0] = "192.0.2.100"
	if first.owners["www.example.test."][dns.TypeA][0].(*dns.A).A.String() != "192.0.2.1" {
		t.Fatal("index aliases API object")
	}
	other := testZone()
	other.Name = "other"
	other.Spec.DomainName = "other.test"
	if err := index.replace(other); err == nil {
		t.Fatal("zone limit was not enforced")
	}
	if index.lookup("other.test.") != nil {
		t.Fatal("rejected zone was published")
	}
	original.Spec.Serial++
	if err := index.replace(original); err != nil {
		t.Fatalf("replacement counted against capacity: %v", err)
	}
	current := index.lookup("www.example.test.")
	if current == first || current.serial != 8 || first.serial != 7 {
		t.Fatal("replacement was not immutable")
	}
	broken := original.DeepCopy()
	broken.Spec.Records[3].Values[0] = "not-an-ip"
	if err := index.replace(broken); err == nil {
		t.Fatal("invalid snapshot loaded")
	}
	if index.lookup("www.example.test.") != current {
		t.Fatal("invalid update replaced last good snapshot")
	}
	index.remove(types.NamespacedName{Namespace: "dns", Name: "example"}, "old-uid")
	if index.lookup("www.example.test.") == nil {
		t.Fatal("old UID deleted new snapshot")
	}
	index.remove(current.key, current.uid)
	if index.lookup("www.example.test.") != nil {
		t.Fatal("deleted snapshot is still served")
	}
	if metricValue(t, index.metrics.loaded) != 0 {
		t.Fatal("zone gauge was not decremented")
	}
}

func TestLongestApexAndDuplicateOwnership(t *testing.T) {
	index := newZoneIndex(3, newMetrics())
	parent := testZone()
	if err := index.replace(parent); err != nil {
		t.Fatal(err)
	}
	child := testZone()
	child.Name = "child"
	child.Spec.DomainName = "child.example.test"
	if err := index.replace(child); err != nil {
		t.Fatal(err)
	}
	if got := index.lookup("www.child.example.test."); got == nil || got.apex != "child.example.test." {
		t.Fatalf("longest apex = %#v", got)
	}
	duplicate := parent.DeepCopy()
	duplicate.Namespace = "other"
	if err := index.replace(duplicate); err == nil {
		t.Fatal("duplicate apex stole authority")
	}
	if index.lookup("example.test.") != nil {
		t.Fatal("conflicting apex retained an arbitrary winner")
	}
}

func TestNegativeTTLUsesSOATTLMinimum(t *testing.T) {
	zone := testZone()
	zone.Spec.SOA.Minimum = 300
	compiled, err := compileZone(zone)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.soa.Hdr.Ttl != 300 || compiled.negativeSOA.Hdr.Ttl != 300 {
		t.Fatalf("SOA TTL = %d negative = %d", compiled.soa.Hdr.Ttl, compiled.negativeSOA.Hdr.Ttl)
	}
}

func TestPerSourceTokenBucketAndUDPDrop(t *testing.T) {
	now := time.Unix(1, 0)
	limiter := newSourceLimiter(2)
	one := netip.MustParseAddr("192.0.2.1")
	two := netip.MustParseAddr("192.0.2.2")
	if !limiter.allow(one, now) || !limiter.allow(one, now) || limiter.allow(one, now) {
		t.Fatal("burst limit not enforced")
	}
	if !limiter.allow(two, now) {
		t.Fatal("independent source shared a bucket")
	}
	if limiter.allow(one, now.Add(499*time.Millisecond)) || !limiter.allow(one, now.Add(500*time.Millisecond)) {
		t.Fatal("token refill incorrect")
	}
	if !limiter.allow(one, now.Add(2*time.Second)) || !limiter.allow(one, now.Add(2*time.Second)) || limiter.allow(one, now.Add(2*time.Second)) {
		t.Fatal("refill exceeded burst")
	}
	h := testHandler(t, testZone())
	h.limiter = newSourceLimiter(1)
	request := new(dns.Msg)
	request.SetQuestion("www.example.test.", dns.TypeA)
	first := &responseRecorder{remote: &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 1000}}
	h.ServeDNS(first, request)
	second := &responseRecorder{remote: &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 2000}}
	h.ServeDNS(second, request)
	if first.msg == nil || second.msg != nil || metricValue(t, h.metrics.rateLimited) != 1 {
		t.Fatal("UDP request was not dropped per source address")
	}
	tcp := &responseRecorder{remote: &net.TCPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 2000}}
	h.ServeDNS(tcp, request)
	if tcp.msg == nil {
		t.Fatal("UDP rate limit blocked TCP")
	}
}

func TestSourceTableIsBoundedAndIdleSourcesExpire(t *testing.T) {
	limiter := newSourceLimiter(1)
	now := time.Unix(1, 0)
	for i := range maxRateLimitSources {
		address := netip.AddrFrom4([4]byte{192, byte(i >> 16), byte(i >> 8), byte(i)})
		if !limiter.allow(address, now) {
			t.Fatal("source table filled early")
		}
	}
	newSource := netip.MustParseAddr("203.0.113.1")
	if limiter.allow(newSource, now) || len(limiter.sources) != maxRateLimitSources {
		t.Fatal("active source table is unbounded")
	}
	if !limiter.allow(newSource, now.Add(sourceIdleTimeout)) || len(limiter.sources) > maxRateLimitSources {
		t.Fatal("idle source was not reclaimed")
	}
}

func TestServerOptionsDefaultsAndIdentity(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "dns")
	t.Setenv("POD_NAME", "server-1")
	options, err := (Options{}).defaults()
	if err != nil {
		t.Fatal(err)
	}
	if options.ListenAddr != ":5353" || options.WatchNamespace != "" || options.MaxZones != 4096 || options.UDPRateLimitPerSource != 1000 || options.PodIdentity != "dns/server-1" || options.MetricsAddr != ":8080" || options.HealthAddr != ":8081" || options.RESTConfig != nil {
		t.Fatalf("defaults = %#v", options)
	}
	for _, options := range []Options{{MaxZones: -1}, {UDPRateLimitPerSource: -1}, {PodIdentity: "server"}, {PodIdentity: "/server"}, {PodIdentity: "dns/"}, {PodIdentity: "a/b/c"}} {
		if _, err := options.defaults(); err == nil {
			t.Fatalf("invalid options accepted: %#v", options)
		}
	}
	t.Setenv("POD_NAMESPACE", "")
	if _, err := (Options{}).defaults(); err == nil {
		t.Fatal("missing pod environment accepted")
	}
}

func metricValue(t *testing.T, metric prometheus.Metric) float64 {
	t.Helper()
	value := new(dto.Metric)
	if err := metric.Write(value); err != nil {
		t.Fatal(err)
	}
	if value.Counter != nil {
		return value.Counter.GetValue()
	}
	return value.Gauge.GetValue()
}
