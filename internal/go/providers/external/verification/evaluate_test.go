package verification

import (
	"fmt"
	"testing"

	"github.com/appthrust/dns-api/internal/go/core/dnsprobe"
	"github.com/miekg/dns"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestExactVerification(t *testing.T) {
	cases := []struct {
		name     string
		expected dnsprobe.RRsets
		views    []dnsprobe.View
		reason   string
	}{
		{"exact", dnsprobe.RRsets{A: []string{"192.0.2.1"}}, []dnsprobe.View{{A: []string{"192.0.2.1"}}}, "ResolutionVerified"},
		{"normalized duplicate IPv6", dnsprobe.RRsets{AAAA: []string{"2001:db8::1"}}, []dnsprobe.View{{AAAA: []string{"2001:0DB8:0:0::1", "2001:db8::1"}}}, "ResolutionVerified"},
		{"unexpected family", dnsprobe.RRsets{A: []string{"192.0.2.1"}}, []dnsprobe.View{{A: []string{"192.0.2.1"}, AAAA: []string{"2001:db8::1"}}}, "ResolutionMismatch"},
		{"partial", dnsprobe.RRsets{A: []string{"192.0.2.1", "192.0.2.2"}}, []dnsprobe.View{{A: []string{"192.0.2.1"}}}, "ResolutionPartial"},
		{"wrong", dnsprobe.RRsets{A: []string{"192.0.2.1"}}, []dnsprobe.View{{A: []string{"192.0.2.2"}}}, "ResolutionMismatch"},
		{"NXDOMAIN", dnsprobe.RRsets{A: []string{"192.0.2.1"}}, []dnsprobe.View{{RCode: dns.RcodeNameError}}, "DNSNameNotFound"},
		{"NODATA", dnsprobe.RRsets{A: []string{"192.0.2.1"}}, []dnsprobe.View{{}}, "DNSNoData"},
		{"inconsistent", dnsprobe.RRsets{A: []string{"192.0.2.1"}}, []dnsprobe.View{{ID: "a", A: []string{"192.0.2.1"}}, {ID: "b", RCode: dns.RcodeNameError}}, "ResolutionInconsistent"},
		{"failure beats mismatch", dnsprobe.RRsets{A: []string{"192.0.2.1"}}, []dnsprobe.View{{ID: "a", A: []string{"192.0.2.2"}}, {ID: "b", Failure: "timeout"}}, "DNSProbeFailed"},
		{"SERVFAIL", dnsprobe.RRsets{A: []string{"192.0.2.1"}}, []dnsprobe.View{{RCode: dns.RcodeServerFailure}}, "DNSProbeFailed"},
		{"CNAME to IP", dnsprobe.RRsets{A: []string{"192.0.2.1"}}, []dnsprobe.View{{Chain: []string{"TARGET.EXAMPLE."}, A: []string{"192.0.2.1"}}}, "ResolutionVerified"},
		{"CNAME identity", dnsprobe.RRsets{CNAME: "target.example"}, []dnsprobe.View{{Chain: []string{"TARGET.EXAMPLE."}, A: []string{"192.0.2.1"}}}, "ResolutionVerified"},
		{"wrong CNAME same IP", dnsprobe.RRsets{CNAME: "target.example"}, []dnsprobe.View{{Chain: []string{"other.example"}, A: []string{"192.0.2.1"}}}, "ResolutionMismatch"},
		{"unusable CNAME", dnsprobe.RRsets{CNAME: "target.example"}, []dnsprobe.View{{Chain: []string{"target.example"}}}, "DNSNoData"},
		{"CNAME loop", dnsprobe.RRsets{A: []string{"192.0.2.1"}}, []dnsprobe.View{{Chain: []string{"target.example", "www.example"}, A: []string{"192.0.2.1"}}}, "ResolutionMismatch"},
		{"incompatible CNAME", dnsprobe.RRsets{CNAME: "target.example"}, []dnsprobe.View{{Chain: []string{"target.example"}, A: []string{"192.0.2.1"}, Mismatch: true}}, "ResolutionMismatch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision := Evaluate(dnsprobe.ProbeRequest{Name: "www.example", Expected: tc.expected}, dnsprobe.ProbeResult{Views: tc.views})
			if decision.Reason != tc.reason {
				t.Fatalf("decision = %#v, want %s", decision, tc.reason)
			}
			if len(decision.Message) > 2048 {
				t.Fatal("unbounded message")
			}
		})
	}
}

