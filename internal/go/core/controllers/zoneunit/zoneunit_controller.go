package zoneunit

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/appthrust/dns-api/internal/go/core/celruntime"
	"github.com/appthrust/dns-api/internal/go/core/declaration"
	"github.com/appthrust/dns-api/internal/go/core/providercontract"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	"github.com/google/cel-go/cel"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	coreZoneUnitFieldOwner                = "dns-api-core-composition-controller"
	coreZoneFinalizer                     = "dns.appthrust.io/zone-finalizer"
	coreRecordSetFinalizer                = "dns.appthrust.io/recordset-finalizer"
	coreZoneUnitFinalizer                 = "dns.appthrust.io/zoneunit-finalizer"
	reconcileRequestAnnotation            = "dns.appthrust.io/reconcile-request"
	recordSetNotAcceptedProgrammedMessage = "RecordSet programming is waiting for Accepted=True"
)

// ZoneUnitCompositionReconciler builds ZoneUnit desired state from Zone and RecordSet claims.
type ZoneUnitCompositionReconciler struct {
	client.Client
}

// +kubebuilder:rbac:groups=dns.appthrust.io,resources=providers,verbs=get;list;watch
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=zoneclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=zones,verbs=get;list;watch;patch;update
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=zones/finalizers,verbs=update
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=zones/status,verbs=get;patch;update
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=recordsets,verbs=get;list;watch;patch;update
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=recordsets/finalizers,verbs=update
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=recordsets/status,verbs=get;patch;update
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=zoneunits,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=zoneunits/finalizers,verbs=update
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=zoneunits/status,verbs=get;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
func (r *ZoneUnitCompositionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	recordSets, err := r.recordSetsForZoneKey(ctx, req.NamespacedName)
	if err != nil {
		return ctrl.Result{}, err
	}

	var existing dnsv1alpha1.ZoneUnit
	unitExists := true
	if err := r.Get(ctx, req.NamespacedName, &existing); err != nil {
		if apierrors.IsNotFound(err) {
			unitExists = false
		} else {
			return ctrl.Result{}, err
		}
	}

	var zone dnsv1alpha1.Zone
	if err := r.Get(ctx, req.NamespacedName, &zone); err != nil {
		if apierrors.IsNotFound(err) {
			if unitExists && !existing.DeletionTimestamp.IsZero() && providerZoneCleanupCompleted(&existing) {
				return ctrl.Result{}, r.removeZoneUnitCoreFinalizer(ctx, &existing)
			}
			return ctrl.Result{}, r.writeRecordSetsWaiting(ctx, recordSets, "ZoneNotResolved")
		}
		return ctrl.Result{}, err
	}

	zoneClass, ok, err := r.zoneClassForZone(ctx, &zone)
	if err != nil || !ok {
		if err != nil {
			return ctrl.Result{}, err
		}
		if statusErr := r.setZoneClaimStatus(ctx, &zone, metav1.ConditionUnknown, "ZoneClassNotResolved", metav1.ConditionUnknown, "Reconciling", nil); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{}, r.writeRecordSetsWaiting(ctx, recordSets, "ZoneClassNotResolved")
	}

	if zone.Spec.Provider != zoneClass.Spec.Provider {
		if err := r.setZoneClaimStatus(ctx, &zone, metav1.ConditionFalse, "ProviderMismatch", metav1.ConditionUnknown, "Reconciling", nil); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.writeRecordSetsNotAccepted(ctx, recordSets, "ZoneNotAccepted")
	}

	allowed, err := declaration.ZoneAllowedByClass(ctx, r.Client, zoneClass, zone.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !allowed {
		if err := r.setZoneClaimStatusWithMessages(ctx, &zone, metav1.ConditionFalse, "NotAllowedByZoneClass", "Zone namespace is not allowed by ZoneClass", metav1.ConditionUnknown, "Reconciling", "Reconciling", nil); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.writeRecordSetsNotAccepted(ctx, recordSets, "ZoneNotAccepted")
	}

	var provider dnsv1alpha1.Provider
	if err := r.Get(ctx, client.ObjectKey{Name: zoneClass.Spec.Provider.Name}, &provider); err != nil {
		if apierrors.IsNotFound(err) {
			if statusErr := r.setZoneClaimStatus(ctx, &zone, metav1.ConditionUnknown, "ProviderStorageVersionNotResolved", metav1.ConditionUnknown, "Reconciling", nil); statusErr != nil {
				return ctrl.Result{}, statusErr
			}
			return ctrl.Result{}, r.writeRecordSetsWaiting(ctx, recordSets, "ProviderStorageVersionNotResolved")
		}
		return ctrl.Result{}, err
	}
	inputVersion, ok := providercontract.ServedVersion(&provider, zone.Spec.Provider.Version)
	if !ok {
		if statusErr := r.setZoneClaimStatus(ctx, &zone, metav1.ConditionUnknown, "ProviderStorageVersionNotResolved", metav1.ConditionUnknown, "Reconciling", nil); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{}, r.writeRecordSetsWaiting(ctx, recordSets, "ProviderStorageVersionNotResolved")
	}
	storageVersion, ok := providercontract.StorageVersion(&provider)
	if !ok {
		if statusErr := r.setZoneClaimStatus(ctx, &zone, metav1.ConditionUnknown, "ProviderStorageVersionNotResolved", metav1.ConditionUnknown, "Reconciling", nil); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{}, r.writeRecordSetsWaiting(ctx, recordSets, "ProviderStorageVersionNotResolved")
	}

	if zone.DeletionTimestamp.IsZero() {
		if err := r.ensureFinalizer(ctx, &zone, coreZoneFinalizer); err != nil {
			return ctrl.Result{}, err
		}
	}

	convertedZonePayload, payloadErr := providercontract.ConvertProviderPayloadToStorage(providercontract.ConversionInput{
		Provider:    &provider,
		Target:      providercontract.TargetZone,
		FromVersion: inputVersion,
		ToVersion:   storageVersion,
		Payload: providercontract.Payload{
			Adoption: zone.Spec.Adoption,
		},
		Context: map[string]interface{}{
			"zone": conversionObject(&zone),
		},
	})
	if payloadErr != nil {
		acceptance := providercontract.PayloadErrorToAcceptance(payloadErr)
		if err := r.setZoneClaimStatusWithMessages(ctx, &zone, metav1.ConditionStatus(acceptance.Status), acceptance.Reason, acceptance.Message, metav1.ConditionUnknown, "Reconciling", "Reconciling", nil); err != nil {
			return ctrl.Result{}, err
		}
		if acceptance.Status == string(metav1.ConditionUnknown) {
			return ctrl.Result{}, r.writeRecordSetsWaiting(ctx, recordSets, acceptance.Reason)
		}
		return ctrl.Result{}, r.writeRecordSetsNotAccepted(ctx, recordSets, "ZoneNotAccepted")
	}

	allowedRecordSets, policyRejected := r.recordSetsAllowedByZone(ctx, recordSets, &zone)
	matchingProviderRecordSets, providerMismatchRejected := recordSetsMatchingProvider(allowedRecordSets, zoneClass.Spec.Provider)
	supportedRecordSets, unsupportedRejected := recordSetsSupportedByProvider(matchingProviderRecordSets, &provider)
	providerStatusesByRef := map[string]dnsv1alpha1.ZoneUnitRecordSetStatus{}
	if unitExists {
		for _, status := range existing.Status.RecordSets {
			providerStatusesByRef[zoneUnitRecordSetStatusKey(status)] = status
		}
	}
	items, ownershipRejected, err := r.acceptedRecordSetItems(ctx, supportedRecordSets, existing.Spec.RecordSets, providerStatusesByRef, &provider, storageVersion, &zone)
	if err != nil {
		return ctrl.Result{}, err
	}
	retainedItems, err := r.retainedNotAllowedRecordSetItems(ctx, policyRejected, existing.Spec.RecordSets, providerStatusesByRef)
	if err != nil {
		return ctrl.Result{}, err
	}
	items = append(items, retainedItems...)
	zoneStatus := recordSetZoneStatus(&zone, zoneClass)
	rejected := append(policyRejected, providerMismatchRejected...)
	rejected = append(rejected, unsupportedRejected...)
	rejected = append(rejected, ownershipRejected...)
	for _, rejectedRecordSet := range rejected {
		message := rejectedRecordSet.message
		if message == "" {
			message = rejectedRecordSet.reason
		}
		if err := r.setRecordSetClaimStatusWithZoneAndMessages(ctx, rejectedRecordSet.recordSet, rejectedRecordSet.status, rejectedRecordSet.reason, message, metav1.ConditionUnknown, "Reconciling", recordSetNotAcceptedProgrammedMessage, zoneStatus, nil); err != nil {
			return ctrl.Result{}, err
		}
	}

	desired := dnsv1alpha1.ZoneUnit{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: zone.Namespace,
			Name:      zone.Name,
			Finalizers: []string{
				coreZoneUnitFinalizer,
			},
		},
		Spec: dnsv1alpha1.ZoneUnitSpec{
			Provider: dnsv1alpha1.ProviderReference{Name: provider.Name, Version: storageVersion.Name},
			Zone: dnsv1alpha1.ZoneUnitZoneSpec{
				Ref:                dnsv1alpha1.ObjectReference{Namespace: zone.Namespace, Name: zone.Name},
				ObservedGeneration: zone.Generation,
				DomainName:         zone.Spec.DomainName,
				ZoneClassRef:       dnsv1alpha1.ObjectReference{Namespace: zoneClass.Namespace, Name: zoneClass.Name},
			},
			RecordSets: items,
		},
	}
	syncZoneUnitReconcileRequestAnnotation(&desired, &zone)
	if len(convertedZonePayload.Adoption.Raw) > 0 {
		desired.Spec.Zone.Adoption = convertedZonePayload.Adoption
	}

	if !zone.DeletionTimestamp.IsZero() {
		if len(recordSets) > 0 {
			if !unitExists || existing.DeletionTimestamp.IsZero() {
				if err := r.applyZoneUnitDesired(ctx, &desired, !unitExists); err != nil {
					return ctrl.Result{}, err
				}
			}
			var providerStatus *dnsv1alpha1.ZoneUnitZoneStatus
			if unitExists {
				providerStatus = existing.Status.Zone
			}
			return ctrl.Result{}, r.setZoneClaimStatusWithMessages(
				ctx,
				&zone,
				metav1.ConditionTrue,
				"Accepted",
				"Zone is accepted",
				metav1.ConditionFalse,
				"ProviderChangePending",
				fmt.Sprintf("%d RecordSet resources still reference this Zone; delete or move them before Zone cleanup can continue", len(recordSets)),
				providerStatus,
			)
		}
		if !unitExists {
			return ctrl.Result{}, r.removeFinalizer(ctx, &zone, coreZoneFinalizer)
		}
		if existing.DeletionTimestamp.IsZero() {
			if err := r.Delete(ctx, &existing); err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, r.setZoneClaimStatus(ctx, &zone, metav1.ConditionUnknown, "Reconciling", metav1.ConditionUnknown, "ProviderChangePending", nil)
		}
		if !providerZoneCleanupCompleted(&existing) {
			return ctrl.Result{}, r.setZoneClaimStatus(ctx, &zone, metav1.ConditionUnknown, "Reconciling", metav1.ConditionUnknown, "ProviderChangePending", nil)
		}
		if err := r.removeFinalizer(ctx, &zone, coreZoneFinalizer); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.removeZoneUnitCoreFinalizer(ctx, &existing)
	}

	if err := r.applyZoneUnitDesired(ctx, &desired, !unitExists); err != nil {
		return ctrl.Result{}, err
	}

	if existing.Status.Zone != nil || len(existing.Status.RecordSets) > 0 {
		projectionUnit := existing.DeepCopy()
		projectionUnit.Spec = desired.Spec
		if err := r.projectZoneUnitStatus(ctx, &zone, zoneClass, recordSets, projectionUnit); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

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

func (r *ZoneUnitCompositionReconciler) recordSetsForZoneKey(ctx context.Context, key client.ObjectKey) ([]dnsv1alpha1.RecordSet, error) {
	var list dnsv1alpha1.RecordSetList
	if err := r.List(ctx, &list); err != nil {
		return nil, err
	}
	recordSets := make([]dnsv1alpha1.RecordSet, 0)
	for _, recordSet := range list.Items {
		namespace, name := recordSetZoneKey(&recordSet)
		if namespace == key.Namespace && name == key.Name {
			recordSets = append(recordSets, recordSet)
		}
	}
	slices.SortFunc(recordSets, func(a, b dnsv1alpha1.RecordSet) int {
		if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
			if a.CreationTimestamp.Before(&b.CreationTimestamp) {
				return -1
			}
			return 1
		}
		if a.Namespace != b.Namespace {
			return compareString(a.Namespace, b.Namespace)
		}
		return compareString(a.Name, b.Name)
	})
	return recordSets, nil
}

func (r *ZoneUnitCompositionReconciler) zoneClassForZone(ctx context.Context, zone *dnsv1alpha1.Zone) (*dnsv1alpha1.ZoneClass, bool, error) {
	namespace := zone.Namespace
	if zone.Spec.ZoneClassRef.Namespace != nil && *zone.Spec.ZoneClassRef.Namespace != "" {
		namespace = *zone.Spec.ZoneClassRef.Namespace
	}
	var zoneClass dnsv1alpha1.ZoneClass
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: zone.Spec.ZoneClassRef.Name}, &zoneClass); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return &zoneClass, true, nil
}

