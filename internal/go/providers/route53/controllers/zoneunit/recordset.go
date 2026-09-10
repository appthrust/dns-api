package route53

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const RecordSetFinalizer = "route53.dns.appthrust.io/recordset-finalizer"

type plannedRecordSetChange struct {
	recordSet *dnsv1alpha1.RecordSet
	source    *dnsv1alpha1.RecordSet
	change    RecordSetChange
}

func (r *ZoneReconciler) reconcileZoneRecordSets(ctx context.Context, provider Provider, zone *dnsv1alpha1.Zone, zoneClass *dnsv1alpha1.ZoneClass, hostedZone HostedZone) (ctrl.Result, error) {
	recordSets, err := r.recordSetsForZone(ctx, zone)
	if err != nil {
		return ctrl.Result{}, err
	}
	ownership, err := r.zoneUnitOwnershipForZone(ctx, zone)
	if err != nil {
		return ctrl.Result{}, err
	}

	if result, done, err := r.refreshPendingRecordSetChange(ctx, provider, zone, recordSets); done || err != nil {
		return result, err
	}

	currentRecords, err := provider.ListRecordSets(ctx, hostedZone.ID)
	if err != nil {
		if statusErr := r.setRecordSetsProviderError(ctx, recordSets, err); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return r.resultForProviderError(err), nil
	}
	current := indexRecordSets(currentRecords)

	var planned []plannedRecordSetChange
	deferred := false
	batchCost := 0
	for index := range recordSets {
		recordSet := &recordSets[index]
		source := recordSet.DeepCopy()
		change, ok, err := r.planRecordSet(ctx, zone, zoneClass, hostedZone, recordSet, ownership, current)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !ok {
			continue
		}
		cost := route53ChangeCost(change)
		if batchCost+cost > route53RecordSetChangeBatchLimit {
			deferred = true
			if err := r.setRecordSetProgrammed(ctx, recordSet, metav1.ConditionFalse, "ProviderChangeDeferred", "Route 53 change batch limit deferred this RecordSet"); err != nil {
				return ctrl.Result{}, err
			}
			continue
		}
		batchCost += cost
		planned = append(planned, plannedRecordSetChange{
			recordSet: recordSet,
			source:    source,
			change:    change,
		})
	}

	if len(planned) == 0 {
		if deferred {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, nil
	}

	changes := make([]RecordSetChange, 0, len(planned))
	for _, item := range planned {
		changes = append(changes, item.change)
	}

	if err := r.fenceRecordSetChangeDispatch(ctx, zone, planned...); err != nil {
		return ctrl.Result{}, err
	}

	change, err := provider.ChangeRecordSets(ctx, hostedZone.ID, changes)
	if err != nil {
		if providerErrorReason(err) == "ProviderInvalidRequest" {
			return r.reconcileInvalidRecordSetBatch(ctx, provider, zone, hostedZone.ID, planned, err)
		}
		for _, item := range planned {
			if statusErr := r.setRecordSetProviderError(ctx, item.recordSet, err); statusErr != nil {
				return ctrl.Result{}, statusErr
			}
		}
		return r.resultForProviderError(err), nil
	}

	if change != nil {
		for _, item := range planned {
			if err := r.setRecordSetProgrammed(ctx, item.recordSet, metav1.ConditionFalse, "ProviderChangePending", "Route 53 record set change is pending"); err != nil {
				return ctrl.Result{}, err
			}
			r.recordEvent(item.recordSet, corev1.EventTypeNormal, "Route53RecordSetChangeSubmitted", fmt.Sprintf("Route 53 record set change %s was submitted for type=%s name=%s", change.ID, item.recordSet.Spec.Type, item.recordSet.Spec.Name))
		}
	}
	if change != nil && change.Status == route53v1alpha1.Route53ChangeStatusPending {
		if err := r.patchRoute53ZoneStatus(ctx, zone, func(data *route53v1alpha1.Route53ZoneStatusData) {
			data.PendingRecordSetChange = pendingRecordSetChangeFromChange(change, route53BatchOperation(planned), affectedRecordSets(planned))
		}); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: r.changeCheckAfter()}, nil
	}

	return ctrl.Result{Requeue: true}, nil
}

