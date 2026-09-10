package zoneunit

import (
	"context"
	"slices"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *ZoneUnitCompositionReconciler) projectZoneUnitStatus(ctx context.Context, zone *dnsv1alpha1.Zone, zoneClass *dnsv1alpha1.ZoneClass, recordSets []dnsv1alpha1.RecordSet, unit *dnsv1alpha1.ZoneUnit) error {
	if unit.Status.Zone != nil {
		accepted := projectedAccepted(unit.Spec.Zone.ObservedGeneration, zone.Status.Conditions, unit.Status.Zone.Conditions)
		programmed := projectedProgrammed(unit.Spec.Zone.ObservedGeneration, accepted.Status, unit.Status.Zone.Conditions)
		if err := r.setZoneClaimStatus(ctx, zone, accepted.Status, accepted.Reason, programmed.Status, programmed.Reason, unit.Status.Zone); err != nil {
			return err
		}
	}
	statusByRef := map[string]dnsv1alpha1.ZoneUnitRecordSetStatus{}
	incompatibleStatusByRef := map[string]struct{}{}
	desiredByRef := map[string]dnsv1alpha1.ZoneUnitRecordSetSpec{}
	for _, item := range unit.Spec.RecordSets {
		desiredByRef[zoneUnitRecordSetItemKey(item)] = item
	}
	for _, status := range unit.Status.RecordSets {
		refKey := zoneUnitRecordSetStatusKey(status)
		desiredItem, ok := desiredByRef[refKey]
		if !ok || retainedNotAllowedRecordSetItem(desiredItem) {
			continue
		}
		if staleCompletedDeletionStatusForActiveItem(status, desiredItem) || !zoneUnitRecordSetStatusMatchesItem(status, desiredItem) {
			incompatibleStatusByRef[refKey] = struct{}{}
			continue
		}
		statusByRef[refKey] = status
	}
	for index := range recordSets {
		recordSet := &recordSets[index]
		refKey := recordSetClaimKey(recordSet.Namespace, recordSet.Name)
		desiredItem, currentClaim := desiredByRef[refKey]
		providerStatus, hasProviderStatus := statusByRef[refKey]
		if !currentClaim || !zoneUnitRecordSetItemMatchesRecordSet(desiredItem, recordSet) {
			continue
		}
		if !hasProviderStatus {
			if _, incompatible := incompatibleStatusByRef[refKey]; !incompatible {
				continue
			}
			if err := r.setRecordSetClaimStatusWithZone(ctx, recordSet, metav1.ConditionUnknown, "OwnerStateNotResolved", metav1.ConditionUnknown, "Reconciling", recordSetZoneStatus(zone, zoneClass), &dnsv1alpha1.ZoneUnitRecordSetStatus{}); err != nil {
				return err
			}
			continue
		}
		var providerConditions []metav1.Condition
		if providerStatus.ObservedGeneration == desiredItem.ObservedGeneration {
			providerConditions = providerStatus.Conditions
		}
		accepted := projectedAccepted(desiredItem.ObservedGeneration, recordSet.Status.Conditions, providerConditions)
		programmed := projectedProgrammed(desiredItem.ObservedGeneration, accepted.Status, providerConditions)
		if err := r.setRecordSetClaimStatusWithZone(ctx, recordSet, accepted.Status, accepted.Reason, programmed.Status, programmed.Reason, recordSetZoneStatus(zone, zoneClass), &providerStatus); err != nil {
			return err
		}
	}
	return nil
}

func staleCompletedDeletionStatusForActiveItem(status dnsv1alpha1.ZoneUnitRecordSetStatus, item dnsv1alpha1.ZoneUnitRecordSetSpec) bool {
	return status.DeletionCompleted && !item.DeletionRequested
}

func retainedNotAllowedRecordSetItem(item dnsv1alpha1.ZoneUnitRecordSetSpec) bool {
	return !item.IsAllowed() && !item.DeletionRequested
}

type projectedCondition struct {
	Status metav1.ConditionStatus
	Reason string
}