type rejectedRecordSet struct {
	recordSet *dnsv1alpha1.RecordSet
	status    metav1.ConditionStatus
	reason    string
	message   string
}

func (r *ZoneUnitCompositionReconciler) recordSetsAllowedByZone(ctx context.Context, recordSets []dnsv1alpha1.RecordSet, zone *dnsv1alpha1.Zone) ([]dnsv1alpha1.RecordSet, []rejectedRecordSet) {
	allowed := make([]dnsv1alpha1.RecordSet, 0, len(recordSets))
	rejected := make([]rejectedRecordSet, 0)
	for index := range recordSets {
		recordSet := &recordSets[index]
		ok, message := declaration.RecordSetAllowedByZone(ctx, r.Client, recordSet, zone)
		if !ok {
			rejected = append(rejected, rejectedRecordSet{
				recordSet: recordSet,
				status:    metav1.ConditionFalse,
				reason:    "NotAllowedByZone",
				message:   message,
			})
			continue
		}
		allowed = append(allowed, *recordSet)
	}
	return allowed, rejected
}

func recordSetsMatchingProvider(recordSets []dnsv1alpha1.RecordSet, providerRef dnsv1alpha1.ProviderReference) ([]dnsv1alpha1.RecordSet, []rejectedRecordSet) {
	matching := make([]dnsv1alpha1.RecordSet, 0, len(recordSets))
	rejected := make([]rejectedRecordSet, 0)
	for index := range recordSets {
		recordSet := &recordSets[index]
		if recordSet.Spec.Provider != providerRef {
			rejected = append(rejected, rejectedRecordSet{
				recordSet: recordSet,
				status:    metav1.ConditionFalse,
				reason:    "ProviderMismatch",
				message:   "RecordSet provider does not match referenced ZoneClass",
			})
			continue
		}
		matching = append(matching, *recordSet)
	}
	return matching, rejected
}