func TestCNAMEChainBoundary(t *testing.T) {
	for _, links := range []int{8, 9} {
		chain := make([]string, links)
		for i := range chain {
			chain[i] = fmt.Sprintf("link-%d.example", i)
		}
		decision := Evaluate(dnsprobe.ProbeRequest{Name: "www.example", Expected: dnsprobe.RRsets{A: []string{"192.0.2.1"}}}, dnsprobe.ProbeResult{Views: []dnsprobe.View{{Chain: chain, A: []string{"192.0.2.1"}}}})
		want := "ResolutionVerified"
		if links == 9 {
			want = "DNSProbeFailed"
		}
		if decision.Reason != want {
			t.Fatalf("links=%d: %#v", links, decision)
		}
	}
}

func TestNameserverEvidence(t *testing.T) {
	request := dnsprobe.ProbeRequest{Zone: "example.test", Policy: dnsprobe.Policy{Servers: []string{"192.0.2.53:53"}, ExpectedNameservers: []string{"ns.example.test"}}}
	good := dnsprobe.AuthorityObservation{ID: "recursive/192.0.2.53:53", Nameservers: []string{"NS.EXAMPLE.TEST."}, SOAOwner: "example.test"}
	authority := dnsprobe.AuthorityObservation{ID: "authority/192.0.2.54:53", Nameservers: []string{"ns.example.test"}, SOAOwner: "example.test", Authoritative: true}
	cases := []struct {
		name   string
		mutate func(*dnsprobe.ZoneEvidence)
		reason string
	}{
		{"verified", func(*dnsprobe.ZoneEvidence) {}, "NameserversVerified"},
		{"non apex", func(e *dnsprobe.ZoneEvidence) { e.Views[0].SOAOwner = "test"; e.Views[1].SOAOwner = "test" }, "NameserverMismatch"},
		{"NS mismatch", func(e *dnsprobe.ZoneEvidence) {
			for i := range e.Views {
				e.Views[i].Nameservers = []string{"wrong.example.test"}
			}
		}, "NameserverMismatch"},
		{"inconsistent", func(e *dnsprobe.ZoneEvidence) { e.Views[1].Nameservers = []string{"wrong.example.test"} }, "ResolutionInconsistent"},
		{"missing AA", func(e *dnsprobe.ZoneEvidence) { e.Views[1].Authoritative = false }, "DNSProbeFailed"},
		{"failed authority", func(e *dnsprobe.ZoneEvidence) { e.Views[1].Failure = "REFUSED" }, "DNSProbeFailed"},
		{"missing authority", func(e *dnsprobe.ZoneEvidence) { e.Views = e.Views[:1] }, "DNSProbeFailed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			evidence := &dnsprobe.ZoneEvidence{Views: []dnsprobe.AuthorityObservation{good, authority}, Authorities: []string{"192.0.2.54:53"}}
			tc.mutate(evidence)
			decision := dnsprobe.EvaluateZone(request, evidence)
			if decision.Reason != tc.reason {
				t.Fatalf("decision = %#v, want %s", decision, tc.reason)
			}
		})
	}
	request.Policy.ExpectedNameservers = nil
	if decision := dnsprobe.EvaluateZone(request, nil); decision.Status != metav1.ConditionTrue || len(decision.Nameservers) != 0 {
		t.Fatalf("suffix = %#v", decision)
	}
}

func TestStatusPreviewDoesNotAffectEquality(t *testing.T) {
	values := make([]string, 20)
	for i := range values {
		values[i] = fmt.Sprintf("192.0.2.%d", i+1)
	}
	observed := append([]string(nil), values...)
	observed[19] = "198.51.100.1"
	decision := Evaluate(dnsprobe.ProbeRequest{Name: "www.example", Expected: dnsprobe.RRsets{A: values}}, dnsprobe.ProbeResult{Views: []dnsprobe.View{{A: observed}}})
	if decision.Reason != "ResolutionMismatch" {
		t.Fatalf("decision = %#v", decision)
	}
	preview := dnsprobe.Preview(decision.Actual)
	if preview.Total != 20 || !preview.Truncated || len(preview.Values) != 16 {
		t.Fatalf("preview = %#v", preview)
	}
}
