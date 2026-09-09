package zoneunit

import (
	"context"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func (r *ZoneUnitCompositionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("zoneunit-composition").
		For(&dnsv1alpha1.Zone{}).
		Watches(&dnsv1alpha1.RecordSet{}, handler.EnqueueRequestsFromMapFunc(r.mapRecordSetToZone)).
		Watches(&dnsv1alpha1.ZoneUnit{}, handler.EnqueueRequestsFromMapFunc(r.mapZoneUnitToZone)).
		Watches(&dnsv1alpha1.ZoneClass{}, handler.EnqueueRequestsFromMapFunc(r.mapZoneClassToZones)).
		Watches(&dnsv1alpha1.Provider{}, handler.EnqueueRequestsFromMapFunc(r.mapProviderToZones)).
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(r.mapNamespaceToZones)).
		Complete(r)
}

func (r *ZoneUnitCompositionReconciler) mapRecordSetToZone(_ context.Context, obj client.Object) []reconcile.Request {
	recordSet, ok := obj.(*dnsv1alpha1.RecordSet)
	if !ok {
		return nil
	}
	namespace, name := recordSetZoneKey(recordSet)
	return []reconcile.Request{{NamespacedName: client.ObjectKey{Namespace: namespace, Name: name}}}
}

func (r *ZoneUnitCompositionReconciler) mapZoneUnitToZone(_ context.Context, obj client.Object) []reconcile.Request {
	unit, ok := obj.(*dnsv1alpha1.ZoneUnit)
	if !ok {
		return nil
	}
	return []reconcile.Request{{NamespacedName: client.ObjectKey{Namespace: unit.Spec.Zone.Ref.Namespace, Name: unit.Spec.Zone.Ref.Name}}}
}

func (r *ZoneUnitCompositionReconciler) mapZoneClassToZones(ctx context.Context, obj client.Object) []reconcile.Request {
	zoneClass, ok := obj.(*dnsv1alpha1.ZoneClass)
	if !ok {
		return nil
	}
	var zones dnsv1alpha1.ZoneList
	if err := r.List(ctx, &zones); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0)
	for _, zone := range zones.Items {
		namespace := zone.Namespace
		if zone.Spec.ZoneClassRef.Namespace != nil && *zone.Spec.ZoneClassRef.Namespace != "" {
			namespace = *zone.Spec.ZoneClassRef.Namespace
		}
		if namespace == zoneClass.Namespace && zone.Spec.ZoneClassRef.Name == zoneClass.Name {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&zone)})
		}
	}
	return requests
}

func (r *ZoneUnitCompositionReconciler) mapProviderToZones(ctx context.Context, obj client.Object) []reconcile.Request {
	provider, ok := obj.(*dnsv1alpha1.Provider)
	if !ok {
		return nil
	}
	var zones dnsv1alpha1.ZoneList
	if err := r.List(ctx, &zones); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0)
	for _, zone := range zones.Items {
		if zone.Spec.Provider.Name == provider.Name {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&zone)})
		}
	}
	return requests
}

func (r *ZoneUnitCompositionReconciler) mapNamespaceToZones(ctx context.Context, obj client.Object) []reconcile.Request {
	namespace, ok := obj.(*corev1.Namespace)
	if !ok {
		return nil
	}
	seen := map[client.ObjectKey]struct{}{}
	var requests []reconcile.Request
	appendRequest := func(key client.ObjectKey) {
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		requests = append(requests, reconcile.Request{NamespacedName: key})
	}

	var zones dnsv1alpha1.ZoneList
	if err := r.List(ctx, &zones); err != nil {
		return nil
	}
	for _, zone := range zones.Items {
		if zone.Namespace == namespace.Name {
			appendRequest(client.ObjectKeyFromObject(&zone))
		}
	}

	var recordSets dnsv1alpha1.RecordSetList
	if err := r.List(ctx, &recordSets); err != nil {
		return requests
	}
	for _, recordSet := range recordSets.Items {
		if recordSet.Namespace != namespace.Name {
			continue
		}
		zoneNamespace, zoneName := recordSetZoneKey(&recordSet)
		appendRequest(client.ObjectKey{Namespace: zoneNamespace, Name: zoneName})
	}
	return requests
}

func recordSetClaimKey(namespace, name string) string {
	return namespace + "\x00" + name
}

func recordSetZoneKey(recordSet *dnsv1alpha1.RecordSet) (string, string) {
	namespace := recordSet.Namespace
	if recordSet.Spec.ZoneRef.Namespace != nil && *recordSet.Spec.ZoneRef.Namespace != "" {
		namespace = *recordSet.Spec.ZoneRef.Namespace
	}
	return namespace, recordSet.Spec.ZoneRef.Name
}

func zoneUnitRecordSetItemKey(item dnsv1alpha1.ZoneUnitRecordSetSpec) string {
	return recordSetClaimKey(item.RecordSetNamespace, item.RecordSetName)
}

func zoneUnitRecordSetStatusKey(item dnsv1alpha1.ZoneUnitRecordSetStatus) string {
	return recordSetClaimKey(item.RecordSetNamespace, item.RecordSetName)
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