func recordSetsSupportedByProvider(recordSets []dnsv1alpha1.RecordSet, provider *dnsv1alpha1.Provider) ([]dnsv1alpha1.RecordSet, []rejectedRecordSet) {
	supported := make([]dnsv1alpha1.RecordSet, 0, len(recordSets))
	rejected := make([]rejectedRecordSet, 0)
	for index := range recordSets {
		recordSet := &recordSets[index]
		providerVersion, ok := providercontract.ServedVersion(provider, recordSet.Spec.Provider.Version)
		if !ok {
			rejected = append(rejected, rejectedRecordSet{
				recordSet: recordSet,
				status:    metav1.ConditionUnknown,
				reason:    "ProviderStorageVersionNotResolved",
				message:   "RecordSet provider version is not served by the provider",
			})
			continue
		}
		if !providercontract.SupportsRecordType(providerVersion, recordSet.Spec.Type) {
			rejected = append(rejected, rejectedRecordSet{
				recordSet: recordSet,
				status:    metav1.ConditionFalse,
				reason:    "InvalidProvider",
				message:   "RecordSet type is not supported by the provider",
			})
			continue
		}
		supported = append(supported, *recordSet)
	}
	return supported, rejected
}

func (r *ZoneUnitCompositionReconciler) acceptedRecordSetItems(ctx context.Context, recordSets []dnsv1alpha1.RecordSet, existingItems []dnsv1alpha1.ZoneUnitRecordSetSpec, providerStatusesByRef map[string]dnsv1alpha1.ZoneUnitRecordSetStatus, provider *dnsv1alpha1.Provider, storageVersion *dnsv1alpha1.ProviderVersion, zone *dnsv1alpha1.Zone) ([]dnsv1alpha1.ZoneUnitRecordSetSpec, []rejectedRecordSet, error) {
	existingByRef := map[string]dnsv1alpha1.ZoneUnitRecordSetSpec{}
	for _, item := range existingItems {
		existingByRef[zoneUnitRecordSetItemKey(item)] = item
	}
	pendingDeletionItems := pendingRecordSetDeletionItems(recordSets, existingByRef, providerStatusesByRef)
	acceptedItems := make([]dnsv1alpha1.ZoneUnitRecordSetSpec, 0, len(existingItems))
	for _, item := range existingItems {
		refKey := zoneUnitRecordSetItemKey(item)
		if zoneUnitItemOwnedByPendingDeletion(item, refKey, pendingDeletionItems, storageVersion, zone, provider) {
			continue
		}
		acceptedItems = append(acceptedItems, item)
	}

	orderedRecordSets := slices.Clone(recordSets)
	slices.SortStableFunc(orderedRecordSets, func(a, b dnsv1alpha1.RecordSet) int {
		aDeleting := pendingRecordSetDeletion(&a, providerStatusesByRef)
		bDeleting := pendingRecordSetDeletion(&b, providerStatusesByRef)
		if aDeleting == bDeleting {
			return 0
		}
		if aDeleting {
			return -1
		}
		return 1
	})
	items := make([]dnsv1alpha1.ZoneUnitRecordSetSpec, 0, len(recordSets))
	rejected := make([]rejectedRecordSet, 0)
	for index := range orderedRecordSets {
		recordSet := &orderedRecordSets[index]
		servedVersion, ok := providercontract.ServedVersion(provider, recordSet.Spec.Provider.Version)
		if !ok {
			rejected = append(rejected, rejectedRecordSet{
				recordSet: recordSet,
				status:    metav1.ConditionUnknown,
				reason:    "ProviderStorageVersionNotResolved",
				message:   "RecordSet provider version is not served by the provider",
			})
			continue
		}
		convertedPayload, payloadErr := providercontract.ConvertProviderPayloadToStorage(providercontract.ConversionInput{
			Provider:    provider,
			Target:      providercontract.TargetRecordSet,
			FromVersion: servedVersion,
			ToVersion:   storageVersion,
			Payload: providercontract.Payload{
				Options:  recordSet.Spec.Options,
				Adoption: recordSet.Spec.Adoption,
			},
			Context: map[string]interface{}{
				"zone":      conversionObject(zone),
				"recordSet": conversionObject(recordSet),
			},
		})
		if payloadErr != nil {
			acceptance := providercontract.PayloadErrorToAcceptance(payloadErr)
			rejected = append(rejected, rejectedRecordSet{
				recordSet: recordSet,
				status:    metav1.ConditionStatus(acceptance.Status),
				reason:    acceptance.Reason,
				message:   acceptance.Message,
			})
			continue
		}
		item := zoneUnitRecordSetItem(recordSet, convertedPayload)
		refKey := recordSetClaimKey(recordSet.Namespace, recordSet.Name)
		if !recordSet.DeletionTimestamp.IsZero() {
			if providerRecordSetDeletionCompleted(providerStatusesByRef[refKey]) {
				if err := r.removeFinalizer(ctx, recordSet, coreRecordSetFinalizer); err != nil {
					return nil, nil, err
				}
				continue
			}
		} else if err := r.ensureFinalizer(ctx, recordSet, coreRecordSetFinalizer); err != nil {
			return nil, nil, err
		}
		if recordSet.DeletionTimestamp.IsZero() {
			if existing, ok := existingByRef[refKey]; ok && !zoneUnitRecordSetSpecEqual(existing, item) {
				rejected = append(rejected, rejectedRecordSet{
					recordSet: recordSet,
					status:    metav1.ConditionFalse,
					reason:    "RecordSetConflict",
					message:   recordSetConflictMessage(zone, item, zoneUnitRecordSetItemKey(existing)),
				})
				continue
			}
		}
		if recordSet.DeletionTimestamp.IsZero() {
			if ownerRef, ok := conflictingOwnerRef(item, refKey, pendingDeletionItems, storageVersion, zone, provider); ok {
				rejected = append(rejected, rejectedRecordSet{
					recordSet: recordSet,
					status:    metav1.ConditionFalse,
					reason:    "RecordSetConflict",
					message:   recordSetConflictMessage(zone, item, ownerRef),
				})
				continue
			}
		}
		if conflictOwner, ok := conflictingOwner(item, refKey, acceptedItems, storageVersion, zone, provider); ok {
			rejected = append(rejected, rejectedRecordSet{
				recordSet: recordSet,
				status:    metav1.ConditionFalse,
				reason:    "RecordSetConflict",
				message:   recordSetConflictMessage(zone, item, zoneUnitRecordSetItemKey(conflictOwner)),
			})
			continue
		}
		items = append(items, item)
		acceptedItems = append(acceptedItems, item)
	}
	return items, rejected, nil
}

