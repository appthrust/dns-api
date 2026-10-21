package dnsprobe

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/miekg/dns"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AnswerEvidence describes one complete query, including CNAME links. TTLs are
// maxima of relevant positive records, or RFC 2308 negative TTLs, not poll caps.
type AnswerEvidence struct {
	Name        string   `json:"name"`
	QType       string   `json:"qtype"`
	RCode       int      `json:"rcode"`
	Addresses   []string `json:"addresses,omitempty"`
	CNAME       string   `json:"cname,omitempty"`
	PositiveTTL *uint32  `json:"positiveTTL,omitempty"`
	NegativeTTL *uint32  `json:"negativeTTL,omitempty"`
	Failure     string   `json:"failure,omitempty"`
}

// WithdrawalCacheEvidence is complete decision evidence, not a diagnostic preview.
// PositiveTTL is retained only where this observation saw the retired address.
type WithdrawalCacheEvidence struct {
	Name        string  `json:"name"`
	View        string  `json:"view"`
	QType       string  `json:"qtype"`
	PositiveTTL *uint32 `json:"positiveTTL,omitempty"`
	NegativeTTL *uint32 `json:"negativeTTL,omitempty"`
}

type WithdrawalDecision struct {
	Decision
	Cache []WithdrawalCacheEvidence
}

func WithdrawalFailed(name, view, failure string) WithdrawalDecision {
	switch failure {
	case "timeout", "SERVFAIL", "REFUSED", "transport", "invalid-response", "resolver-config", "chain-limit", "probe-limit", "negative-soa-missing":
	default:
		failure = "transport"
	}
	return WithdrawalDecision{Decision: Decision{Status: metav1.ConditionUnknown, Reason: "DNSProbeFailed", Message: Bounded(fmt.Sprintf("DNS withdrawal cannot be verified for %s in %s: %s.", name, view, failure))}}
}

// EvaluateWithdrawal proves only current exclusion. The reconciler additionally
// requires a continuous absence window, retained cache horizons and a fresh
// confirming attempt before it may publish Withdrawn=True.
func EvaluateWithdrawal(request ProbeRequest, result ProbeResult) WithdrawalDecision {
	address, err := netip.ParseAddr(request.WithdrawalAddress)
	if err != nil || address.Zone() != "" {
		return WithdrawalFailed(request.Name, "required-views", "invalid-response")
	}
	address = address.Unmap()
	fail := func(view, code string) WithdrawalDecision {
		decision := WithdrawalFailed(request.Name, view, code)
		decision.Cache = positiveWithdrawalEvidence(result, address)
		return decision
	}
	if len(request.Policy.Servers) == 0 || len(result.Views) == 0 {
		return fail("required-views", "invalid-response")
	}
	for _, server := range request.Policy.Servers {
		if !slices.ContainsFunc(result.Views, func(v View) bool { return v.ID == "recursive/"+server }) {
			return fail("recursive/"+server, "invalid-response")
		}
	}
	if len(request.Policy.ExpectedNameservers) != 0 {
		if request.Evidence == nil || request.Evidence.Zone != request.Zone || request.Evidence.PolicyFingerprint != Fingerprint(request.Policy) || EvaluateZone(request, request.Evidence).Status != metav1.ConditionTrue {
			return fail("required-authorities", "invalid-response")
		}
		for _, authority := range request.Evidence.Authorities {
			prefix := "authority/" + authority + "/via/"
			for _, server := range request.Policy.Servers {
				if !slices.ContainsFunc(result.Views, func(v View) bool { return v.ID == prefix+server && v.Authoritative }) {
					return fail(prefix+server, "invalid-response")
				}
			}
		}
	}
	views := slices.Clone(result.Views)
	slices.SortFunc(views, func(a, b View) int { return strings.Compare(a.ID, b.ID) })
	decision := WithdrawalDecision{Decision: Decision{Status: metav1.ConditionTrue, Reason: "WithdrawalVerified"}}
	for i, view := range views {
		if i > 0 && views[i-1].ID == view.ID {
			return fail(view.ID, "invalid-response")
		}
		decision.Views = append(decision.Views, view.ID)
		cache, qtype, failure := withdrawalViewEvidence(request.Name, address, view)
		decision.Cache = append(decision.Cache, cache...)
		if failure != "" {
			return fail(view.ID, failure)
		}
		for _, item := range cache {
			decision.NegativeTTL = maxTTL(decision.NegativeTTL, item.NegativeTTL)
		}
		if qtype != "" && decision.Status == metav1.ConditionTrue {
			decision.Status, decision.Reason = metav1.ConditionFalse, "DNSStillResolves"
			decision.Message = Bounded(fmt.Sprintf("DNS %s still resolves to retired address %s in %s (%s).", request.Name, address, view.ID, qtype))
		}
	}
	return decision
}