func (r *ZoneReconciler) reconcileInvalidRecordSetBatch(ctx context.Context, provider Provider, zone *dnsv1alpha1.Zone, hostedZoneID string, planned []plannedRecordSetChange, batchErr error) (ctrl.Result, error) {
	handled := false
	for index, item := range planned {
		if err := r.fenceRecordSetChangeDispatch(ctx, zone, item); err != nil {
			return ctrl.Result{}, err
		}

		change, err := provider.ChangeRecordSets(ctx, hostedZoneID, []RecordSetChange{item.change})
		if err != nil {
			if statusErr := r.setRecordSetProviderError(ctx, item.recordSet, err); statusErr != nil {
				return ctrl.Result{}, statusErr
			}
			handled = true
			continue
		}
		if change == nil {
			continue
		}
		handled = true
		if err := r.setRecordSetProgrammed(ctx, item.recordSet, metav1.ConditionFalse, "ProviderChangePending", "Route 53 record set change is pending"); err != nil {
			return ctrl.Result{}, err
		}
		r.recordEvent(item.recordSet, corev1.EventTypeNormal, "Route53RecordSetChangeSubmitted", fmt.Sprintf("Route 53 record set change %s was submitted for type=%s name=%s", change.ID, item.recordSet.Spec.Type, item.recordSet.Spec.Name))
		for _, deferred := range planned[index+1:] {
			if err := r.setRecordSetProgrammed(ctx, deferred.recordSet, metav1.ConditionFalse, "ProviderChangeDeferred", "Route 53 batch was rejected; this RecordSet will be retried separately"); err != nil {
				return ctrl.Result{}, err
			}
		}
		if change.Status == route53v1alpha1.Route53ChangeStatusPending {
			if err := r.patchRoute53ZoneStatus(ctx, zone, func(data *route53v1alpha1.Route53ZoneStatusData) {
				submitted := []plannedRecordSetChange{item}
				data.PendingRecordSetChange = pendingRecordSetChangeFromChange(change, route53BatchOperation(submitted), affectedRecordSets(submitted))
			}); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: r.changeCheckAfter()}, nil
		}
		return ctrl.Result{Requeue: true}, nil
	}
	if handled {
		return ctrl.Result{}, nil
	}
	for _, item := range planned {
		if statusErr := r.setRecordSetProviderError(ctx, item.recordSet, batchErr); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
	}
	return r.resultForProviderError(batchErr), nil
}

