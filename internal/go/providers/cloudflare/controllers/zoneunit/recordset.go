package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	cloudflarev1alpha1 "github.com/appthrust/dns-api/pkg/go/api/cloudflare/v1alpha1"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	RecordSetFinalizer    = "cloudflare.dns.appthrust.io/recordset-finalizer"
	cloudflareFixedTTLMin = int32(60)
	cloudflareFixedTTLMax = int32(86400)
)

type CloudflareDNSRecord struct {
	ID        string
	Type      string
	Name      string
	Content   string
	Priority  *int32
	TTL       *int32
	Proxied   *bool
	Proxiable *bool
	Comment   string
	Tags      []string
	CAA       *CloudflareCAAData
}

type CloudflareCAAData struct {
	Flags int32
	Tag   string
	Value string
}

type RecordSetProvider interface {
	ListDNSRecords(ctx context.Context, zoneID, name string) ([]CloudflareDNSRecord, error)
	GetDNSRecord(ctx context.Context, zoneID, recordID string) (CloudflareDNSRecord, error)
	BatchDNSRecords(ctx context.Context, zoneID string, batch CloudflareDNSRecordBatch) (CloudflareDNSRecordBatch, error)
}

type CloudflareDNSRecordBatch struct {
	Deletes []CloudflareDNSRecord
	Patches []CloudflareDNSRecord
	Posts   []CloudflareDNSRecord
}

type recordSetReconciler struct {
	client.Client

	Scheme              *runtime.Scheme
	Provider            RecordSetProvider
	ProviderFactory     ZoneProviderFactory
	ControllerName      string
	ProviderName        string
	ProviderVersion     string
	RequeueAfter        time.Duration
	TemporaryRetryAfter time.Duration
	Recorder            record.EventRecorder
}

func (r *recordSetReconciler) reconcileZoneUnitRecordSets(ctx context.Context, unit *dnsv1alpha1.ZoneUnit) (ctrl.Result, error) {
	if pruned, err := r.pruneOrphanedRecordSetStatuses(ctx, unit); err != nil || pruned {
		return ctrl.Result{Requeue: pruned}, err
	}
	statusByRef := map[string]dnsv1alpha1.ZoneUnitRecordSetStatus{}
	for _, status := range unit.Status.RecordSets {
		statusByRef[cloudflareRecordSetClaimKey(status.RecordSetNamespace, status.RecordSetName)] = status
	}
	legacy := legacyRecordSetReceipts(unit)

	var aggregate ctrl.Result
	for _, item := range unit.Spec.RecordSets {
		if !item.IsAllowed() && !item.DeletionRequested {
			continue
		}
		if item.RecordSetUID == "" {
			aggregate = mergeCloudflareRecordSetResult(aggregate, ctrl.Result{RequeueAfter: r.requeueAfter()})
			continue
		}
		claimKey := cloudflareRecordSetClaimKey(item.RecordSetNamespace, item.RecordSetName)
		recordSet := cloudflareRecordSetFromZoneUnitItem(unit, item, statusByRef[claimKey])
		ctxData, accepted, err := r.acceptRecordSetFromZoneUnit(ctx, &recordSet, unit)
		ctxData.RecordSetSource = item.DeepCopy()
		if receipt, ok := legacy[claimKey]; ok {
			ctxData.LegacyReceipt = &receipt
		}
		if err != nil || !accepted {
			return aggregate, err
		}
		var result ctrl.Result
		if !recordSet.DeletionTimestamp.IsZero() {
			result, err = r.reconcileDeleteWithContext(ctx, &recordSet, ctxData)
		} else if !cloudflareConditionCurrent(ctxData.Identity.Status.Conditions, string(dnsv1alpha1.ConditionReady), ctxData.Identity.Generation) {
			result = ctrl.Result{RequeueAfter: r.requeueAfter()}
			err = r.setProgrammed(ctx, &recordSet, metav1.ConditionFalse, "ProviderIdentityNotReady", "referenced CloudflareIdentity is not Ready")
		} else if !cloudflareConditionCurrent(ctxData.Zone.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), ctxData.Zone.Generation) {
			result = ctrl.Result{RequeueAfter: r.requeueAfter()}
			err = r.setProgrammed(ctx, &recordSet, metav1.ConditionFalse, "ProviderChangePending", "referenced Cloudflare Zone is not programmed")
		} else if ctxData.ZoneID == "" {
			result = ctrl.Result{RequeueAfter: r.requeueAfter()}
			err = r.setProgrammed(ctx, &recordSet, metav1.ConditionFalse, "ZoneNotAccepted", "Cloudflare zone ID is not observed yet")
		} else {
			var provider RecordSetProvider
			provider, err = r.providerForIdentity(ctx, ctxData.Identity)
			if err == nil {
				result, err = r.reconcileNormal(ctx, provider, &recordSet, ctxData)
			} else {
				result, err = r.failProgrammedForProviderError(ctx, &recordSet, err)
			}
		}
		if err != nil {
			return aggregate, err
		}
		aggregate = mergeCloudflareRecordSetResult(aggregate, result)
	}
	return aggregate, nil
}

