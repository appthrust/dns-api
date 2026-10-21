package dnsprobe

import (
	"fmt"
	"net"
	"slices"
	"testing"

	"github.com/miekg/dns"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func withdrawalView(id string, a, aaaa []string, ttl uint32) View {
	return View{ID: id, A: a, AAAA: aaaa, Answers: []AnswerEvidence{
		{Name: "www.example.test", QType: "A", Addresses: a, PositiveTTL: &ttl, NegativeTTL: &ttl},
		{Name: "www.example.test", QType: "AAAA", Addresses: aaaa, PositiveTTL: &ttl, NegativeTTL: &ttl},
	}}
}

func TestEvaluateWithdrawal(t *testing.T) {
	request := ProbeRequest{Name: "www.example.test", Zone: "example.test", WithdrawalAddress: "192.0.2.1", Policy: Policy{Servers: []string{"192.0.2.53:53", "192.0.2.54:53"}}}
	cases := []struct {
		name   string
		mutate func(*ProbeRequest, *[]View)
		status metav1.ConditionStatus
		reason string
	}{
		{"replacement views differ", func(_ *ProbeRequest, v *[]View) {
			(*v)[1] = withdrawalView((*v)[1].ID, []string{"192.0.2.3"}, nil, 600)
		}, metav1.ConditionTrue, "WithdrawalVerified"},
		{"old address", func(_ *ProbeRequest, v *[]View) {
			(*v)[1] = withdrawalView((*v)[1].ID, []string{"192.0.2.1"}, nil, 600)
		}, metav1.ConditionFalse, "DNSStillResolves"},
		{"opposite family mapped", func(_ *ProbeRequest, v *[]View) {
			(*v)[1] = withdrawalView((*v)[1].ID, nil, []string{"::ffff:192.0.2.1"}, 600)
		}, metav1.ConditionFalse, "DNSStillResolves"},
		{"IPv6", func(r *ProbeRequest, v *[]View) {
			r.WithdrawalAddress = "2001:db8::1"
			(*v)[1] = withdrawalView((*v)[1].ID, nil, []string{"2001:0DB8:0:0::1"}, 600)
		}, metav1.ConditionFalse, "DNSStillResolves"},
		{"missing view", func(_ *ProbeRequest, v *[]View) { *v = (*v)[:1] }, metav1.ConditionUnknown, "DNSProbeFailed"},
		{"missing family", func(_ *ProbeRequest, v *[]View) { (*v)[0].Answers = (*v)[0].Answers[:1] }, metav1.ConditionUnknown, "DNSProbeFailed"},
		{"missing SOA", func(_ *ProbeRequest, v *[]View) { (*v)[0].Answers[1].NegativeTTL = nil }, metav1.ConditionUnknown, "DNSProbeFailed"},
		{"failed view", func(_ *ProbeRequest, v *[]View) { (*v)[1].Failure = "timeout" }, metav1.ConditionUnknown, "DNSProbeFailed"},
		{"CNAME loop", func(_ *ProbeRequest, v *[]View) { (*v)[0].Chain = []string{"www.example.test"} }, metav1.ConditionUnknown, "DNSProbeFailed"},
		{"malformed address", func(_ *ProbeRequest, v *[]View) { (*v)[0].Answers[0].Addresses = []string{"not-an-address"} }, metav1.ConditionUnknown, "DNSProbeFailed"},
		{"NXDOMAIN", func(_ *ProbeRequest, v *[]View) {
			for i := range *v {
				(*v)[i] = withdrawalView((*v)[i].ID, nil, nil, 600)
				(*v)[i].RCode = dns.RcodeNameError
				for j := range (*v)[i].Answers {
					(*v)[i].Answers[j].RCode = dns.RcodeNameError
				}
			}
		}, metav1.ConditionTrue, "WithdrawalVerified"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := request
			views := []View{withdrawalView("recursive/192.0.2.53:53", []string{"192.0.2.2"}, nil, 120), withdrawalView("recursive/192.0.2.54:53", []string{"192.0.2.2"}, nil, 600)}
			tc.mutate(&r, &views)
			decision := EvaluateWithdrawal(r, ProbeResult{Views: views})
			if decision.Status != tc.status || decision.Reason != tc.reason {
				t.Fatalf("decision = %#v", decision)
			}
			if decision.Status == metav1.ConditionTrue && (decision.NegativeTTL == nil || *decision.NegativeTTL != 600) {
				t.Fatalf("maximum negative horizon lost: %#v", decision)
			}
			if len(decision.Message) > 2048 {
				t.Fatal("unbounded message")
			}
		})
	}
}

func TestWithdrawalEvidenceOrderStable(t *testing.T) {
	r := ProbeRequest{Name: "www.example.test", WithdrawalAddress: "192.0.2.1", Policy: Policy{Servers: []string{"a", "b"}}}
	result := ProbeResult{Views: []View{withdrawalView("recursive/a", nil, nil, 0), withdrawalView("recursive/b", []string{"192.0.2.3", "192.0.2.2"}, nil, 60)}}
	first := EvaluateWithdrawal(r, result)
	slices.Reverse(result.Views)
	for i := range result.Views {
		slices.Reverse(result.Views[i].Answers)
	}
	second := EvaluateWithdrawal(r, result)
	if Fingerprint(first) != Fingerprint(second) {
		t.Fatal("resolver order changed withdrawal evidence")
	}
}