// fenceRecordSetChangeDispatch proves that the API object which authorized a
// provider mutation still owns every planned source. The status patch carries
// a resource-version precondition even when it has no status delta, so a
// cached observation cannot authorize an external mutation.
func (r *ZoneReconciler) fenceRecordSetChangeDispatch(ctx context.Context, zone *dnsv1alpha1.Zone, planned ...plannedRecordSetChange) error {
	var unit dnsv1alpha1.ZoneUnit
	key := client.ObjectKey{Namespace: zone.Namespace, Name: zone.Name}
	if err := r.Get(ctx, key, &unit); err != nil {
		if apierrors.IsNotFound(err) {
			return apierrors.NewConflict(dnsv1alpha1.Resource("zoneunits"), zone.Name, fmt.Errorf("ZoneUnit was deleted before RecordSet change could be submitted"))
		}
		return err
	}
	for _, item := range planned {
		if item.source == nil || !zoneUnitRecordSetSourceCurrent(&unit, item.source) {
			return apierrors.NewConflict(dnsv1alpha1.Resource("zoneunits"), zone.Name, fmt.Errorf("ZoneUnit RecordSet source changed before provider mutation"))
		}
	}
	base := unit.DeepCopy()
	return r.Status().Patch(ctx, &unit, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
}

func (r *ZoneReconciler) planRecordSet(
	ctx context.Context,
	zone *dnsv1alpha1.Zone,
	zoneClass *dnsv1alpha1.ZoneClass,
	hostedZone HostedZone,
	recordSet *dnsv1alpha1.RecordSet,
	ownership *zoneUnitRecordSetOwnership,
	current map[string]RecordSetResource,
) (RecordSetChange, bool, error) {
	identity := recordSetIdentity(recordSet.Spec.Type, canonicalRecordName(recordSet.Spec.Name, zone.Spec.DomainName))

	if !recordSet.DeletionTimestamp.IsZero() {
		return r.planRecordSetDelete(ctx, recordSet, ownership, current[identity.key()])
	}

	statusData, err := route53RecordSetStatusData(recordSet)
	if err != nil {
		return RecordSetChange{}, false, r.setRecordSetProgrammed(ctx, recordSet, metav1.ConditionFalse, "ReconcileError", err.Error())
	}
	if message, mismatch, err := route53RecordSetManagedResourceMismatch(recordSet, statusData); err != nil {
		return RecordSetChange{}, false, r.setRecordSetAccepted(ctx, recordSet, metav1.ConditionFalse, "InvalidAdoption", err.Error())
	} else if mismatch {
		return RecordSetChange{}, false, r.setRecordSetAccepted(ctx, recordSet, metav1.ConditionFalse, "ManagedResourceMismatch", message)
	}

	if accepted, err := r.acceptRecordSetForZone(ctx, zone, zoneClass, recordSet); err != nil || !accepted {
		return RecordSetChange{}, false, err
	}

	if accepted, conflict := ownership.acceptedOwner(recordSet); !accepted {
		if conflict {
			return RecordSetChange{}, false, r.setRecordSetAccepted(ctx, recordSet, metav1.ConditionFalse, "RecordSetConflict", "record identity is owned by another RecordSet")
		}
		return RecordSetChange{}, false, r.setRecordSetProgrammed(ctx, recordSet, metav1.ConditionFalse, "ProviderChangePending", "ZoneUnit ownership is not ready yet")
	}

	if err := r.ensureRecordSetFinalizer(ctx, recordSet); err != nil {
		return RecordSetChange{}, false, err
	}

	options, err := route53RecordSetOptions(recordSet)
	if err != nil {
		return RecordSetChange{}, false, r.setRecordSetAccepted(ctx, recordSet, metav1.ConditionFalse, "DeniedByPolicy", err.Error())
	}
	if message := validateRoute53RecordSetBody(recordSet, options); message != "" {
		return RecordSetChange{}, false, r.setRecordSetAccepted(ctx, recordSet, metav1.ConditionFalse, "DeniedByPolicy", message)
	}
	desired := desiredRoute53RecordSet(recordSet, hostedZone.ID, identity.recordName, options)
	existing, exists := current[identity.key()]
	managed := providerStatusHasPayload(recordSet.Status.Provider)

	if adopting, err := route53RecordSetAdoptionEnabled(recordSet); err != nil {
		return RecordSetChange{}, false, r.setRecordSetAccepted(ctx, recordSet, metav1.ConditionFalse, "InvalidAdoption", err.Error())
	} else if adopting && !managed {
		if !exists {
			return RecordSetChange{}, false, r.setRecordSetProgrammed(ctx, recordSet, metav1.ConditionFalse, "ExternalResourceNotFound", "adopted Route 53 record set was not found")
		}
		if !route53RecordSetEqual(existing, desired) {
			return RecordSetChange{}, false, r.setRecordSetProgrammed(ctx, recordSet, metav1.ConditionFalse, "ExternalResourceMismatch", "adopted Route 53 record set does not match RecordSet spec")
		}
		return RecordSetChange{}, false, r.setRecordSetReady(ctx, recordSet, desired)
	}

	if exists && !managed {
		return RecordSetChange{}, false, r.setRecordSetProgrammed(ctx, recordSet, metav1.ConditionFalse, "ProviderConflict", "same Route 53 record set already exists")
	}
	if exists && route53RecordSetEqual(existing, desired) {
		return RecordSetChange{}, false, r.setRecordSetReady(ctx, recordSet, desired)
	}

	if err := r.setRecordSetAccepted(ctx, recordSet, metav1.ConditionTrue, "Accepted", "RecordSet is accepted by Route 53 policy"); err != nil {
		return RecordSetChange{}, false, err
	}
	if err := r.patchRoute53RecordSetStatus(ctx, recordSet, func(data *route53v1alpha1.Route53RecordSetStatusData) {
		setRoute53RecordSetStatusData(data, desired)
	}); err != nil {
		return RecordSetChange{}, false, err
	}
	return RecordSetChange{Action: RecordSetChangeActionUpsert, RecordSet: desired}, true, nil
}

func (r *ZoneReconciler) planRecordSetDelete(ctx context.Context, recordSet *dnsv1alpha1.RecordSet, ownership *zoneUnitRecordSetOwnership, existing RecordSetResource) (RecordSetChange, bool, error) {
	if !slices.Contains(recordSet.Finalizers, RecordSetFinalizer) {
		return RecordSetChange{}, false, nil
	}
	if accepted, conflict := ownership.acceptedOwner(recordSet); !accepted {
		if conflict {
			return RecordSetChange{}, false, r.setRecordSetDeletionConflict(ctx, recordSet)
		}
		if existing.Name == "" {
			return RecordSetChange{}, false, r.removeRecordSetFinalizer(ctx, recordSet)
		}
		return RecordSetChange{}, false, r.setRecordSetProgrammed(ctx, recordSet, metav1.ConditionFalse, "ProviderConflict", "same Route 53 record set exists without current RecordSet ownership")
	}
	if existing.Name == "" {
		return RecordSetChange{}, false, r.removeRecordSetFinalizer(ctx, recordSet)
	}
	adopting, err := route53RecordSetAdoptionEnabled(recordSet)
	if err != nil {
		return RecordSetChange{}, false, r.setRecordSetAccepted(ctx, recordSet, metav1.ConditionFalse, "InvalidAdoption", err.Error())
	}
	if _, err := route53RecordSetStatusData(recordSet); err != nil {
		return RecordSetChange{}, false, r.setRecordSetProgrammed(ctx, recordSet, metav1.ConditionFalse, "ReconcileError", err.Error())
	}
	if !adopting && !providerStatusHasPayload(recordSet.Status.Provider) {
		return RecordSetChange{}, false, r.setRecordSetProgrammed(ctx, recordSet, metav1.ConditionFalse, "ProviderConflict", "same Route 53 record set exists without current RecordSet ownership")
	}
	return RecordSetChange{Action: RecordSetChangeActionDelete, RecordSet: existing}, true, nil
}

func (r *ZoneReconciler) acceptRecordSetForZone(ctx context.Context, zone *dnsv1alpha1.Zone, zoneClass *dnsv1alpha1.ZoneClass, recordSet *dnsv1alpha1.RecordSet) (bool, error) {
	if recordSet.Status.Zone != nil && recordSet.Status.Zone.Ref != (dnsv1alpha1.ObjectReference{Namespace: zone.Namespace, Name: zone.Name}) {
		return false, r.setRecordSetAccepted(ctx, recordSet, metav1.ConditionFalse, "InvalidZoneRef", "RecordSet status points to another Zone")
	}

	provider, version, err := resolveDNSProviderVersion(ctx, r.Client, recordSet.Spec.Provider)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, r.setRecordSetAccepted(ctx, recordSet, metav1.ConditionFalse, "InvalidProvider", "referenced Provider was not found")
		}
		return false, err
	}
	if recordSet.Spec.Provider != zoneClass.Spec.Provider {
		return false, r.setRecordSetAccepted(ctx, recordSet, metav1.ConditionFalse, "ProviderMismatch", "RecordSet provider does not match referenced ZoneClass")
	}
	if zoneClass.Spec.ControllerName != r.controllerName() || !route53ProviderVersionMatches(provider, version, r.providerReference()) {
		return false, r.setRecordSetAccepted(ctx, recordSet, metav1.ConditionFalse, "InvalidProvider", "RecordSet provider is not handled by Route 53 controller")
	}
	if !providerVersionSupportsType(version, recordSet.Spec.Type) {
		return false, r.setRecordSetAccepted(ctx, recordSet, metav1.ConditionFalse, "DeniedByPolicy", "RecordSet type is not supported by the provider")
	}
	if _, err := route53RecordSetAdoptionEnabled(recordSet); err != nil {
		return false, r.setRecordSetAccepted(ctx, recordSet, metav1.ConditionFalse, "InvalidAdoption", err.Error())
	}

	return true, r.patchRecordSetStatus(ctx, recordSet, func(status *dnsv1alpha1.RecordSetStatus) {
		status.ObservedGeneration = recordSet.Generation
		status.Zone = &dnsv1alpha1.RecordSetZoneStatus{
			Ref: dnsv1alpha1.ObjectReference{
				Namespace: zone.Namespace,
				Name:      zone.Name,
			},
		}
		setRecordSetCondition(&status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted", "RecordSet is accepted by Route 53 policy", recordSet.Generation)
	})
}

