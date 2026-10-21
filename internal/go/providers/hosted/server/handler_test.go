package server

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	hostedv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/hosted/v1alpha1"
	"github.com/miekg/dns"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func testZone() *hostedv1alpha1.HostedZone {
	return &hostedv1alpha1.HostedZone{
		ObjectMeta: metav1.ObjectMeta{Namespace: "dns", Name: "example", UID: "zone-1", OwnerReferences: []metav1.OwnerReference{{APIVersion: "dns.appthrust.io/v1alpha1", Kind: "ZoneUnit", Name: "example", UID: "unit-1"}}},
		Spec: hostedv1alpha1.HostedZoneSpec{
			DomainName: "example.test", Serial: 7, ContentDigest: "sha256:" + strings.Repeat("a", 64),
			SOA:         hostedv1alpha1.SOA{MName: "ns.example.test", RName: "hostmaster.example.test", Refresh: 3600, Retry: 900, Expire: 1209600, Minimum: 30},
			NameServers: []hostedv1alpha1.NameServer{{Name: "ns.example.test", Addresses: []string{"192.0.2.53", "2001:db8::53"}}},
			Records: []hostedv1alpha1.HostedZoneRecord{
				{Name: "@", Type: "NS", TTL: 60, Values: []string{"ns.example.test."}},
				{Name: "ns", Type: "A", TTL: 60, Values: []string{"192.0.2.53"}},
				{Name: "ns", Type: "AAAA", TTL: 60, Values: []string{"2001:db8::53"}},
				{Name: "www", Type: "A", TTL: 90, Values: []string{"192.0.2.1"}},
				{Name: "www", Type: "AAAA", TTL: 90, Values: []string{"2001:db8::1"}},
				{Name: "alias", Type: "CNAME", TTL: 80, Values: []string{"www.example.test."}},
				{Name: "outside", Type: "CNAME", TTL: 80, Values: []string{"other.test."}},
				{Name: "dangling", Type: "CNAME", TTL: 80, Values: []string{"missing.example.test."}},
				{Name: "text", Type: "TXT", TTL: 70, Values: []string{`"hello" "world"`}},
				{Name: "mail", Type: "MX", TTL: 70, Values: []string{"10 www.example.test."}},
				{Name: "caa", Type: "CAA", TTL: 70, Values: []string{`0 issue "ca.example"`}},
				{Name: "sub", Type: "NS", TTL: 70, Values: []string{"ns.example.test."}},
				{Name: "*.wild", Type: "A", TTL: 70, Values: []string{"192.0.2.2"}},
				{Name: "child.block.wild", Type: "TXT", TTL: 70, Values: []string{`"block"`}},
				{Name: "*.aliaswild", Type: "CNAME", TTL: 70, Values: []string{"www.example.test."}},
				{Name: "loop", Type: "CNAME", TTL: 70, Values: []string{"loop.example.test."}},
			},
		},
	}
}

func testHandler(t *testing.T, zone *hostedv1alpha1.HostedZone) *handler {
	t.Helper()
	index := newZoneIndex(4096, newMetrics())
	if err := index.replace(zone); err != nil {
		t.Fatal(err)
	}
	return &handler{index: index, limiter: newSourceLimiter(1000), metrics: index.metrics}
}

