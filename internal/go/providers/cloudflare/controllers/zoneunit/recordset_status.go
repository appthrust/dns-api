package cloudflare

import (
	"context"
	"encoding/json"
	"fmt"
	cloudflarev1alpha1 "github.com/appthrust/dns-api/pkg/go/api/cloudflare/v1alpha1"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"slices"
)

func (r *recordSetReconciler) setReady(ctx context.Context, recordSet *dnsv1alpha1.RecordSet, observed []CloudflareDNSRecord) error {
	if err := validateCloudflareDNSRecordIDs(observed, "Cloudflare DNS record id"); err != nil {
		return r.setProgrammed(ctx, recordSet, metav1.ConditionFalse, "ProviderInvalidRequest", err.Error())
	}
	if err := r.patchCloudflareRecordSetStatus(ctx, recordSet, func(data *cloudflarev1alpha1.CloudflareRecordSetStatusData, state *cloudflarev1alpha1.CloudflareRecordSetState) {
		data.Records = cloudflareDNSRecordStatuses(observed)
		state.Records = cloudflareDNSRecordStates(observed)
	}); err != nil {
		return err
	}
	if err := r.setAccepted(ctx, recordSet, metav1.ConditionTrue, "Accepted", "RecordSet is accepted by Cloudflare policy"); err != nil {
		return err
	}
	return r.setProgrammed(ctx, recordSet, metav1.ConditionTrue, "Programmed", "Cloudflare DNS records match desired state")
}

func (r *recordSetReconciler) setAccepted(ctx context.Context, recordSet *dnsv1alpha1.RecordSet, status metav1.ConditionStatus, reason, message string) error {
	return r.patchStatus(ctx, recordSet, false, func(recordSetStatus *dnsv1alpha1.RecordSetStatus) error {
		recordSetStatus.ObservedGeneration = recordSet.Generation
		setCondition(&recordSetStatus.Conditions, string(dnsv1alpha1.ConditionAccepted), status, reason, message, recordSet.Generation)
		return nil
	})
}

func (r *recordSetReconciler) setProgrammed(ctx context.Context, recordSet *dnsv1alpha1.RecordSet, status metav1.ConditionStatus, reason, message string) error {
	return r.patchStatus(ctx, recordSet, false, func(recordSetStatus *dnsv1alpha1.RecordSetStatus) error {
		recordSetStatus.ObservedGeneration = recordSet.Generation
		setCondition(&recordSetStatus.Conditions, string(dnsv1alpha1.ConditionProgrammed), status, reason, message, recordSet.Generation)
		return nil
	})
}

func (r *recordSetReconciler) patchStatus(ctx context.Context, recordSet *dnsv1alpha1.RecordSet, markDeletionCompleted bool, mutate func(*dnsv1alpha1.RecordSetStatus) error) error {
	zoneNamespace, zoneName := recordSetZoneKey(recordSet)
	var unit dnsv1alpha1.ZoneUnit
	if err := r.Get(ctx, client.ObjectKey{Namespace: zoneNamespace, Name: zoneName}, &unit); err != nil {
		if apierrors.IsNotFound(err) {
			return apierrors.NewConflict(dnsv1alpha1.Resource("zoneunits"), zoneName, fmt.Errorf("ZoneUnit was deleted before RecordSet status could be written"))
		}
		return err
	}
	itemIndex := slices.IndexFunc(unit.Spec.RecordSets, func(item dnsv1alpha1.ZoneUnitRecordSetSpec) bool {
		return cloudflareZoneUnitRecordSetSourceMatches(&unit, item, recordSet)
	})
	if itemIndex < 0 {
		return apierrors.NewConflict(dnsv1alpha1.Resource("zoneunits"), zoneName, fmt.Errorf("ZoneUnit RecordSet source changed before status could be written"))
	}
	item := unit.Spec.RecordSets[itemIndex]
	statusIndex := slices.IndexFunc(unit.Status.RecordSets, func(status dnsv1alpha1.ZoneUnitRecordSetStatus) bool {
		return status.RecordSetNamespace == recordSet.Namespace && status.RecordSetName == recordSet.Name
	})
	deletionAlreadyCompleted := false
	if statusIndex >= 0 && cloudflareZoneUnitRecordSetStatusMatchesItem(unit.Status.RecordSets[statusIndex], item) {
		recordSet.Status = cloudflareRecordSetStatusFromZoneUnitStatus(unit.Status.RecordSets[statusIndex])
		deletionAlreadyCompleted = unit.Status.RecordSets[statusIndex].DeletionCompleted
	} else {
		recordSet.Status = dnsv1alpha1.RecordSetStatus{}
	}
	base := recordSet.DeepCopy()
	if err := mutate(&recordSet.Status); err != nil {
		return err
	}
	if equality.Semantic.DeepEqual(base.Status, recordSet.Status) && (!markDeletionCompleted || deletionAlreadyCompleted) {
		return nil
	}

	unitBase := unit.DeepCopy()
	next := dnsv1alpha1.ZoneUnitRecordSetStatus{
		RecordSetNamespace: recordSet.Namespace,
		RecordSetName:      recordSet.Name,
		RecordSetUID:       recordSet.UID,
		ObservedGeneration: recordSet.Status.ObservedGeneration,
		Provider:           recordSet.Status.Provider,
		Conditions:         slices.Clone(recordSet.Status.Conditions),
		DeletionCompleted:  deletionAlreadyCompleted || markDeletionCompleted,
	}
	if statusIndex >= 0 {
		unit.Status.RecordSets[statusIndex] = next
	} else {
		unit.Status.RecordSets = append(unit.Status.RecordSets, next)
	}
	unit.Status.ObservedGeneration = unit.Generation
	setZoneUnitProgrammedCondition(&unit)
	if equality.Semantic.DeepEqual(unitBase.Status, unit.Status) {
		return nil
	}
	return r.Status().Patch(ctx, &unit, client.MergeFromWithOptions(unitBase, client.MergeFromWithOptimisticLock{}))
}