func (r *ZoneReconciler) refreshPendingRecordSetChange(ctx context.Context, provider Provider, zone *dnsv1alpha1.Zone, recordSets []dnsv1alpha1.RecordSet) (ctrl.Result, bool, error) {
	statusData, err := route53ZoneStatusData(zone)
	if err != nil {
		return ctrl.Result{}, true, r.setProgrammed(ctx, zone, metav1.ConditionFalse, "ReconcileError", err.Error())
	}
	pending := statusData.PendingRecordSetChange
	if pending == nil || pending.ID == "" || pending.Status != route53v1alpha1.Route53ChangeStatusPending {
		return ctrl.Result{}, false, nil
	}

	change, err := provider.GetChange(ctx, pending.ID)
	if err != nil {
		for index := range recordSets {
			if affectedRecordSetIncludes(pending.AffectedRecordSets, &recordSets[index]) {
				if statusErr := r.setRecordSetProviderError(ctx, &recordSets[index], err); statusErr != nil {
					return ctrl.Result{}, true, statusErr
				}
			}
		}
		return r.resultForProviderError(err), true, nil
	}
	if change == nil || change.Status != route53v1alpha1.Route53ChangeStatusInSync {
		if change != nil {
			pending = pendingRecordSetChangeFromChange(change, pending.Operation, pending.AffectedRecordSets)
			if err := r.patchRoute53ZoneStatus(ctx, zone, func(data *route53v1alpha1.Route53ZoneStatusData) {
				data.PendingRecordSetChange = pending
			}); err != nil {
				return ctrl.Result{}, true, err
			}
		}
		for index := range recordSets {
			if affectedRecordSetIncludes(pending.AffectedRecordSets, &recordSets[index]) {
				if statusErr := r.setRecordSetProgrammed(ctx, &recordSets[index], metav1.ConditionFalse, "ProviderChangePending", "Route 53 record set change is pending"); statusErr != nil {
					return ctrl.Result{}, true, statusErr
				}
			}
		}
		return ctrl.Result{RequeueAfter: r.changeCheckAfter()}, true, nil
	}

	for index := range recordSets {
		if affectedRecordSetIncludes(pending.AffectedRecordSets, &recordSets[index]) {
			r.recordEvent(&recordSets[index], corev1.EventTypeNormal, "Route53RecordSetChangeInSync", fmt.Sprintf("Route 53 record set change %s is INSYNC for type=%s name=%s", pending.ID, recordSets[index].Spec.Type, recordSets[index].Spec.Name))
		}
	}
	// INSYNC confirms the submitted batch, not the current desired generation.
	// Clear it and use the normal provider list/plan path before attesting Ready.
	if err := r.patchRoute53ZoneStatus(ctx, zone, func(data *route53v1alpha1.Route53ZoneStatusData) {
		data.PendingRecordSetChange = nil
	}); err != nil {
		return ctrl.Result{}, true, err
	}
	return ctrl.Result{}, false, nil
}

