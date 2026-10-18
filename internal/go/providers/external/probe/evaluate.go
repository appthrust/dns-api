package probe

import (
	"fmt"
	"slices"
	"strings"

	"github.com/appthrust/dns-api/internal/go/providers/external/config"
	externalv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/external/v1alpha1"
	"github.com/miekg/dns"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type Decision struct {
	Status      metav1.ConditionStatus
	Reason      string
	Message     string
	Actual      []string
	Missing     []string
	Extra       []string
	Chain       []string
	Views       []string
	Nameservers []string
	TTL         *uint32
	NegativeTTL *uint32
}

func (d Decision) Outcome() string {
	if d.Status == metav1.ConditionUnknown {
		return "failed"
	}
	if d.Status == metav1.ConditionTrue {
		return "verified"
	}
	if d.Reason == "DNSNameNotFound" || d.Reason == "DNSNoData" {
		return "negative"
	}
	return "mismatch"
}

func Failed(name, view, failure string) Decision {
	switch failure {
	case "timeout", "SERVFAIL", "REFUSED", "transport", "invalid-response", "resolver-config", "chain-limit", "probe-limit":
	default:
		failure = "transport"
	}
	return Decision{Status: metav1.ConditionUnknown, Reason: "DNSProbeFailed", Message: bounded(fmt.Sprintf("DNS verification unavailable for %s from %s: %s.", name, view, failure))}
}

func EvaluateZone(request ProbeRequest, evidence *ZoneEvidence) Decision {
	return evaluateZone(request, evidence, true)
}

func evaluateZone(request ProbeRequest, evidence *ZoneEvidence, requireAuthorities bool) Decision {
	if len(request.Policy.ExpectedNameservers) == 0 {
		return Decision{Status: metav1.ConditionTrue, Reason: "VerificationConfigured", Message: bounded(fmt.Sprintf("DNS verifier configured for %s; authoritative nameserver checks were not requested.", request.Zone))}
	}
	if evidence == nil || len(evidence.Views) < len(request.Policy.Resolver.Servers) || len(evidence.Views) == 0 {
		return Failed(request.Zone, "required-views", "invalid-response")
	}
	for _, server := range request.Policy.Resolver.Servers {
		if !slices.ContainsFunc(evidence.Views, func(view AuthorityObservation) bool { return view.ID == "recursive/"+server }) {
			return Failed(request.Zone, "recursive/"+server, "invalid-response")
		}
	}
	views := slices.Clone(evidence.Views)
	slices.SortFunc(views, func(a, b AuthorityObservation) int { return strings.Compare(a.ID, b.ID) })
	decision := Decision{Status: metav1.ConditionTrue, Reason: "NameserversVerified"}
	for _, view := range views {
		decision.Views = append(decision.Views, view.ID)
		if view.Failure != "" {
			return Failed(request.Zone, view.ID, view.Failure)
		}
		for _, name := range view.Nameservers {
			if !config.ValidName(CanonicalName(name)) {
				return Failed(request.Zone, view.ID, "invalid-response")
			}
		}
		decision.TTL = minTTL(decision.TTL, view.TTL)
		decision.NegativeTTL = minTTL(decision.NegativeTTL, view.NegativeTTL)
	}
	first := views[0]
	decision.Nameservers = Names(first.Nameservers)
	decision.Actual = typed("NS", decision.Nameservers)
	for _, view := range views[1:] {
		if !slices.Equal(decision.Nameservers, Names(view.Nameservers)) || CanonicalName(view.SOAOwner) != CanonicalName(first.SOAOwner) {
			return inconsistent(request.Zone, decision)
		}
	}
	if CanonicalName(first.SOAOwner) != request.Zone || !slices.Equal(decision.Nameservers, Names(request.Policy.ExpectedNameservers)) {
		decision.Status, decision.Reason = metav1.ConditionFalse, "NameserverMismatch"
		decision.Missing, decision.Extra = difference(typed("NS", Names(request.Policy.ExpectedNameservers)), decision.Actual)
		decision.Message = bounded(fmt.Sprintf("DNS %s nameservers differ: expected %s; observed %s.", request.Zone, preview(Names(request.Policy.ExpectedNameservers)), preview(decision.Nameservers)))
		return decision
	}
	if requireAuthorities && len(evidence.Authorities) == 0 {
		return Failed(request.Zone, "required-authorities", "invalid-response")
	}
	if requireAuthorities {
		for _, authority := range evidence.Authorities {
			found := false
			for _, view := range views {
				if view.ID == "authority/"+authority && view.Authoritative {
					found = true
					break
				}
			}
			if !found {
				return Failed(request.Zone, "authority/"+authority, "invalid-response")
			}
		}
	}
	decision.Message = bounded(fmt.Sprintf("DNS %s nameservers match expectations in every required view; no DNS changes were made.", request.Zone))
	return decision
}

func Evaluate(request ProbeRequest, result ProbeResult) Decision {
	if len(result.Views) == 0 || len(result.Views) < len(request.Policy.Resolver.Servers) {
		return Failed(request.Name, "required-views", "invalid-response")
	}
	for _, server := range request.Policy.Resolver.Servers {
		if !slices.ContainsFunc(result.Views, func(view View) bool { return view.ID == "recursive/"+server }) {
			return Failed(request.Name, "recursive/"+server, "invalid-response")
		}
	}
	if len(request.Policy.ExpectedNameservers) != 0 {
		if request.Evidence == nil || len(request.Evidence.Authorities) == 0 {
			return Failed(request.Name, "required-authorities", "invalid-response")
		}
		for _, authority := range request.Evidence.Authorities {
			if !slices.ContainsFunc(result.Views, func(view View) bool {
				return strings.HasPrefix(view.ID, "authority/"+authority+"/via/") && view.Authoritative
			}) {
				return Failed(request.Name, "authority/"+authority, "invalid-response")
			}
		}
	}
	views := slices.Clone(result.Views)
	slices.SortFunc(views, func(a, b View) int { return strings.Compare(a.ID, b.ID) })
	decision := Decision{Status: metav1.ConditionTrue, Reason: "ResolutionVerified"}
	for i := range views {
		view := &views[i]
		view.A, view.AAAA = Addresses(view.A), Addresses(view.AAAA)
		view.Chain = slices.Clone(view.Chain)
		seen := map[string]bool{CanonicalName(request.Name): true}
		for j, name := range view.Chain {
			name = CanonicalName(name)
			view.Chain[j] = name
			if seen[name] {
				view.Mismatch = true
			}
			seen[name] = true
		}
		if len(view.Chain) > MaxCNAMEs {
			view.Failure = "chain-limit"
		}
		if view.Failure != "" {
			return Failed(request.Name, view.ID, view.Failure)
		}
		if view.RCode != dns.RcodeSuccess && view.RCode != dns.RcodeNameError {
			failure := "invalid-response"
			if view.RCode == dns.RcodeServerFailure {
				failure = "SERVFAIL"
			}
			if view.RCode == dns.RcodeRefused {
				failure = "REFUSED"
			}
			return Failed(request.Name, view.ID, failure)
		}
		decision.Views = append(decision.Views, view.ID)
		decision.TTL = minTTL(decision.TTL, view.TTL)
		decision.NegativeTTL = minTTL(decision.NegativeTTL, view.NegativeTTL)
	}
	if request.Evidence != nil {
		for _, view := range request.Evidence.Views {
			decision.TTL = minTTL(decision.TTL, view.TTL)
		}
	}
	first := views[0]
	decision.Actual = append(typed("A", first.A), typed("AAAA", first.AAAA)...)
	decision.Chain = first.Chain
	for _, view := range views[1:] {
		if view.RCode != first.RCode || view.Mismatch != first.Mismatch || !slices.Equal(view.A, first.A) || !slices.Equal(view.AAAA, first.AAAA) || !slices.Equal(view.Chain, first.Chain) {
			return inconsistent(request.Name, decision)
		}
	}
	expected := append(typed("A", Addresses(request.Expected.A)), typed("AAAA", Addresses(request.Expected.AAAA))...)
	if request.Expected.CNAME != "" {
		expected = typed("CNAME", []string{CanonicalName(request.Expected.CNAME)})
		decision.Actual = nil
		if len(first.Chain) > 0 {
			decision.Actual = typed("CNAME", first.Chain[:1])
		}
	}
	decision.Missing, decision.Extra = difference(expected, decision.Actual)
	if first.Mismatch {
		return mismatch(request.Name, decision)
	}
	if first.RCode == dns.RcodeNameError {
		decision.Status, decision.Reason = metav1.ConditionFalse, "DNSNameNotFound"
		decision.Message = bounded(fmt.Sprintf("DNS %s returned NXDOMAIN from %s; expected %s.", request.Name, first.ID, preview(expected)))
		return decision
	}
	if len(decision.Extra) != 0 {
		return mismatch(request.Name, decision)
	}
	if request.Expected.CNAME != "" && len(decision.Missing) != 0 {
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
		decision.Message = bounded(fmt.Sprintf("DNS %s returned NOERROR with no %s answers; expected %s.", request.Name, qtype, preview(expected)))
		return decision
	}
	if len(decision.Missing) != 0 {
		decision.Status, decision.Reason = metav1.ConditionFalse, "ResolutionPartial"
		decision.Message = bounded(fmt.Sprintf("DNS %s is missing expected targets %s; observed %s.", request.Name, preview(decision.Missing), preview(decision.Actual)))
		return decision
	}
	decision.Message = bounded(fmt.Sprintf("DNS %s matches expected targets in every required resolver view; no DNS changes were made.", request.Name))
	return decision
}

func inconsistent(name string, decision Decision) Decision {
	decision.Status, decision.Reason = metav1.ConditionFalse, "ResolutionInconsistent"
	decision.Message = bounded(fmt.Sprintf("DNS %s differs between required resolver/authority views %s.", name, preview(decision.Views)))
	return decision
}

func mismatch(name string, decision Decision) Decision {
	decision.Status, decision.Reason = metav1.ConditionFalse, "ResolutionMismatch"
	decision.Message = bounded(fmt.Sprintf("DNS %s targets differ: missing %s; unexpected %s.", name, preview(decision.Missing), preview(decision.Extra)))
	return decision
}

func typed(kind string, values []string) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = kind + " " + value
	}
	return out
}

