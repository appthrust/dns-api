package zoneunit

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	probe "github.com/appthrust/dns-api/internal/go/core/dnsprobe"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	externalv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/external/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func prepareWithdrawals(unit *dnsv1alpha1.ZoneUnit, state *schedulerState, env *environment, now time.Time) bool {
	old := make(map[string]dnsv1alpha1.ZoneUnitWithdrawalStatus, len(unit.Status.Withdrawals))
	for _, status := range unit.Status.Withdrawals {
		old[status.ClaimNamespace+"/"+status.ClaimName] = status
	}
	var statuses []dnsv1alpha1.ZoneUnitWithdrawalStatus
	active := make(map[string]bool, len(unit.Spec.Withdrawals))
	changed := false
	for _, item := range unit.Spec.Withdrawals {
		key := withdrawalKey(item)
		active[key] = true
		status := old[key]
		if status.Claim != item.Claim || status.ObservedGeneration != item.ObservedGeneration {
			status = dnsv1alpha1.ZoneUnitWithdrawalStatus{}
		}
		status.ClaimNamespace, status.ClaimName, status.Claim, status.ObservedGeneration = item.ClaimNamespace, item.ClaimName, item.Claim, item.ObservedGeneration
		stored, exists := state.Withdrawals[key]
		identity := withdrawalIdentity(unit, item)
		if !exists || stored.Identity != identity {
			publication := state.Publications[item.Name]
			stored = withdrawalState{Identity: identity, CheckedNames: withdrawalNames(unit, item, publication), MissingHistory: status.LastProbeTime != nil}
			if positive := publication.Positive[item.Address]; len(positive) != 0 {
				stored.Positive = make(map[string]time.Time, len(positive))
				for key, expires := range positive {
					stored.Positive[key] = expires
				}
			}
			invalidateWithdrawal(&status, &stored)
			changed = true
		}
		status.CheckedNames = slices.Clone(stored.CheckedNames)
		status.SampledWildcard = item.Name == "*" || strings.HasPrefix(item.Name, "*.")
		decision := probe.WithdrawalFailed(item.Name, "required-views", "invalid-response").Decision
		switch {
		case !item.Allowed || !validWithdrawal(unit, item):
			invalidateWithdrawal(&status, &stored)
			reason := "InvalidWithdrawal"
			if !item.Allowed {
				reason = "NotAllowed"
			}
			setCondition(&status.Conditions, "Accepted", metav1.ConditionFalse, reason, "External DNS withdrawal intent is not accepted.", item.ObservedGeneration, now)
			setWithdrawalConditions(&status, decision, now)
			status.WaiverRef = nil
			meta.RemoveStatusCondition(&status.Conditions, "OverrideAccepted")
		default:
			if env != nil {
				setCondition(&status.Conditions, "Accepted", metav1.ConditionTrue, "Accepted", "External DNS withdrawal intent is accepted.", item.ObservedGeneration, now)
			}
			if meta.FindStatusCondition(status.Conditions, "Withdrawn") == nil {
				setWithdrawalConditions(&status, decision, now)
			}
			if env != nil && stored.Policy != "" && stored.Policy != env.fingerprint {
				invalidateWithdrawal(&status, &stored)
				setWithdrawalConditions(&status, policyChanged(item.Name), now)
				changed = true
			}
			if status.LastProbeTime != nil && (now.Before(status.LastProbeTime.Time) || status.NextProbeTime == nil || now.After(status.NextProbeTime.Add(300*time.Second))) {
				invalidateWithdrawal(&status, &stored)
				stored.Schedule.Next = time.Time{}
				setWithdrawalConditions(&status, decision, now)
				changed = true
			}
			bindWithdrawalWaiver(&status, item, now)
			if status.ResultDigest != "" {
				status.ResultDigest = withdrawalResultDigest(unit, item, status)
			}
		}
		state.Withdrawals[key] = stored
		statuses = append(statuses, status)
	}
	for key := range state.Withdrawals {
		if !active[key] {
			delete(state.Withdrawals, key)
			changed = true
		}
	}
	unit.Status.Withdrawals = statuses
	return changed
}