func (r *ZoneUnitCompositionReconciler) retainedNotAllowedRecordSetItems(ctx context.Context, rejected []rejectedRecordSet, existingItems []dnsv1alpha1.ZoneUnitRecordSetSpec, providerStatusesByRef map[string]dnsv1alpha1.ZoneUnitRecordSetStatus) ([]dnsv1alpha1.ZoneUnitRecordSetSpec, error) {
	existingByRef := map[string]dnsv1alpha1.ZoneUnitRecordSetSpec{}
	for _, item := range existingItems {
		existingByRef[zoneUnitRecordSetItemKey(item)] = item
	}
	items := make([]dnsv1alpha1.ZoneUnitRecordSetSpec, 0)
	for _, rejectedRecordSet := range rejected {
		recordSet := rejectedRecordSet.recordSet
		refKey := recordSetClaimKey(recordSet.Namespace, recordSet.Name)
		existing, ok := existingByRef[refKey]
		if !ok {
			continue
		}
		if !recordSet.DeletionTimestamp.IsZero() && providerRecordSetDeletionCompleted(providerStatusesByRef[refKey]) {
			if err := r.removeFinalizer(ctx, recordSet, coreRecordSetFinalizer); err != nil {
				return nil, err
			}
			continue
		}
		if recordSet.DeletionTimestamp.IsZero() {
			if err := r.ensureFinalizer(ctx, recordSet, coreRecordSetFinalizer); err != nil {
				return nil, err
			}
		}
		item := existing
		item.Allowed = boolPtr(false)
		item.DeletionRequested = !recordSet.DeletionTimestamp.IsZero()
		items = append(items, item)
	}
	return items, nil
}

