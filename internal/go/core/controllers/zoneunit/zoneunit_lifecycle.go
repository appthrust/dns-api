package zoneunit

import (
	"context"
	"slices"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *ZoneUnitCompositionReconciler) applyZoneUnitDesired(ctx context.Context, desired *dnsv1alpha1.ZoneUnit, create bool) error {
	if create {
		desired.TypeMeta = metav1.TypeMeta{APIVersion: dnsv1alpha1.SchemeGroupVersion.String(), Kind: "ZoneUnit"}
		return client.IgnoreAlreadyExists(r.Create(ctx, desired, client.FieldOwner(coreZoneUnitFieldOwner)))
	}
	var current dnsv1alpha1.ZoneUnit
	if err := r.Get(ctx, client.ObjectKeyFromObject(desired), &current); err != nil {
		return err
	}
	base := current.DeepCopy()
	current.Spec = desired.Spec
	if !slices.Contains(current.Finalizers, coreZoneUnitFinalizer) && current.DeletionTimestamp.IsZero() {
		current.Finalizers = append(current.Finalizers, coreZoneUnitFinalizer)
	}
	syncZoneUnitReconcileRequestAnnotation(&current, desired)
	if equality.Semantic.DeepEqual(base.Spec, current.Spec) &&
		equality.Semantic.DeepEqual(base.Annotations, current.Annotations) &&
		equality.Semantic.DeepEqual(base.Finalizers, current.Finalizers) {
		return nil
	}
	return r.Patch(ctx, &current, client.MergeFrom(base), client.FieldOwner(coreZoneUnitFieldOwner))
}

func (r *ZoneUnitCompositionReconciler) ensureFinalizer(ctx context.Context, obj client.Object, finalizer string) error {
	if slices.Contains(obj.GetFinalizers(), finalizer) || !obj.GetDeletionTimestamp().IsZero() {
		return nil
	}
	base := obj.DeepCopyObject().(client.Object)
	obj.SetFinalizers(append(obj.GetFinalizers(), finalizer))
	return r.Patch(ctx, obj, client.MergeFrom(base))
}

func (r *ZoneUnitCompositionReconciler) removeFinalizer(ctx context.Context, obj client.Object, finalizer string) error {
	if !slices.Contains(obj.GetFinalizers(), finalizer) {
		return nil
	}
	base := obj.DeepCopyObject().(client.Object)
	obj.SetFinalizers(slices.DeleteFunc(obj.GetFinalizers(), func(value string) bool {
		return value == finalizer
	}))
	return client.IgnoreNotFound(r.Patch(ctx, obj, client.MergeFrom(base)))
}

func (r *ZoneUnitCompositionReconciler) removeZoneUnitCoreFinalizer(ctx context.Context, unit *dnsv1alpha1.ZoneUnit) error {
	return r.removeFinalizer(ctx, unit, coreZoneUnitFinalizer)
}

func providerZoneCleanupCompleted(unit *dnsv1alpha1.ZoneUnit) bool {
	for _, finalizer := range unit.Finalizers {
		if finalizer != coreZoneUnitFinalizer {
			return false
		}
	}
	return true
}

func providerRecordSetDeletionCompleted(status dnsv1alpha1.ZoneUnitRecordSetStatus, recordSetUID types.UID) bool {
	return status.DeletionCompleted && recordSetUIDsMatch(status.RecordSetUID, recordSetUID)
}

func syncZoneUnitReconcileRequestAnnotation(unit *dnsv1alpha1.ZoneUnit, source client.Object) {
	sourceAnnotations := source.GetAnnotations()
	value, ok := sourceAnnotations[reconcileRequestAnnotation]
	if !ok {
		if unit.Annotations != nil {
			delete(unit.Annotations, reconcileRequestAnnotation)
			if len(unit.Annotations) == 0 {
				unit.Annotations = nil
			}
		}
		return
	}
	if unit.Annotations == nil {
		unit.Annotations = map[string]string{}
	}
	unit.Annotations[reconcileRequestAnnotation] = value
}
