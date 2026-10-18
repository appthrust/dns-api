package zoneunit

import (
	"context"

	"github.com/appthrust/dns-api/internal/go/core/declaration"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// RecordWithdrawalWaiverReconciler owns waiver status independently of Zone
// existence, so missing or archived claims cannot leave Accepted=True behind.
// Composition still validates exact bindings directly before attaching a waiver.
type RecordWithdrawalWaiverReconciler struct{ client.Client }

func (r *RecordWithdrawalWaiverReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var waiver dnsv1alpha1.RecordWithdrawalWaiver
	if err := r.Get(ctx, req.NamespacedName, &waiver); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	var claim dnsv1alpha1.RecordWithdrawal
	err := r.Get(ctx, client.ObjectKey{Namespace: waiver.Spec.WithdrawalRef.Namespace, Name: waiver.Spec.WithdrawalRef.Name}, &claim)
	if err != nil && client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, r.setWaiverStatus(ctx, &waiver, err == nil && declaration.WithdrawalWaiverMatches(&waiver, &claim))
}

func (r *RecordWithdrawalWaiverReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).Named("recordwithdrawal-waiver").For(&dnsv1alpha1.RecordWithdrawalWaiver{}).Watches(&dnsv1alpha1.RecordWithdrawal{}, handler.EnqueueRequestsFromMapFunc(r.mapClaimToWaivers)).Complete(r)
}

func (r *RecordWithdrawalWaiverReconciler) mapClaimToWaivers(ctx context.Context, obj client.Object) []reconcile.Request {
	var waivers dnsv1alpha1.RecordWithdrawalWaiverList
	if err := r.List(ctx, &waivers, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0)
	for i := range waivers.Items {
		waiver := &waivers.Items[i]
		if waiver.Spec.WithdrawalRef.Namespace == obj.GetNamespace() && waiver.Spec.WithdrawalRef.Name == obj.GetName() {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(waiver)})
		}
	}
	return requests
}
func (r *RecordWithdrawalWaiverReconciler) setWaiverStatus(ctx context.Context, waiver *dnsv1alpha1.RecordWithdrawalWaiver, valid bool) error {
	base := waiver.DeepCopy()
	condition := metav1.Condition{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionFalse, Reason: "InvalidWithdrawal", Message: "Waiver does not match the exact claim UID, immutable spec digest and address", ObservedGeneration: waiver.Generation}
	if valid {
		condition.Status, condition.Reason, condition.Message = metav1.ConditionTrue, "Accepted", "Waiver is bound to the exact claim and receiving reservation"
	}
	waiver.Status.ObservedGeneration = waiver.Generation
	meta.SetStatusCondition(&waiver.Status.Conditions, condition)
	if equality.Semantic.DeepEqual(base.Status, waiver.Status) {
		return nil
	}
	return client.IgnoreNotFound(r.Status().Patch(ctx, waiver, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})))
}