func boolPtr(value bool) *bool {
	return &value
}

func pendingRecordSetDeletionItems(recordSets []dnsv1alpha1.RecordSet, existingByRef map[string]dnsv1alpha1.ZoneUnitRecordSetSpec, providerStatusesByRef map[string]dnsv1alpha1.ZoneUnitRecordSetStatus) []dnsv1alpha1.ZoneUnitRecordSetSpec {
	items := []dnsv1alpha1.ZoneUnitRecordSetSpec{}
	for index := range recordSets {
		recordSet := &recordSets[index]
		if !pendingRecordSetDeletion(recordSet, providerStatusesByRef) {
			continue
		}
		refKey := recordSetClaimKey(recordSet.Namespace, recordSet.Name)
		if existing, ok := existingByRef[refKey]; ok {
			items = append(items, existing)
			continue
		}
		items = append(items, dnsv1alpha1.ZoneUnitRecordSetSpec{
			RecordSetNamespace: recordSet.Namespace,
			RecordSetName:      recordSet.Name,
			Name:               recordSet.Spec.Name,
			Type:               recordSet.Spec.Type,
		})
	}
	return items
}

func pendingRecordSetDeletion(recordSet *dnsv1alpha1.RecordSet, providerStatusesByRef map[string]dnsv1alpha1.ZoneUnitRecordSetStatus) bool {
	if recordSet.DeletionTimestamp.IsZero() {
		return false
	}
	return !providerRecordSetDeletionCompleted(providerStatusesByRef[recordSetClaimKey(recordSet.Namespace, recordSet.Name)])
}

