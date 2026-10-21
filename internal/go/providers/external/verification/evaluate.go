// Package verification owns external-provider exact-set publication semantics.
package verification

import (
	"fmt"
	"slices"
	"strings"

	"github.com/appthrust/dns-api/internal/go/core/dnsprobe"
	"github.com/miekg/dns"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func Evaluate(request dnsprobe.ProbeRequest, result dnsprobe.ProbeResult) dnsprobe.Decision {
	if len(result.Views) == 0 || len(result.Views) < len(request.Policy.Servers) {
		return dnsprobe.Failed(request.Name, "required-views", "invalid-response")
	}
	for _, server := range request.Policy.Servers {
		if !slices.ContainsFunc(result.Views, func(view dnsprobe.View) bool { return view.ID == "recursive/"+server }) {
			return dnsprobe.Failed(request.Name, "recursive/"+server, "invalid-response")
		}
	}
	if len(request.Policy.ExpectedNameservers) != 0 {
		if request.Evidence == nil || len(request.Evidence.Authorities) == 0 {
			return dnsprobe.Failed(request.Name, "required-authorities", "invalid-response")
		}
		for _, authority := range request.Evidence.Authorities {
			if !slices.ContainsFunc(result.Views, func(view dnsprobe.View) bool {
				return strings.HasPrefix(view.ID, "authority/"+authority+"/via/") && view.Authoritative
			}) {
				return dnsprobe.Failed(request.Name, "authority/"+authority, "invalid-response")
			}
		}
	}
	views := slices.Clone(result.Views)
	slices.SortFunc(views, func(a, b dnsprobe.View) int { return strings.Compare(a.ID, b.ID) })
	decision := dnsprobe.Decision{Status: metav1.ConditionTrue, Reason: "ResolutionVerified"}
	for i := range views {
		view := &views[i]
		view.A, view.AAAA = dnsprobe.Addresses(view.A), dnsprobe.Addresses(view.AAAA)
		view.Chain = slices.Clone(view.Chain)
		seen := map[string]bool{dnsprobe.CanonicalName(request.Name): true}
		for j, name := range view.Chain {
			name = dnsprobe.CanonicalName(name)
			view.Chain[j] = name
			if seen[name] {
				view.Mismatch = true
			}
			seen[name] = true
		}
		if len(view.Chain) > dnsprobe.MaxCNAMEs {
			view.Failure = "chain-limit"
		}
		if view.Failure != "" {
			return dnsprobe.Failed(request.Name, view.ID, view.Failure)
		}
		if view.RCode != dns.RcodeSuccess && view.RCode != dns.RcodeNameError {
			failure := "invalid-response"
			if view.RCode == dns.RcodeServerFailure {
				failure = "SERVFAIL"
			}
			if view.RCode == dns.RcodeRefused {
				failure = "REFUSED"
			}
			return dnsprobe.Failed(request.Name, view.ID, failure)
		}
		decision.Views = append(decision.Views, view.ID)
		decision.TTL = dnsprobe.MinTTL(decision.TTL, view.TTL)
		decision.NegativeTTL = dnsprobe.MinTTL(decision.NegativeTTL, view.NegativeTTL)
	}
	if request.Evidence != nil {
		for _, view := range request.Evidence.Views {
			decision.TTL = dnsprobe.MinTTL(decision.TTL, view.TTL)
		}
	}
	first := views[0]
	decision.Actual = append(dnsprobe.Typed("A", first.A), dnsprobe.Typed("AAAA", first.AAAA)...)
	decision.Chain = first.Chain
	for _, view := range views[1:] {
		if view.RCode != first.RCode || view.Mismatch != first.Mismatch || !slices.Equal(view.A, first.A) || !slices.Equal(view.AAAA, first.AAAA) || !slices.Equal(view.Chain, first.Chain) {
			return dnsprobe.Inconsistent(request.Name, decision)
		}
	}
	expected := append(dnsprobe.Typed("A", dnsprobe.Addresses(request.Expected.A)), dnsprobe.Typed("AAAA", dnsprobe.Addresses(request.Expected.AAAA))...)
	if request.Expected.CNAME != "" {
		expected = dnsprobe.Typed("CNAME", []string{dnsprobe.CanonicalName(request.Expected.CNAME)})
		decision.Actual = nil
		if len(first.Chain) > 0 {
			decision.Actual = dnsprobe.Typed("CNAME", first.Chain[:1])
		}
	}
	decision.Missing, decision.Extra = dnsprobe.Difference(expected, decision.Actual)
	if first.Mismatch {
		return mismatch(request.Name, decision)
	}
	if first.RCode == dns.RcodeNameError {
		decision.Status, decision.Reason = metav1.ConditionFalse, "DNSNameNotFound"
		decision.Message = dnsprobe.Bounded(fmt.Sprintf("DNS %s returned NXDOMAIN from %s; expected %s.", request.Name, first.ID, dnsprobe.MessagePreview(expected)))
		return decision
	}
	if len(decision.Extra) != 0 || (request.Expected.CNAME != "" && len(decision.Missing) != 0) {
		return mismatch(request.Name, decision)
	}
	qtype := ""
	if len(request.Expected.A) > 0 && len(first.A) == 0 {
		qtype = "A"
	}
	if qtype == "" && len(request.Expected.AAAA) > 0 && len(first.AAAA) == 0 {
		qtype = "AAAA"
	}
	if request.Expected.CNAME != "" && len(first.A)+len(first.AAAA) == 0 {
		qtype = "A or AAAA"
	}
	if qtype != "" {
		decision.Status, decision.Reason = metav1.ConditionFalse, "DNSNoData"
		decision.Message = dnsprobe.Bounded(fmt.Sprintf("DNS %s returned NOERROR with no %s answers; expected %s.", request.Name, qtype, dnsprobe.MessagePreview(expected)))
		return decision
	}
	if len(decision.Missing) != 0 {
		decision.Status, decision.Reason = metav1.ConditionFalse, "ResolutionPartial"
		decision.Message = dnsprobe.Bounded(fmt.Sprintf("DNS %s is missing expected targets %s; observed %s.", request.Name, dnsprobe.MessagePreview(decision.Missing), dnsprobe.MessagePreview(decision.Actual)))
		return decision
	}
	decision.Message = dnsprobe.Bounded(fmt.Sprintf("DNS %s matches expected targets in every required resolver view; no DNS changes were made.", request.Name))
	return decision
}

func mismatch(name string, decision dnsprobe.Decision) dnsprobe.Decision {
	decision.Status, decision.Reason = metav1.ConditionFalse, "ResolutionMismatch"
	decision.Message = dnsprobe.Bounded(fmt.Sprintf("DNS %s targets differ: missing %s; unexpected %s.", name, dnsprobe.MessagePreview(decision.Missing), dnsprobe.MessagePreview(decision.Extra)))
	return decision
}