func TestAuthoritativeAnswers(t *testing.T) {
	h := testHandler(t, testZone())
	cases := []struct {
		name     string
		qtype    uint16
		rcode    int
		types    []uint16
		negative bool
		glue     int
	}{
		{"example.test.", dns.TypeSOA, dns.RcodeSuccess, []uint16{dns.TypeSOA}, false, 2},
		{"example.test.", dns.TypeNS, dns.RcodeSuccess, []uint16{dns.TypeNS}, false, 2},
		{"WwW.ExAmPlE.TeSt.", dns.TypeA, dns.RcodeSuccess, []uint16{dns.TypeA}, false, 0},
		{"www.example.test.", dns.TypeAAAA, dns.RcodeSuccess, []uint16{dns.TypeAAAA}, false, 0},
		{"text.example.test.", dns.TypeTXT, dns.RcodeSuccess, []uint16{dns.TypeTXT}, false, 0},
		{"mail.example.test.", dns.TypeMX, dns.RcodeSuccess, []uint16{dns.TypeMX}, false, 2},
		{"caa.example.test.", dns.TypeCAA, dns.RcodeSuccess, []uint16{dns.TypeCAA}, false, 0},
		{"sub.example.test.", dns.TypeNS, dns.RcodeSuccess, []uint16{dns.TypeNS}, false, 2},
		{"alias.example.test.", dns.TypeA, dns.RcodeSuccess, []uint16{dns.TypeCNAME, dns.TypeA}, false, 0},
		{"alias.example.test.", dns.TypeCNAME, dns.RcodeSuccess, []uint16{dns.TypeCNAME}, false, 0},
		{"outside.example.test.", dns.TypeA, dns.RcodeSuccess, []uint16{dns.TypeCNAME}, false, 0},
		{"dangling.example.test.", dns.TypeA, dns.RcodeNameError, []uint16{dns.TypeCNAME}, true, 0},
		{"one.wild.example.test.", dns.TypeA, dns.RcodeSuccess, []uint16{dns.TypeA}, false, 0},
		{"one.two.wild.example.test.", dns.TypeA, dns.RcodeSuccess, []uint16{dns.TypeA}, false, 0},
		{"one.wild.example.test.", dns.TypeAAAA, dns.RcodeSuccess, nil, true, 0},
		{"wild.example.test.", dns.TypeA, dns.RcodeSuccess, nil, true, 0},
		{"one.block.wild.example.test.", dns.TypeA, dns.RcodeNameError, nil, true, 0},
		{"block.wild.example.test.", dns.TypeA, dns.RcodeSuccess, nil, true, 0},
		{"OnE.Aliaswild.Example.Test.", dns.TypeA, dns.RcodeSuccess, []uint16{dns.TypeCNAME, dns.TypeA}, false, 0},
		{"missing.example.test.", dns.TypeA, dns.RcodeNameError, nil, true, 0},
		{"www.example.test.", dns.TypeMX, dns.RcodeSuccess, nil, true, 0},
		{"www.example.test.", dns.TypeANY, dns.RcodeSuccess, []uint16{dns.TypeHINFO}, false, 0},
		{"example.test.", dns.TypeAXFR, dns.RcodeRefused, nil, false, 0},
		{"example.test.", dns.TypeIXFR, dns.RcodeRefused, nil, false, 0},
		{"notexample.test.", dns.TypeA, dns.RcodeRefused, nil, false, 0},
		{"loop.example.test.", dns.TypeA, dns.RcodeServerFailure, nil, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name+dns.TypeToString[tc.qtype], func(t *testing.T) {
			request := new(dns.Msg)
			request.SetQuestion(tc.name, tc.qtype)
			w := &responseRecorder{remote: &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 12345}}
			h.ServeDNS(w, request)
			response := w.msg
			if response == nil || response.Rcode != tc.rcode || !response.Authoritative || response.RecursionAvailable || response.Question[0] != request.Question[0] {
				t.Fatalf("response = %#v", response)
			}
			if len(response.Answer) != len(tc.types) {
				t.Fatalf("answers = %v, want types %v", response.Answer, tc.types)
			}
			for i, kind := range tc.types {
				if response.Answer[i].Header().Rrtype != kind {
					t.Fatalf("answer %d = %v", i, response.Answer[i])
				}
			}
			if len(response.Answer) > 0 && response.Answer[0].Header().Name != tc.name {
				t.Fatalf("question case lost: %v", response.Answer[0])
			}
			if tc.negative {
				if len(response.Ns) != 1 || response.Ns[0].Header().Rrtype != dns.TypeSOA || response.Ns[0].Header().Ttl != 30 {
					t.Fatalf("negative SOA = %v", response.Ns)
				}
			} else if len(response.Ns) != 0 {
				t.Fatalf("unexpected authority: %v", response.Ns)
			}
			if len(response.Extra) != tc.glue {
				t.Fatalf("additional = %v, want %d", response.Extra, tc.glue)
			}
			if tc.qtype == dns.TypeANY {
				hinfo := response.Answer[0].(*dns.HINFO)
				if hinfo.Cpu != "RFC8482" || hinfo.Os != "" {
					t.Fatalf("HINFO = %v", hinfo)
				}
			}
		})
	}
}