func zoneUnitItemOwnedByPendingDeletion(item dnsv1alpha1.ZoneUnitRecordSetSpec, refKey string, pendingItems []dnsv1alpha1.ZoneUnitRecordSetSpec, providerVersion *dnsv1alpha1.ProviderVersion, zone *dnsv1alpha1.Zone, provider *dnsv1alpha1.Provider) bool {
	_, ok := conflictingOwner(item, refKey, pendingItems, providerVersion, zone, provider)
	return ok
}

func conflictingOwnerRef(item dnsv1alpha1.ZoneUnitRecordSetSpec, refKey string, owners []dnsv1alpha1.ZoneUnitRecordSetSpec, providerVersion *dnsv1alpha1.ProviderVersion, zone *dnsv1alpha1.Zone, provider *dnsv1alpha1.Provider) (string, bool) {
	owner, ok := conflictingOwner(item, refKey, owners, providerVersion, zone, provider)
	if !ok {
		return "", false
	}
	return zoneUnitRecordSetItemKey(owner), true
}

func conflictingOwner(item dnsv1alpha1.ZoneUnitRecordSetSpec, refKey string, owners []dnsv1alpha1.ZoneUnitRecordSetSpec, providerVersion *dnsv1alpha1.ProviderVersion, zone *dnsv1alpha1.Zone, provider *dnsv1alpha1.Provider) (dnsv1alpha1.ZoneUnitRecordSetSpec, bool) {
	for _, owner := range owners {
		if zoneUnitRecordSetItemKey(owner) == refKey {
			continue
		}
		if zoneUnitRecordSetsConflict(item, owner, providerVersion, zone, provider) {
			return owner, true
		}
	}
	return dnsv1alpha1.ZoneUnitRecordSetSpec{}, false
}