type cloudflareRecordSetContext struct {
	Zone            *dnsv1alpha1.Zone
	ZoneClass       *dnsv1alpha1.ZoneClass
	Identity        *cloudflarev1alpha1.CloudflareIdentity
	Provider        *dnsv1alpha1.Provider
	Version         *dnsv1alpha1.ProviderVersion
	ZoneUnit        *dnsv1alpha1.ZoneUnit
	RecordSetSource *dnsv1alpha1.ZoneUnitRecordSetSpec
	// LegacyReceipt is the UID-less pre-upgrade ledger entry for this claim,
	// if any. It is never loaded into RecordSet status; see legacyReceiptRecords.
	LegacyReceipt *dnsv1alpha1.ZoneUnitRecordSetStatus
	ZoneID        string
	FullName      string
}

func cloudflareRecordSetFromZoneUnitItem(unit *dnsv1alpha1.ZoneUnit, item dnsv1alpha1.ZoneUnitRecordSetSpec, status dnsv1alpha1.ZoneUnitRecordSetStatus) dnsv1alpha1.RecordSet {
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
	}
	if cloudflareZoneUnitRecordSetStatusMatchesItem(status, item) {
		recordSet.Status = cloudflareRecordSetStatusFromZoneUnitStatus(status)
	} else {
		recordSet.Status.ObservedGeneration = item.ObservedGeneration
	}
	if item.DeletionRequested {
		now := metav1.Now()
		recordSet.DeletionTimestamp = &now
	}
	return recordSet
}

func cloudflareRecordSetStatusFromZoneUnitStatus(status dnsv1alpha1.ZoneUnitRecordSetStatus) dnsv1alpha1.RecordSetStatus {
	return dnsv1alpha1.RecordSetStatus{
		ObservedGeneration: status.ObservedGeneration,
		Provider:           status.Provider,
		Conditions:         slices.Clone(status.Conditions),
	}
}

func cloudflareZoneUnitRecordSetStatusMatchesItem(status dnsv1alpha1.ZoneUnitRecordSetStatus, item dnsv1alpha1.ZoneUnitRecordSetSpec) bool {
	// Ownership belongs to the incarnation, not one desired generation. An
	// ordinary update must retain provider IDs; readiness checks freshness below.
	return status.RecordSetNamespace == item.RecordSetNamespace &&
		status.RecordSetName == item.RecordSetName &&
		status.RecordSetUID != "" &&
		status.RecordSetUID == item.RecordSetUID
}

func cloudflareZoneUnitRecordSetSourceMatches(unit *dnsv1alpha1.ZoneUnit, item dnsv1alpha1.ZoneUnitRecordSetSpec, recordSet *dnsv1alpha1.RecordSet) bool {
	zoneNamespace, zoneName := recordSetZoneKey(recordSet)
	return item.RecordSetNamespace == recordSet.Namespace &&
		item.RecordSetName == recordSet.Name &&
		item.RecordSetUID != "" &&
		item.RecordSetUID == recordSet.UID &&
		item.ObservedGeneration == recordSet.Generation &&
		item.DeletionRequested == !recordSet.DeletionTimestamp.IsZero() &&
		unit.Spec.Provider == recordSet.Spec.Provider &&
		zoneNamespace == unit.Namespace &&
		zoneName == unit.Name &&
		item.Name == recordSet.Spec.Name &&
		item.Type == recordSet.Spec.Type &&
		equality.Semantic.DeepEqual(item.TTL, recordSet.Spec.TTL) &&
		equality.Semantic.DeepEqual(item.A, recordSet.Spec.A) &&
		equality.Semantic.DeepEqual(item.AAAA, recordSet.Spec.AAAA) &&
		equality.Semantic.DeepEqual(item.TXT, recordSet.Spec.TXT) &&
		equality.Semantic.DeepEqual(item.CNAME, recordSet.Spec.CNAME) &&
		equality.Semantic.DeepEqual(item.MX, recordSet.Spec.MX) &&
		equality.Semantic.DeepEqual(item.CAA, recordSet.Spec.CAA) &&
		equality.Semantic.DeepEqual(item.NS, recordSet.Spec.NS) &&
		equality.Semantic.DeepEqual(item.Options, recordSet.Spec.Options) &&
		equality.Semantic.DeepEqual(item.Adoption, recordSet.Spec.Adoption)
}

