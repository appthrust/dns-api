package zoneunit

import (
	"net/netip"
	"slices"
	"strings"
	"time"

	probe "github.com/appthrust/dns-api/internal/go/core/dnsprobe"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The independent claims are the obligation inventory. This private state only
// retains cache bounds, policy floors and witnesses for those current claims.
type withdrawalState struct {
	Identity     string               `json:"identity"`
	Policy       string               `json:"policy"`
	Floor        probe.Policy         `json:"floor"`
	Views        []string             `json:"views,omitempty"`
	CheckedNames []string             `json:"checkedNames"`
	Positive     map[string]time.Time `json:"positive,omitempty"`
	Negative     map[string]uint32    `json:"negative,omitempty"`
	// Preserve subsecond t0: metav1.Time's wire encoding has only second precision.
	WindowStart    time.Time `json:"windowStart,omitempty"`
	Schedule       schedule  `json:"schedule"`
	MissingHistory bool      `json:"missingHistory,omitempty"`
}

type publicationEvidence struct {
	Witnesses map[string]string               `json:"witnesses,omitempty"`
	Positive  map[string]map[string]time.Time `json:"positive,omitempty"`
}

func withdrawalKey(item dnsv1alpha1.ZoneUnitWithdrawal) string {
	return item.ClaimNamespace + "/" + item.ClaimName
}

func withdrawalIdentity(unit *dnsv1alpha1.ZoneUnit, item dnsv1alpha1.ZoneUnitWithdrawal) string {
	return probe.Fingerprint(struct {
		UnitUID       string
		Zone          dnsv1alpha1.ObjectReference
		Claim         dnsv1alpha1.WithdrawalObjectReference
		Generation    int64
		Name, Address string
	}{string(unit.UID), unit.Spec.Zone.Ref, item.Claim, item.ObservedGeneration, item.Name, item.Address})
}

func withdrawalNames(unit *dnsv1alpha1.ZoneUnit, item dnsv1alpha1.ZoneUnitWithdrawal, publication publicationEvidence) []string {
	name := unit.Spec.Zone.DomainName
	if item.Name != "@" {
		name = item.Name + "." + name
	}
	if item.Name != "*" && !strings.HasPrefix(item.Name, "*.") {
		return []string{name}
	}
	witness := probe.Fingerprint(struct {
		ClaimUID string
		Zone     dnsv1alpha1.ObjectReference
		UnitUID  string
	}{string(item.Claim.UID), unit.Spec.Zone.Ref, string(unit.UID)})
	names := []string{"dns-retire-" + witness[:24] + strings.TrimPrefix(name, "*")}
	if witness := publication.Witnesses[item.Address]; witness != "" {
		names = append(names, witness)
	}
	slices.Sort(names)
	return slices.Compact(names)
}

func validWithdrawal(unit *dnsv1alpha1.ZoneUnit, item dnsv1alpha1.ZoneUnitWithdrawal) bool {
	ip, err := netip.ParseAddr(item.Address)
	if err != nil || ip.Zone() != "" || ip.Unmap().String() != item.Address || item.Claim.UID == "" || item.ClaimNamespace == "" || item.ClaimName == "" || item.Claim.Namespace != item.ClaimNamespace || item.Claim.Name != item.ClaimName || item.ObservedGeneration < 1 {
		return false
	}
	name := strings.TrimPrefix(item.Name, "*.")
	if item.Name == "@" || item.Name == "*" {
		name = unit.Spec.Zone.DomainName
	} else {
		name += "." + unit.Spec.Zone.DomainName
	}
	return probe.ValidName(name)
}

func cacheKey(checked string, evidence probe.WithdrawalCacheEvidence) string {
	// A chain's maximum is sufficient for each checked-name/view/family bound;
	// retaining terminal owner keys would grow without bound as CNAMEs change.
	return checked + "/" + evidence.View + "/" + evidence.QType
}

func retainPositive(state *withdrawalState, checked string, decision probe.WithdrawalDecision, now time.Time) {
	for _, item := range decision.Cache {
		if item.PositiveTTL == nil {
			continue
		}
		if state.Positive == nil {
			state.Positive = map[string]time.Time{}
		}
		key := cacheKey(checked, item)
		expires := now.Add(time.Duration(*item.PositiveTTL) * time.Second)
		if expires.After(state.Positive[key]) {
			state.Positive[key] = expires
		}
	}
}

func retainPublication(state *schedulerState, group nameGroup, request probe.ProbeRequest, result probe.ProbeResult, now time.Time) {
	old := state.Publications[group.Name]
	if old.Witnesses == nil {
		old.Witnesses = map[string]string{}
	}
	if old.Positive == nil {
		old.Positive = map[string]map[string]time.Time{}
	}
	for address, horizons := range old.Positive {
		for key, expires := range horizons {
			if !expires.After(now) {
				delete(horizons, key)
			}
		}
		if len(horizons) == 0 {
			delete(old.Positive, address)
			delete(old.Witnesses, address)
		}
	}
	addresses := append(slices.Clone(group.Expected.A), group.Expected.AAAA...)
	for address := range old.Positive {
		addresses = append(addresses, address)
	}
	slices.Sort(addresses)
	// Retain any renewed observation of an already-known retired address, even
	// while the active publication intent has moved to its replacement.
	for _, address := range slices.Compact(addresses) {
		request.WithdrawalAddress = address
		decision := probe.EvaluateWithdrawal(request, result)
		temporary := withdrawalState{Positive: old.Positive[address]}
		retainPositive(&temporary, group.CheckedName, decision, now)
		if slices.ContainsFunc(decision.Cache, func(evidence probe.WithdrawalCacheEvidence) bool { return evidence.PositiveTTL != nil }) {
			old.Witnesses[address] = group.CheckedName
		}
		if len(temporary.Positive) != 0 {
			old.Positive[address] = temporary.Positive
		}
	}
	state.Publications[group.Name] = old
}

func publicationUnexpired(publication publicationEvidence, now time.Time) bool {
	for _, horizons := range publication.Positive {
		for _, expires := range horizons {
			if expires.After(now) {
				return true
			}
		}
	}
	return false
}

func requiredWithdrawalViews(policy probe.Policy, evidence *probe.ZoneEvidence) []string {
	var views []string
	for _, server := range policy.Servers {
		views = append(views, "recursive/"+server)
	}
	if len(policy.ExpectedNameservers) != 0 && evidence != nil {
		for _, authority := range evidence.Authorities {
			for _, server := range policy.Servers {
				views = append(views, "authority/"+authority+"/via/"+server)
			}
		}
	}
	slices.Sort(views)
	return slices.Compact(views)
}

func containsAll(values, floor []string) bool {
	for _, value := range floor {
		if !slices.Contains(values, value) {
			return false
		}
	}
	return true
}

func policyWeakened(state withdrawalState, policy probe.Policy, views []string) bool {
	return state.MissingHistory || !containsAll(policy.Servers, state.Floor.Servers) || !containsAll(policy.ExpectedNameservers, state.Floor.ExpectedNameservers) || !containsAll(views, state.Views)
}

func addFloor(state *withdrawalState, policy probe.Policy, views []string) {
	state.Floor.Servers = sortedUnion(state.Floor.Servers, policy.Servers)
	state.Floor.ExpectedNameservers = sortedUnion(state.Floor.ExpectedNameservers, policy.ExpectedNameservers)
	state.Views = sortedUnion(state.Views, views)
}

func sortedUnion(a, b []string) []string {
	out := append(slices.Clone(a), b...)
	slices.Sort(out)
	return slices.Compact(out)
}

func invalidateWithdrawal(status *dnsv1alpha1.ZoneUnitWithdrawalStatus, state *withdrawalState) {
	status.AbsentSince, status.NotBefore, status.VerifiedAt = nil, nil, nil
	status.ResultDigest = ""
	state.Negative = nil
	state.WindowStart = time.Time{}
}

func withdrawalWindow(status *dnsv1alpha1.ZoneUnitWithdrawalStatus, state *withdrawalState, decision probe.WithdrawalDecision, now time.Time) probe.Decision {
	if decision.Status != metav1.ConditionTrue {
		invalidateWithdrawal(status, state)
		return decision.Decision
	}
	if status.AbsentSince != nil && state.WindowStart.IsZero() {
		invalidateWithdrawal(status, state)
	}
	confirming := status.AbsentSince != nil && status.LastProbeTime != nil && now.After(status.LastProbeTime.Time)
	if status.AbsentSince == nil {
		status.AbsentSince = timePointer(now)
		state.WindowStart = now
	}
	notBefore := state.WindowStart
	if status.NotBefore != nil && status.NotBefore.After(notBefore) {
		notBefore = status.NotBefore.Time
	}
	for _, expires := range state.Positive {
		if expires.After(notBefore) {
			notBefore = expires
		}
	}
	if state.Negative == nil {
		state.Negative = map[string]uint32{}
	}
	for _, item := range decision.Cache {
		if item.NegativeTTL == nil {
			continue
		}
		key := item.View + "/" + item.QType
		state.Negative[key] = max(state.Negative[key], *item.NegativeTTL)
	}
	for _, ttl := range state.Negative {
		expires := state.WindowStart.Add(time.Duration(ttl) * time.Second)
		if expires.After(notBefore) {
			notBefore = expires
		}
	}
	// Round the public horizon up, never down across a status round-trip.
	if notBefore.Nanosecond() != 0 {
		notBefore = notBefore.Truncate(time.Second).Add(time.Second)
	}
	status.NotBefore = timePointer(notBefore)
	if !confirming || now.Before(notBefore) {
		status.VerifiedAt = nil
		return probe.Decision{Status: metav1.ConditionFalse, Reason: "NegativeTTLNotElapsed"}
	}
	status.VerifiedAt = timePointer(now)
	return probe.Decision{Status: metav1.ConditionTrue, Reason: "WithdrawalVerified"}
}