func (r *ZoneReconciler) recordSetsForZone(ctx context.Context, zone *dnsv1alpha1.Zone) ([]dnsv1alpha1.RecordSet, error) {
	var unit dnsv1alpha1.ZoneUnit
	if err := r.Get(ctx, client.ObjectKey{Namespace: zone.Namespace, Name: zone.Name}, &unit); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	statusByClaim := make(map[string]dnsv1alpha1.ZoneUnitRecordSetStatus, len(unit.Status.RecordSets))
	for _, status := range unit.Status.RecordSets {
		if status.RecordSetUID != "" {
			statusByClaim[zoneUnitRecordSetStatusKey(status)] = status
		}
	}
	recordSets := make([]dnsv1alpha1.RecordSet, 0, len(unit.Spec.RecordSets))
	for _, item := range unit.Spec.RecordSets {
		if !item.IsAllowed() && !item.DeletionRequested {
			continue
		}
		status := statusByClaim[zoneUnitRecordSetItemKey(item)]
		if !zoneUnitRecordSetStatusMatchesItem(status, item) || (status.DeletionCompleted && !item.DeletionRequested) {
			status = dnsv1alpha1.ZoneUnitRecordSetStatus{}
		}
		recordSets = append(recordSets, route53RecordSetFromZoneUnitItem(&unit, item, status))
	}
	return recordSets, nil
}

func route53RecordSetFromZoneUnitItem(unit *dnsv1alpha1.ZoneUnit, item dnsv1alpha1.ZoneUnitRecordSetSpec, status dnsv1alpha1.ZoneUnitRecordSetStatus) dnsv1alpha1.RecordSet {
	recordSet := dnsv1alpha1.RecordSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:  item.RecordSetNamespace,
			Name:       item.RecordSetName,
			UID:        item.RecordSetUID,
			Generation: item.ObservedGeneration,
		},
		Spec: dnsv1alpha1.RecordSetSpec{
			ZoneRef:  dnsv1alpha1.ZoneReference{Namespace: &unit.Namespace, Name: unit.Name},
			Provider: unit.Spec.Provider,
			Type:     item.Type,
			Name:     item.Name,
			TTL:      item.TTL,
			A:        item.A,
			AAAA:     item.AAAA,
			TXT:      item.TXT,
			CNAME:    item.CNAME,
			MX:       item.MX,
			CAA:      item.CAA,
			NS:       item.NS,
			Options:  item.Options,
			Adoption: item.Adoption,
		},
		Status: dnsv1alpha1.RecordSetStatus{
			ObservedGeneration: status.ObservedGeneration,
			Provider:           status.Provider,
			Conditions:         slices.Clone(status.Conditions),
		},
	}
	if status.ObservedGeneration == 0 {
		recordSet.Status.ObservedGeneration = item.ObservedGeneration
	}
	if item.DeletionRequested {
		now := metav1.Now()
		recordSet.DeletionTimestamp = &now
	}
	if item.DeletionRequested || providerStatusHasPayload(status.Provider) {
		recordSet.Finalizers = []string{RecordSetFinalizer}
	}
	return recordSet
}