func selectWithdrawalDue(unit *dnsv1alpha1.ZoneUnit, state schedulerState, policy string, now time.Time, budget int) []int {
	indices := make([]int, 0, len(unit.Spec.Withdrawals))
	for i, item := range unit.Spec.Withdrawals {
		if item.Allowed && validWithdrawal(unit, item) {
			indices = append(indices, i)
		}
	}
	slices.SortFunc(indices, func(a, b int) int {
		return strings.Compare(withdrawalKey(unit.Spec.Withdrawals[a]), withdrawalKey(unit.Spec.Withdrawals[b]))
	})
	start := 0
	for start < len(indices) && withdrawalKey(unit.Spec.Withdrawals[indices[start]]) <= state.WithdrawalCursor {
		start++
	}
	var due []int
	for offset := range len(indices) {
		index := indices[(start+offset)%len(indices)]
		stored := state.Withdrawals[withdrawalKey(unit.Spec.Withdrawals[index])]
		if stored.Policy == policy && stored.Schedule.Next.After(now) {
			continue
		}
		cost := len(stored.CheckedNames)
		if cost > budget {
			break
		}
		budget -= cost
		due = append(due, index)
	}
	return due
}

func withdrawalProbeCost(unit *dnsv1alpha1.ZoneUnit, state schedulerState, indices []int) int {
	cost := 0
	for _, i := range indices {
		cost += len(state.Withdrawals[withdrawalKey(unit.Spec.Withdrawals[i])].CheckedNames)
	}
	return cost
}

func (r *ZoneReconciler) probeWithdrawals(ctx context.Context, unit *dnsv1alpha1.ZoneUnit, state *schedulerState, env *environment, evidence *probe.ZoneEvidence, zoneDecision probe.Decision, due []int) {
	for _, index := range due {
		item := unit.Spec.Withdrawals[index]
		key := withdrawalKey(item)
		stored := state.Withdrawals[key]
		status := &unit.Status.Withdrawals[index]
		started := r.Options.Clock()
		views := requiredWithdrawalViews(env.policy, evidence)
		decision := probe.WithdrawalDecision{Decision: probe.Decision{Status: metav1.ConditionTrue, Reason: "WithdrawalVerified"}}
		if zoneDecision.Status != metav1.ConditionTrue {
			decision = probe.WithdrawalFailed(item.Name, "required-authorities", "invalid-response")
			if env.policyError != nil {
				decision = probe.WithdrawalFailed(item.Name, "configured-resolvers", "resolver-config")
			}
		} else {
			for _, name := range stored.CheckedNames {
				request := probe.ProbeRequest{Name: name, Zone: unit.Spec.Zone.DomainName, Policy: env.policy, Evidence: evidence, DueTime: stored.Schedule.Next, WithdrawalAddress: item.Address}
				observed, err := r.Options.Resolver.Probe(ctx, request)
				one := probe.EvaluateWithdrawal(request, observed)
				retainPositive(&stored, name, one, r.Options.Clock())
				if err != nil {
					one = probe.WithdrawalFailed(name, "required-views", failureCode(err))
				}
				decision.Cache = append(decision.Cache, one.Cache...)
				if one.Status == metav1.ConditionUnknown || (one.Status == metav1.ConditionFalse && decision.Status == metav1.ConditionTrue) {
					decision.Decision = one.Decision
				}
			}
		}
		now := r.Options.Clock()
		if now.Before(started) || now.Sub(started) > 300*time.Second {
			decision.Decision = probe.WithdrawalFailed(item.Name, "required-views", "timeout").Decision
		}
		if status.LastProbeTime != nil && (now.Before(status.LastProbeTime.Time) || status.NextProbeTime == nil || now.After(status.NextProbeTime.Add(300*time.Second))) {
			invalidateWithdrawal(status, &stored)
		}
		fingerprint := probe.Fingerprint(struct {
			Policy string
			Views  []string
		}{env.fingerprint, views})
		changed := stored.Policy != "" && (stored.Policy != env.fingerprint || !slices.Equal(status.RequiredViews, views))
		weakened := policyWeakened(stored, env.policy, views)
		if changed || weakened {
			invalidateWithdrawal(status, &stored)
			decision.Decision = policyChanged(item.Name)
		}
		// A failed authority discovery cannot lower the persisted policy floor.
		if zoneDecision.Status == metav1.ConditionTrue && !weakened {
			addFloor(&stored, env.policy, views)
		}
		stored.Policy = env.fingerprint
		status.PolicyFingerprint = fingerprint
		status.RequiredViews = views
		result := withdrawalWindow(status, &stored, decision, now)
		if result.Reason == "NegativeTTLNotElapsed" {
			result.Message = fmt.Sprintf("Retired address %s is absent in every required view; waiting until %s for cache safety and a confirming probe.", item.Address, status.NotBefore.UTC().Format(time.RFC3339))
		}
		if result.Reason == "WithdrawalVerified" {
			result.Message = fmt.Sprintf("Retired address %s was verified absent from %s in every required view at %s.", item.Address, item.Name, now.UTC().Format(time.RFC3339))
		}
		stored.Schedule = advance(stored.Schedule, stored.Identity+fingerprint, result, now, r.Options.Jitter())
		status.LastProbeTime, status.NextProbeTime = timePointer(now), timePointer(stored.Schedule.Next)
		setWithdrawalConditions(status, result, now)
		data, _ := json.Marshal(struct {
			Views externalv1alpha1.VerificationValues `json:"views"`
		}{Views: externalv1alpha1.VerificationValues(probe.Preview(views))})
		status.Provider = &dnsv1alpha1.ProviderStatus{Data: runtime.RawExtension{Raw: data}}
		bindWithdrawalWaiver(status, item, now)
		status.ResultDigest = withdrawalResultDigest(unit, item, *status)
		state.Withdrawals[key] = stored
		state.WithdrawalCursor = key
	}
}