// authorizeCloudflareRecordSetMutation makes the status subresource the
// API-server-authoritative mutation fence. The source check rejects a current
// cache observation that no longer represents this claim; the optimistic,
// otherwise no-op patch rejects an observation superseded in the API server.
// It is intentionally called only immediately before an external write.
func (r *recordSetReconciler) authorizeCloudflareRecordSetMutation(ctx context.Context, recordSet *dnsv1alpha1.RecordSet, source *dnsv1alpha1.ZoneUnitRecordSetSpec) error {
	zoneNamespace, zoneName := recordSetZoneKey(recordSet)
	var unit dnsv1alpha1.ZoneUnit
	if err := r.Get(ctx, client.ObjectKey{Namespace: zoneNamespace, Name: zoneName}, &unit); err != nil {
		if apierrors.IsNotFound(err) {
			return apierrors.NewConflict(dnsv1alpha1.Resource("zoneunits"), zoneName, fmt.Errorf("ZoneUnit was deleted before Cloudflare DNS records could be changed"))
		}
		return err
	}
	itemIndex := -1
	if source != nil {
		itemIndex = slices.IndexFunc(unit.Spec.RecordSets, func(item dnsv1alpha1.ZoneUnitRecordSetSpec) bool {
			return item.RecordSetNamespace == source.RecordSetNamespace && item.RecordSetName == source.RecordSetName
		})
	}
	if itemIndex < 0 ||
		!cloudflareZoneUnitRecordSetSourceMatches(&unit, unit.Spec.RecordSets[itemIndex], recordSet) ||
		!equality.Semantic.DeepEqual(unit.Spec.RecordSets[itemIndex], *source) {
		return apierrors.NewConflict(dnsv1alpha1.Resource("zoneunits"), zoneName, fmt.Errorf("ZoneUnit RecordSet source changed before Cloudflare DNS records could be changed"))
	}
	unitBase := unit.DeepCopy()
	return r.Status().Patch(ctx, &unit, client.MergeFromWithOptions(unitBase, client.MergeFromWithOptimisticLock{}))
}

func mergeCloudflareRecordSetResult(left, right ctrl.Result) ctrl.Result {
	if right.Requeue {
		left.Requeue = true
	}
	if left.RequeueAfter == 0 || (right.RequeueAfter != 0 && right.RequeueAfter < left.RequeueAfter) {
		left.RequeueAfter = right.RequeueAfter
	}
	return left
}

func (r *recordSetReconciler) acceptRecordSetFromZoneUnit(ctx context.Context, recordSet *dnsv1alpha1.RecordSet, unit *dnsv1alpha1.ZoneUnit) (cloudflareRecordSetContext, bool, error) {
	var out cloudflareRecordSetContext
	zone := cloudflareZoneFromZoneUnit(unit)
	applyCloudflareZoneStatusFromZoneUnit(&zone, unit)
	var zoneClass dnsv1alpha1.ZoneClass
	if err := r.Get(ctx, client.ObjectKey{Namespace: unit.Spec.Zone.ZoneClassRef.Namespace, Name: unit.Spec.Zone.ZoneClassRef.Name}, &zoneClass); err != nil {
		if apierrors.IsNotFound(err) {
			return out, false, r.setAccepted(ctx, recordSet, metav1.ConditionFalse, "InvalidZoneClassRef", "referenced ZoneClass was not found")
		}
		return out, false, err
	}
	if zoneClass.Spec.ControllerName != r.controllerName() {
		return out, false, nil
	}
	if recordSet.Spec.Provider != zoneClass.Spec.Provider || recordSet.Spec.Provider != zone.Spec.Provider {
		return out, false, r.setAccepted(ctx, recordSet, metav1.ConditionFalse, "ProviderMismatch", "RecordSet provider does not match referenced Zone")
	}
	provider, version, err := resolveDNSProviderVersion(ctx, r.Client, recordSet.Spec.Provider)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return out, false, r.setAccepted(ctx, recordSet, metav1.ConditionFalse, "InvalidProvider", "referenced Provider was not found")
		}
		return out, false, err
	}
	if !cloudflareProviderVersionMatches(provider, version, r.providerReference()) {
		return out, false, r.setAccepted(ctx, recordSet, metav1.ConditionFalse, "InvalidProvider", "RecordSet provider is not handled by Cloudflare controller")
	}
	if !slices.Contains(version.RecordSet.SupportedTypes, recordSet.Spec.Type) {
		return out, false, r.setAccepted(ctx, recordSet, metav1.ConditionFalse, "DeniedByPolicy", "RecordSet type is not supported by the provider")
	}
	if !cloudflareConditionCurrent(zoneClass.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), zoneClass.Generation) {
		return out, false, r.setAccepted(ctx, recordSet, metav1.ConditionFalse, "ZoneClassNotAccepted", "referenced ZoneClass is not accepted")
	}
	if !cloudflareConditionCurrent(zone.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), zone.Generation) {
		return out, false, r.setAccepted(ctx, recordSet, metav1.ConditionFalse, "ZoneNotAccepted", "referenced Zone is not accepted")
	}
	identity, ok, err := r.identityForZoneClass(ctx, recordSet, &zoneClass)
	if err != nil || !ok {
		return out, false, err
	}
	if externalRef, adopting, err := cloudflareRecordSetAdoptionRef(recordSet); err != nil {
		return out, false, r.setAccepted(ctx, recordSet, metav1.ConditionFalse, "InvalidAdoption", err.Error())
	} else if adopting {
		statusData, err := cloudflareRecordSetStatusData(recordSet)
		if err != nil {
			return out, false, r.setProgrammed(ctx, recordSet, metav1.ConditionFalse, "ReconcileError", err.Error())
		}
		if len(statusData.Records) > 0 && !cloudflareRecordStatusIDsMatchAdoption(statusData.Records, externalRef.RecordIDs) {
			return out, false, r.setAccepted(ctx, recordSet, metav1.ConditionFalse, "ManagedResourceMismatch", "spec.adoption.recordIDs differs from managed Cloudflare DNS record IDs in status")
		}
	}
	zoneStatus, err := cloudflareZoneStatusData(&zone)
	if err != nil {
		return out, false, r.setProgrammed(ctx, recordSet, metav1.ConditionFalse, "ReconcileError", err.Error())
	}
	out = cloudflareRecordSetContext{
		Zone:      &zone,
		ZoneClass: &zoneClass,
		Identity:  identity,
		Provider:  provider,
		Version:   version,
		ZoneUnit:  unit,
		ZoneID:    zoneStatus.Zone.ID,
		FullName:  cloudflareFullRecordName(recordSet.Spec.Name, zone.Spec.DomainName),
	}
	if err := r.patchStatus(ctx, recordSet, false, func(status *dnsv1alpha1.RecordSetStatus) error {
		status.ObservedGeneration = recordSet.Generation
		status.Zone = &dnsv1alpha1.RecordSetZoneStatus{
			Ref: dnsv1alpha1.ObjectReference{Namespace: zone.Namespace, Name: zone.Name},
		}
		setCondition(&status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted", "RecordSet is accepted by Cloudflare policy", recordSet.Generation)
		return nil
	}); err != nil {
		return out, false, err
	}
	return out, true, nil
}