func (r *ZoneReconciler) zoneUnitOwnershipForZone(ctx context.Context, zone *dnsv1alpha1.Zone) (*zoneUnitRecordSetOwnership, error) {
	var unit dnsv1alpha1.ZoneUnit
	if err := r.Get(ctx, client.ObjectKey{Namespace: zone.Namespace, Name: zone.Name}, &unit); err != nil {
		if apierrors.IsNotFound(err) {
			return newZoneUnitRecordSetOwnership(nil), nil
		}
		return nil, err
	}
	return newZoneUnitRecordSetOwnership(unit.Spec.RecordSets), nil
}

func (r *ZoneReconciler) recordSetAllowedByZone(ctx context.Context, recordSet *dnsv1alpha1.RecordSet, zone *dnsv1alpha1.Zone) bool {
	allowed, _ := r.recordSetAllowedByZoneMessage(ctx, recordSet, zone)
	return allowed
}

func (r *ZoneReconciler) recordSetAllowedByZoneMessage(ctx context.Context, recordSet *dnsv1alpha1.RecordSet, zone *dnsv1alpha1.Zone) (bool, string) {
	if recordSet.Namespace == zone.Namespace {
		return true, ""
	}
	namespaceMatched := false
	recordNameMatched := false
	for _, grant := range zone.Spec.AllowedRecordSets {
		if !r.namespaceAllowed(ctx, &grant.Namespaces.Selector, recordSet.Namespace) {
			continue
		}
		namespaceMatched = true
		for _, record := range grant.Records {
			matched, err := fullRecordNamePatternMatch(record.Name.Pattern, recordSet.Spec.Name)
			if err != nil || !matched {
				continue
			}
			recordNameMatched = true
			if slices.Contains(record.Types, recordSet.Spec.Type) {
				return true, ""
			}
		}
	}
	if !namespaceMatched {
		return false, "RecordSet namespace is not allowed by the referenced Zone."
	}
	if !recordNameMatched {
		return false, "RecordSet name is not allowed by the referenced Zone."
	}
	return false, "RecordSet type is not allowed for this RecordSet name by the referenced Zone."
}

func (r *ZoneReconciler) namespaceAllowed(ctx context.Context, selectorSpec *metav1.LabelSelector, recordSetNamespace string) bool {
	if selectorSpec == nil || (len(selectorSpec.MatchLabels) == 0 && len(selectorSpec.MatchExpressions) == 0) {
		return false
	}
	selector, err := metav1.LabelSelectorAsSelector(selectorSpec)
	if err != nil {
		return false
	}
	var namespace corev1.Namespace
	if err := r.Get(ctx, client.ObjectKey{Name: recordSetNamespace}, &namespace); err != nil {
		return false
	}
	return selector.Matches(labels.Set(namespace.Labels))
}

func (r *ZoneReconciler) ensureRecordSetFinalizer(ctx context.Context, recordSet *dnsv1alpha1.RecordSet) error {
	return nil
}

func (r *ZoneReconciler) removeRecordSetFinalizer(ctx context.Context, recordSet *dnsv1alpha1.RecordSet) error {
	return r.patchRecordSetDeletionCompleted(ctx, recordSet, func(status *dnsv1alpha1.RecordSetStatus) {
		status.ObservedGeneration = recordSet.Generation
		setRecordSetCondition(&status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionTrue, "Programmed", "Route 53 record set deletion is complete", recordSet.Generation)
	})
}

func (r *ZoneReconciler) setRecordSetReady(ctx context.Context, recordSet *dnsv1alpha1.RecordSet, observed RecordSetResource) error {
	if err := r.patchRoute53RecordSetStatus(ctx, recordSet, func(data *route53v1alpha1.Route53RecordSetStatusData) {
		setRoute53RecordSetStatusData(data, observed)
	}); err != nil {
		return err
	}
	if err := r.setRecordSetAccepted(ctx, recordSet, metav1.ConditionTrue, "Accepted", "RecordSet is accepted by Route 53 policy"); err != nil {
		return err
	}
	return r.setRecordSetProgrammed(ctx, recordSet, metav1.ConditionTrue, "Programmed", "Route 53 record set is programmed")
}

func (r *ZoneReconciler) setRecordSetsProviderError(ctx context.Context, recordSets []dnsv1alpha1.RecordSet, err error) error {
	for index := range recordSets {
		if statusErr := r.setRecordSetProviderError(ctx, &recordSets[index], err); statusErr != nil {
			return statusErr
		}
	}
	return nil
}