func withdrawalViewEvidence(name string, address netip.Addr, view View) ([]WithdrawalCacheEvidence, string, string) {
	if view.Failure != "" {
		return nil, "", view.Failure
	}
	if view.RCode != dns.RcodeSuccess && view.RCode != dns.RcodeNameError {
		code := "invalid-response"
		if view.RCode == dns.RcodeServerFailure {
			code = "SERVFAIL"
		}
		if view.RCode == dns.RcodeRefused {
			code = "REFUSED"
		}
		return nil, "", code
	}
	if view.Mismatch || len(view.Chain) > MaxCNAMEs {
		return nil, "", "chain-limit"
	}
	owners := append([]string{CanonicalName(name)}, namesInOrder(view.Chain)...)
	seen := map[string]bool{}
	for _, owner := range owners {
		if seen[owner] || !ValidName(owner) {
			return nil, "", "invalid-response"
		}
		seen[owner] = true
	}
	if len(view.Answers) != 2*len(owners) {
		return nil, "", "invalid-response"
	}
	answers := slices.Clone(view.Answers)
	slices.SortFunc(answers, func(a, b AnswerEvidence) int {
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.QType, b.QType)
	})
	qtype := ""
	presentA, presentAAAA := false, false
	for _, answer := range answers {
		for _, value := range answer.Addresses {
			ip, err := netip.ParseAddr(value)
			if err != nil || ip.Zone() != "" || (answer.QType == "A" && !ip.Is4()) || (answer.QType == "AAAA" && !ip.Is6()) {
				return nil, "", "invalid-response"
			}
			if ip.Unmap() == address {
				qtype = answer.QType
				presentA = presentA || answer.QType == "A"
				presentAAAA = presentAAAA || answer.QType == "AAAA"
			}
		}
	}
	cache := make([]WithdrawalCacheEvidence, 0, len(answers))
	for i, owner := range owners {
		for _, family := range []string{"A", "AAAA"} {
			var found *AnswerEvidence
			for j := range answers {
				if CanonicalName(answers[j].Name) == owner && answers[j].QType == family {
					if found != nil {
						return cache, qtype, "invalid-response"
					}
					found = &answers[j]
				}
			}
			if found == nil {
				return cache, qtype, "invalid-response"
			}
			if found.Failure != "" {
				return cache, qtype, found.Failure
			}
			if found.RCode != dns.RcodeSuccess && found.RCode != dns.RcodeNameError {
				return cache, qtype, "invalid-response"
			}
			item := WithdrawalCacheEvidence{Name: owner, View: view.ID, QType: family}
			if i < len(owners)-1 {
				if found.RCode != dns.RcodeSuccess || CanonicalName(found.CNAME) != owners[i+1] || len(found.Addresses) != 0 || found.PositiveTTL == nil {
					return cache, qtype, "invalid-response"
				}
			} else {
				terminal := view.A
				if family == "AAAA" {
					terminal = view.AAAA
				}
				if found.RCode != view.RCode || !slices.Equal(Addresses(found.Addresses), Addresses(terminal)) {
					return cache, qtype, "invalid-response"
				}
				if found.CNAME != "" || (found.RCode == dns.RcodeNameError && len(found.Addresses) != 0) {
					return cache, qtype, "invalid-response"
				}
				if len(found.Addresses) == 0 {
					if found.NegativeTTL == nil {
						return cache, qtype, "negative-soa-missing"
					}
					item.NegativeTTL = found.NegativeTTL
				} else if found.PositiveTTL == nil {
					return cache, qtype, "invalid-response"
				}
			}
			if qtype != "" && (found.CNAME != "" || (family == "A" && presentA) || (family == "AAAA" && presentAAAA)) {
				item.PositiveTTL = found.PositiveTTL
			}
			cache = append(cache, item)
		}
	}
	slices.SortFunc(cache, func(a, b WithdrawalCacheEvidence) int {
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.QType, b.QType)
	})
	return cache, qtype, ""
}