func projectedAccepted(observedGeneration int64, compositionConditions, providerConditions []metav1.Condition) projectedCondition {
	provider := conditionAtGeneration(providerConditions, string(dnsv1alpha1.ConditionAccepted), observedGeneration)
	if provider != nil {
		return projectedCondition{Status: provider.Status, Reason: provider.Reason}
	}
	composition := conditionAtGeneration(compositionConditions, string(dnsv1alpha1.ConditionAccepted), observedGeneration)
	if composition != nil && composition.Status != metav1.ConditionTrue {
		return projectedCondition{Status: composition.Status, Reason: composition.Reason}
	}
	return projectedCondition{Status: metav1.ConditionUnknown, Reason: "OwnerStateNotResolved"}
}

func projectedProgrammed(observedGeneration int64, accepted metav1.ConditionStatus, providerConditions []metav1.Condition) projectedCondition {
	if accepted != metav1.ConditionTrue {
		return projectedCondition{Status: metav1.ConditionUnknown, Reason: "Reconciling"}
	}
	provider := conditionAtGeneration(providerConditions, string(dnsv1alpha1.ConditionProgrammed), observedGeneration)
	if provider == nil {
		return projectedCondition{Status: metav1.ConditionUnknown, Reason: "Reconciling"}
	}
	return projectedCondition{Status: provider.Status, Reason: provider.Reason}
}

func conditionAtGeneration(conditions []metav1.Condition, conditionType string, observedGeneration int64) *metav1.Condition {
	condition := meta.FindStatusCondition(conditions, conditionType)
	if condition == nil || condition.ObservedGeneration != observedGeneration {
		return nil
	}
	return condition
}

func (r *ZoneUnitCompositionReconciler) writeRecordSetsWaiting(ctx context.Context, recordSets []dnsv1alpha1.RecordSet, reason string) error {
	for index := range recordSets {
		if err := r.setRecordSetClaimStatus(ctx, &recordSets[index], metav1.ConditionUnknown, reason, metav1.ConditionUnknown, "Reconciling", nil); err != nil {
			return err
		}
	}
	return nil
}

func (r *ZoneUnitCompositionReconciler) writeRecordSetsNotAccepted(ctx context.Context, recordSets []dnsv1alpha1.RecordSet, reason string) error {
	for index := range recordSets {
		if err := r.setRecordSetClaimStatus(ctx, &recordSets[index], metav1.ConditionFalse, reason, metav1.ConditionUnknown, "Reconciling", nil); err != nil {
			return err
		}
	}
	return nil
}

func (r *ZoneUnitCompositionReconciler) setZoneClaimStatus(ctx context.Context, zone *dnsv1alpha1.Zone, acceptedStatus metav1.ConditionStatus, acceptedReason string, programmedStatus metav1.ConditionStatus, programmedReason string, providerStatus *dnsv1alpha1.ZoneUnitZoneStatus) error {
	return r.setZoneClaimStatusWithMessages(ctx, zone, acceptedStatus, acceptedReason, acceptedReason, programmedStatus, programmedReason, programmedReason, providerStatus)
}

func (r *ZoneUnitCompositionReconciler) setZoneClaimStatusWithMessages(ctx context.Context, zone *dnsv1alpha1.Zone, acceptedStatus metav1.ConditionStatus, acceptedReason, acceptedMessage string, programmedStatus metav1.ConditionStatus, programmedReason, programmedMessage string, providerStatus *dnsv1alpha1.ZoneUnitZoneStatus) error {
	base := zone.DeepCopy()
	zone.Status.ObservedGeneration = zone.Generation
	meta.SetStatusCondition(&zone.Status.Conditions, metav1.Condition{Type: string(dnsv1alpha1.ConditionAccepted), Status: acceptedStatus, Reason: acceptedReason, Message: acceptedMessage, ObservedGeneration: zone.Generation})
	meta.SetStatusCondition(&zone.Status.Conditions, metav1.Condition{Type: string(dnsv1alpha1.ConditionProgrammed), Status: programmedStatus, Reason: programmedReason, Message: programmedMessage, ObservedGeneration: zone.Generation})
	if providerStatus != nil {
		zone.Status.NameServers = slices.Clone(providerStatus.NameServers)
		if providerStatus.Provider != nil && len(providerStatus.Provider.Data.Raw) > 0 {
			zone.Status.Provider = &dnsv1alpha1.ProviderStatus{Data: providerStatus.Provider.Data}
		} else {
			zone.Status.Provider = nil
		}
	}
	if equality.Semantic.DeepEqual(base.Status, zone.Status) {
		return nil
	}
	return client.IgnoreNotFound(r.Status().Patch(ctx, zone, client.MergeFrom(base)))
}

