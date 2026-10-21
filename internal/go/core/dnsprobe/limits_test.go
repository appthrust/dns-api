package dnsprobe

import (
	"context"
	"fmt"
	"math"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/miekg/dns"
)

func TestWireChainBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		links   int
		loop    bool
		failure string
	}{
		{"eight", 8, false, ""},
		{"nine", 9, false, "chain-limit"},
		{"loop", 2, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := localDNS(t, func(w dns.ResponseWriter, request *dns.Msg) {
				reply := new(dns.Msg)
				reply.SetReply(request)
				question := request.Question[0]
				var link int
				if _, err := fmt.Sscanf(question.Name, "link-%d.example.test.", &link); err != nil {
					t.Error(err)
				}
				if link < tc.links {
					next := link + 1
					if tc.loop && next == tc.links {
						next = 0
					}
					reply.Answer = []dns.RR{&dns.CNAME{Hdr: dns.RR_Header{Name: question.Name, Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: 60}, Target: fmt.Sprintf("link-%d.example.test.", next)}}
				} else if question.Qtype == dns.TypeA {
					reply.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: question.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 120}, A: net.ParseIP("192.0.2.1")}}
				}
				_ = w.WriteMsg(reply)
			})
			resolver, _ := NewWireResolver(WireOptions{})
			session := wireSession{resolver: resolver}
			view := session.resolve(context.Background(), "link-0.example.test", server, "", "example.test")
			if view.Failure != tc.failure || view.Mismatch != tc.loop || (tc.failure == "" && !tc.loop && (len(view.A) != 1 || view.A[0] != "192.0.2.1")) {
				t.Fatalf("view=%#v", view)
			}
		})
	}
}

func TestForeignCNAMEUsesSelectedRecursiveView(t *testing.T) {
	var authorityQueries, recursiveQueries atomic.Int32
	authority := localDNS(t, func(w dns.ResponseWriter, request *dns.Msg) {
		authorityQueries.Add(1)
		if request.RecursionDesired || request.Question[0].Name != "www.example.test." {
			t.Errorf("unexpected authority query %#v", request)
		}
		reply := new(dns.Msg)
		reply.SetReply(request)
		reply.Authoritative = true
		reply.Answer = []dns.RR{&dns.CNAME{Hdr: dns.RR_Header{Name: "www.example.test.", Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: 60}, Target: "target.foreign.test."}}
		_ = w.WriteMsg(reply)
	})
	recursive := localDNS(t, func(w dns.ResponseWriter, request *dns.Msg) {
		recursiveQueries.Add(1)
		if !request.RecursionDesired || request.Question[0].Name != "target.foreign.test." {
			t.Errorf("unexpected recursive query %#v", request)
		}
		reply := new(dns.Msg)
		reply.SetReply(request)
		if request.Question[0].Qtype == dns.TypeA {
			reply.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: "target.foreign.test.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 120}, A: net.ParseIP("192.0.2.1")}}
		}
		_ = w.WriteMsg(reply)
	})
	resolver, _ := NewWireResolver(WireOptions{})
	session := wireSession{resolver: resolver, authorityAnswers: map[questionKey]*dns.Msg{}}
	view := session.resolve(context.Background(), "www.example.test", recursive, authority, "example.test")
	if view.Failure != "" || !view.Authoritative || len(view.A) != 1 || authorityQueries.Load() != 2 || recursiveQueries.Load() != 2 {
		t.Fatalf("view=%#v authority=%d recursive=%d", view, authorityQueries.Load(), recursiveQueries.Load())
	}
	// Reusing this name's in-zone evidence for another recursive suffix walk sends no duplicate authority exchanges.
	session.resolve(context.Background(), "www.example.test", recursive, authority, "example.test")
	if authorityQueries.Load() != 2 {
		t.Fatal("in-zone authority work multiplied by recursive views")
	}
}

func TestMalformedWireResponseCannotVerify(t *testing.T) {
	server := localDNS(t, func(w dns.ResponseWriter, request *dns.Msg) {
		reply := new(dns.Msg)
		reply.SetReply(request)
		reply.Opcode = dns.OpcodeUpdate
		_ = w.WriteMsg(reply)
	})
	resolver, _ := NewWireResolver(WireOptions{})
	session := wireSession{resolver: resolver}
	_, failure := session.exchange(context.Background(), server, "www.example.test", dns.TypeA, true)
	if failure == "" {
		t.Fatal("invalid opcode was accepted")
	}
}

func TestLimiterDefaultsAndClosedFailureCodes(t *testing.T) {
	resolver, err := NewWireResolver(WireOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if resolver.limiter.Limit() != 40 || resolver.limiter.Burst() != 80 || cap(resolver.slots) != 4 {
		t.Fatal("manager-wide bounds differ from contract")
	}
	for _, qps := range []float64{-1, math.NaN(), math.Inf(1)} {
		if _, err := NewWireResolver(WireOptions{QPS: qps}); err == nil {
			t.Fatal("invalid limiter accepted")
		}
	}
	decision := Failed("www.example.test", "view", "raw secret-containing diagnostic")
	if strings.Contains(decision.Message, "secret") || !strings.HasSuffix(decision.Message, "transport.") {
		t.Fatalf("message=%s", decision.Message)
	}
}

func TestNameserverAddressFanoutFailsClosed(t *testing.T) {
	var queries atomic.Int32
	server := localDNS(t, func(w dns.ResponseWriter, request *dns.Msg) {
		queries.Add(1)
		if !request.RecursionDesired || request.Opcode != dns.OpcodeQuery {
			t.Error("NS discovery must be recursive QUERY")
		}
		reply := new(dns.Msg)
		reply.SetReply(request)
		question := request.Question[0]
		switch question.Qtype {
		case dns.TypeNS:
			reply.Answer = []dns.RR{&dns.NS{Hdr: dns.RR_Header{Name: "example.test.", Rrtype: dns.TypeNS, Class: dns.ClassINET, Ttl: 300}, Ns: "ns.example.test."}}
		case dns.TypeSOA:
			reply.Answer = []dns.RR{&dns.SOA{Hdr: dns.RR_Header{Name: "example.test.", Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: 300}, Ns: "ns.example.test.", Mbox: "hostmaster.example.test.", Minttl: 60}}
		case dns.TypeA:
			for _, address := range []string{"127.0.0.2", "127.0.0.3", "127.0.0.4"} {
				reply.Answer = append(reply.Answer, &dns.A{Hdr: dns.RR_Header{Name: question.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300}, A: net.ParseIP(address)})
			}
		}
		_ = w.WriteMsg(reply)
	})
	resolver, _ := NewWireResolver(WireOptions{})
	request := ProbeRequest{Zone: "example.test", Name: "example.test", ZoneOnly: true}
	request.Policy.Servers = []string{server}
	request.Policy.ExpectedNameservers = []string{"ns.example.test"}
	result, err := resolver.Probe(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	decision := EvaluateZone(request, result.Zone)
	if decision.Reason != "DNSProbeFailed" || !strings.Contains(decision.Message, "probe-limit") || queries.Load() != 4 {
		t.Fatalf("decision=%#v queries=%d", decision, queries.Load())
	}
}