func TestWithdrawalAuthorityAndChainEvidence(t *testing.T) {
	request := ProbeRequest{Name: "www.example.test", Zone: "example.test", WithdrawalAddress: "192.0.2.1",
		Policy: Policy{Servers: []string{"recursive"}, ExpectedNameservers: []string{"ns.example.test"}}}
	request.Evidence = &ZoneEvidence{Zone: request.Zone, PolicyFingerprint: Fingerprint(request.Policy), Authorities: []string{"192.0.2.53:53"}, Views: []AuthorityObservation{
		{ID: "recursive/recursive", Nameservers: []string{"ns.example.test"}, SOAOwner: request.Zone},
		{ID: "authority/192.0.2.53:53", Nameservers: []string{"ns.example.test"}, SOAOwner: request.Zone, Authoritative: true},
	}}
	for _, links := range []int{0, 8, 9} {
		result := ProbeResult{Views: []View{withdrawalView("recursive/recursive", nil, nil, 60), withdrawalView("authority/192.0.2.53:53/via/recursive", nil, nil, 3600)}}
		result.Views[1].Authoritative = true
		for i := range result.Views {
			view := &result.Views[i]
			terminal := view.Answers
			view.Answers = nil
			owner := request.Name
			for n := range links {
				target := fmt.Sprintf("link-%d.foreign.test", n)
				view.Chain = append(view.Chain, target)
				ttl := uint32(7200)
				for _, family := range []string{"A", "AAAA"} {
					view.Answers = append(view.Answers, AnswerEvidence{Name: owner, QType: family, CNAME: target, PositiveTTL: &ttl})
				}
				owner = target
			}
			for j := range terminal {
				terminal[j].Name = owner
			}
			view.Answers = append(view.Answers, terminal...)
		}
		decision := EvaluateWithdrawal(request, result)
		if links <= 8 && (decision.Status != metav1.ConditionTrue || decision.NegativeTTL == nil || *decision.NegativeTTL != 3600) {
			t.Fatalf("links=%d decision=%#v", links, decision)
		}
		if links > 8 && decision.Status != metav1.ConditionUnknown {
			t.Fatal("accepted overlong chain")
		}
		result.Views = result.Views[:1]
		if EvaluateWithdrawal(request, result).Status != metav1.ConditionUnknown {
			t.Fatal("missing authority authorized withdrawal")
		}
	}
}

func TestWithdrawalQueryCacheEvidence(t *testing.T) {
	for _, family := range []uint16{dns.TypeA, dns.TypeAAAA} {
		soa := &dns.SOA{Hdr: dns.RR_Header{Name: "example.test.", Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: 3600}, Ns: "ns.example.test.", Mbox: "hostmaster.example.test.", Minttl: 7200}
		reply := &dns.Msg{MsgHdr: dns.MsgHdr{Rcode: dns.RcodeNameError}, Ns: []dns.RR{soa}}
		evidence := answerEvidence(reply, "www.example.test", family)
		if evidence.NegativeTTL == nil || *evidence.NegativeTTL != 3600 || evidence.Failure != "" {
			t.Fatalf("negative evidence=%#v", evidence)
		}
		soa.Minttl = 0
		if got := answerEvidence(reply, "www.example.test", family); got.NegativeTTL == nil || *got.NegativeTTL != 0 {
			t.Fatal("zero negative TTL was not retained")
		}
		soa.Hdr.Name = "unrelated.test."
		if got := answerEvidence(reply, "www.example.test", family); got.Failure != "invalid-response" {
			t.Fatal("foreign SOA accepted")
		}
		reply.Ns = nil
		if got := answerEvidence(reply, "www.example.test", family); got.NegativeTTL != nil {
			t.Fatal("missing SOA invented zero TTL")
		}
	}
	reply := &dns.Msg{Answer: []dns.RR{
		&dns.A{Hdr: dns.RR_Header{Name: "www.example.test.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.ParseIP("192.0.2.1")},
		&dns.A{Hdr: dns.RR_Header{Name: "www.example.test.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 3600}, A: net.ParseIP("192.0.2.2")},
	}}
	if got := answerEvidence(reply, "www.example.test", dns.TypeA); got.PositiveTTL == nil || *got.PositiveTTL != 3600 {
		t.Fatal("positive cache horizon used minimum TTL")
	}
}

func TestWithdrawalFailureRetainsLaterPositiveHorizon(t *testing.T) {
	request := ProbeRequest{Name: "www.example.test", WithdrawalAddress: "192.0.2.1", Policy: Policy{Servers: []string{"a", "b"}}}
	result := ProbeResult{Views: []View{{ID: "recursive/a", Failure: "timeout"}, withdrawalView("recursive/b", []string{"192.0.2.1"}, nil, 3600)}}
	decision := EvaluateWithdrawal(request, result)
	if decision.Status != metav1.ConditionUnknown || len(decision.Cache) == 0 || decision.Cache[0].PositiveTTL == nil || *decision.Cache[0].PositiveTTL != 3600 {
		t.Fatalf("lost positive evidence=%#v", decision)
	}
}