func TestCNAMEChainLimit(t *testing.T) {
	for _, links := range []int{8, 9} {
		t.Run(fmt.Sprint(links), func(t *testing.T) {
			zone := testZone()
			for i := range links {
				zone.Spec.Records = append(zone.Spec.Records, hostedv1alpha1.HostedZoneRecord{Name: fmt.Sprintf("c%d", i), Type: "CNAME", TTL: 60, Values: []string{fmt.Sprintf("c%d.example.test.", i+1)}})
			}
			zone.Spec.Records = append(zone.Spec.Records, hostedv1alpha1.HostedZoneRecord{Name: fmt.Sprintf("c%d", links), Type: "A", TTL: 60, Values: []string{"192.0.2.1"}})
			request := new(dns.Msg)
			request.SetQuestion("c0.example.test.", dns.TypeA)
			response, _ := testHandler(t, zone).answer(request)
			if links == 8 && (response.Rcode != dns.RcodeSuccess || len(response.Answer) != 9) {
				t.Fatalf("8 links = %v", response)
			}
			if links == 9 && (response.Rcode != dns.RcodeServerFailure || len(response.Answer) != 0) {
				t.Fatalf("9 links = %v", response)
			}
		})
	}
}

func TestLoopbackUDPAndTCP(t *testing.T) {
	zone := testZone()
	values := make([]string, 16)
	for i := range values {
		values[i] = fmt.Sprintf(`"%02d%s"`, i, strings.Repeat("x", 180))
	}
	zone.Spec.Records = append(zone.Spec.Records, hostedv1alpha1.HostedZoneRecord{Name: "large", Type: "TXT", TTL: 60, Values: values})
	h := testHandler(t, zone)
	address := serveLoopback(t, h)
	for _, tc := range []struct {
		network   string
		size      uint16
		truncated bool
	}{
		{"udp", 0, true}, {"udp", 600, true}, {"udp", 4096, true}, {"tcp", 512, false},
	} {
		t.Run(fmt.Sprintf("%s-%d", tc.network, tc.size), func(t *testing.T) {
			request := new(dns.Msg)
			request.SetQuestion("LaRgE.Example.Test.", dns.TypeTXT)
			if tc.size != 0 {
				request.SetEdns0(tc.size, false)
			}
			client := &dns.Client{Net: tc.network, UDPSize: 4096, Timeout: 2 * time.Second}
			connection, err := client.Dial(address)
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			if err := connection.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if err := connection.WriteMsg(request); err != nil {
				t.Fatal(err)
			}
			wire, err := connection.ReadMsgHeader(nil)
			if err != nil {
				t.Fatal(err)
			}
			response := new(dns.Msg)
			if err := response.Unpack(wire); err != nil {
				t.Fatal(err)
			}
			if response.Truncated != tc.truncated || !response.Authoritative || response.RecursionAvailable {
				t.Fatalf("response = %v", response)
			}
			if tc.network == "tcp" && len(response.Answer) != 16 {
				t.Fatalf("TCP omitted records: %d", len(response.Answer))
			}
			if tc.network == "udp" {
				limit := 512
				if tc.size != 0 {
					limit = min(1232, max(512, int(tc.size)))
				}
				if err != nil || len(wire) > limit {
					t.Fatalf("UDP size %d > %d: %v", len(wire), limit, err)
				}
			}
			if tc.size != 0 && (response.IsEdns0() == nil || response.IsEdns0().UDPSize() != 1232) {
				t.Fatalf("EDNS = %v", response.IsEdns0())
			}
		})
	}
}

func serveLoopback(t *testing.T, h dns.Handler) string {
	t.Helper()
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	udp, err := net.ListenPacket("udp", tcp.Addr().String())
	if err != nil {
		tcp.Close()
		t.Fatal(err)
	}
	for _, server := range []*dns.Server{{Listener: tcp, Handler: h}, {PacketConn: udp, Handler: h, UDPSize: 1232}} {
		ready := make(chan struct{})
		server.NotifyStartedFunc = func() { close(ready) }
		go func() {
			if err := server.ActivateAndServe(); err != nil {
				t.Errorf("DNS serve: %v", err)
			}
		}()
		select {
		case <-ready:
		case <-time.After(2 * time.Second):
			t.Fatal("DNS listener not ready")
		}
		t.Cleanup(func() { _ = server.Shutdown() })
	}
	return tcp.Addr().String()
}

type responseRecorder struct {
	remote net.Addr
	msg    *dns.Msg
}

func (w *responseRecorder) LocalAddr() net.Addr            { return &net.UDPAddr{Port: 5353} }
func (w *responseRecorder) RemoteAddr() net.Addr           { return w.remote }
func (w *responseRecorder) WriteMsg(msg *dns.Msg) error    { w.msg = msg.Copy(); return nil }
func (w *responseRecorder) Write(data []byte) (int, error) { return len(data), nil }
func (w *responseRecorder) Close() error                   { return nil }
func (w *responseRecorder) TsigStatus() error              { return nil }
func (w *responseRecorder) TsigTimersOnly(bool)            {}
func (w *responseRecorder) Hijack()                        {}