func (r *ZoneUnitCompositionReconciler) setRecordSetClaimStatus(ctx context.Context, recordSet *dnsv1alpha1.RecordSet, acceptedStatus metav1.ConditionStatus, acceptedReason string, programmedStatus metav1.ConditionStatus, programmedReason string, providerStatus *dnsv1alpha1.ZoneUnitRecordSetStatus) error {
	return r.setRecordSetClaimStatusWithMessages(ctx, recordSet, acceptedStatus, acceptedReason, acceptedReason, programmedStatus, programmedReason, programmedReason, providerStatus)
}

func (r *ZoneUnitCompositionReconciler) setRecordSetClaimStatusWithZone(ctx context.Context, recordSet *dnsv1alpha1.RecordSet, acceptedStatus metav1.ConditionStatus, acceptedReason string, programmedStatus metav1.ConditionStatus, programmedReason string, zoneStatus *dnsv1alpha1.RecordSetZoneStatus, providerStatus *dnsv1alpha1.ZoneUnitRecordSetStatus) error {
	return r.setRecordSetClaimStatusWithZoneAndMessages(ctx, recordSet, acceptedStatus, acceptedReason, acceptedReason, programmedStatus, programmedReason, programmedReason, zoneStatus, providerStatus)
}

func (r *ZoneUnitCompositionReconciler) setRecordSetClaimStatusWithMessages(ctx context.Context, recordSet *dnsv1alpha1.RecordSet, acceptedStatus metav1.ConditionStatus, acceptedReason, acceptedMessage string, programmedStatus metav1.ConditionStatus, programmedReason, programmedMessage string, providerStatus *dnsv1alpha1.ZoneUnitRecordSetStatus) error {
	return r.setRecordSetClaimStatusWithZoneAndMessages(ctx, recordSet, acceptedStatus, acceptedReason, acceptedMessage, programmedStatus, programmedReason, programmedMessage, nil, providerStatus)
}

func (r *ZoneUnitCompositionReconciler) setRecordSetClaimStatusWithZoneAndMessages(ctx context.Context, recordSet *dnsv1alpha1.RecordSet, acceptedStatus metav1.ConditionStatus, acceptedReason, acceptedMessage string, programmedStatus metav1.ConditionStatus, programmedReason, programmedMessage string, zoneStatus *dnsv1alpha1.RecordSetZoneStatus, providerStatus *dnsv1alpha1.ZoneUnitRecordSetStatus) error {
	base := recordSet.DeepCopy()
	recordSet.Status.ObservedGeneration = recordSet.Generation
	if zoneStatus != nil {
		recordSet.Status.Zone = zoneStatus
	}
	meta.SetStatusCondition(&recordSet.Status.Conditions, metav1.Condition{Type: string(dnsv1alpha1.ConditionAccepted), Status: acceptedStatus, Reason: acceptedReason, Message: acceptedMessage, ObservedGeneration: recordSet.Generation})
	meta.SetStatusCondition(&recordSet.Status.Conditions, metav1.Condition{Type: string(dnsv1alpha1.ConditionProgrammed), Status: programmedStatus, Reason: programmedReason, Message: programmedMessage, ObservedGeneration: recordSet.Generation})
	if providerStatus != nil && providerStatus.Provider != nil && len(providerStatus.Provider.Data.Raw) > 0 {
		recordSet.Status.Provider = &dnsv1alpha1.ProviderStatus{Data: providerStatus.Provider.Data}
	} else if providerStatus != nil {
		recordSet.Status.Provider = nil
	}
	if equality.Semantic.DeepEqual(base.Status, recordSet.Status) {
		return nil
	}
	return client.IgnoreNotFound(r.Status().Patch(ctx, recordSet, client.MergeFrom(base)))
}

func recordSetZoneStatus(zone *dnsv1alpha1.Zone, zoneClass *dnsv1alpha1.ZoneClass) *dnsv1alpha1.RecordSetZoneStatus {
	if zone == nil {
		return nil
	}
	return &dnsv1alpha1.RecordSetZoneStatus{
		Ref: dnsv1alpha1.ObjectReference{Namespace: zone.Namespace, Name: zone.Name},
	}
}