func zoneUnitRecordSetsConflict(self, other dnsv1alpha1.ZoneUnitRecordSetSpec, providerVersion *dnsv1alpha1.ProviderVersion, zone *dnsv1alpha1.Zone, provider *dnsv1alpha1.Provider) bool {
	if self.Name != other.Name {
		return false
	}
	if self.Type == other.Type {
		return true
	}
	if !zoneUnitCNAMECoexistenceDisabled(self, other, providerVersion, zone, provider) && (self.Type == dnsv1alpha1.RecordTypeCNAME || other.Type == dnsv1alpha1.RecordTypeCNAME) {
		return true
	}
	for _, rule := range providerVersion.ZoneUnit.ValidationRules {
		ok, err := evaluateZoneUnitCELBool(rule.Rule, self, other, zone, provider, providerVersion)
		if err != nil || !ok {
			return true
		}
	}
	return false
}

func zoneUnitCNAMECoexistenceDisabled(self, other dnsv1alpha1.ZoneUnitRecordSetSpec, providerVersion *dnsv1alpha1.ProviderVersion, zone *dnsv1alpha1.Zone, provider *dnsv1alpha1.Provider) bool {
	for _, toggle := range providerVersion.ZoneUnit.DisableValidations {
		if toggle.Name != "forbid-cname-coexistence" {
			continue
		}
		ok, err := evaluateZoneUnitCELBool(toggle.When, self, other, zone, provider, providerVersion)
		if err == nil && ok {
			return true
		}
	}
	return false
}

func evaluateZoneUnitCELBool(rule string, self, other dnsv1alpha1.ZoneUnitRecordSetSpec, zone *dnsv1alpha1.Zone, provider *dnsv1alpha1.Provider, providerVersion *dnsv1alpha1.ProviderVersion) (bool, error) {
	return celruntime.EvalBool(rule, map[string]interface{}{
		"self":      zoneUnitRecordSetCELEnvironment(self),
		"other":     zoneUnitRecordSetCELEnvironment(other),
		"zone":      conversionObject(zone),
		"provider":  conversionObject(provider),
		"toVersion": conversionObject(providerVersion),
	},
		cel.Variable("self", cel.DynType),
		cel.Variable("other", cel.DynType),
		cel.Variable("zone", cel.DynType),
		cel.Variable("provider", cel.DynType),
		cel.Variable("toVersion", cel.DynType),
	)
}

func zoneUnitRecordSetCELEnvironment(item dnsv1alpha1.ZoneUnitRecordSetSpec) map[string]interface{} {
	return map[string]interface{}{
		"recordSetNamespace": item.RecordSetNamespace,
		"recordSetName":      item.RecordSetName,
		"observedGeneration": item.ObservedGeneration,
		"name":               item.Name,
		"type":               string(item.Type),
	}
}

func recordSetConflictMessage(zone *dnsv1alpha1.Zone, item dnsv1alpha1.ZoneUnitRecordSetSpec, ownerRef string) string {
	ownerRef = formatRecordSetRefKey(ownerRef)
	return fmt.Sprintf("RecordSet type=%s name=%s in Zone %s/%s conflicts with owner RecordSet %s.", item.Type, item.Name, zone.Namespace, zone.Name, ownerRef)
}

func formatRecordSetRefKey(refKey string) string {
	namespace, name, ok := strings.Cut(refKey, "\x00")
	if !ok {
		return refKey
	}
	return namespace + "/" + name
}