func setZoneUnitProgrammedCondition(unit *dnsv1alpha1.ZoneUnit) {
	status, reason, message := zoneUnitProgrammedCondition(unit)
	setCondition(&unit.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), status, reason, message, unit.Generation)
}

func zoneUnitProgrammedCondition(unit *dnsv1alpha1.ZoneUnit) (metav1.ConditionStatus, string, string) {
	if unit.Status.Zone == nil {
		return metav1.ConditionUnknown, "Reconciling", "ZoneUnit zone status is not observed"
	}
	status := metav1.ConditionTrue
	reason := "Programmed"
	message := "ZoneUnit is programmed"
	if condition := meta.FindStatusCondition(unit.Status.Zone.Conditions, string(dnsv1alpha1.ConditionProgrammed)); condition == nil {
		status = metav1.ConditionUnknown
		reason = "Reconciling"
		message = "ZoneUnit zone Programmed condition is not observed"
	} else if condition.Status == metav1.ConditionFalse {
		return condition.Status, condition.Reason, condition.Message
	} else if condition.Status == metav1.ConditionUnknown {
		status = metav1.ConditionUnknown
		reason = condition.Reason
		message = condition.Message
	}
	for _, item := range unit.Spec.RecordSets {
		if !item.IsAllowed() && !item.DeletionRequested {
			continue
		}
		condition := zoneUnitRecordSetProgrammedCondition(unit, item)
		if condition == nil {
			if status != metav1.ConditionFalse {
				status = metav1.ConditionUnknown
				reason = "Reconciling"
				message = "ZoneUnit record set Programmed condition is not observed"
			}
			continue
		}
		if condition.Status == metav1.ConditionFalse {
			return condition.Status, condition.Reason, condition.Message
		}
		if condition.Status == metav1.ConditionUnknown && status != metav1.ConditionFalse {
			status = metav1.ConditionUnknown
			reason = condition.Reason
			message = condition.Message
		}
	}
	return status, reason, message
}

func zoneUnitRecordSetProgrammedCondition(unit *dnsv1alpha1.ZoneUnit, item dnsv1alpha1.ZoneUnitRecordSetSpec) *metav1.Condition {
	index := slices.IndexFunc(unit.Status.RecordSets, func(status dnsv1alpha1.ZoneUnitRecordSetStatus) bool {
		return cloudflareZoneUnitRecordSetStatusMatchesItem(status, item)
	})
	if index < 0 {
		return nil
	}
	status := unit.Status.RecordSets[index]
	condition := meta.FindStatusCondition(status.Conditions, string(dnsv1alpha1.ConditionProgrammed))
	if status.ObservedGeneration != item.ObservedGeneration || condition == nil ||
		condition.ObservedGeneration != item.ObservedGeneration {
		return nil
	}
	return condition
}

func (r *recordSetReconciler) patchCloudflareRecordSetStatus(ctx context.Context, recordSet *dnsv1alpha1.RecordSet, mutate func(*cloudflarev1alpha1.CloudflareRecordSetStatusData, *cloudflarev1alpha1.CloudflareRecordSetState)) error {
	return r.patchStatus(ctx, recordSet, false, func(status *dnsv1alpha1.RecordSetStatus) error {
		statusData, err := cloudflareRecordSetStatusDataFromProvider(status.Provider)
		if err != nil {
			return err
		}
		statusState, err := cloudflareRecordSetStateFromProvider(status.Provider)
		if err != nil {
			return err
		}
		mutate(&statusData, &statusState)
		rawData, err := json.Marshal(statusData)
		if err != nil {
			return err
		}
		var rawState []byte
		if len(statusState.Records) > 0 {
			rawState, err = json.Marshal(statusState)
			if err != nil {
				return err
			}
		}
		status.ObservedGeneration = recordSet.Generation
		status.Provider = &dnsv1alpha1.ProviderStatus{Data: runtime.RawExtension{Raw: rawData}}
		if len(rawState) > 0 {
			status.Provider.State = runtime.RawExtension{Raw: rawState}
		}
		return nil
	})
}