func (r *ZoneReconciler) setRecordSetProviderError(ctx context.Context, recordSet *dnsv1alpha1.RecordSet, err error) error {
	reason, message := providerErrorCondition(err)
	return r.setRecordSetProgrammed(ctx, recordSet, metav1.ConditionFalse, reason, message)
}

func (r *ZoneReconciler) setRecordSetDeletionConflict(ctx context.Context, recordSet *dnsv1alpha1.RecordSet) error {
	return r.patchRecordSetStatus(ctx, recordSet, func(recordSetStatus *dnsv1alpha1.RecordSetStatus) {
		recordSetStatus.ObservedGeneration = recordSet.Generation
		setRecordSetCondition(&recordSetStatus.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionFalse, "RecordSetConflict", "record identity is owned by another RecordSet", recordSet.Generation)
		setRecordSetCondition(&recordSetStatus.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "RecordSetConflict", "record identity is owned by another RecordSet", recordSet.Generation)
	})
}

func (r *ZoneReconciler) setRecordSetAccepted(ctx context.Context, recordSet *dnsv1alpha1.RecordSet, status metav1.ConditionStatus, reason, message string) error {
	return r.patchRecordSetStatus(ctx, recordSet, func(recordSetStatus *dnsv1alpha1.RecordSetStatus) {
		recordSetStatus.ObservedGeneration = recordSet.Generation
		setRecordSetCondition(&recordSetStatus.Conditions, string(dnsv1alpha1.ConditionAccepted), status, reason, message, recordSet.Generation)
	})
}

func (r *ZoneReconciler) setRecordSetProgrammed(ctx context.Context, recordSet *dnsv1alpha1.RecordSet, status metav1.ConditionStatus, reason, message string) error {
	return r.patchRecordSetStatus(ctx, recordSet, func(recordSetStatus *dnsv1alpha1.RecordSetStatus) {
		recordSetStatus.ObservedGeneration = recordSet.Generation
		setRecordSetCondition(&recordSetStatus.Conditions, string(dnsv1alpha1.ConditionProgrammed), status, reason, message, recordSet.Generation)
	})
}

func (r *ZoneReconciler) patchRecordSetStatus(ctx context.Context, recordSet *dnsv1alpha1.RecordSet, mutate func(*dnsv1alpha1.RecordSetStatus)) error {
	return r.patchRecordSetStatusWithDeletionCompletion(ctx, recordSet, false, mutate)
}

// patchRecordSetDeletionCompleted is reserved for the provider-confirmed
// deletion completion path.
func (r *ZoneReconciler) patchRecordSetDeletionCompleted(ctx context.Context, recordSet *dnsv1alpha1.RecordSet, mutate func(*dnsv1alpha1.RecordSetStatus)) error {
	return r.patchRecordSetStatusWithDeletionCompletion(ctx, recordSet, true, mutate)
}

func (r *ZoneReconciler) patchRecordSetStatusWithDeletionCompletion(ctx context.Context, recordSet *dnsv1alpha1.RecordSet, deletionCompleted bool, mutate func(*dnsv1alpha1.RecordSetStatus)) error {
	before := recordSet.DeepCopy()
	mutate(&recordSet.Status)

	namespace, name := recordSetZoneKey(recordSet)
	var unit dnsv1alpha1.ZoneUnit
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &unit); err != nil {
		if apierrors.IsNotFound(err) {
			return apierrors.NewConflict(dnsv1alpha1.Resource("zoneunits"), name, fmt.Errorf("ZoneUnit was deleted before RecordSet status could be written"))
		}
		return err
	}
	if !zoneUnitRecordSetSourceCurrent(&unit, recordSet) {
		return apierrors.NewConflict(dnsv1alpha1.Resource("zoneunits"), name, fmt.Errorf("ZoneUnit RecordSet source changed before status could be written"))
	}
	if !deletionCompleted && equality.Semantic.DeepEqual(before.Status, recordSet.Status) {
		return nil
	}

	// RecordSet is synthesized from a prior ZoneUnit observation. Rebase only
	// the fields this writer changed onto a receipt for the same claim, so
	// condition writes cannot replay stale provider state from another UID.
	unitBase := unit.DeepCopy()
	index := -1
	for candidateIndex, status := range unit.Status.RecordSets {
		if status.RecordSetNamespace != recordSet.Namespace || status.RecordSetName != recordSet.Name {
			continue
		}
		if index < 0 {
			index = candidateIndex
		}
		if status.RecordSetUID != "" && status.RecordSetUID == recordSet.UID {
			index = candidateIndex
			break
		}
	}
	next := dnsv1alpha1.ZoneUnitRecordSetStatus{
		RecordSetNamespace: recordSet.Namespace,
		RecordSetName:      recordSet.Name,
		RecordSetUID:       recordSet.UID,
	}
	if index >= 0 &&
		unit.Status.RecordSets[index].RecordSetUID != "" &&
		unit.Status.RecordSets[index].RecordSetUID == recordSet.UID {
		next = unit.Status.RecordSets[index]
	}
	mergeRecordSetStatus(&next, &before.Status, &recordSet.Status)
	next.ObservedGeneration = recordSet.Generation
	next.RecordSetUID = recordSet.UID
	if deletionCompleted {
		next.DeletionCompleted = true
	}
	if index >= 0 {
		unit.Status.RecordSets[index] = next
	} else {
		unit.Status.RecordSets = append(unit.Status.RecordSets, next)
	}
	unit.Status.ObservedGeneration = unit.Generation
	setZoneUnitProgrammedCondition(&unit)
	if equality.Semantic.DeepEqual(unitBase.Status, unit.Status) {
		return nil
	}
	// Merge patches replace arrays. The resource-version precondition turns a
	// stale recordSets snapshot into a reconcile conflict instead of replacing
	// sibling claims.
	return r.Status().Patch(ctx, &unit, client.MergeFromWithOptions(unitBase, client.MergeFromWithOptimisticLock{}))
}