func (r *recordSetReconciler) reconcileNormal(ctx context.Context, provider RecordSetProvider, recordSet *dnsv1alpha1.RecordSet, ctxData cloudflareRecordSetContext) (ctrl.Result, error) {
	options, err := cloudflareRecordSetOptions(recordSet)
	if err != nil {
		return ctrl.Result{}, r.setAccepted(ctx, recordSet, metav1.ConditionFalse, "DeniedByPolicy", err.Error())
	}
	if message := validateCloudflareRecordSetOptions(recordSet, options); message != "" {
		return ctrl.Result{}, r.setAccepted(ctx, recordSet, metav1.ConditionFalse, "DeniedByPolicy", message)
	}
	if accepted, conflict := cloudflareZoneUnitOwnsRecordSet(recordSet, ctxData.ZoneUnit); !accepted {
		if conflict {
			return ctrl.Result{}, r.setAccepted(ctx, recordSet, metav1.ConditionFalse, "RecordSetConflict", "record identity is owned by another RecordSet")
		}
		return ctrl.Result{RequeueAfter: r.requeueAfter()}, r.setProgrammed(ctx, recordSet, metav1.ConditionFalse, "ProviderChangePending", "ZoneUnit ownership is not ready yet")
	}
	desired, err := desiredCloudflareDNSRecords(recordSet, ctxData.FullName, options)
	if err != nil {
		return ctrl.Result{}, r.setAccepted(ctx, recordSet, metav1.ConditionFalse, "DeniedByPolicy", err.Error())
	}
	updated, err := r.ensureFinalizer(ctx, recordSet)
	if err != nil {
		return ctrl.Result{}, err
	}
	if updated {
		return ctrl.Result{RequeueAfter: 0}, nil
	}
	current, err := provider.ListDNSRecords(ctx, ctxData.ZoneID, ctxData.FullName)
	if err != nil {
		return r.failProgrammedForProviderError(ctx, recordSet, err)
	}
	if result, done, err := r.reconcileAdoption(ctx, provider, recordSet, ctxData, desired, current); done || err != nil {
		return result, err
	}
	if conflict := cloudflareSameNameConflict(recordSet.Spec.Type, current); conflict != "" {
		return ctrl.Result{}, r.setProgrammed(ctx, recordSet, metav1.ConditionFalse, "ProviderConflict", conflict)
	}
	sameType := cloudflareRecordsByType(current, string(recordSet.Spec.Type))
	managedIDs := cloudflareStatusRecordIDs(recordSet)
	if len(managedIDs) == 0 {
		if len(sameType) > 0 {
			if ctxData.LegacyReceipt != nil {
				if bound, ok := legacyReceiptRecords(*ctxData.LegacyReceipt, recordSet, ctxData.FullName, current); ok {
					r.recordEvent(recordSet, corev1.EventTypeNormal, "CloudflareRecordSetLegacyOwnershipBound", fmt.Sprintf("pre-upgrade Cloudflare ownership receipt was bound to RecordSet %s", recordSet.UID))
					return ctrl.Result{}, r.setReady(ctx, recordSet, bound)
				}
			}
			return ctrl.Result{}, r.setProgrammed(ctx, recordSet, metav1.ConditionFalse, "ProviderConflict", "same Cloudflare DNS records already exist")
		}
		return r.applyCloudflareRecordSetDiff(ctx, provider, recordSet, ctxData, nil, desired)
	}
	managed, _ := cloudflareRecordsByIDs(sameType, managedIDs)
	// Only IDs durably recorded for this UID are mutable. Desired equality
	// does not establish that an external ID was created or adopted by it.
	for _, record := range sameType {
		if _, owned := managedIDs[record.ID]; !owned {
			return ctrl.Result{}, r.setProgrammed(ctx, recordSet, metav1.ConditionFalse, "ProviderConflict", "same Cloudflare DNS records already exist")
		}
	}
	return r.applyCloudflareRecordSetDiff(ctx, provider, recordSet, ctxData, managed, desired)
}

