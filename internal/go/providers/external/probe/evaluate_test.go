package probe

import (
	"fmt"
	"testing"

	externalv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/external/v1alpha1"
	"github.com/miekg/dns"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestExactVerification(t *testing.T) {
	cases := []struct {
		name     string
		expected RRsets
		views    []View
		reason   string
	}{
		{"exact", RRsets{A: []string{"192.0.2.1"}}, []View{{A: []string{"192.0.2.1"}}}, "ResolutionVerified"},
		{"normalized duplicate IPv6", RRsets{AAAA: []string{"2001:db8::1"}}, []View{{AAAA: []string{"2001:0DB8:0:0::1", "2001:db8::1"}}}, "ResolutionVerified"},
		{"unexpected family", RRsets{A: []string{"192.0.2.1"}}, []View{{A: []string{"192.0.2.1"}, AAAA: []string{"2001:db8::1"}}}, "ResolutionMismatch"},
		{"partial", RRsets{A: []string{"192.0.2.1", "192.0.2.2"}}, []View{{A: []string{"192.0.2.1"}}}, "ResolutionPartial"},
		{"wrong", RRsets{A: []string{"192.0.2.1"}}, []View{{A: []string{"192.0.2.2"}}}, "ResolutionMismatch"},
		{"NXDOMAIN", RRsets{A: []string{"192.0.2.1"}}, []View{{RCode: dns.RcodeNameError}}, "DNSNameNotFound"},
		{"NODATA", RRsets{A: []string{"192.0.2.1"}}, []View{{}}, "DNSNoData"},
		{"inconsistent", RRsets{A: []string{"192.0.2.1"}}, []View{{ID: "a", A: []string{"192.0.2.1"}}, {ID: "b", RCode: dns.RcodeNameError}}, "ResolutionInconsistent"},
		{"failure beats mismatch", RRsets{A: []string{"192.0.2.1"}}, []View{{ID: "a", A: []string{"192.0.2.2"}}, {ID: "b", Failure: "timeout"}}, "DNSProbeFailed"},
		{"SERVFAIL", RRsets{A: []string{"192.0.2.1"}}, []View{{RCode: dns.RcodeServerFailure}}, "DNSProbeFailed"},
		{"CNAME to IP", RRsets{A: []string{"192.0.2.1"}}, []View{{Chain: []string{"TARGET.EXAMPLE."}, A: []string{"192.0.2.1"}}}, "ResolutionVerified"},
		{"CNAME identity", RRsets{CNAME: "target.example"}, []View{{Chain: []string{"TARGET.EXAMPLE."}, A: []string{"192.0.2.1"}}}, "ResolutionVerified"},
		{"wrong CNAME same IP", RRsets{CNAME: "target.example"}, []View{{Chain: []string{"other.example"}, A: []string{"192.0.2.1"}}}, "ResolutionMismatch"},
		{"unusable CNAME", RRsets{CNAME: "target.example"}, []View{{Chain: []string{"target.example"}}}, "DNSNoData"},
		{"CNAME loop", RRsets{A: []string{"192.0.2.1"}}, []View{{Chain: []string{"target.example", "www.example"}, A: []string{"192.0.2.1"}}}, "ResolutionMismatch"},
		{"incompatible CNAME", RRsets{CNAME: "target.example"}, []View{{Chain: []string{"target.example"}, A: []string{"192.0.2.1"}, Mismatch: true}}, "ResolutionMismatch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision := Evaluate(ProbeRequest{Name: "www.example", Expected: tc.expected}, ProbeResult{Views: tc.views})
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
		decision := Evaluate(ProbeRequest{Name: "www.example", Expected: RRsets{A: []string{"192.0.2.1"}}}, ProbeResult{Views: []View{{Chain: chain, A: []string{"192.0.2.1"}}}})
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
	request := ProbeRequest{Zone: "example.test", Policy: externalv1alpha1.ExternalZoneClassParameters{Resolver: externalv1alpha1.ResolverPolicy{Servers: []string{"192.0.2.53:53"}}, ExpectedNameservers: []string{"ns.example.test"}}}
	good := AuthorityObservation{ID: "recursive/192.0.2.53:53", Nameservers: []string{"NS.EXAMPLE.TEST."}, SOAOwner: "example.test"}
	authority := AuthorityObservation{ID: "authority/192.0.2.54:53", Nameservers: []string{"ns.example.test"}, SOAOwner: "example.test", Authoritative: true}
	cases := []struct {
		name   string
		mutate func(*ZoneEvidence)
		reason string
	}{
		{"verified", func(*ZoneEvidence) {}, "NameserversVerified"},
		{"non apex", func(e *ZoneEvidence) { e.Views[0].SOAOwner = "test"; e.Views[1].SOAOwner = "test" }, "NameserverMismatch"},
		{"NS mismatch", func(e *ZoneEvidence) {
			for i := range e.Views {
				e.Views[i].Nameservers = []string{"wrong.example.test"}
			}
		}, "NameserverMismatch"},
		{"inconsistent", func(e *ZoneEvidence) { e.Views[1].Nameservers = []string{"wrong.example.test"} }, "ResolutionInconsistent"},
		{"missing AA", func(e *ZoneEvidence) { e.Views[1].Authoritative = false }, "DNSProbeFailed"},
		{"failed authority", func(e *ZoneEvidence) { e.Views[1].Failure = "REFUSED" }, "DNSProbeFailed"},
		{"missing authority", func(e *ZoneEvidence) { e.Views = e.Views[:1] }, "DNSProbeFailed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			evidence := &ZoneEvidence{Views: []AuthorityObservation{good, authority}, Authorities: []string{"192.0.2.54:53"}}
			tc.mutate(evidence)
			decision := EvaluateZone(request, evidence)
			if decision.Reason != tc.reason {
				t.Fatalf("decision = %#v, want %s", decision, tc.reason)
			}
		})
	}
	request.Policy.ExpectedNameservers = nil
	if decision := EvaluateZone(request, nil); decision.Status != metav1.ConditionTrue || len(decision.Nameservers) != 0 {
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
	decision := Evaluate(ProbeRequest{Name: "www.example", Expected: RRsets{A: values}}, ProbeResult{Views: []View{{A: observed}}})
	if decision.Reason != "ResolutionMismatch" {
		t.Fatalf("decision = %#v", decision)
	}
	preview := Preview(decision.Actual)
	if preview.Total != 20 || !preview.Truncated || len(preview.Values) != 16 {
		t.Fatalf("preview = %#v", preview)
	}
}
