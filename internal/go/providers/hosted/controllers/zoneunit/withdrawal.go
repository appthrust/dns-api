package zoneunit

import (
	"context"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/appthrust/dns-api/internal/go/core/dnsprobe"
	"github.com/appthrust/dns-api/internal/go/providers/hosted/withdrawal"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	hostedv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/hosted/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func withdrawalKey(item dnsv1alpha1.ZoneUnitWithdrawal) string {
	return item.ClaimNamespace + "/" + item.ClaimName
}
func withdrawalIdentity(unit *dnsv1alpha1.ZoneUnit, item dnsv1alpha1.ZoneUnitWithdrawal) string {
	return dnsprobe.Fingerprint(struct {
		UnitUID       string
		Zone          dnsv1alpha1.ObjectReference
		Claim         dnsv1alpha1.WithdrawalObjectReference
		Generation    int64
		Name, Address string
	}{string(unit.UID), unit.Spec.Zone.Ref, item.Claim, item.ObservedGeneration, item.Name, item.Address})
}
func validWithdrawal(unit *dnsv1alpha1.ZoneUnit, item dnsv1alpha1.ZoneUnitWithdrawal) bool {
	address, err := netip.ParseAddr(item.Address)
	name := unit.Spec.Zone.DomainName
	if item.Name != "@" && item.Name != "*" {
		name = strings.TrimPrefix(item.Name, "*.") + "." + name
	}
	return err == nil && address.Zone() == "" && address.Unmap().String() == item.Address && item.Claim.UID != "" && item.ClaimNamespace != "" && item.ClaimName != "" && item.ClaimNamespace == item.Claim.Namespace && item.ClaimName == item.Claim.Name && item.ObservedGeneration > 0 && dnsprobe.ValidName(name)
}
func invalidateState(state *withdrawal.State) {
	if state != nil {
		state.AbsentSince = nil
		state.NotBefore = nil
		state.VerifiedAt = nil
		state.WindowStart = time.Time{}
		state.Negative = nil
	}
}
func invalidateStatus(status *dnsv1alpha1.ZoneUnitWithdrawalStatus) {
	status.AbsentSince = nil
	status.NotBefore = nil
	status.VerifiedAt = nil
	status.ResultDigest = ""
}

func (r *ZoneReconciler) reconcileWithdrawals(ctx context.Context, unit *dnsv1alpha1.ZoneUnit, env *environment, snapshot hostedv1alpha1.HostedZoneSpec, servers []hostedv1alpha1.HostedZoneServerStatus, state *schedulerState, now time.Time) (time.Duration, error) {
	if len(unit.Spec.Withdrawals) == 0 {
		unit.Status.Withdrawals = nil
		state.Withdrawals = nil
		state.Cursor = ""
		return 0, nil
	}
	old := make(map[string]dnsv1alpha1.ZoneUnitWithdrawalStatus, len(unit.Status.Withdrawals))
	for _, status := range unit.Status.Withdrawals {
		old[status.ClaimNamespace+"/"+status.ClaimName] = status
	}
	unit.Status.Withdrawals = make([]dnsv1alpha1.ZoneUnitWithdrawalStatus, len(unit.Spec.Withdrawals))
	active := make(map[string]bool, len(unit.Spec.Withdrawals))
	var indices []int
	for i, item := range unit.Spec.Withdrawals {
		key := withdrawalKey(item)
		active[key] = true
		status := old[key]
		stored := state.Withdrawals[key]
		identity := withdrawalIdentity(unit, item)
		if status.Claim != item.Claim || status.ObservedGeneration != item.ObservedGeneration {
			status = dnsv1alpha1.ZoneUnitWithdrawalStatus{}
		}
		if stored.Identity != identity {
			missing := stored.Identity == "" && status.LastProbeTime != nil
			stored = withdrawalSchedule{Identity: identity}
			seed := &withdrawal.State{MissingHistory: missing}
			if expires := state.Publications[item.Name+"/"+item.Address]; expires.After(now) {
				seed.Positive = map[string]time.Time{"publication": expires}
			}
			if missing || len(seed.Positive) > 0 {
				stored.State = seed
			}
			invalidateStatus(&status)
		}
		if withdrawal.SnapshotExcludes(snapshot, item) {
			if stored.OmissionSerial == 0 {
				stored.OmissionSerial, stored.OmissionDigest = snapshot.Serial, snapshot.ContentDigest
			}
		} else {
			stored.OmissionSerial = 0
			stored.OmissionDigest = ""
		}
		served := revisionServed(stored.OmissionSerial, stored.OmissionDigest, snapshot, servers)
		fingerprint := withdrawalFingerprint(env.fingerprint, snapshot.ContentDigest, served, item.Waiver)
		status.ClaimNamespace, status.ClaimName, status.Claim, status.ObservedGeneration = item.ClaimNamespace, item.ClaimName, item.Claim, item.ObservedGeneration
		status.SampledWildcard = item.Name == "*" || strings.HasPrefix(item.Name, "*.")
		if !item.Allowed || !validWithdrawal(unit, item) || unit.Spec.Zone.ZoneClassUID != env.class.UID || unit.Spec.Zone.ZoneClassObservedGeneration != env.class.Generation {
			invalidateStatus(&status)
			invalidateState(stored.State)
			stored.Policy = ""
			status.WaiverRef = nil
			meta.RemoveStatusCondition(&status.Conditions, "OverrideAccepted")
			reason := "InvalidWithdrawal"
			if !item.Allowed {
				reason = "NotAllowed"
			}
			setCondition(&status.Conditions, "Accepted", metav1.ConditionFalse, reason, "Hosted DNS withdrawal binding is not accepted.", item.ObservedGeneration, now)
			setWithdrawalDecision(&status, withdrawal.Decision{Withdrawn: metav1.ConditionUnknown, Ready: metav1.ConditionFalse, Reason: hostedv1alpha1.ReasonDNSProbeFailed, Message: "Hosted DNS withdrawal binding is not accepted."}, now)
		} else {
			setCondition(&status.Conditions, "Accepted", metav1.ConditionTrue, "Accepted", "Hosted DNS withdrawal intent is accepted.", item.ObservedGeneration, now)
			if stored.Policy != fingerprint || !stored.Next.After(now) {
				indices = append(indices, i)
			}
			if stored.Policy != fingerprint || meta.FindStatusCondition(status.Conditions, "Withdrawn") == nil {
				invalidateStatus(&status)
				setWithdrawalDecision(&status, withdrawal.Decision{Withdrawn: metav1.ConditionUnknown, Ready: metav1.ConditionFalse, Reason: hostedv1alpha1.ReasonDNSProbeFailed, Message: "Hosted withdrawal verification is pending current served snapshot and policy."}, now)
			}
			bindWithdrawalWaiver(&status, item, now)
			if status.ResultDigest != "" {
				status.ResultDigest = withdrawalResultDigest(unit, item, status)
			}
		}
		state.Withdrawals[key] = stored
		unit.Status.Withdrawals[i] = status
	}
	for key := range state.Withdrawals {
		if !active[key] {
			delete(state.Withdrawals, key)
		}
	}
	slices.SortFunc(indices, func(a, b int) int {
		return strings.Compare(withdrawalKey(unit.Spec.Withdrawals[a]), withdrawalKey(unit.Spec.Withdrawals[b]))
	})
	start := 0
	for start < len(indices) && withdrawalKey(unit.Spec.Withdrawals[indices[start]]) <= state.Cursor {
		start++
	}
	var views []string
	var viewErr error
	if len(indices) > 0 {
		views = env.params.Verification.Views
		if len(views) == 0 {
			views, viewErr = resolveViews(ctx, r.Options.DefaultViews)
		}
	}
	budget := MaxNamesPerPass
	for offset := range len(indices) {
		index := indices[(start+offset)%len(indices)]
		item := unit.Spec.Withdrawals[index]
		key := withdrawalKey(item)
		stored := state.Withdrawals[key]
		cost := 1
		if stored.State != nil {
			cost = max(1, len(stored.State.CheckedNames))
		}
		if cost > budget {
			break
		}
		budget -= cost
		bindingChanged := stored.Binding != "" && stored.Binding != env.fingerprint
		if bindingChanged {
			invalidateState(stored.State)
		}
		decision := withdrawal.Decision{}
		err := viewErr
		if err == nil {
			served := revisionServed(stored.OmissionSerial, stored.OmissionDigest, snapshot, servers)
			decision, err = r.Options.Evaluator.Evaluate(ctx, withdrawal.Input{Claim: item, Snapshot: snapshot, Served: served, Policy: dnsprobe.Policy{Mode: "Recursive", Servers: views}, SOAMinimum: snapshot.SOA.Minimum, Now: now, Previous: stored.State})
		}
		completed := r.Options.Clock()
		if err != nil {
			invalidateState(stored.State)
			decision = withdrawal.Decision{Withdrawn: metav1.ConditionUnknown, Ready: metav1.ConditionFalse, Reason: hostedv1alpha1.ReasonDNSProbeFailed, Message: "Hosted DNS withdrawal probe failed in a required view.", State: stored.State, NextProbe: completed.Add(30 * time.Second)}
		}
		if bindingChanged {
			invalidateState(decision.State)
			decision.Withdrawn, decision.Ready, decision.Reason = metav1.ConditionUnknown, metav1.ConditionFalse, hostedv1alpha1.ReasonPolicyChanged
			decision.Message = "Hosted DNS withdrawal policy changed; the prior required view policy remains a floor."
			decision.AbsentSince, decision.NotBefore, decision.VerifiedAt = nil, nil, nil
		}
		delay := decision.NextProbe.Sub(completed)
		delay = time.Duration(float64(delay) * (1 + max(-1, min(1, r.Options.Jitter()))*0.1))
		delay = max(30*time.Second, min(300*time.Second, delay))
		decision.NextProbe = completed.Add(delay)
		if decision.State != nil {
			decision.State.NextProbe = decision.NextProbe
		}
		status := &unit.Status.Withdrawals[index]
		setWithdrawalDecision(status, decision, completed)
		status.LastProbeTime, status.NextProbeTime = timePointer(completed), timePointer(decision.NextProbe)
		status.PolicyFingerprint = decision.PolicyFingerprint
		status.RequiredViews = slices.Clone(decision.RequiredViews)
		status.CheckedNames = slices.Clone(decision.CheckedNames)
		status.Provider = nil
		if decision.ProviderData != nil {
			status.Provider = providerData(decision.ProviderData)
		}
		status.WaiverRef = decision.WaiverRef
		if decision.OverrideAccepted != nil {
			condition := *decision.OverrideAccepted
			meta.SetStatusCondition(&status.Conditions, condition)
		} else {
			meta.RemoveStatusCondition(&status.Conditions, "OverrideAccepted")
		}
		if err != nil {
			bindWithdrawalWaiver(status, item, completed)
		}
		status.ResultDigest = withdrawalResultDigest(unit, item, *status)
		served := revisionServed(stored.OmissionSerial, stored.OmissionDigest, snapshot, servers)
		stored.Policy, stored.Next, stored.State = withdrawalFingerprint(env.fingerprint, snapshot.ContentDigest, served, item.Waiver), decision.NextProbe, decision.State
		stored.Binding = env.fingerprint
		state.Withdrawals[key] = stored
		state.Cursor = key
		state.Probed = true
	}
	var next time.Duration
	for _, index := range indices {
		stored := state.Withdrawals[withdrawalKey(unit.Spec.Withdrawals[index])]
		next = minDuration(next, max(time.Second, stored.Next.Sub(now)))
	}
	for _, stored := range state.Withdrawals {
		if stored.Next.After(now) {
			next = minDuration(next, stored.Next.Sub(now))
		}
	}
	return next, nil
}
func setWithdrawalDecision(status *dnsv1alpha1.ZoneUnitWithdrawalStatus, decision withdrawal.Decision, now time.Time) {
	status.AbsentSince, status.NotBefore, status.VerifiedAt = decision.AbsentSince, decision.NotBefore, decision.VerifiedAt
	setCondition(&status.Conditions, "Withdrawn", decision.Withdrawn, decision.Reason, decision.Message, status.ObservedGeneration, now)
	setCondition(&status.Conditions, "Ready", decision.Ready, decision.Reason, decision.Message, status.ObservedGeneration, now)
}
func withdrawalResultDigest(unit *dnsv1alpha1.ZoneUnit, item dnsv1alpha1.ZoneUnitWithdrawal, status dnsv1alpha1.ZoneUnitWithdrawalStatus) string {
	status.ResultDigest = ""
	status.Provider = nil
	status.Conditions = slices.Clone(status.Conditions)
	for i := range status.Conditions {
		status.Conditions[i].LastTransitionTime = metav1.Time{}
	}
	slices.SortFunc(status.Conditions, func(a, b metav1.Condition) int { return strings.Compare(a.Type, b.Type) })
	return "sha256:" + dnsprobe.Fingerprint(struct {
		Identity string
		Provider dnsv1alpha1.ProviderReference
		Status   dnsv1alpha1.ZoneUnitWithdrawalStatus
	}{withdrawalIdentity(unit, item), unit.Spec.Provider, status})
}
func rejectWithdrawals(unit *dnsv1alpha1.ZoneUnit, now time.Time) {
	if len(unit.Spec.Withdrawals) == 0 {
		unit.Status.Withdrawals = nil
		return
	}
	state := loadState(unit)
	old := make(map[string]dnsv1alpha1.ZoneUnitWithdrawalStatus, len(unit.Status.Withdrawals))
	for _, status := range unit.Status.Withdrawals {
		old[status.ClaimNamespace+"/"+status.ClaimName] = status
	}
	unit.Status.Withdrawals = make([]dnsv1alpha1.ZoneUnitWithdrawalStatus, len(unit.Spec.Withdrawals))
	for i, item := range unit.Spec.Withdrawals {
		status := old[withdrawalKey(item)]
		if status.Claim != item.Claim || status.ObservedGeneration != item.ObservedGeneration {
			status = dnsv1alpha1.ZoneUnitWithdrawalStatus{}
		}
		status.ClaimNamespace, status.ClaimName, status.Claim, status.ObservedGeneration = item.ClaimNamespace, item.ClaimName, item.Claim, item.ObservedGeneration
		invalidateStatus(&status)
		status.WaiverRef = nil
		meta.RemoveStatusCondition(&status.Conditions, "OverrideAccepted")
		setCondition(&status.Conditions, "Accepted", metav1.ConditionFalse, "InvalidWithdrawal", "Hosted DNS withdrawal binding is not accepted.", item.ObservedGeneration, now)
		setWithdrawalDecision(&status, withdrawal.Decision{Withdrawn: metav1.ConditionUnknown, Ready: metav1.ConditionFalse, Reason: hostedv1alpha1.ReasonDNSProbeFailed, Message: "Hosted DNS withdrawal binding is not accepted."}, now)
		unit.Status.Withdrawals[i] = status
		stored := state.Withdrawals[withdrawalKey(item)]
		invalidateState(stored.State)
		stored.Next = time.Time{}
		state.Withdrawals[withdrawalKey(item)] = stored
	}
	saveState(unit, state)
}

func updatePending(unit *dnsv1alpha1.ZoneUnit) {
	counts := map[string]int{}
	for _, status := range unit.Status.Withdrawals {
		condition := meta.FindStatusCondition(status.Conditions, "Withdrawn")
		if accepted := meta.FindStatusCondition(status.Conditions, "Accepted"); accepted != nil && accepted.Status != metav1.ConditionTrue {
			counts[accepted.Reason]++
		} else if condition == nil {
			counts[hostedv1alpha1.ReasonDNSProbeFailed]++
		} else if condition.Status != metav1.ConditionTrue {
			counts[condition.Reason]++
		}
	}
	withdrawal.SetPending(unit.Namespace+"/"+unit.Name, counts)
}

func withdrawalFingerprint(policy, digest string, served bool, waiver *dnsv1alpha1.ZoneUnitWithdrawalWaiver) string {
	return dnsprobe.Fingerprint(struct {
		Policy, Digest string
		Served         bool
		Waiver         *dnsv1alpha1.ZoneUnitWithdrawalWaiver
	}{policy, digest, served, waiver})
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