func difference(expected, actual []string) (missing, extra []string) {
	for _, value := range expected {
		if !slices.Contains(actual, value) {
			missing = append(missing, value)
		}
	}
	for _, value := range actual {
		if !slices.Contains(expected, value) {
			extra = append(extra, value)
		}
	}
	slices.Sort(missing)
	slices.Sort(extra)
	return missing, extra
}

func Preview(values []string) externalv1alpha1.VerificationValues {
	result := externalv1alpha1.VerificationValues{Total: int32(len(values)), Truncated: len(values) > 16}
	result.Values = slices.Clone(values[:min(len(values), 16)])
	for i, value := range result.Values {
		if len(value) > 512 {
			result.Values[i] = value[:512]
			result.Truncated = true
		}
	}
	return result
}

func preview(values []string) string {
	// The message preview is smaller than public data so two previews plus a name fit 2048 bytes.
	shown := values[:min(len(values), 4)]
	result := "[" + strings.Join(shown, ", ") + "]"
	if len(values) > len(shown) {
		result += fmt.Sprintf(" (+%d omitted)", len(values)-len(shown))
	}
	if len(result) > 700 {
		result = result[:680] + " (truncated)"
	}
	return result
}

func bounded(value string) string {
	if len(value) > 2048 {
		return value[:2032] + " (truncated)"
	}
	return value
}
