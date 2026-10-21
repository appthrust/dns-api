package zoneunit

import (
	"encoding/json"
	"slices"
	"time"

	probe "github.com/appthrust/dns-api/internal/go/core/dnsprobe"
	"github.com/appthrust/dns-api/internal/go/providers/external/verification"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	externalv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/external/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func setCondition(conditions *[]metav1.Condition, kind string, status metav1.ConditionStatus, reason, message string, generation int64, now time.Time) {
	meta.SetStatusCondition(conditions, metav1.Condition{Type: kind, Status: status, Reason: reason, Message: message, ObservedGeneration: generation, LastTransitionTime: metav1.NewTime(now)})
}

func prepareReceipts(unit *dnsv1alpha1.ZoneUnit) {
	old := make(map[string]dnsv1alpha1.ZoneUnitRecordSetStatus, len(unit.Status.RecordSets))
	for _, item := range unit.Status.RecordSets {
		old[item.RecordSetNamespace+"/"+item.RecordSetName] = item
	}
	unit.Status.RecordSets = make([]dnsv1alpha1.ZoneUnitRecordSetStatus, len(unit.Spec.RecordSets))
	for i, item := range unit.Spec.RecordSets {
		status := old[itemKey(item)]
		if status.RecordSetUID != item.RecordSetUID || status.ObservedGeneration != item.ObservedGeneration {
			status = dnsv1alpha1.ZoneUnitRecordSetStatus{}
		}
		status.RecordSetNamespace, status.RecordSetName, status.RecordSetUID, status.ObservedGeneration = item.RecordSetNamespace, item.RecordSetName, item.RecordSetUID, item.ObservedGeneration
		status.DeletionCompleted = item.DeletionRequested && item.RecordSetUID != ""
		if item.DeletionRequested || !item.IsAllowed() || item.RecordSetUID == "" {
			status.Provider = nil
			status.Conditions = nil
		}
		unit.Status.RecordSets[i] = status
	}
	if unit.Status.Zone == nil {
		unit.Status.Zone = &dnsv1alpha1.ZoneUnitZoneStatus{}
	}
}

func resetProgrammed(status *dnsv1alpha1.ZoneUnitRecordSetStatus, now time.Time) {
	status.Provider = nil
	setCondition(&status.Conditions, "Programmed", metav1.ConditionUnknown, "Reconciling", "DNS verification is pending current inputs.", status.ObservedGeneration, now)
}

func currentDecision(conditions []metav1.Condition) probe.Decision {
	condition := meta.FindStatusCondition(conditions, "Programmed")
	if condition == nil {
		return probe.Decision{Status: metav1.ConditionUnknown, Reason: "Reconciling", Message: "DNS verification is pending."}
	}
	return probe.Decision{Status: condition.Status, Reason: condition.Reason, Message: condition.Message}
}

func publicData(policy probe.Policy, name string, sampled bool, decision probe.Decision, now, next time.Time) *dnsv1alpha1.ProviderStatus {
	data := externalv1alpha1.ExternalVerificationData{
		ResolverMode: externalv1alpha1.ResolverMode(policy.Mode), CheckedName: name, SampledWildcard: sampled,
		Actual: externalv1alpha1.VerificationValues(probe.Preview(decision.Actual)), Missing: externalv1alpha1.VerificationValues(probe.Preview(decision.Missing)), Extra: externalv1alpha1.VerificationValues(probe.Preview(decision.Extra)),
		CNAMEChain: externalv1alpha1.VerificationValues(probe.Preview(decision.Chain)), Views: externalv1alpha1.VerificationValues(probe.Preview(decision.Views)),
		ObservedTTLSeconds: decision.TTL, NegativeTTLSeconds: decision.NegativeTTL,
		LastProbeTime: timePointer(now), NextProbeTime: timePointer(next),
	}
	if decision.NegativeTTL != nil {
		data.NegativeExpiryTime = timePointer(now.Add(time.Duration(*decision.NegativeTTL) * time.Second))
	}
	encoded, _ := json.Marshal(data)
	return &dnsv1alpha1.ProviderStatus{Data: runtime.RawExtension{Raw: encoded}}
}

func timePointer(value time.Time) *metav1.Time { result := metav1.NewTime(value); return &result }

func setZoneResult(unit *dnsv1alpha1.ZoneUnit, policy probe.Policy, decision probe.Decision, now, next time.Time) {
	zone := unit.Status.Zone
	zone.NameServers = slices.Clone(decision.Nameservers)
	zone.Provider = publicData(policy, unit.Spec.Zone.DomainName, false, decision, now, next)
	setCondition(&zone.Conditions, "Accepted", metav1.ConditionTrue, "Accepted", "External DNS Zone binding is accepted.", unit.Spec.Zone.ObservedGeneration, now)
	setCondition(&zone.Conditions, "Programmed", decision.Status, decision.Reason, decision.Message, unit.Spec.Zone.ObservedGeneration, now)
}

func setGroupResult(unit *dnsv1alpha1.ZoneUnit, group nameGroup, policy probe.Policy, decision probe.Decision, now, next time.Time) {
	data := publicData(policy, group.CheckedName, group.SampledWildcard, decision, now, next)
	for _, index := range group.Items {
		item := &unit.Status.RecordSets[index]
		item.Provider = data
		setCondition(&item.Conditions, "Accepted", metav1.ConditionTrue, "Accepted", "External DNS record intent is accepted.", item.ObservedGeneration, now)
		setCondition(&item.Conditions, "Programmed", decision.Status, decision.Reason, decision.Message, item.ObservedGeneration, now)
	}
}

// UnitReason is the explicit boundary to core's fixed ZoneUnit reason allowlist.
func UnitReason(decision probe.Decision) (metav1.ConditionStatus, string) {
	if decision.Status == metav1.ConditionTrue {
		return metav1.ConditionTrue, "Programmed"
	}
	if decision.Reason == "DNSProbeFailed" {
		return metav1.ConditionUnknown, "ProviderUnavailable"
	}
	if decision.Status == metav1.ConditionUnknown {
		return metav1.ConditionUnknown, "Reconciling"
	}
	if decision.Reason == "DNSNameNotFound" {
		return metav1.ConditionFalse, "ExternalResourceNotFound"
	}
	return metav1.ConditionFalse, "ExternalResourceMismatch"
}

func setAggregate(unit *dnsv1alpha1.ZoneUnit, now time.Time) {
	decision := currentDecision(unit.Status.Zone.Conditions)
	for i, status := range unit.Status.RecordSets {
		item := unit.Spec.RecordSets[i]
		if !item.IsAllowed() || item.DeletionRequested {
			continue
		}
		candidate := currentDecision(status.Conditions)
		if decision.Status != metav1.ConditionFalse && (candidate.Status == metav1.ConditionFalse || (candidate.Status == metav1.ConditionUnknown && decision.Status == metav1.ConditionTrue)) {
			decision = candidate
		}
	}
	status, reason := UnitReason(decision)
	setCondition(&unit.Status.Conditions, "Programmed", status, reason, decision.Message, unit.Generation, now)
}

func (r *ZoneReconciler) reject(unit *dnsv1alpha1.ZoneUnit, rejected acceptance) {
	now := r.Options.Clock()
	unit.Status.Zone.Provider = nil
	unit.Status.Zone.NameServers = nil
	setCondition(&unit.Status.Zone.Conditions, "Accepted", rejected.Status, rejected.Reason, rejected.Message, unit.Spec.Zone.ObservedGeneration, now)
	setCondition(&unit.Status.Zone.Conditions, "Programmed", metav1.ConditionUnknown, "Reconciling", rejected.Message, unit.Spec.Zone.ObservedGeneration, now)
	for i := range unit.Status.RecordSets {
		if unit.Spec.RecordSets[i].DeletionRequested {
			continue
		}
		setCondition(&unit.Status.RecordSets[i].Conditions, "Accepted", metav1.ConditionFalse, "ZoneNotAccepted", rejected.Message, unit.Status.RecordSets[i].ObservedGeneration, now)
		resetProgrammed(&unit.Status.RecordSets[i], now)
	}
	rejectWithdrawals(unit, now)
	setAggregate(unit, now)
	r.updatePending(unit)
}

func (r *ZoneReconciler) updatePending(unit *dnsv1alpha1.ZoneUnit) {
	counts := map[string]int{}
	for i, status := range unit.Status.RecordSets {
		item := unit.Spec.RecordSets[i]
		if !item.IsAllowed() || item.DeletionRequested {
			continue
		}
		decision := currentDecision(status.Conditions)
		if decision.Status != metav1.ConditionTrue {
			counts[decision.Reason]++
		}
	}
	verification.SetPending(unit.Namespace+"/"+unit.Name, counts)
	withdrawals := map[string]int{}
	for _, status := range unit.Status.Withdrawals {
		condition := meta.FindStatusCondition(status.Conditions, "Withdrawn")
		if accepted := meta.FindStatusCondition(status.Conditions, "Accepted"); accepted != nil && accepted.Status != metav1.ConditionTrue {
			withdrawals[accepted.Reason]++
		} else if condition == nil {
			withdrawals["DNSProbeFailed"]++
		} else if condition.Status != metav1.ConditionTrue {
			withdrawals[condition.Reason]++
		}
	}
	verification.SetWithdrawalsPending(unit.Namespace+"/"+unit.Name, withdrawals)
}
