package zoneunit

import (
	"context"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func (r *ZoneReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Reader = mgr.GetAPIReader()
	return ctrl.NewControllerManagedBy(mgr).Named("external-zoneunit").
		WithOptions(controller.Options{MaxConcurrentReconciles: 4}).
		For(&dnsv1alpha1.ZoneUnit{}, builder.WithPredicates(predicate.Funcs{UpdateFunc: func(e event.UpdateEvent) bool {
			return e.ObjectOld.GetGeneration() != e.ObjectNew.GetGeneration() || !e.ObjectOld.GetDeletionTimestamp().Equal(e.ObjectNew.GetDeletionTimestamp())
		}})).
		Watches(&dnsv1alpha1.ZoneClass{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
			return r.mapUnits(ctx, func(unit *dnsv1alpha1.ZoneUnit) bool {
				ref := unit.Spec.Zone.ZoneClassRef
				return ref.Namespace == obj.GetNamespace() && ref.Name == obj.GetName()
			})
		})).
		Watches(&dnsv1alpha1.Provider{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
			return r.mapUnits(ctx, func(unit *dnsv1alpha1.ZoneUnit) bool { return unit.Spec.Provider.Name == obj.GetName() })
		}), builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
			return r.mapUnits(ctx, func(unit *dnsv1alpha1.ZoneUnit) bool {
				if unit.Spec.Zone.Ref.Namespace == obj.GetName() {
					return true
				}
				for _, item := range unit.Spec.RecordSets {
					if item.RecordSetNamespace == obj.GetName() {
						return true
					}
				}
				return false
			})
		}), builder.WithPredicates(predicate.LabelChangedPredicate{})).
		Complete(r)
}

func (r *ZoneReconciler) mapUnits(ctx context.Context, matches func(*dnsv1alpha1.ZoneUnit) bool) []reconcile.Request {
	// Real API pagination bounds each dependency read; cache List does not provide
	// continuation tokens and custom informer indexes are not API field selectors.
	reader := r.Reader
	if reader == nil {
		reader = r.Client
	}
	var requests []reconcile.Request
	continuation := ""
	for {
		var units dnsv1alpha1.ZoneUnitList
		if err := reader.List(ctx, &units, client.Limit(256), client.Continue(continuation)); err != nil {
			return nil
		}
		for i := range units.Items {
			if matches(&units.Items[i]) {
				requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&units.Items[i])})
			}
		}
		continuation = units.Continue
		if continuation == "" {
			return requests
		}
	}
}