func policyChanged(name string) probe.Decision {
	return probe.Decision{Status: metav1.ConditionUnknown, Reason: "PolicyChanged", Message: fmt.Sprintf("External DNS withdrawal policy changed for %s; the prior required view policy remains a floor.", name)}
}

func setWithdrawalConditions(status *dnsv1alpha1.ZoneUnitWithdrawalStatus, decision probe.Decision, now time.Time) {
	if strings.HasPrefix(decision.Message, "DNS withdrawal ") {
		decision.Message = "External " + decision.Message
	}
	setCondition(&status.Conditions, "Withdrawn", decision.Status, decision.Reason, decision.Message, status.ObservedGeneration, now)
	ready := metav1.ConditionFalse
	if decision.Status == metav1.ConditionTrue {
		ready = metav1.ConditionTrue
	}
	setCondition(&status.Conditions, "Ready", ready, decision.Reason, decision.Message, status.ObservedGeneration, now)
}

func bindWithdrawalWaiver(status *dnsv1alpha1.ZoneUnitWithdrawalStatus, item dnsv1alpha1.ZoneUnitWithdrawal, now time.Time) {
	status.WaiverRef = nil
	waiver := item.Waiver
	if waiver == nil || waiver.Name == "" || waiver.UID == "" || waiver.Digest == "" || waiver.ReservationRef.UID == "" {
		meta.RemoveStatusCondition(&status.Conditions, "OverrideAccepted")
		return
	}
	status.WaiverRef = &dnsv1alpha1.RecordWithdrawalWaiverReference{Name: waiver.Name, UID: waiver.UID, Digest: waiver.Digest, ReservationRef: waiver.ReservationRef}
	setCondition(&status.Conditions, "OverrideAccepted", metav1.ConditionTrue, "OperatorAuthorizedRelease", "Core-validated withdrawal waiver is bound to its exact UID and immutable scope.", item.ObservedGeneration, now)
}