func namesInOrder(values []string) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = CanonicalName(value)
	}
	return out
}

// Failed/incomplete observations still retain any positively observed address
// and CNAME horizon. An earlier failed view must not hide a later positive one.
func positiveWithdrawalEvidence(result ProbeResult, address netip.Addr) []WithdrawalCacheEvidence {
	var cache []WithdrawalCacheEvidence
	for _, view := range result.Views {
		presentA, presentAAAA := false, false
		for _, answer := range view.Answers {
			for _, value := range answer.Addresses {
				if ip, err := netip.ParseAddr(value); err == nil && ip.Unmap() == address {
					presentA = presentA || answer.QType == "A"
					presentAAAA = presentAAAA || answer.QType == "AAAA"
				}
			}
		}
		if !presentA && !presentAAAA {
			continue
		}
		for _, answer := range view.Answers {
			if answer.PositiveTTL != nil && (answer.CNAME != "" || (answer.QType == "A" && presentA) || (answer.QType == "AAAA" && presentAAAA)) {
				cache = append(cache, WithdrawalCacheEvidence{Name: answer.Name, View: view.ID, QType: answer.QType, PositiveTTL: answer.PositiveTTL})
			}
		}
	}
	slices.SortFunc(cache, func(a, b WithdrawalCacheEvidence) int {
		if c := strings.Compare(a.View, b.View); c != 0 {
			return c
		}
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.QType, b.QType)
	})
	return cache
}

func maxTTL(current, next *uint32) *uint32 {
	if next != nil && (current == nil || *next > *current) {
		value := *next
		return &value
	}
	return current
}

// answerEvidence validates query-local records without changing publication's
// existing evaluation semantics. Missing negative SOA is never invented as zero.
func answerEvidence(reply *dns.Msg, name string, qtype uint16) AnswerEvidence {
	answer := AnswerEvidence{Name: name, QType: dns.TypeToString[qtype], RCode: reply.Rcode}
	for _, rr := range reply.Answer {
		if CanonicalName(rr.Header().Name) != name {
			continue
		}
		ttl := rr.Header().Ttl
		switch value := rr.(type) {
		case *dns.CNAME:
			target := CanonicalName(value.Target)
			if answer.CNAME != "" && answer.CNAME != target {
				answer.Failure = "invalid-response"
			}
			answer.CNAME = target
		case *dns.A:
			if qtype != dns.TypeA {
				answer.Failure = "invalid-response"
			}
			answer.Addresses = append(answer.Addresses, value.A.String())
		case *dns.AAAA:
			if qtype != dns.TypeAAAA {
				answer.Failure = "invalid-response"
			}
			answer.Addresses = append(answer.Addresses, value.AAAA.String())
		default:
			continue
		}
		if rr.Header().Class != dns.ClassINET {
			answer.Failure = "invalid-response"
		}
		answer.PositiveTTL = maxTTL(answer.PositiveTTL, &ttl)
	}
	if answer.CNAME != "" && len(answer.Addresses) != 0 {
		answer.Failure = "invalid-response"
	}
	if reply.Rcode == dns.RcodeNameError || (answer.CNAME == "" && len(answer.Addresses) == 0) {
		for _, rr := range reply.Ns {
			soa, ok := rr.(*dns.SOA)
			if !ok {
				continue
			}
			owner := CanonicalName(soa.Hdr.Name)
			if (owner != "" && owner != name && !strings.HasSuffix(name, "."+owner)) || soa.Hdr.Class != dns.ClassINET || !ValidName(CanonicalName(soa.Ns)) || !ValidName(CanonicalName(soa.Mbox)) {
				answer.Failure = "invalid-response"
				continue
			}
			ttl := min(soa.Hdr.Ttl, soa.Minttl)
			answer.NegativeTTL = maxTTL(answer.NegativeTTL, &ttl)
		}
	}
	return answer
}