func (r *recordSetReconciler) reconcileAdoption(ctx context.Context, provider RecordSetProvider, recordSet *dnsv1alpha1.RecordSet, ctxData cloudflareRecordSetContext, desired []CloudflareDNSRecord, listed []CloudflareDNSRecord) (ctrl.Result, bool, error) {
	externalRef, adopting, err := cloudflareRecordSetAdoptionRef(recordSet)
	if err != nil || !adopting || len(cloudflareStatusRecordIDs(recordSet)) > 0 {
		return ctrl.Result{}, false, err
	}
	observed := make([]CloudflareDNSRecord, 0, len(externalRef.RecordIDs))
	for _, id := range externalRef.RecordIDs {
		record, err := provider.GetDNSRecord(ctx, ctxData.ZoneID, id)
		if err != nil {
			if isCloudflareNotFound(err) {
				return ctrl.Result{}, true, r.setProgrammed(ctx, recordSet, metav1.ConditionFalse, "ExternalResourceNotFound", "adopted Cloudflare DNS record was not found")
			}
			result, err := r.failProgrammedForProviderError(ctx, recordSet, err)
			return result, true, err
		}
		observed = append(observed, record)
	}
	if !cloudflareDNSRecordSetEqual(observed, desired) {
		return ctrl.Result{}, true, r.setProgrammed(ctx, recordSet, metav1.ConditionFalse, "ExternalResourceMismatch", "adopted Cloudflare DNS records do not match RecordSet spec")
	}
	if err := validateCloudflareDNSRecordIDs(observed, "Cloudflare DNS record id"); err != nil {
		return ctrl.Result{}, true, r.setProgrammed(ctx, recordSet, metav1.ConditionFalse, "ProviderInvalidRequest", err.Error())
	}
	externalIDs := make(map[string]struct{}, len(externalRef.RecordIDs))
	for _, id := range externalRef.RecordIDs {
		externalIDs[id] = struct{}{}
	}
	if unmanaged := cloudflareRecordsExcludingIDs(cloudflareRecordsByType(listed, string(recordSet.Spec.Type)), externalIDs); len(unmanaged) > 0 {
		return ctrl.Result{}, true, r.setProgrammed(ctx, recordSet, metav1.ConditionFalse, "ProviderConflict", "same Cloudflare DNS records already exist")
	}
	r.recordEvent(recordSet, corev1.EventTypeNormal, "ExternalResourceAdopted", "Cloudflare DNS records were adopted")
	return ctrl.Result{}, true, r.setReady(ctx, recordSet, observed)
}