func withdrawalResultDigest(unit *dnsv1alpha1.ZoneUnit, item dnsv1alpha1.ZoneUnitWithdrawal, status dnsv1alpha1.ZoneUnitWithdrawalStatus) string {
	status.ResultDigest = ""
	status.Provider = nil // Bounded diagnostic previews are never decision inputs.
	// Transition timestamps do not alter the observation/result identity.
	status.Conditions = slices.Clone(status.Conditions)
	for i := range status.Conditions {
		status.Conditions[i].LastTransitionTime = metav1.Time{}
	}
	slices.SortFunc(status.Conditions, func(a, b metav1.Condition) int { return strings.Compare(a.Type, b.Type) })
	return "sha256:" + probe.Fingerprint(struct {
		Identity string
		Provider dnsv1alpha1.ProviderReference
		Zone     dnsv1alpha1.ZoneUnitZoneSpec
		Status   dnsv1alpha1.ZoneUnitWithdrawalStatus
	}{withdrawalIdentity(unit, item), unit.Spec.Provider, unit.Spec.Zone, status})
}

func nextWithdrawalRequeue(unit *dnsv1alpha1.ZoneUnit, state schedulerState, policy string, now time.Time) time.Duration {
	next := 300 * time.Second
	for _, item := range unit.Spec.Withdrawals {
		if !item.Allowed || !validWithdrawal(unit, item) {
			continue
		}
		stored := state.Withdrawals[withdrawalKey(item)]
		if stored.Policy != policy || !stored.Schedule.Next.After(now) {
			return time.Second
		}
		next = min(next, stored.Schedule.Next.Sub(now))
	}
	return max(time.Second, next)
}

func invalidateWithdrawalZone(unit *dnsv1alpha1.ZoneUnit, state *schedulerState, now time.Time) {
	for i, item := range unit.Spec.Withdrawals {
		if !item.Allowed {
			continue
		}
		status := &unit.Status.Withdrawals[i]
		stored := state.Withdrawals[withdrawalKey(item)]
		invalidateWithdrawal(status, &stored)
		if condition := meta.FindStatusCondition(status.Conditions, "Withdrawn"); condition == nil || condition.Reason != "PolicyChanged" {
			setWithdrawalConditions(status, probe.WithdrawalFailed(item.Name, "required-authorities", "invalid-response").Decision, now)
		}
		state.Withdrawals[withdrawalKey(item)] = stored
	}
}

func rejectWithdrawals(unit *dnsv1alpha1.ZoneUnit, now time.Time) {
	var prior schedulerState
	if unit.Status.Provider != nil {
		_ = json.Unmarshal(unit.Status.Provider.State.Raw, &prior)
	}
	state := loadState(unit, prior.Policy)
	// Invalidate reconstructible active schedules, but never discard retained
	// publication horizons or a withdrawal's prior policy floor on rejection.
	state.Zone = schedule{}
	state.Names = map[string]schedule{}
	prepareWithdrawals(unit, &state, nil, now)
	for i, item := range unit.Spec.Withdrawals {
		status := &unit.Status.Withdrawals[i]
		stored := state.Withdrawals[withdrawalKey(item)]
		invalidateWithdrawal(status, &stored)
		stored.Schedule.Next = time.Time{}
		state.Withdrawals[withdrawalKey(item)] = stored
		setWithdrawalConditions(status, probe.WithdrawalFailed(item.Name, "binding", "invalid-response").Decision, now)
		setCondition(&status.Conditions, "Accepted", metav1.ConditionFalse, "InvalidWithdrawal", "External DNS withdrawal binding is not accepted.", item.ObservedGeneration, now)
		status.WaiverRef = nil
		meta.RemoveStatusCondition(&status.Conditions, "OverrideAccepted")
	}
	// These maps are omitted from private JSON when empty. Normalize them before
	// comparing the decoded state, which contains native (not metav1) times.
	if len(state.Publications) == 0 {
		state.Publications = nil
	}
	if len(state.Withdrawals) == 0 {
		state.Withdrawals = nil
	}
	if (unit.Status.Provider != nil || len(unit.Spec.Withdrawals) != 0) && !reflect.DeepEqual(prior, state) {
		raw, _ := json.Marshal(state)
		unit.Status.Provider = &dnsv1alpha1.ProviderStatus{State: runtime.RawExtension{Raw: raw}}
	}
}
