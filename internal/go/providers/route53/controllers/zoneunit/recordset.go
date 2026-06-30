package route53

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"

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

var (
	cnameTargetPattern  = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?|_[a-z0-9]([a-z0-9-]{0,60}[a-z0-9])?)(\.([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?|_[a-z0-9]([a-z0-9-]{0,60}[a-z0-9])?))*$`)
	aliasDNSNamePattern = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)(\.([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?))*\.$`)
	mxExchangePattern   = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)(\.([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?))*$`)
	caaTagPattern       = regexp.MustCompile(`^[a-z0-9]+$`)
)

type plannedRecordSetChange struct {
	recordSet *dnsv1alpha1.RecordSet
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
	managed := route53RecordSetStatusMatches(statusData, desired) || slices.Contains(recordSet.Finalizers, RecordSetFinalizer)

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
			return RecordSetChange{}, false, r.setRecordSetAccepted(ctx, recordSet, metav1.ConditionFalse, "RecordSetConflict", "record identity is owned by another RecordSet")
		}
		return RecordSetChange{}, false, r.removeRecordSetFinalizer(ctx, recordSet)
	}
	if existing.Name == "" {
		return RecordSetChange{}, false, r.removeRecordSetFinalizer(ctx, recordSet)
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
	if err := r.patchRoute53ZoneStatus(ctx, zone, func(data *route53v1alpha1.Route53ZoneStatusData) {
		data.PendingRecordSetChange = nil
	}); err != nil {
		return ctrl.Result{}, true, err
	}
	return ctrl.Result{Requeue: true}, true, nil
}

func (r *ZoneReconciler) recordSetsForZone(ctx context.Context, zone *dnsv1alpha1.Zone) ([]dnsv1alpha1.RecordSet, error) {
	var unit dnsv1alpha1.ZoneUnit
	if err := r.Get(ctx, client.ObjectKey{Namespace: zone.Namespace, Name: zone.Name}, &unit); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	statusByRef := map[string]dnsv1alpha1.ZoneUnitRecordSetStatus{}
	for _, status := range unit.Status.RecordSets {
		statusByRef[zoneUnitRecordSetStatusKey(status)] = status
	}
	recordSets := make([]dnsv1alpha1.RecordSet, 0, len(unit.Spec.RecordSets))
	for _, item := range unit.Spec.RecordSets {
		if !item.IsAllowed() && !item.DeletionRequested {
			continue
		}
		status := statusByRef[zoneUnitRecordSetItemKey(item)]
		if status.DeletionCompleted && !item.DeletionRequested {
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
	return r.patchRecordSetStatus(ctx, recordSet, func(status *dnsv1alpha1.RecordSetStatus) {
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
	base := recordSet.DeepCopy()
	mutate(&recordSet.Status)
	if equality.Semantic.DeepEqual(base.Status, recordSet.Status) {
		return nil
	}

	namespace, name := recordSetZoneKey(recordSet)
	var unit dnsv1alpha1.ZoneUnit
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &unit); err != nil {
		return client.IgnoreNotFound(err)
	}
	unitBase := unit.DeepCopy()
	index := slices.IndexFunc(unit.Status.RecordSets, func(status dnsv1alpha1.ZoneUnitRecordSetStatus) bool {
		return status.RecordSetNamespace == recordSet.Namespace && status.RecordSetName == recordSet.Name
	})
	next := dnsv1alpha1.ZoneUnitRecordSetStatus{
		RecordSetNamespace: recordSet.Namespace,
		RecordSetName:      recordSet.Name,
		ObservedGeneration: recordSet.Status.ObservedGeneration,
		Provider:           recordSet.Status.Provider,
		Conditions:         slices.Clone(recordSet.Status.Conditions),
	}
	if recordSet.DeletionTimestamp != nil {
		programmed := meta.FindStatusCondition(recordSet.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed))
		next.DeletionCompleted = programmed != nil && programmed.Status == metav1.ConditionTrue
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
	return client.IgnoreNotFound(r.Status().Patch(ctx, &unit, client.MergeFrom(unitBase)))
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

type route53RecordSetIdentity struct {
	recordType dnsv1alpha1.RecordType
	recordName string
}

func recordSetIdentity(recordType dnsv1alpha1.RecordType, recordName string) route53RecordSetIdentity {
	return route53RecordSetIdentity{
		recordType: recordType,
		recordName: normalizeRoute53RecordName(recordName),
	}
}

func (i route53RecordSetIdentity) key() string {
	return string(i.recordType) + "\x00" + i.recordName
}

func indexRecordSets(recordSets []RecordSetResource) map[string]RecordSetResource {
	index := make(map[string]RecordSetResource, len(recordSets))
	for _, recordSet := range recordSets {
		identity := recordSetIdentity(recordSet.Type, recordSet.Name)
		index[identity.key()] = recordSet
	}
	return index
}

func desiredRoute53RecordSet(recordSet *dnsv1alpha1.RecordSet, hostedZoneID, recordName string, options route53v1alpha1.Route53RecordSetOptions) RecordSetResource {
	desired := RecordSetResource{
		HostedZoneID: normalizeHostedZoneID(hostedZoneID),
		Name:         normalizeRoute53RecordName(recordName),
		Type:         recordSet.Spec.Type,
	}
	if options.Alias != nil {
		desired.Alias = options.Alias
		return desired
	}
	ttl := int64(*recordSet.Spec.TTL)
	desired.TTL = &ttl
	switch recordSet.Spec.Type {
	case dnsv1alpha1.RecordTypeA:
		desired.Values = slices.Clone(recordSet.Spec.A.Addresses)
	case dnsv1alpha1.RecordTypeAAAA:
		desired.Values = slices.Clone(recordSet.Spec.AAAA.Addresses)
	case dnsv1alpha1.RecordTypeTXT:
		for _, value := range recordSet.Spec.TXT.Values {
			desired.Values = append(desired.Values, quoteRoute53TXTValue(value))
		}
	case dnsv1alpha1.RecordTypeCNAME:
		desired.Values = []string{normalizeRoute53RecordName(recordSet.Spec.CNAME.Target)}
	case dnsv1alpha1.RecordTypeMX:
		for _, record := range recordSet.Spec.MX.Records {
			desired.Values = append(desired.Values, formatRoute53MXValue(record))
		}
	case dnsv1alpha1.RecordTypeCAA:
		for _, record := range recordSet.Spec.CAA.Records {
			desired.Values = append(desired.Values, formatRoute53CAAValue(record))
		}
	case dnsv1alpha1.RecordTypeNS:
		for _, nameServer := range recordSet.Spec.NS.NameServers {
			desired.Values = append(desired.Values, normalizeRoute53RecordName(nameServer))
		}
	}
	slices.Sort(desired.Values)
	return desired
}

func route53RecordSetEqual(a, b RecordSetResource) bool {
	if normalizeHostedZoneID(a.HostedZoneID) != normalizeHostedZoneID(b.HostedZoneID) ||
		normalizeRoute53RecordName(a.Name) != normalizeRoute53RecordName(b.Name) ||
		a.Type != b.Type {
		return false
	}
	if (a.TTL == nil) != (b.TTL == nil) {
		return false
	}
	if a.TTL != nil && b.TTL != nil && *a.TTL != *b.TTL {
		return false
	}
	if (a.Alias == nil) != (b.Alias == nil) {
		return false
	}
	if a.Alias != nil && b.Alias != nil {
		if normalizeRoute53AliasDNSNameForCompare(a.Alias.DNSName) != normalizeRoute53AliasDNSNameForCompare(b.Alias.DNSName) ||
			normalizeHostedZoneID(a.Alias.HostedZoneID) != normalizeHostedZoneID(b.Alias.HostedZoneID) ||
			a.Alias.EvaluateTargetHealth != b.Alias.EvaluateTargetHealth {
			return false
		}
	}
	aValues, ok := canonicalRecordSetValues(a.Type, a.Values)
	if !ok {
		return false
	}
	bValues, ok := canonicalRecordSetValues(b.Type, b.Values)
	if !ok {
		return false
	}
	slices.Sort(aValues)
	slices.Sort(bValues)
	return slices.Equal(aValues, bValues)
}

func normalizeRoute53AliasDNSNameForCompare(name string) string {
	normalized := normalizeRoute53RecordName(name)
	withoutDualstack := strings.TrimPrefix(normalized, "dualstack.")
	if withoutDualstack != normalized && isRoute53ELBAliasDNSName(withoutDualstack) {
		return withoutDualstack
	}
	return normalized
}

func isRoute53ELBAliasDNSName(name string) bool {
	trimmed := strings.TrimSuffix(name, ".")
	return strings.HasSuffix(trimmed, ".elb.amazonaws.com") ||
		strings.Contains(trimmed, ".elb.") && strings.HasSuffix(trimmed, ".amazonaws.com") ||
		strings.HasSuffix(trimmed, ".elb.amazonaws.com.cn") ||
		strings.Contains(trimmed, ".elb.") && strings.HasSuffix(trimmed, ".amazonaws.com.cn")
}

func route53RecordSetOptions(recordSet *dnsv1alpha1.RecordSet) (route53v1alpha1.Route53RecordSetOptions, error) {
	if len(recordSet.Spec.Options.Raw) == 0 {
		return route53v1alpha1.Route53RecordSetOptions{}, nil
	}
	var options route53v1alpha1.Route53RecordSetOptions
	if err := json.Unmarshal(recordSet.Spec.Options.Raw, &options); err != nil {
		return route53v1alpha1.Route53RecordSetOptions{}, fmt.Errorf("options must match Route 53 RecordSet schema: %w", err)
	}
	return options, nil
}

func validateRoute53RecordSetBody(recordSet *dnsv1alpha1.RecordSet, options route53v1alpha1.Route53RecordSetOptions) string {
	if options.Alias != nil {
		if recordSet.Spec.Type != dnsv1alpha1.RecordTypeA && recordSet.Spec.Type != dnsv1alpha1.RecordTypeAAAA {
			return "Route 53 alias supports only A and AAAA record types"
		}
		if recordSet.Spec.TTL != nil {
			return "ttl must not be specified when route53 alias is used"
		}
		if recordSet.Spec.A != nil && len(recordSet.Spec.A.Addresses) > 0 {
			return "a.addresses must not be specified when route53 alias is used"
		}
		if recordSet.Spec.AAAA != nil && len(recordSet.Spec.AAAA.Addresses) > 0 {
			return "aaaa.addresses must not be specified when route53 alias is used"
		}
		if recordSet.Spec.TXT != nil && len(recordSet.Spec.TXT.Values) > 0 {
			return "txt.values must not be specified when route53 alias is used"
		}
		if recordSet.Spec.CNAME != nil && recordSet.Spec.CNAME.Target != "" {
			return "cname.target must not be specified when route53 alias is used"
		}
		if recordSet.Spec.MX != nil && len(recordSet.Spec.MX.Records) > 0 {
			return "mx.records must not be specified when route53 alias is used"
		}
		if recordSet.Spec.CAA != nil && len(recordSet.Spec.CAA.Records) > 0 {
			return "caa.records must not be specified when route53 alias is used"
		}
		if recordSet.Spec.NS != nil && len(recordSet.Spec.NS.NameServers) > 0 {
			return "ns.nameServers must not be specified when route53 alias is used"
		}
		if options.Alias.DNSName == "" || options.Alias.HostedZoneID == "" {
			return "options.alias.dnsName and options.alias.hostedZoneID are required"
		}
		if err := validateAliasDNSName(options.Alias.DNSName); err != nil {
			return "options.alias.dnsName " + err.Error()
		}
		return ""
	}
	if recordSet.Spec.TTL == nil {
		return "ttl is required for standard records"
	}
	switch recordSet.Spec.Type {
	case dnsv1alpha1.RecordTypeA:
		if recordSet.Spec.A == nil || len(recordSet.Spec.A.Addresses) == 0 {
			return "a.addresses is required for standard A records"
		}
		if err := validateRecordSetIPAddresses(recordSet.Spec.A.Addresses, false); err != nil {
			return "a.addresses " + err.Error()
		}
		if recordSet.Spec.AAAA != nil {
			return "aaaa must not be specified for standard A records"
		}
		if recordSet.Spec.TXT != nil {
			return "txt must not be specified for standard A records"
		}
		if recordSet.Spec.CNAME != nil {
			return "cname must not be specified for standard A records"
		}
		if recordSet.Spec.MX != nil {
			return "mx must not be specified for standard A records"
		}
		if recordSet.Spec.CAA != nil {
			return "caa must not be specified for standard A records"
		}
		if recordSet.Spec.NS != nil {
			return "ns must not be specified for standard A records"
		}
	case dnsv1alpha1.RecordTypeAAAA:
		if recordSet.Spec.AAAA == nil || len(recordSet.Spec.AAAA.Addresses) == 0 {
			return "aaaa.addresses is required for standard AAAA records"
		}
		if err := validateRecordSetIPAddresses(recordSet.Spec.AAAA.Addresses, true); err != nil {
			return "aaaa.addresses " + err.Error()
		}
		if recordSet.Spec.A != nil {
			return "a must not be specified for standard AAAA records"
		}
		if recordSet.Spec.TXT != nil {
			return "txt must not be specified for standard AAAA records"
		}
		if recordSet.Spec.CNAME != nil {
			return "cname must not be specified for standard AAAA records"
		}
		if recordSet.Spec.MX != nil {
			return "mx must not be specified for standard AAAA records"
		}
		if recordSet.Spec.CAA != nil {
			return "caa must not be specified for standard AAAA records"
		}
		if recordSet.Spec.NS != nil {
			return "ns must not be specified for standard AAAA records"
		}
	case dnsv1alpha1.RecordTypeTXT:
		if recordSet.Spec.TXT == nil || len(recordSet.Spec.TXT.Values) == 0 {
			return "txt.values is required for standard TXT records"
		}
		if err := validateRecordSetTXTValues(recordSet.Spec.TXT.Values); err != nil {
			return "txt.values " + err.Error()
		}
		if recordSet.Spec.A != nil {
			return "a must not be specified for standard TXT records"
		}
		if recordSet.Spec.AAAA != nil {
			return "aaaa must not be specified for standard TXT records"
		}
		if recordSet.Spec.CNAME != nil {
			return "cname must not be specified for standard TXT records"
		}
		if recordSet.Spec.MX != nil {
			return "mx must not be specified for standard TXT records"
		}
		if recordSet.Spec.CAA != nil {
			return "caa must not be specified for standard TXT records"
		}
		if recordSet.Spec.NS != nil {
			return "ns must not be specified for standard TXT records"
		}
	case dnsv1alpha1.RecordTypeCNAME:
		if recordSet.Spec.CNAME == nil || recordSet.Spec.CNAME.Target == "" {
			return "cname.target is required for standard CNAME records"
		}
		if err := validateCNAMETarget(recordSet.Spec.CNAME.Target); err != nil {
			return "cname.target " + err.Error()
		}
		if recordSet.Spec.A != nil {
			return "a must not be specified for standard CNAME records"
		}
		if recordSet.Spec.AAAA != nil {
			return "aaaa must not be specified for standard CNAME records"
		}
		if recordSet.Spec.TXT != nil {
			return "txt must not be specified for standard CNAME records"
		}
		if recordSet.Spec.MX != nil {
			return "mx must not be specified for standard CNAME records"
		}
		if recordSet.Spec.CAA != nil {
			return "caa must not be specified for standard CNAME records"
		}
		if recordSet.Spec.NS != nil {
			return "ns must not be specified for standard CNAME records"
		}
	case dnsv1alpha1.RecordTypeMX:
		if recordSet.Spec.MX == nil || len(recordSet.Spec.MX.Records) == 0 {
			return "mx.records is required for standard MX records"
		}
		if err := validateRecordSetMXRecords(recordSet.Spec.MX.Records); err != nil {
			return "mx.records " + err.Error()
		}
		if recordSet.Spec.A != nil {
			return "a must not be specified for standard MX records"
		}
		if recordSet.Spec.AAAA != nil {
			return "aaaa must not be specified for standard MX records"
		}
		if recordSet.Spec.TXT != nil {
			return "txt must not be specified for standard MX records"
		}
		if recordSet.Spec.CNAME != nil {
			return "cname must not be specified for standard MX records"
		}
		if recordSet.Spec.CAA != nil {
			return "caa must not be specified for standard MX records"
		}
		if recordSet.Spec.NS != nil {
			return "ns must not be specified for standard MX records"
		}
	case dnsv1alpha1.RecordTypeCAA:
		if recordSet.Spec.CAA == nil || len(recordSet.Spec.CAA.Records) == 0 {
			return "caa.records is required for standard CAA records"
		}
		if err := validateRecordSetCAARecords(recordSet.Spec.CAA.Records); err != nil {
			return "caa.records " + err.Error()
		}
		if recordSet.Spec.A != nil {
			return "a must not be specified for standard CAA records"
		}
		if recordSet.Spec.AAAA != nil {
			return "aaaa must not be specified for standard CAA records"
		}
		if recordSet.Spec.TXT != nil {
			return "txt must not be specified for standard CAA records"
		}
		if recordSet.Spec.CNAME != nil {
			return "cname must not be specified for standard CAA records"
		}
		if recordSet.Spec.MX != nil {
			return "mx must not be specified for standard CAA records"
		}
		if recordSet.Spec.NS != nil {
			return "ns must not be specified for standard CAA records"
		}
	case dnsv1alpha1.RecordTypeNS:
		if recordSet.Spec.Name == "@" {
			return "record name @ is not allowed for delegated NS records"
		}
		if strings.HasPrefix(recordSet.Spec.Name, "*") {
			return "wildcard record names are not allowed for delegated NS records"
		}
		if recordSet.Spec.NS == nil || len(recordSet.Spec.NS.NameServers) == 0 {
			return "ns.nameServers is required for standard NS records"
		}
		if err := validateRecordSetNSNameServers(recordSet.Spec.NS.NameServers); err != nil {
			return "ns.nameServers " + err.Error()
		}
		if recordSet.Spec.A != nil {
			return "a must not be specified for standard NS records"
		}
		if recordSet.Spec.AAAA != nil {
			return "aaaa must not be specified for standard NS records"
		}
		if recordSet.Spec.TXT != nil {
			return "txt must not be specified for standard NS records"
		}
		if recordSet.Spec.CNAME != nil {
			return "cname must not be specified for standard NS records"
		}
		if recordSet.Spec.MX != nil {
			return "mx must not be specified for standard NS records"
		}
		if recordSet.Spec.CAA != nil {
			return "caa must not be specified for standard NS records"
		}
	default:
		return "only A, AAAA, TXT, CNAME, MX, CAA, and delegated NS records are supported without a Route 53 alias option"
	}
	return ""
}

type route53RecordSetAdoption struct {
	Enabled bool `json:"enabled"`
}

func route53RecordSetAdoptionEnabled(recordSet *dnsv1alpha1.RecordSet) (bool, error) {
	if len(recordSet.Spec.Adoption.Raw) == 0 {
		return false, nil
	}
	var adoption route53RecordSetAdoption
	if err := json.Unmarshal(recordSet.Spec.Adoption.Raw, &adoption); err != nil {
		return true, fmt.Errorf("adoption must be an object with enabled: %w", err)
	}
	if !adoption.Enabled {
		return true, errors.New("adoption.enabled must be true")
	}
	return true, nil
}

func route53RecordSetManagedResourceMismatch(recordSet *dnsv1alpha1.RecordSet, statusData route53v1alpha1.Route53RecordSetStatusData) (string, bool, error) {
	adopting, err := route53RecordSetAdoptionEnabled(recordSet)
	if err != nil || !adopting {
		return "", false, err
	}
	statusZoneID := normalizeHostedZoneID(statusData.HostedZoneID)
	statusRecordName := normalizeRoute53RecordName(statusData.RecordName)
	if statusZoneID == "" || statusRecordName == "" || statusData.RecordType == "" {
		return "", false, nil
	}
	return "", false, nil
}

func route53RecordSetStatusData(recordSet *dnsv1alpha1.RecordSet) (route53v1alpha1.Route53RecordSetStatusData, error) {
	if recordSet.Status.Provider != nil && len(recordSet.Status.Provider.State.Raw) > 0 {
		var data route53v1alpha1.Route53RecordSetStatusData
		if err := json.Unmarshal(recordSet.Status.Provider.State.Raw, &data); err != nil {
			return route53v1alpha1.Route53RecordSetStatusData{}, fmt.Errorf("RecordSet status.provider.state must match Route 53 schema: %w", err)
		}
		return data, nil
	}
	if recordSet.Status.Provider != nil && len(recordSet.Status.Provider.Data.Raw) > 0 {
		var data route53v1alpha1.Route53RecordSetStatusData
		if err := json.Unmarshal(recordSet.Status.Provider.Data.Raw, &data); err != nil {
			return route53v1alpha1.Route53RecordSetStatusData{}, fmt.Errorf("RecordSet status.provider.data must match Route 53 schema: %w", err)
		}
		return data, nil
	}
	return route53v1alpha1.Route53RecordSetStatusData{}, nil
}

func providerStatusHasPayload(provider *dnsv1alpha1.ProviderStatus) bool {
	return provider != nil && (len(provider.Data.Raw) > 0 || len(provider.State.Raw) > 0)
}

func setRoute53RecordSetStatusData(data *route53v1alpha1.Route53RecordSetStatusData, recordSet RecordSetResource) {
	data.HostedZoneID = normalizeHostedZoneID(recordSet.HostedZoneID)
	data.RecordName = normalizeRoute53RecordName(recordSet.Name)
	data.RecordType = string(recordSet.Type)
}

func route53RecordSetStatusMatches(data route53v1alpha1.Route53RecordSetStatusData, recordSet RecordSetResource) bool {
	return normalizeHostedZoneID(data.HostedZoneID) == normalizeHostedZoneID(recordSet.HostedZoneID) &&
		normalizeRoute53RecordName(data.RecordName) == normalizeRoute53RecordName(recordSet.Name) &&
		data.RecordType == string(recordSet.Type)
}

func recordSetZoneKey(recordSet *dnsv1alpha1.RecordSet) (string, string) {
	namespace := recordSet.Namespace
	if recordSet.Spec.ZoneRef.Namespace != nil && *recordSet.Spec.ZoneRef.Namespace != "" {
		namespace = *recordSet.Spec.ZoneRef.Namespace
	}
	return namespace, recordSet.Spec.ZoneRef.Name
}

type zoneUnitRecordSetOwnership struct {
	byRef      map[string]dnsv1alpha1.ZoneUnitRecordSetSpec
	byIdentity map[string]dnsv1alpha1.ZoneUnitRecordSetSpec
}

func newZoneUnitRecordSetOwnership(items []dnsv1alpha1.ZoneUnitRecordSetSpec) *zoneUnitRecordSetOwnership {
	ownership := &zoneUnitRecordSetOwnership{
		byRef:      make(map[string]dnsv1alpha1.ZoneUnitRecordSetSpec, len(items)),
		byIdentity: make(map[string]dnsv1alpha1.ZoneUnitRecordSetSpec, len(items)),
	}
	for _, item := range items {
		refKey := recordSetClaimKey(item.RecordSetNamespace, item.RecordSetName)
		ownership.byRef[refKey] = item
		ownership.byIdentity[recordIdentityKey(item.Name, item.Type)] = item
		if item.Type == dnsv1alpha1.RecordTypeCNAME {
			ownership.byIdentity[cnameExclusionKey(item.Name)] = item
		}
	}
	return ownership
}

func (o *zoneUnitRecordSetOwnership) acceptedOwner(recordSet *dnsv1alpha1.RecordSet) (bool, bool) {
	refKey := recordSetClaimKey(recordSet.Namespace, recordSet.Name)
	if item, ok := o.byRef[refKey]; ok {
		if item.Name == recordSet.Spec.Name && item.Type == recordSet.Spec.Type {
			return true, false
		}
		return false, true
	}
	for _, identityKey := range zoneUnitRecordSetIdentityKeys(recordSet.Spec.Name, recordSet.Spec.Type) {
		if owner, ok := o.byIdentity[identityKey]; ok && recordSetClaimKey(owner.RecordSetNamespace, owner.RecordSetName) != refKey {
			return false, true
		}
	}
	return false, false
}

func zoneUnitRecordSetIdentityKeys(recordName string, recordType dnsv1alpha1.RecordType) []string {
	keys := []string{recordIdentityKey(recordName, recordType)}
	keys = append(keys, cnameExclusionKey(recordName))
	return keys
}

func recordSetClaimKey(namespace, name string) string {
	return namespace + "\x00" + name
}

func zoneUnitRecordSetItemKey(item dnsv1alpha1.ZoneUnitRecordSetSpec) string {
	return recordSetClaimKey(item.RecordSetNamespace, item.RecordSetName)
}

func zoneUnitRecordSetStatusKey(status dnsv1alpha1.ZoneUnitRecordSetStatus) string {
	return recordSetClaimKey(status.RecordSetNamespace, status.RecordSetName)
}

func recordIdentityKey(name string, recordType dnsv1alpha1.RecordType) string {
	return name + "\x00" + string(recordType)
}

func cnameExclusionKey(name string) string {
	return name + "\x00" + string(dnsv1alpha1.RecordTypeCNAME) + "\x00exclusive"
}

func canonicalRecordName(ownerName, domainName string) string {
	switch ownerName {
	case "@":
		return domainName + "."
	case "*":
		return "*." + domainName + "."
	default:
		return ownerName + "." + domainName + "."
	}
}

func providerVersionSupportsType(providerVersion *dnsv1alpha1.ProviderVersion, recordType dnsv1alpha1.RecordType) bool {
	return providerVersion != nil && slices.Contains(providerVersion.RecordSet.SupportedTypes, recordType)
}

func fullRecordNamePatternMatch(pattern, recordName string) (bool, error) {
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return false, err
	}
	match := compiled.FindStringIndex(recordName)
	return match != nil && match[0] == 0 && match[1] == len(recordName), nil
}

func validateRecordSetIPAddresses(values []string, wantIPv6 bool) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		address, err := netip.ParseAddr(value)
		if err != nil {
			return errors.New("must contain valid IP addresses")
		}
		if address.Is6() != wantIPv6 {
			if wantIPv6 {
				return errors.New("must contain only IPv6 addresses")
			}
			return errors.New("must contain only IPv4 addresses")
		}
		key := address.String()
		if _, ok := seen[key]; ok {
			return errors.New("must not contain duplicate IP addresses")
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateRecordSetTXTValues(values []string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		valueLen := len([]byte(value))
		if valueLen == 0 {
			return errors.New("must not contain empty values")
		}
		if valueLen > 4000 {
			return errors.New("must contain values of 4000 UTF-8 octets or fewer")
		}
		if !isPrintableASCII(value) {
			return errors.New("must contain only printable ASCII characters")
		}
		if _, ok := seen[value]; ok {
			return errors.New("must not contain duplicate values")
		}
		seen[value] = struct{}{}
	}
	return nil
}

func isPrintableASCII(value string) bool {
	for index := 0; index < len(value); index++ {
		if value[index] < 0x20 || value[index] > 0x7e {
			return false
		}
	}
	return true
}

func validateCNAMETarget(target string) error {
	if target == "" {
		return errors.New("must not be empty")
	}
	if len(target) > 253 {
		return errors.New("must be 253 octets or fewer")
	}
	if _, err := netip.ParseAddr(target); err == nil {
		return errors.New("must be a DNS name, not an IP address")
	}
	if !cnameTargetPattern.MatchString(target) {
		return errors.New("must be normalized lowercase ASCII without a trailing root dot")
	}
	return nil
}

func validateAliasDNSName(name string) error {
	if name == "" {
		return errors.New("must not be empty")
	}
	if len(name) > 254 {
		return errors.New("must be 254 octets or fewer")
	}
	if _, err := netip.ParseAddr(strings.TrimSuffix(name, ".")); err == nil {
		return errors.New("must be a DNS name, not an IP address")
	}
	if !aliasDNSNamePattern.MatchString(name) {
		return errors.New("must be normalized lowercase ASCII with a trailing root dot")
	}
	return nil
}

func validateRecordSetMXRecords(records []dnsv1alpha1.MXRecord) error {
	if len(records) == 0 {
		return errors.New("is required")
	}
	seen := make(map[string]struct{}, len(records))
	nullMXIndex := -1
	for index, record := range records {
		if record.Preference < 0 || record.Preference > 65535 {
			return fmt.Errorf("[%d].preference must be in range 0..65535", index)
		}
		if record.Exchange == "" {
			return fmt.Errorf("[%d].exchange is required", index)
		}
		if record.Exchange == "." {
			nullMXIndex = index
		} else if err := validateMXExchange(record.Exchange); err != nil {
			return fmt.Errorf("[%d].exchange %w", index, err)
		}
		key := fmt.Sprintf("%d\x00%s", record.Preference, record.Exchange)
		if _, ok := seen[key]; ok {
			return errors.New("must not contain duplicate preference and exchange pairs")
		}
		seen[key] = struct{}{}
	}
	if nullMXIndex >= 0 {
		if len(records) != 1 {
			return errors.New("must contain only one record when exchange is \".\"")
		}
		if records[nullMXIndex].Preference != 0 {
			return fmt.Errorf("[%d].preference must be 0 when exchange is \".\"", nullMXIndex)
		}
	}
	return nil
}

func validateRecordSetCAARecords(records []dnsv1alpha1.CAARecord) error {
	if len(records) == 0 {
		return errors.New("is required")
	}
	seen := make(map[string]struct{}, len(records))
	for index, record := range records {
		if record.Flags < 0 || record.Flags > 255 {
			return fmt.Errorf("[%d].flags must be in range 0..255", index)
		}
		if record.Tag == "" {
			return fmt.Errorf("[%d].tag is required", index)
		}
		if !caaTagPattern.MatchString(record.Tag) {
			return fmt.Errorf("[%d].tag must be lowercase ASCII alphanumeric", index)
		}
		if record.Value == "" {
			return fmt.Errorf("[%d].value is required", index)
		}
		key := fmt.Sprintf("%d\x00%s\x00%s", record.Flags, record.Tag, record.Value)
		if _, ok := seen[key]; ok {
			return errors.New("must not contain duplicate flags, tag, and value tuples")
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateRecordSetNSNameServers(nameServers []string) error {
	if len(nameServers) == 0 {
		return errors.New("is required")
	}
	seen := make(map[string]struct{}, len(nameServers))
	for index, nameServer := range nameServers {
		if err := validateNSNameServer(nameServer); err != nil {
			return fmt.Errorf("[%d] %w", index, err)
		}
		if _, ok := seen[nameServer]; ok {
			return errors.New("must not contain duplicate name servers")
		}
		seen[nameServer] = struct{}{}
	}
	return nil
}

func validateNSNameServer(nameServer string) error {
	if nameServer == "" {
		return errors.New("must not be empty")
	}
	if len(nameServer) > 253 {
		return errors.New("must be 253 octets or fewer")
	}
	if _, err := netip.ParseAddr(nameServer); err == nil {
		return errors.New("must be a DNS name, not an IP address")
	}
	if !mxExchangePattern.MatchString(nameServer) {
		return errors.New("must be normalized lowercase ASCII without a trailing root dot")
	}
	return nil
}

func validateMXExchange(exchange string) error {
	if exchange == "" {
		return errors.New("must not be empty")
	}
	if len(exchange) > 253 {
		return errors.New("must be 253 octets or fewer")
	}
	if _, err := netip.ParseAddr(exchange); err == nil {
		return errors.New("must be a DNS name, not an IP address")
	}
	if !mxExchangePattern.MatchString(exchange) {
		return errors.New("must be normalized lowercase ASCII without a trailing root dot")
	}
	return nil
}

func canonicalRecordSetValues(recordType dnsv1alpha1.RecordType, values []string) ([]string, bool) {
	switch recordType {
	case dnsv1alpha1.RecordTypeA, dnsv1alpha1.RecordTypeAAAA:
		canonical := make([]string, 0, len(values))
		for _, value := range values {
			address, err := netip.ParseAddr(value)
			if err != nil {
				return nil, false
			}
			if recordType == dnsv1alpha1.RecordTypeA && !address.Is4() {
				return nil, false
			}
			if recordType == dnsv1alpha1.RecordTypeAAAA && !address.Is6() {
				return nil, false
			}
			canonical = append(canonical, address.String())
		}
		return canonical, true
	case dnsv1alpha1.RecordTypeTXT:
		canonical := make([]string, 0, len(values))
		for _, value := range values {
			parsed, ok := parseRoute53TXTValue(value)
			if !ok {
				return nil, false
			}
			canonical = append(canonical, parsed)
		}
		return canonical, true
	case dnsv1alpha1.RecordTypeCNAME:
		canonical := make([]string, 0, len(values))
		for _, value := range values {
			canonical = append(canonical, normalizeRoute53RecordName(value))
		}
		return canonical, true
	case dnsv1alpha1.RecordTypeMX:
		canonical := make([]string, 0, len(values))
		for _, value := range values {
			record, ok := parseRoute53MXValue(value)
			if !ok {
				return nil, false
			}
			canonical = append(canonical, canonicalMXValue(record))
		}
		return canonical, true
	case dnsv1alpha1.RecordTypeCAA:
		canonical := make([]string, 0, len(values))
		for _, value := range values {
			record, ok := parseRoute53CAAValue(value)
			if !ok {
				return nil, false
			}
			canonical = append(canonical, canonicalCAAValue(record))
		}
		return canonical, true
	case dnsv1alpha1.RecordTypeNS:
		canonical := make([]string, 0, len(values))
		for _, value := range values {
			nameServer := strings.TrimSuffix(normalizeRoute53RecordName(value), ".")
			if err := validateNSNameServer(nameServer); err != nil {
				return nil, false
			}
			canonical = append(canonical, nameServer)
		}
		return canonical, true
	default:
		return slices.Clone(values), true
	}
}

func formatRoute53MXValue(record dnsv1alpha1.MXRecord) string {
	if record.Exchange == "." {
		return fmt.Sprintf("%d .", record.Preference)
	}
	return fmt.Sprintf("%d %s", record.Preference, normalizeRoute53RecordName(record.Exchange))
}

func canonicalMXValue(record dnsv1alpha1.MXRecord) string {
	if record.Exchange == "." {
		return fmt.Sprintf("%d .", record.Preference)
	}
	return fmt.Sprintf("%d %s", record.Preference, strings.TrimSuffix(normalizeRoute53RecordName(record.Exchange), "."))
}

func parseRoute53MXValue(value string) (dnsv1alpha1.MXRecord, bool) {
	parts := strings.Fields(value)
	if len(parts) != 2 {
		return dnsv1alpha1.MXRecord{}, false
	}
	preference, err := strconv.ParseInt(parts[0], 10, 32)
	if err != nil || preference < 0 || preference > 65535 {
		return dnsv1alpha1.MXRecord{}, false
	}
	exchange := parts[1]
	if exchange == "." {
		if preference != 0 {
			return dnsv1alpha1.MXRecord{}, false
		}
		return dnsv1alpha1.MXRecord{Preference: int32(preference), Exchange: "."}, true
	}
	normalized := strings.TrimSuffix(normalizeRoute53RecordName(exchange), ".")
	if err := validateMXExchange(normalized); err != nil {
		return dnsv1alpha1.MXRecord{}, false
	}
	return dnsv1alpha1.MXRecord{Preference: int32(preference), Exchange: normalized}, true
}

func formatRoute53CAAValue(record dnsv1alpha1.CAARecord) string {
	return fmt.Sprintf("%d %s %s", record.Flags, record.Tag, quoteRoute53TXTChunk(record.Value))
}

func canonicalCAAValue(record dnsv1alpha1.CAARecord) string {
	return fmt.Sprintf("%d %s %s", record.Flags, record.Tag, record.Value)
}

func parseRoute53CAAValue(value string) (dnsv1alpha1.CAARecord, bool) {
	parts := strings.Fields(value)
	if len(parts) < 3 {
		return dnsv1alpha1.CAARecord{}, false
	}
	flags, err := strconv.ParseInt(parts[0], 10, 32)
	if err != nil || flags < 0 || flags > 255 {
		return dnsv1alpha1.CAARecord{}, false
	}
	tag := parts[1]
	if !caaTagPattern.MatchString(tag) {
		return dnsv1alpha1.CAARecord{}, false
	}
	parsedValue, ok := parseRoute53TXTValue(strings.Join(parts[2:], " "))
	if !ok || parsedValue == "" {
		return dnsv1alpha1.CAARecord{}, false
	}
	return dnsv1alpha1.CAARecord{Flags: int32(flags), Tag: tag, Value: parsedValue}, true
}

func quoteRoute53TXTValue(value string) string {
	if value == "" {
		return `""`
	}
	chunks := make([]string, 0, len(value)/255+1)
	for len(value) > 0 {
		chunkLen := route53TXTChunkLen(value, 255)
		chunks = append(chunks, quoteRoute53TXTChunk(value[:chunkLen]))
		value = value[chunkLen:]
	}
	return strings.Join(chunks, " ")
}

func route53TXTChunkLen(value string, maxBytes int) int {
	if len(value) <= maxBytes {
		return len(value)
	}
	last := 0
	for index := range value {
		if index > maxBytes {
			break
		}
		last = index
	}
	if last == 0 {
		return maxBytes
	}
	return last
}

func quoteRoute53TXTChunk(chunk string) string {
	var out strings.Builder
	out.Grow(len(chunk) + 2)
	out.WriteByte('"')
	for _, r := range chunk {
		if r == '"' || r == '\\' {
			out.WriteByte('\\')
		}
		out.WriteRune(r)
	}
	out.WriteByte('"')
	return out.String()
}

func parseRoute53TXTValue(value string) (string, bool) {
	var out strings.Builder
	for index := 0; index < len(value); {
		for index < len(value) && (value[index] == ' ' || value[index] == '\t') {
			index++
		}
		if index >= len(value) {
			break
		}
		if value[index] != '"' {
			return "", false
		}
		index++
		for {
			if index >= len(value) {
				return "", false
			}
			if value[index] == '"' {
				index++
				break
			}
			if value[index] == '\\' {
				if index+3 < len(value) && isDecimalDigit(value[index+1]) && isDecimalDigit(value[index+2]) && isDecimalDigit(value[index+3]) {
					escaped := int(value[index+1]-'0')*100 + int(value[index+2]-'0')*10 + int(value[index+3]-'0')
					if escaped > 255 {
						return "", false
					}
					out.WriteByte(byte(escaped))
					index += 4
					continue
				}
				if index+1 >= len(value) {
					return "", false
				}
				index++
			}
			out.WriteByte(value[index])
			index++
		}
	}
	return out.String(), true
}

func isDecimalDigit(value byte) bool {
	return value >= '0' && value <= '9'
}

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
		})
	}
	return affected
}

func affectedRecordSetIncludes(affected []route53v1alpha1.Route53AffectedRecordSet, recordSet *dnsv1alpha1.RecordSet) bool {
	for _, item := range affected {
		if item.Namespace == recordSet.Namespace && item.Name == recordSet.Name {
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