func (r *recordSetReconciler) reconcileDeleteWithContext(ctx context.Context, recordSet *dnsv1alpha1.RecordSet, ctxData cloudflareRecordSetContext) (ctrl.Result, error) {
	provider, err := r.providerForIdentity(ctx, ctxData.Identity)
	if err != nil {
		_, statusErr := r.failProgrammedForProviderError(ctx, recordSet, err)
		if statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{}, err
	}
	statusData, err := cloudflareRecordSetStatusData(recordSet)
	if err != nil {
		return ctrl.Result{}, r.setProgrammed(ctx, recordSet, metav1.ConditionFalse, "ReconcileError", err.Error())
	}
	ids := cloudflareRecordStatusIDs(statusData.Records)
	if len(ids) == 0 {
		if externalRef, adopting, err := cloudflareRecordSetAdoptionRef(recordSet); err != nil {
			return ctrl.Result{}, r.setAccepted(ctx, recordSet, metav1.ConditionFalse, "InvalidAdoption", err.Error())
		} else if adopting {
			ids = externalRef.RecordIDs
		}
	}
	if len(ids) == 0 {
		observed, err := provider.ListDNSRecords(ctx, ctxData.ZoneID, ctxData.FullName)
		if err != nil {
			return r.failProgrammedForProviderError(ctx, recordSet, err)
		}
		if len(observed) == 0 {
			return ctrl.Result{}, r.removeFinalizer(ctx, recordSet)
		}
		bound, err := r.bindLegacyReceiptForDeletion(ctx, recordSet, ctxData, observed)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !bound {
			return ctrl.Result{}, r.setProgrammed(ctx, recordSet, metav1.ConditionFalse, "ProviderOwnershipNotEstablished", "Cloudflare DNS records exist but are not owned by the current RecordSet UID")
		}
		// patchStatus rebased recordSet.Status onto the bound ledger entry.
		statusData, err = cloudflareRecordSetStatusData(recordSet)
		if err != nil {
			return ctrl.Result{}, r.setProgrammed(ctx, recordSet, metav1.ConditionFalse, "ReconcileError", err.Error())
		}
		ids = cloudflareRecordStatusIDs(statusData.Records)
	}
	var deletes []CloudflareDNSRecord
	for _, id := range ids {
		observed, err := provider.GetDNSRecord(ctx, ctxData.ZoneID, id)
		if err != nil {
			if isCloudflareNotFound(err) {
				if len(statusData.Records) > 0 {
					if statusErr := r.removeRecordIDFromStatus(ctx, recordSet, id); statusErr != nil {
						return ctrl.Result{}, statusErr
					}
					return ctrl.Result{RequeueAfter: 0}, nil
				}
				continue
			}
			return r.failProgrammedForProviderError(ctx, recordSet, err)
		}
		if !cloudflareRecordMatchesDeletingRecordSet(observed, recordSet, ctxData.FullName) {
			return ctrl.Result{}, r.setProgrammed(ctx, recordSet, metav1.ConditionFalse, "ExternalResourceMismatch", "Cloudflare DNS record does not match deleting RecordSet")
		}
		deletes = append(deletes, observed)
	}
	if len(deletes) > 0 {
		if err := r.authorizeCloudflareRecordSetMutation(ctx, recordSet, ctxData.RecordSetSource); err != nil {
			return ctrl.Result{}, err
		}
		if _, err := provider.BatchDNSRecords(ctx, ctxData.ZoneID, CloudflareDNSRecordBatch{Deletes: deletes}); err != nil && !isCloudflareNotFound(err) {
			return r.failProgrammedForProviderError(ctx, recordSet, err)
		}
		for _, record := range deletes {
			r.recordEvent(recordSet, corev1.EventTypeNormal, "CloudflareRecordDeleted", fmt.Sprintf("Cloudflare DNS record %s was deleted", record.ID))
			if err := r.removeRecordIDFromStatus(ctx, recordSet, record.ID); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{RequeueAfter: 0}, r.setProgrammed(ctx, recordSet, metav1.ConditionFalse, "ProviderChangePending", "Cloudflare DNS record deletion is pending")
	}
	return ctrl.Result{}, r.removeFinalizer(ctx, recordSet)
}

type cloudflareRecordSetOperation struct {
	action  string
	current CloudflareDNSRecord
	desired CloudflareDNSRecord
}

func (r *recordSetReconciler) applyCloudflareRecordSetDiff(ctx context.Context, provider RecordSetProvider, recordSet *dnsv1alpha1.RecordSet, ctxData cloudflareRecordSetContext, current, desired []CloudflareDNSRecord) (ctrl.Result, error) {
	if cloudflareDNSRecordSetEqual(current, desired) {
		return ctrl.Result{}, r.setReady(ctx, recordSet, current)
	}
	operations := cloudflareRecordSetOperations(current, desired)
	if len(operations) == 0 {
		return ctrl.Result{}, r.setReady(ctx, recordSet, current)
	}
	batch := cloudflareDNSRecordBatchFromOperations(operations)
	if err := r.authorizeCloudflareRecordSetMutation(ctx, recordSet, ctxData.RecordSetSource); err != nil {
		return ctrl.Result{}, err
	}
	applied, err := provider.BatchDNSRecords(ctx, ctxData.ZoneID, batch)
	if err != nil {
		return r.failProgrammedForProviderError(ctx, recordSet, err)
	}
	if len(applied.Posts) != len(batch.Posts) || len(applied.Patches) != len(batch.Patches) {
		return ctrl.Result{}, r.setProgrammed(ctx, recordSet, metav1.ConditionFalse, "ProviderInvalidRequest", "Cloudflare batch response did not include every created or patched DNS record")
	}
	for _, created := range applied.Posts {
		if err := validateCloudflareID(created.ID, "Cloudflare DNS record id"); err != nil {
			return ctrl.Result{}, r.setProgrammed(ctx, recordSet, metav1.ConditionFalse, "ProviderInvalidRequest", err.Error())
		}
		r.recordEvent(recordSet, corev1.EventTypeNormal, "CloudflareRecordCreated", fmt.Sprintf("Cloudflare DNS record %s was created for type=%s name=%s", created.ID, created.Type, created.Name))
		if err := r.upsertRecordStatus(ctx, recordSet, created); err != nil {
			return ctrl.Result{}, err
		}
	}
	for _, updated := range applied.Patches {
		if err := validateCloudflareID(updated.ID, "Cloudflare DNS record id"); err != nil {
			return ctrl.Result{}, r.setProgrammed(ctx, recordSet, metav1.ConditionFalse, "ProviderInvalidRequest", err.Error())
		}
		r.recordEvent(recordSet, corev1.EventTypeNormal, "CloudflareRecordUpdated", fmt.Sprintf("Cloudflare DNS record %s was updated for type=%s name=%s", updated.ID, updated.Type, updated.Name))
		if err := r.upsertRecordStatus(ctx, recordSet, updated); err != nil {
			return ctrl.Result{}, err
		}
	}
	for _, deleted := range batch.Deletes {
		r.recordEvent(recordSet, corev1.EventTypeNormal, "CloudflareRecordDeleted", fmt.Sprintf("Cloudflare DNS record %s was deleted", deleted.ID))
		if err := r.removeRecordIDFromStatus(ctx, recordSet, deleted.ID); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: 0}, r.setProgrammed(ctx, recordSet, metav1.ConditionFalse, "ProviderChangePending", "Cloudflare DNS record change is pending")
}

func cloudflareDNSRecordBatchFromOperations(operations []cloudflareRecordSetOperation) CloudflareDNSRecordBatch {
	var batch CloudflareDNSRecordBatch
	for _, operation := range operations {
		switch operation.action {
		case "CREATE":
			batch.Posts = append(batch.Posts, operation.desired)
		case "PATCH":
			record := operation.desired
			record.ID = operation.current.ID
			batch.Patches = append(batch.Patches, record)
		case "DELETE":
			batch.Deletes = append(batch.Deletes, operation.current)
		}
	}
	return batch
}

func cloudflareRecordSetOperations(current, desired []CloudflareDNSRecord) []cloudflareRecordSetOperation {
	currentByValue := make(map[string]CloudflareDNSRecord, len(current))
	for _, record := range current {
		currentByValue[cloudflareRecordValueKey(record)] = record
	}
	desiredByValue := make(map[string]CloudflareDNSRecord, len(desired))
	for _, record := range desired {
		desiredByValue[cloudflareRecordValueKey(record)] = record
	}
	var operations []cloudflareRecordSetOperation
	for _, record := range desired {
		key := cloudflareRecordValueKey(record)
		if currentRecord, ok := currentByValue[key]; !ok {
			operations = append(operations, cloudflareRecordSetOperation{action: "CREATE", desired: record})
		} else if !cloudflareDNSRecordEqual(currentRecord, record) {
			operations = append(operations, cloudflareRecordSetOperation{action: "PATCH", current: currentRecord, desired: record})
		}
	}
	for _, record := range current {
		if _, ok := desiredByValue[cloudflareRecordValueKey(record)]; !ok {
			operations = append(operations, cloudflareRecordSetOperation{action: "DELETE", current: record})
		}
	}
	return operations
}

func cloudflareRecordValueKey(record CloudflareDNSRecord) string {
	switch dnsv1alpha1.RecordType(record.Type) {
	case dnsv1alpha1.RecordTypeMX:
		return fmt.Sprintf("%s\x00%s\x00%d", record.Type, normalizeCloudflareName(record.Content), int32PtrValue(record.Priority))
	case dnsv1alpha1.RecordTypeCAA:
		return fmt.Sprintf("%s\x00%s", record.Type, cloudflareCAAKey(record.CAA))
	case dnsv1alpha1.RecordTypeCNAME:
		return fmt.Sprintf("%s\x00%s", record.Type, normalizeCloudflareName(record.Name))
	case dnsv1alpha1.RecordTypeNS:
		return fmt.Sprintf("%s\x00%s", record.Type, normalizeCloudflareName(record.Content))
	default:
		return fmt.Sprintf("%s\x00%s", record.Type, record.Content)
	}
}

func (r *recordSetReconciler) identityForZoneClass(ctx context.Context, recordSet *dnsv1alpha1.RecordSet, zoneClass *dnsv1alpha1.ZoneClass) (*cloudflarev1alpha1.CloudflareIdentity, bool, error) {
	if zoneClass.Spec.IdentityRef.Name == "" {
		return nil, false, r.setAccepted(ctx, recordSet, metav1.ConditionFalse, "InvalidIdentityRef", "spec.identityRef.name is required")
	}
	var identity cloudflarev1alpha1.CloudflareIdentity
	if err := r.Get(ctx, client.ObjectKey{Namespace: zoneClass.Namespace, Name: zoneClass.Spec.IdentityRef.Name}, &identity); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, false, r.setAccepted(ctx, recordSet, metav1.ConditionUnknown, "IdentityNotResolved", "referenced CloudflareIdentity was not found")
		}
		return nil, false, err
	}
	accepted := meta.FindStatusCondition(identity.Status.Conditions, string(dnsv1alpha1.ConditionAccepted))
	if accepted != nil && accepted.Status == metav1.ConditionFalse && accepted.ObservedGeneration == identity.Generation {
		return nil, false, r.setAccepted(ctx, recordSet, metav1.ConditionFalse, "InvalidIdentityRef", "referenced CloudflareIdentity is not accepted")
	}
	if accepted == nil || accepted.Status != metav1.ConditionTrue || accepted.ObservedGeneration != identity.Generation {
		return nil, false, r.setAccepted(ctx, recordSet, metav1.ConditionUnknown, "IdentityNotResolved", "referenced CloudflareIdentity acceptance is not resolved")
	}
	return &identity, true, nil
}

