package withdrawal

import (
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/appthrust/dns-api/internal/go/core/dnsprobe"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// State is private, identity-bound evidence persisted by the ZoneUnit controller.
// Positive expiry bounds and the required-view floor survive failed observations.
// WindowStart uses native time.Time to preserve subsecond precision in JSON.
// A controller applying scheduling jitter must persist the same NextProbe here.
// An empty Identity permits a positive-only publication seed supplied by the
// controller for this exact claim address/name. It never restores a window.
type State struct {
	Identity       string               `json:"identity"`
	Policy         string               `json:"policy"`
	Views          []string             `json:"views,omitempty"`
	SOAMinimum     uint32               `json:"soaMinimum"`
	CheckedNames   []string             `json:"checkedNames"`
	Positive       map[string]time.Time `json:"positive,omitempty"`
	Negative       map[string]uint32    `json:"negative,omitempty"`
	WindowStart    time.Time            `json:"windowStart,omitempty"`
	AbsentSince    *metav1.Time         `json:"absentSince,omitempty"`
	NotBefore      *metav1.Time         `json:"notBefore,omitempty"`
	VerifiedAt     *metav1.Time         `json:"verifiedAt,omitempty"`
	LastProbe      time.Time            `json:"lastProbe,omitempty"`
	NextProbe      time.Time            `json:"nextProbe,omitempty"`
	Failures       int                  `json:"failures,omitempty"`
	MissingHistory bool                 `json:"missingHistory,omitempty"`
}

func initialState(in Input) *State {
	identity := claimIdentity(&in)
	if in.Previous != nil && in.Previous.Identity == identity {
		state := *in.Previous
		state.Views = slices.Clone(state.Views)
		state.CheckedNames = slices.Clone(state.CheckedNames)
		state.Positive = maps.Clone(state.Positive)
		state.Negative = maps.Clone(state.Negative)
		state.AbsentSince = state.AbsentSince.DeepCopy()
		state.NotBefore = state.NotBefore.DeepCopy()
		state.VerifiedAt = state.VerifiedAt.DeepCopy()
		state.MissingHistory = state.MissingHistory || len(state.CheckedNames) == 0 || (!state.LastProbe.IsZero() && state.Policy == "")
		return &state
	}
	state := &State{Identity: identity, CheckedNames: []string{checkedName(&in, identity)}}
	// An explicit missing-history marker must not be repaired by inventing a
	// fresh floor, even when the controller cannot recover the old identity.
	if in.Previous != nil {
		state.MissingHistory = in.Previous.MissingHistory
		if in.Previous.Identity == "" {
			state.Positive = maps.Clone(in.Previous.Positive)
		}
	}
	return state
}

func claimIdentity(in *Input) string {
	return dnsprobe.Fingerprint(struct {
		Zone          string
		Claim         dnsv1alpha1.WithdrawalObjectReference
		Generation    int64
		Name, Address string
	}{in.Snapshot.DomainName, in.Claim.Claim, in.Claim.ObservedGeneration, in.Claim.Name, in.Claim.Address})
}

func checkedName(in *Input, identity string) string {
	name := in.Snapshot.DomainName
	if in.Claim.Name != "@" {
		name = in.Claim.Name + "." + name
	}
	if in.Claim.Name == "*" || strings.HasPrefix(in.Claim.Name, "*.") {
		name = "dns-retire-" + identity[:24] + strings.TrimPrefix(name, "*")
	}
	return name
}

func (s *State) invalidate() {
	s.AbsentSince, s.NotBefore, s.VerifiedAt = nil, nil, nil
	s.WindowStart = time.Time{}
	s.Negative = nil
}

func (s *State) retainPositive(name string, evidence []dnsprobe.WithdrawalCacheEvidence, now time.Time) {
	for _, item := range evidence {
		if item.PositiveTTL == nil {
			continue
		}
		if s.Positive == nil {
			s.Positive = map[string]time.Time{}
		}
		key := name + "/" + item.View + "/" + item.QType
		expires := now.Add(time.Duration(*item.PositiveTTL) * time.Second)
		if expires.After(s.Positive[key]) {
			s.Positive[key] = expires
		}
	}
}

func (s *State) window(minimum uint32, now time.Time) dnsprobe.Decision {
	if s.AbsentSince != nil && s.WindowStart.IsZero() {
		s.invalidate()
	}
	confirming := s.AbsentSince != nil && !s.LastProbe.IsZero() && now.After(s.LastProbe)
	if s.AbsentSince == nil {
		s.AbsentSince = timePointer(now)
		s.WindowStart = now
	}
	if s.Negative == nil {
		s.Negative = map[string]uint32{}
	}
	s.Negative["soa.minimum"] = max(s.Negative["soa.minimum"], minimum)
	notBefore := s.WindowStart.Add(time.Duration(s.Negative["soa.minimum"]) * time.Second)
	if s.NotBefore != nil && s.NotBefore.After(notBefore) {
		notBefore = s.NotBefore.Time
	}
	for _, expires := range s.Positive {
		if expires.After(notBefore) {
			notBefore = expires
		}
	}
	// Public horizons must never round down during a Kubernetes status round-trip.
	if notBefore.Nanosecond() != 0 {
		notBefore = notBefore.Truncate(time.Second).Add(time.Second)
	}
	s.NotBefore = timePointer(notBefore)
	if !confirming || now.Before(notBefore) {
		s.VerifiedAt = nil
		return dnsprobe.Decision{Status: metav1.ConditionFalse, Reason: "NegativeTTLNotElapsed"}
	}
	s.VerifiedAt = timePointer(now)
	return dnsprobe.Decision{Status: metav1.ConditionTrue, Reason: "WithdrawalVerified"}
}

func (s *State) schedule(status metav1.ConditionStatus, changed bool, now time.Time) {
	interval := 30 * time.Second
	if changed || status != metav1.ConditionUnknown {
		s.Failures = 0
	}
	if status == metav1.ConditionUnknown {
		steps := [...]time.Duration{30 * time.Second, 60 * time.Second, 120 * time.Second, 300 * time.Second}
		s.Failures = min(s.Failures+1, len(steps))
		interval = steps[max(s.Failures-1, 0)]
	}
	s.LastProbe, s.NextProbe = now, now.Add(interval)
}

func timePointer(value time.Time) *metav1.Time {
	t := metav1.NewTime(value)
	return &t
}
