package dnsprobe

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func localDNS(t *testing.T, handler dns.HandlerFunc) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	packet, err := net.ListenPacket("udp", listener.Addr().String())
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	udpReady, tcpReady := make(chan struct{}), make(chan struct{})
	udp := &dns.Server{PacketConn: packet, Handler: handler, NotifyStartedFunc: func() { close(udpReady) }}
	tcp := &dns.Server{Listener: listener, Handler: handler, NotifyStartedFunc: func() { close(tcpReady) }}
	go func() { _ = udp.ActivateAndServe() }()
	go func() { _ = tcp.ActivateAndServe() }()
	t.Cleanup(func() { _ = udp.Shutdown(); _ = tcp.Shutdown() })
	for _, ready := range []chan struct{}{udpReady, tcpReady} {
		select {
		case <-ready:
		case <-time.After(2 * time.Second):
			t.Fatal("fixture did not start")
		}
	}
	return listener.Addr().String()
}

func TestWireTruncationAndNegativeSOA(t *testing.T) {
	var udpQueries, tcpQueries atomic.Int32
	server := localDNS(t, func(w dns.ResponseWriter, request *dns.Msg) {
		if request.Opcode != dns.OpcodeQuery || !request.RecursionDesired || !strings.HasSuffix(request.Question[0].Name, ".") {
			t.Errorf("not an absolute recursive QUERY: %#v", request)
		}
		reply := new(dns.Msg)
		reply.SetReply(request)
		if _, ok := w.RemoteAddr().(*net.UDPAddr); ok {
			udpQueries.Add(1)
			reply.Truncated = true
		} else {
			tcpQueries.Add(1)
			reply.Rcode = dns.RcodeNameError
			reply.Ns = []dns.RR{&dns.SOA{Hdr: dns.RR_Header{Name: "example.test.", Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: 600}, Ns: "ns.example.test.", Mbox: "hostmaster.example.test.", Minttl: 120}}
		}
		_ = w.WriteMsg(reply)
	})
	resolver, err := NewWireResolver(WireOptions{})
	if err != nil {
		t.Fatal(err)
	}
	request := ProbeRequest{Name: "absent.example.test", Policy: Policy{Mode: ModeRecursive, Servers: []string{server}}}
	result, err := resolver.Probe(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Views) != 1 || result.Views[0].RCode != dns.RcodeNameError || result.Views[0].NegativeTTL == nil || *result.Views[0].NegativeTTL != 120 || len(result.Views[0].Answers) != 2 {
		t.Fatalf("result = %#v", result)
	}
	if udpQueries.Load() != 2 || tcpQueries.Load() != 2 {
		t.Fatalf("UDP=%d TCP=%d", udpQueries.Load(), tcpQueries.Load())
	}
}

func TestWireCNAMEAndAuthorityRD(t *testing.T) {
	server := localDNS(t, func(w dns.ResponseWriter, request *dns.Msg) {
		if request.Opcode != dns.OpcodeQuery {
			t.Error("DNS mutation opcode")
		}
		if request.RecursionDesired {
			t.Error("authority query requested recursion")
		}
		reply := new(dns.Msg)
		reply.SetReply(request)
		reply.Authoritative = true
		question := request.Question[0]
		if question.Name == "www.example.test." {
			reply.Answer = []dns.RR{&dns.CNAME{Hdr: dns.RR_Header{Name: question.Name, Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: 42}, Target: "target.example.test."}}
		} else if question.Qtype == dns.TypeA {
			reply.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: question.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300}, A: net.ParseIP("192.0.2.1")}}
		}
		_ = w.WriteMsg(reply)
	})
	resolver, _ := NewWireResolver(WireOptions{})
	session := wireSession{resolver: resolver}
	view := session.resolve(context.Background(), "www.example.test", "192.0.2.53:53", server, "example.test")
	if view.Failure != "" || len(view.Chain) != 1 || len(view.A) != 1 || !view.Authoritative || view.TTL == nil || *view.TTL != 42 {
		t.Fatalf("view = %#v", view)
	}
}

func TestWireNoFallbackAndCancellation(t *testing.T) {
	var queries atomic.Int32
	server := localDNS(t, func(w dns.ResponseWriter, request *dns.Msg) {
		queries.Add(1)
		reply := new(dns.Msg)
		reply.SetReply(request)
		reply.Rcode = dns.RcodeRefused
		_ = w.WriteMsg(reply)
	})
	resolver, _ := NewWireResolver(WireOptions{})
	request := ProbeRequest{Name: "www.example.test", Policy: Policy{Servers: []string{server}}}
	result, err := resolver.Probe(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Views) != 1 || result.Views[0].Failure != "REFUSED" || queries.Load() != 1 {
		t.Fatalf("result = %#v, queries=%d", result, queries.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := resolver.Probe(ctx, request); err == nil {
		t.Fatal("canceled probe succeeded")
	}
	if queries.Load() != 1 {
		t.Fatal("canceled probe sent DNS")
	}
}

func TestClusterPolicyRereadAndBounds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resolv.conf")
	if err := os.WriteFile(path, []byte("nameserver 192.0.2.53\nsearch must.not.expand\n"), 0600); err != nil {
		t.Fatal(err)
	}
	resolver, _ := NewWireResolver(WireOptions{ResolvConf: path})
	first, err := resolver.EffectivePolicy(Policy{})
	if err != nil || len(first.Servers) != 1 || first.Servers[0] != "192.0.2.53:53" {
		t.Fatalf("policy=%#v error=%v", first, err)
	}
	if err := os.WriteFile(path, []byte("nameserver 192.0.2.54\n"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := resolver.EffectivePolicy(Policy{})
	if err != nil || Fingerprint(first) == Fingerprint(second) {
		t.Fatal("resolv.conf change did not invalidate policy")
	}
	if err := os.WriteFile(path, []byte("nameserver 192.0.2.1\nnameserver 192.0.2.2\nnameserver 192.0.2.3\nnameserver 192.0.2.4\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.EffectivePolicy(Policy{}); err == nil {
		t.Fatal("oversized resolver set accepted")
	}
}