func (r *recordSetReconciler) upsertRecordStatus(ctx context.Context, recordSet *dnsv1alpha1.RecordSet, record CloudflareDNSRecord) error {
	return r.patchCloudflareRecordSetStatus(ctx, recordSet, func(data *cloudflarev1alpha1.CloudflareRecordSetStatusData, state *cloudflarev1alpha1.CloudflareRecordSetState) {
		status := cloudflareDNSRecordStatus(record)
		foundData := false
		for index := range data.Records {
			if data.Records[index].ID == record.ID {
				data.Records[index] = status
				foundData = true
				break
			}
		}
		if !foundData {
			data.Records = append(data.Records, status)
		}

		recordState := cloudflareDNSRecordState(record)
		for index := range state.Records {
			if state.Records[index].ID == record.ID {
				state.Records[index] = recordState
				return
			}
		}
		state.Records = append(state.Records, recordState)
	})
}

func (r *recordSetReconciler) removeRecordIDFromStatus(ctx context.Context, recordSet *dnsv1alpha1.RecordSet, id string) error {
	return r.patchCloudflareRecordSetStatus(ctx, recordSet, func(data *cloudflarev1alpha1.CloudflareRecordSetStatusData, state *cloudflarev1alpha1.CloudflareRecordSetState) {
		data.Records = slices.DeleteFunc(data.Records, func(record cloudflarev1alpha1.CloudflareDNSRecordStatus) bool {
			return record.ID == id
		})
		state.Records = slices.DeleteFunc(state.Records, func(record cloudflarev1alpha1.CloudflareDNSRecordState) bool {
			return record.ID == id
		})
	})
}

