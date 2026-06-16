package route53

import (
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func isAcceptedCurrent(conditions []metav1.Condition, generation int64) bool {
	condition := meta.FindStatusCondition(conditions, string(dnsv1alpha1.ConditionAccepted))
	return condition != nil &&
		condition.Status == metav1.ConditionTrue &&
		condition.ObservedGeneration == generation
}

func isReadyCurrent(conditions []metav1.Condition, generation int64) bool {
	condition := meta.FindStatusCondition(conditions, "Ready")
	return condition != nil &&
		condition.Status == metav1.ConditionTrue &&
		condition.ObservedGeneration == generation
}