func zoneUnitRecordSetSourceCurrent(unit *dnsv1alpha1.ZoneUnit, recordSet *dnsv1alpha1.RecordSet) bool {
	index := slices.IndexFunc(unit.Spec.RecordSets, func(item dnsv1alpha1.ZoneUnitRecordSetSpec) bool {
		return item.RecordSetNamespace == recordSet.Namespace && item.RecordSetName == recordSet.Name
	})
	if index < 0 {
		return false
	}
	item := unit.Spec.RecordSets[index]
	if !item.IsAllowed() && !item.DeletionRequested {
		return false
	}
	if item.RecordSetUID == "" ||
		recordSet.UID == "" ||
		item.RecordSetUID != recordSet.UID ||
		item.ObservedGeneration != recordSet.Generation ||
		item.DeletionRequested != !recordSet.DeletionTimestamp.IsZero() {
		return false
	}
	expected := route53RecordSetFromZoneUnitItem(unit, item, dnsv1alpha1.ZoneUnitRecordSetStatus{})
	return equality.Semantic.DeepEqual(expected.Spec, recordSet.Spec)
}

func mergeRecordSetStatus(current *dnsv1alpha1.ZoneUnitRecordSetStatus, before, after *dnsv1alpha1.RecordSetStatus) {
	if !equality.Semantic.DeepEqual(before.Provider, after.Provider) {
		current.Provider = after.Provider
	}
	mergeRecordSetConditions(&current.Conditions, before.Conditions, after.Conditions)
}

func mergeRecordSetConditions(current *[]metav1.Condition, before, after []metav1.Condition) {
	for _, beforeCondition := range before {
		if meta.FindStatusCondition(after, beforeCondition.Type) == nil {
			*current = slices.DeleteFunc(*current, func(condition metav1.Condition) bool {
				return condition.Type == beforeCondition.Type
			})
		}
	}
	for _, afterCondition := range after {
		beforeCondition := meta.FindStatusCondition(before, afterCondition.Type)
		if beforeCondition != nil && equality.Semantic.DeepEqual(*beforeCondition, afterCondition) {
			continue
		}
		index := slices.IndexFunc(*current, func(condition metav1.Condition) bool {
			return condition.Type == afterCondition.Type
		})
		if index >= 0 {
			(*current)[index] = afterCondition
		} else {
			*current = append(*current, afterCondition)
		}
	}
}

func (r *ZoneReconciler) patchRoute53RecordSetStatus(ctx context.Context, recordSet *dnsv1alpha1.RecordSet, mutate func(*route53v1alpha1.Route53RecordSetStatusData)) error {
	statusData, err := route53RecordSetStatusData(recordSet)
	if err != nil {
		return err
	}
	mutate(&statusData)
	raw, err := json.Marshal(statusData)
	if err != nil {
		return err
	}
	return r.patchRecordSetStatus(ctx, recordSet, func(status *dnsv1alpha1.RecordSetStatus) {
		status.ObservedGeneration = recordSet.Generation
		status.Provider = nil
		if statusData.HostedZoneID != "" || statusData.RecordName != "" || statusData.RecordType != "" {
			status.Provider = &dnsv1alpha1.ProviderStatus{State: runtime.RawExtension{Raw: raw}}
		}
	})
}