func (r *recordSetReconciler) ensureFinalizer(ctx context.Context, recordSet *dnsv1alpha1.RecordSet) (bool, error) {
	return false, nil
}

func (r *recordSetReconciler) removeFinalizer(ctx context.Context, recordSet *dnsv1alpha1.RecordSet) error {
	return r.patchStatus(ctx, recordSet, true, func(status *dnsv1alpha1.RecordSetStatus) error {
		status.ObservedGeneration = recordSet.Generation
		setCondition(&status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionTrue, "Programmed", "Cloudflare DNS record deletion is complete", recordSet.Generation)
		return nil
	})
}

func (r *recordSetReconciler) providerForIdentity(ctx context.Context, identity *cloudflarev1alpha1.CloudflareIdentity) (RecordSetProvider, error) {
	if r.ProviderFactory != nil {
		provider, err := r.ProviderFactory.ProviderForIdentity(ctx, r.Client, identity)
		if err != nil {
			return nil, err
		}
		recordSetProvider, ok := provider.(RecordSetProvider)
		if !ok {
			return nil, errors.New("cloudflare provider does not support RecordSet operations")
		}
		return recordSetProvider, nil
	}
	if r.Provider != nil {
		return r.Provider, nil
	}
	return nil, errors.New("cloudflare recordset provider is required")
}

func (r *recordSetReconciler) failProgrammedForProviderError(ctx context.Context, recordSet *dnsv1alpha1.RecordSet, err error) (ctrl.Result, error) {
	reason := "ReconcileError"
	message := err.Error()
	var reasonErr *identityReasonError
	if errors.As(err, &reasonErr) {
		reason = reasonErr.reason
		message = reasonErr.message
	}
	if reason == "AccessTokenInvalid" || reason == "AccessTokenInactive" {
		reason = "ProviderAccessDenied"
	}
	if err := r.setProgrammed(ctx, recordSet, metav1.ConditionFalse, reason, message); err != nil {
		return ctrl.Result{}, err
	}
	if reason == "ProviderUnavailable" {
		return ctrl.Result{RequeueAfter: r.temporaryRetryAfter()}, nil
	}
	return ctrl.Result{}, nil
}

func (r *recordSetReconciler) recordEvent(recordSet *dnsv1alpha1.RecordSet, eventType, reason, message string) {
	if r.Recorder != nil {
		r.Recorder.Event(recordSet, eventType, reason, message)
	}
}

func (r *recordSetReconciler) requeueAfter() time.Duration {
	if r.RequeueAfter > 0 {
		return r.RequeueAfter
	}
	return defaultZoneRequeueAfter
}

func (r *recordSetReconciler) temporaryRetryAfter() time.Duration {
	if r.TemporaryRetryAfter > 0 {
		return r.TemporaryRetryAfter
	}
	return defaultCloudflareTemporaryRetry
}

func (r *recordSetReconciler) controllerName() string {
	if r.ControllerName != "" {
		return r.ControllerName
	}
	return DefaultControllerName
}

func (r *recordSetReconciler) providerReference() dnsv1alpha1.ProviderReference {
	return cloudflareProviderReference(r.ProviderName, r.ProviderVersion)
}
