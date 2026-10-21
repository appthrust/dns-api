package dnsprobe

import (
	"fmt"
	"slices"
	"strings"

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

func Failed(name, view, failure string) Decision {
	switch failure {
	case "timeout", "SERVFAIL", "REFUSED", "transport", "invalid-response", "resolver-config", "chain-limit", "probe-limit":
	default:
		failure = "transport"
	}
	return Decision{Status: metav1.ConditionUnknown, Reason: "DNSProbeFailed", Message: Bounded(fmt.Sprintf("DNS verification unavailable for %s from %s: %s.", name, view, failure))}
}

func EvaluateZone(request ProbeRequest, evidence *ZoneEvidence) Decision {
	return evaluateZone(request, evidence, true)
}

func evaluateZone(request ProbeRequest, evidence *ZoneEvidence, requireAuthorities bool) Decision {
	if len(request.Policy.ExpectedNameservers) == 0 {
		return Decision{Status: metav1.ConditionTrue, Reason: "VerificationConfigured", Message: Bounded(fmt.Sprintf("DNS verifier configured for %s; authoritative nameserver checks were not requested.", request.Zone))}
	}
	if evidence == nil || len(evidence.Views) < len(request.Policy.Servers) || len(evidence.Views) == 0 {
		return Failed(request.Zone, "required-views", "invalid-response")
	}
	for _, server := range request.Policy.Servers {
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
			if !ValidName(CanonicalName(name)) {
				return Failed(request.Zone, view.ID, "invalid-response")
			}
		}
		decision.TTL = MinTTL(decision.TTL, view.TTL)
		decision.NegativeTTL = MinTTL(decision.NegativeTTL, view.NegativeTTL)
	}
	first := views[0]
	decision.Nameservers = Names(first.Nameservers)
	decision.Actual = Typed("NS", decision.Nameservers)
	for _, view := range views[1:] {
		if !slices.Equal(decision.Nameservers, Names(view.Nameservers)) || CanonicalName(view.SOAOwner) != CanonicalName(first.SOAOwner) {
			return Inconsistent(request.Zone, decision)
		}
	}
	if CanonicalName(first.SOAOwner) != request.Zone || !slices.Equal(decision.Nameservers, Names(request.Policy.ExpectedNameservers)) {
		decision.Status, decision.Reason = metav1.ConditionFalse, "NameserverMismatch"
		decision.Missing, decision.Extra = Difference(Typed("NS", Names(request.Policy.ExpectedNameservers)), decision.Actual)
		decision.Message = Bounded(fmt.Sprintf("DNS %s nameservers differ: expected %s; observed %s.", request.Zone, MessagePreview(Names(request.Policy.ExpectedNameservers)), MessagePreview(decision.Nameservers)))
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
	decision.Message = Bounded(fmt.Sprintf("DNS %s nameservers match expectations in every required view; no DNS changes were made.", request.Zone))
	return decision
}

func Inconsistent(name string, decision Decision) Decision {
	decision.Status, decision.Reason = metav1.ConditionFalse, "ResolutionInconsistent"
	decision.Message = Bounded(fmt.Sprintf("DNS %s differs between required resolver/authority views %s.", name, MessagePreview(decision.Views)))
	return decision
}

func Typed(kind string, values []string) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = kind + " " + value
	}
	return out
}

func Difference(expected, actual []string) (missing, extra []string) {
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

func Preview(values []string) ValuesPreview {
	result := ValuesPreview{Total: int32(len(values)), Truncated: len(values) > 16}
	result.Values = slices.Clone(values[:min(len(values), 16)])
	for i, value := range result.Values {
		if len(value) > 512 {
			result.Values[i] = value[:512]
			result.Truncated = true
		}
	}
	return result
}

func MessagePreview(values []string) string {
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

func Bounded(value string) string {
	if len(value) > 2048 {
		return value[:2032] + " (truncated)"
	}
	return value
}
