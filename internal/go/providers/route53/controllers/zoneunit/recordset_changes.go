package route53

import (
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
)

func setRecordSetCondition(conditions *[]metav1.Condition, conditionType string, status metav1.ConditionStatus, reason, message string, observedGeneration int64) {
	meta.SetStatusCondition(conditions, metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: observedGeneration,
	})
}

func route53ChangeCost(change RecordSetChange) int {
	cost := 1
	if len(change.RecordSet.Values) > 0 {
		cost = len(change.RecordSet.Values)
	}
	if change.Action == RecordSetChangeActionUpsert {
		cost *= 2
	}
	return cost
}

func route53BatchOperation(planned []plannedRecordSetChange) string {
	for _, item := range planned {
		if item.change.Action == RecordSetChangeActionUpsert {
			return "UPSERT_BATCH"
		}
	}
	return "DELETE_BATCH"
}

func affectedRecordSets(planned []plannedRecordSetChange) []route53v1alpha1.Route53AffectedRecordSet {
	affected := make([]route53v1alpha1.Route53AffectedRecordSet, 0, len(planned))
	for _, item := range planned {
		affected = append(affected, route53v1alpha1.Route53AffectedRecordSet{
			Namespace: item.recordSet.Namespace,
			Name:      item.recordSet.Name,
			UID:       item.recordSet.UID,
		})
	}
	return affected
}

func affectedRecordSetIncludes(affected []route53v1alpha1.Route53AffectedRecordSet, recordSet *dnsv1alpha1.RecordSet) bool {
	if recordSet.UID == "" {
		return false
	}
	for _, item := range affected {
		if item.UID != "" &&
			item.UID == recordSet.UID &&
			item.Namespace == recordSet.Namespace &&
			item.Name == recordSet.Name {
			return true
		}
	}
	return false
}

func pendingChangeFromChange(change *route53v1alpha1.Route53Change, operation string) *route53v1alpha1.Route53PendingChange {
	if change == nil {
		return nil
	}
	return &route53v1alpha1.Route53PendingChange{
		ID:          change.ID,
		Status:      change.Status,
		Operation:   operation,
		SubmittedAt: change.SubmittedAt,
	}
}

func pendingRecordSetChangeFromChange(change *route53v1alpha1.Route53Change, operation string, affected []route53v1alpha1.Route53AffectedRecordSet) *route53v1alpha1.Route53PendingRecordSetChange {
	if change == nil {
		return nil
	}
	return &route53v1alpha1.Route53PendingRecordSetChange{
		ID:                 change.ID,
		Status:             change.Status,
		Operation:          operation,
		SubmittedAt:        change.SubmittedAt,
		AffectedRecordSets: affected,
	}
}

func compareString(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func (r *ZoneReconciler) resultForProviderError(err error) ctrl.Result {
	if providerErrorReason(err) == "ProviderUnavailable" {
		return ctrl.Result{RequeueAfter: r.changeCheckAfter()}
	}
	return ctrl.Result{RequeueAfter: r.requeueAfter()}
}

func providerErrorMessage(err error) string {
	_, message := providerErrorCondition(err)
	return message
}