func zoneUnitRecordSetItem(recordSet *dnsv1alpha1.RecordSet, payload providercontract.Payload) dnsv1alpha1.ZoneUnitRecordSetSpec {
	item := dnsv1alpha1.ZoneUnitRecordSetSpec{
		RecordSetNamespace: recordSet.Namespace,
		RecordSetName:      recordSet.Name,
		ObservedGeneration: recordSet.Generation,
		Name:               recordSet.Spec.Name,
		Type:               recordSet.Spec.Type,
		TTL:                recordSet.Spec.TTL,
		A:                  recordSet.Spec.A,
		AAAA:               recordSet.Spec.AAAA,
		TXT:                recordSet.Spec.TXT,
		CNAME:              recordSet.Spec.CNAME,
		MX:                 recordSet.Spec.MX,
		CAA:                recordSet.Spec.CAA,
		NS:                 recordSet.Spec.NS,
		Options:            payload.Options,
		DeletionRequested:  !recordSet.DeletionTimestamp.IsZero(),
	}
	if len(payload.Adoption.Raw) > 0 {
		item.Adoption = payload.Adoption
	}
	return item
}

func zoneUnitRecordSetSpecEqual(a, b dnsv1alpha1.ZoneUnitRecordSetSpec) bool {
	a.ObservedGeneration = b.ObservedGeneration
	a.Allowed = b.Allowed
	return equality.Semantic.DeepEqual(a, b)
}

func conversionObject(value interface{}) map[string]interface{} {
	object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(value)
	if err != nil {
		return map[string]interface{}{}
	}
	return object
}

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

func providerRecordSetDeletionCompleted(status dnsv1alpha1.ZoneUnitRecordSetStatus) bool {
	return status.DeletionCompleted
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

func (r *ZoneUnitCompositionReconciler) projectZoneUnitStatus(ctx context.Context, zone *dnsv1alpha1.Zone, zoneClass *dnsv1alpha1.ZoneClass, recordSets []dnsv1alpha1.RecordSet, unit *dnsv1alpha1.ZoneUnit) error {
	if unit.Status.Zone != nil {
		accepted := projectedAccepted(zone.Status.Conditions, unit.Status.Zone.Conditions)
		programmed := projectedProgrammed(accepted.Status, unit.Status.Zone.Conditions)
		if err := r.setZoneClaimStatus(ctx, zone, accepted.Status, accepted.Reason, programmed.Status, programmed.Reason, unit.Status.Zone); err != nil {
			return err
		}
	}
	statusByRef := map[string]dnsv1alpha1.ZoneUnitRecordSetStatus{}
	desiredByRef := map[string]dnsv1alpha1.ZoneUnitRecordSetSpec{}
	for _, item := range unit.Spec.RecordSets {
		desiredByRef[zoneUnitRecordSetItemKey(item)] = item
	}
	for _, item := range unit.Status.RecordSets {
		refKey := zoneUnitRecordSetStatusKey(item)
		desiredItem, ok := desiredByRef[refKey]
		if !ok || retainedNotAllowedRecordSetItem(desiredItem) || staleCompletedDeletionStatusForActiveItem(item, desiredItem) {
			continue
		}
		statusByRef[refKey] = item
	}
	for index := range recordSets {
		recordSet := &recordSets[index]
		providerStatus, ok := statusByRef[recordSetClaimKey(recordSet.Namespace, recordSet.Name)]
		if !ok {
			continue
		}
		accepted := projectedAccepted(recordSet.Status.Conditions, providerStatus.Conditions)
		programmed := projectedProgrammed(accepted.Status, providerStatus.Conditions)
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

func projectedAccepted(compositionConditions, providerConditions []metav1.Condition) projectedCondition {
	provider := meta.FindStatusCondition(providerConditions, string(dnsv1alpha1.ConditionAccepted))
	if provider != nil {
		return projectedCondition{Status: provider.Status, Reason: provider.Reason}
	}
	composition := meta.FindStatusCondition(compositionConditions, string(dnsv1alpha1.ConditionAccepted))
	if composition != nil && composition.Status != metav1.ConditionTrue {
		return projectedCondition{Status: composition.Status, Reason: composition.Reason}
	}
	return projectedCondition{Status: metav1.ConditionUnknown, Reason: "OwnerStateNotResolved"}
}

func projectedProgrammed(accepted metav1.ConditionStatus, providerConditions []metav1.Condition) projectedCondition {
	if accepted != metav1.ConditionTrue {
		return projectedCondition{Status: metav1.ConditionUnknown, Reason: "Reconciling"}
	}
	provider := meta.FindStatusCondition(providerConditions, string(dnsv1alpha1.ConditionProgrammed))
	if provider == nil {
		return projectedCondition{Status: metav1.ConditionUnknown, Reason: "Reconciling"}
	}
	return projectedCondition{Status: provider.Status, Reason: provider.Reason}
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
