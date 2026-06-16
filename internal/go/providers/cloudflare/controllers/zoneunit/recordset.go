package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
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

var cloudflareTagPattern = regexp.MustCompile(`^([A-Za-z0-9_-]{1,32}):(.*)$`)

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
	statusByRef := map[string]dnsv1alpha1.ZoneUnitRecordSetStatus{}
	for _, status := range unit.Status.RecordSets {
		statusByRef[cloudflareRecordSetClaimKey(status.RecordSetNamespace, status.RecordSetName)] = status
	}

	var aggregate ctrl.Result
	for _, item := range unit.Spec.RecordSets {
		if !item.IsAllowed() && !item.DeletionRequested {
			continue
		}
		recordSet := cloudflareRecordSetFromZoneUnitItem(unit, item, statusByRef[cloudflareRecordSetClaimKey(item.RecordSetNamespace, item.RecordSetName)])
		ctxData, accepted, err := r.acceptRecordSetFromZoneUnit(ctx, &recordSet, unit)
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
	Zone      *dnsv1alpha1.Zone
	ZoneClass *dnsv1alpha1.ZoneClass
	Identity  *cloudflarev1alpha1.CloudflareIdentity
	Provider  *dnsv1alpha1.Provider
	Version   *dnsv1alpha1.ProviderVersion
	ZoneUnit  *dnsv1alpha1.ZoneUnit
	ZoneID    string
	FullName  string
}

func cloudflareRecordSetFromZoneUnitItem(unit *dnsv1alpha1.ZoneUnit, item dnsv1alpha1.ZoneUnitRecordSetSpec, status dnsv1alpha1.ZoneUnitRecordSetStatus) dnsv1alpha1.RecordSet {
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
	return recordSet
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
	if err := r.patchStatus(ctx, recordSet, func(status *dnsv1alpha1.RecordSetStatus) {
		status.ObservedGeneration = recordSet.Generation
		status.Zone = &dnsv1alpha1.RecordSetZoneStatus{
			Ref: dnsv1alpha1.ObjectReference{Namespace: zone.Namespace, Name: zone.Name},
		}
		setCondition(&status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted", "RecordSet is accepted by Cloudflare policy", recordSet.Generation)
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
			return ctrl.Result{}, r.setProgrammed(ctx, recordSet, metav1.ConditionFalse, "ProviderConflict", "same Cloudflare DNS records already exist")
		}
		return r.applyCloudflareRecordSetDiff(ctx, provider, recordSet, ctxData, nil, desired)
	}
	managed, _ := cloudflareRecordsByIDs(sameType, managedIDs)
	if unmanaged := cloudflareRecordsExcludingIDs(sameType, managedIDs); len(unmanaged) > 0 {
		observed := append(slices.Clone(managed), unmanaged...)
		if cloudflareDNSRecordSetEqual(observed, desired) {
			return ctrl.Result{}, r.setReady(ctx, recordSet, observed)
		}
		return ctrl.Result{}, r.setProgrammed(ctx, recordSet, metav1.ConditionFalse, "ProviderConflict", "same Cloudflare DNS records already exist")
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
		return ctrl.Result{}, r.removeFinalizer(ctx, recordSet)
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

func (r *recordSetReconciler) setReady(ctx context.Context, recordSet *dnsv1alpha1.RecordSet, observed []CloudflareDNSRecord) error {
	if err := validateCloudflareDNSRecordIDs(observed, "Cloudflare DNS record id"); err != nil {
		return r.setProgrammed(ctx, recordSet, metav1.ConditionFalse, "ProviderInvalidRequest", err.Error())
	}
	if err := r.patchCloudflareRecordSetStatus(ctx, recordSet, func(data *cloudflarev1alpha1.CloudflareRecordSetStatusData, state *cloudflarev1alpha1.CloudflareRecordSetState) {
		data.Records = cloudflareDNSRecordStatuses(observed)
		state.Records = cloudflareDNSRecordStates(observed)
	}); err != nil {
		return err
	}
	if err := r.setAccepted(ctx, recordSet, metav1.ConditionTrue, "Accepted", "RecordSet is accepted by Cloudflare policy"); err != nil {
		return err
	}
	return r.setProgrammed(ctx, recordSet, metav1.ConditionTrue, "Programmed", "Cloudflare DNS records match desired state")
}

func (r *recordSetReconciler) setAccepted(ctx context.Context, recordSet *dnsv1alpha1.RecordSet, status metav1.ConditionStatus, reason, message string) error {
	return r.patchStatus(ctx, recordSet, func(recordSetStatus *dnsv1alpha1.RecordSetStatus) {
		recordSetStatus.ObservedGeneration = recordSet.Generation
		setCondition(&recordSetStatus.Conditions, string(dnsv1alpha1.ConditionAccepted), status, reason, message, recordSet.Generation)
	})
}

func (r *recordSetReconciler) setProgrammed(ctx context.Context, recordSet *dnsv1alpha1.RecordSet, status metav1.ConditionStatus, reason, message string) error {
	return r.patchStatus(ctx, recordSet, func(recordSetStatus *dnsv1alpha1.RecordSetStatus) {
		recordSetStatus.ObservedGeneration = recordSet.Generation
		setCondition(&recordSetStatus.Conditions, string(dnsv1alpha1.ConditionProgrammed), status, reason, message, recordSet.Generation)
	})
}

func (r *recordSetReconciler) patchStatus(ctx context.Context, recordSet *dnsv1alpha1.RecordSet, mutate func(*dnsv1alpha1.RecordSetStatus)) error {
	base := recordSet.DeepCopy()
	mutate(&recordSet.Status)
	if equality.Semantic.DeepEqual(base.Status, recordSet.Status) {
		return nil
	}

	zoneNamespace, zoneName := recordSetZoneKey(recordSet)
	var unit dnsv1alpha1.ZoneUnit
	if err := r.Get(ctx, client.ObjectKey{Namespace: zoneNamespace, Name: zoneName}, &unit); err != nil {
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

func setZoneUnitProgrammedCondition(unit *dnsv1alpha1.ZoneUnit) {
	status, reason, message := zoneUnitProgrammedCondition(unit)
	setCondition(&unit.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), status, reason, message, unit.Generation)
}

func zoneUnitProgrammedCondition(unit *dnsv1alpha1.ZoneUnit) (metav1.ConditionStatus, string, string) {
	if unit.Status.Zone == nil {
		return metav1.ConditionUnknown, "Reconciling", "ZoneUnit zone status is not observed"
	}
	status := metav1.ConditionTrue
	reason := "Programmed"
	message := "ZoneUnit is programmed"
	if condition := meta.FindStatusCondition(unit.Status.Zone.Conditions, string(dnsv1alpha1.ConditionProgrammed)); condition == nil {
		status = metav1.ConditionUnknown
		reason = "Reconciling"
		message = "ZoneUnit zone Programmed condition is not observed"
	} else if condition.Status == metav1.ConditionFalse {
		return condition.Status, condition.Reason, condition.Message
	} else if condition.Status == metav1.ConditionUnknown {
		status = metav1.ConditionUnknown
		reason = condition.Reason
		message = condition.Message
	}
	for _, item := range unit.Spec.RecordSets {
		if !item.IsAllowed() && !item.DeletionRequested {
			continue
		}
		condition := zoneUnitRecordSetProgrammedCondition(unit, item)
		if condition == nil {
			if status != metav1.ConditionFalse {
				status = metav1.ConditionUnknown
				reason = "Reconciling"
				message = "ZoneUnit record set Programmed condition is not observed"
			}
			continue
		}
		if condition.Status == metav1.ConditionFalse {
			return condition.Status, condition.Reason, condition.Message
		}
		if condition.Status == metav1.ConditionUnknown && status != metav1.ConditionFalse {
			status = metav1.ConditionUnknown
			reason = condition.Reason
			message = condition.Message
		}
	}
	return status, reason, message
}

func zoneUnitRecordSetProgrammedCondition(unit *dnsv1alpha1.ZoneUnit, item dnsv1alpha1.ZoneUnitRecordSetSpec) *metav1.Condition {
	index := slices.IndexFunc(unit.Status.RecordSets, func(status dnsv1alpha1.ZoneUnitRecordSetStatus) bool {
		return status.RecordSetNamespace == item.RecordSetNamespace && status.RecordSetName == item.RecordSetName
	})
	if index < 0 {
		return nil
	}
	return meta.FindStatusCondition(unit.Status.RecordSets[index].Conditions, string(dnsv1alpha1.ConditionProgrammed))
}

func (r *recordSetReconciler) patchCloudflareRecordSetStatus(ctx context.Context, recordSet *dnsv1alpha1.RecordSet, mutate func(*cloudflarev1alpha1.CloudflareRecordSetStatusData, *cloudflarev1alpha1.CloudflareRecordSetState)) error {
	statusData, err := cloudflareRecordSetStatusData(recordSet)
	if err != nil {
		return err
	}
	statusState, err := cloudflareRecordSetState(recordSet)
	if err != nil {
		return err
	}
	mutate(&statusData, &statusState)
	rawData, err := json.Marshal(statusData)
	if err != nil {
		return err
	}
	var rawState []byte
	if len(statusState.Records) > 0 {
		rawState, err = json.Marshal(statusState)
		if err != nil {
			return err
		}
	}
	return r.patchStatus(ctx, recordSet, func(status *dnsv1alpha1.RecordSetStatus) {
		status.ObservedGeneration = recordSet.Generation
		status.Provider = &dnsv1alpha1.ProviderStatus{Data: runtime.RawExtension{Raw: rawData}}
		if len(rawState) > 0 {
			status.Provider.State = runtime.RawExtension{Raw: rawState}
		}
	})
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
	return r.patchStatus(ctx, recordSet, func(status *dnsv1alpha1.RecordSetStatus) {
		status.ObservedGeneration = recordSet.Generation
		setCondition(&status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionTrue, "Programmed", "Cloudflare DNS record deletion is complete", recordSet.Generation)
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

func cloudflareRecordSetOptions(recordSet *dnsv1alpha1.RecordSet) (cloudflarev1alpha1.CloudflareRecordSetOptions, error) {
	if len(recordSet.Spec.Options.Raw) == 0 {
		return cloudflarev1alpha1.CloudflareRecordSetOptions{}, nil
	}
	var options cloudflarev1alpha1.CloudflareRecordSetOptions
	if err := json.Unmarshal(recordSet.Spec.Options.Raw, &options); err != nil {
		return cloudflarev1alpha1.CloudflareRecordSetOptions{}, fmt.Errorf("options must match Cloudflare RecordSet schema: %w", err)
	}
	return options, nil
}

func validateCloudflareRecordSetOptions(recordSet *dnsv1alpha1.RecordSet, options cloudflarev1alpha1.CloudflareRecordSetOptions) string {
	if options.TTL != "" && options.TTL != cloudflarev1alpha1.CloudflareRecordSetTTLModeAuto {
		return "options.ttl must be Auto"
	}
	if options.TTL != "" && recordSet.Spec.TTL != nil {
		return "ttl must not be specified when cloudflare automatic ttl is used"
	}
	if options.TTL != cloudflarev1alpha1.CloudflareRecordSetTTLModeAuto {
		if recordSet.Spec.TTL == nil {
			return "ttl is required for Cloudflare records unless options.ttl is Auto"
		}
		if !cloudflareFixedTTLAllowed(*recordSet.Spec.TTL) {
			return "cloudflare fixed ttl must be between 60 and 86400 seconds"
		}
	}
	if options.Proxied != nil && *options.Proxied {
		if recordSet.Spec.Type != dnsv1alpha1.RecordTypeA && recordSet.Spec.Type != dnsv1alpha1.RecordTypeAAAA && recordSet.Spec.Type != dnsv1alpha1.RecordTypeCNAME {
			return "cloudflare proxied is supported only for A, AAAA, and CNAME records"
		}
		if options.TTL != cloudflarev1alpha1.CloudflareRecordSetTTLModeAuto || recordSet.Spec.TTL != nil {
			return "cloudflare proxied records must use automatic ttl"
		}
	}
	for _, tag := range options.Tags {
		matches := cloudflareTagPattern.FindStringSubmatch(tag)
		if matches == nil || strings.ContainsAny(matches[2], "\r\n") || len(matches[2]) > 100 {
			return "cloudflare tags must use name:value format"
		}
		if strings.HasPrefix(strings.ToLower(matches[1]), "cf-") {
			return "cloudflare tag names starting with cf- are reserved"
		}
	}
	seenTags := map[string]struct{}{}
	for _, tag := range options.Tags {
		matches := cloudflareTagPattern.FindStringSubmatch(tag)
		if matches == nil {
			continue
		}
		key := strings.ToLower(matches[1]) + ":" + matches[2]
		if _, ok := seenTags[key]; ok {
			return "cloudflare tags must not contain duplicate name:value pairs"
		}
		seenTags[key] = struct{}{}
	}
	return ""
}

func desiredCloudflareDNSRecords(recordSet *dnsv1alpha1.RecordSet, fullName string, options cloudflarev1alpha1.CloudflareRecordSetOptions) ([]CloudflareDNSRecord, error) {
	ttl := int32(1)
	if options.TTL != cloudflarev1alpha1.CloudflareRecordSetTTLModeAuto {
		if recordSet.Spec.TTL == nil {
			return nil, errors.New("ttl is required for Cloudflare records unless options.ttl is Auto")
		}
		if !cloudflareFixedTTLAllowed(*recordSet.Spec.TTL) {
			return nil, errors.New("cloudflare fixed ttl must be between 60 and 86400 seconds")
		}
		ttl = *recordSet.Spec.TTL
	}
	base := CloudflareDNSRecord{
		Type:    string(recordSet.Spec.Type),
		Name:    fullName,
		TTL:     &ttl,
		Comment: options.Comment,
		Tags:    slices.Clone(options.Tags),
	}
	if recordSet.Spec.Type == dnsv1alpha1.RecordTypeA || recordSet.Spec.Type == dnsv1alpha1.RecordTypeAAAA || recordSet.Spec.Type == dnsv1alpha1.RecordTypeCNAME {
		proxied := false
		if options.Proxied != nil {
			proxied = *options.Proxied
		}
		base.Proxied = &proxied
	}
	var records []CloudflareDNSRecord
	switch recordSet.Spec.Type {
	case dnsv1alpha1.RecordTypeA:
		for _, address := range recordSet.Spec.A.Addresses {
			record := base
			record.Content = address
			records = append(records, record)
		}
	case dnsv1alpha1.RecordTypeAAAA:
		for _, address := range recordSet.Spec.AAAA.Addresses {
			record := base
			record.Content = address
			records = append(records, record)
		}
	case dnsv1alpha1.RecordTypeTXT:
		for _, value := range recordSet.Spec.TXT.Values {
			record := base
			record.Content = value
			records = append(records, record)
		}
	case dnsv1alpha1.RecordTypeCNAME:
		record := base
		record.Content = recordSet.Spec.CNAME.Target
		records = append(records, record)
	case dnsv1alpha1.RecordTypeMX:
		for _, item := range recordSet.Spec.MX.Records {
			record := base
			record.Content = item.Exchange
			record.Priority = &item.Preference
			records = append(records, record)
		}
	case dnsv1alpha1.RecordTypeCAA:
		for _, item := range recordSet.Spec.CAA.Records {
			record := base
			record.Content = item.Value
			record.CAA = &CloudflareCAAData{Flags: item.Flags, Tag: item.Tag, Value: item.Value}
			records = append(records, record)
		}
	case dnsv1alpha1.RecordTypeNS:
		for _, nameServer := range recordSet.Spec.NS.NameServers {
			record := base
			record.Content = nameServer
			records = append(records, record)
		}
	default:
		return nil, errors.New("Cloudflare supports A, AAAA, TXT, CNAME, MX, CAA, and delegated NS records")
	}
	slices.SortFunc(records, compareCloudflareDNSRecord)
	return records, nil
}

func cloudflareFixedTTLAllowed(ttl int32) bool {
	return ttl >= cloudflareFixedTTLMin && ttl <= cloudflareFixedTTLMax
}

func cloudflareDNSRecordSetEqual(a, b []CloudflareDNSRecord) bool {
	left := slices.Clone(a)
	right := slices.Clone(b)
	slices.SortFunc(left, compareCloudflareDNSRecord)
	slices.SortFunc(right, compareCloudflareDNSRecord)
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !cloudflareDNSRecordEqual(left[index], right[index]) {
			return false
		}
	}
	return true
}

func cloudflareDNSRecordEqual(a, b CloudflareDNSRecord) bool {
	return a.Type == b.Type &&
		normalizeCloudflareName(a.Name) == normalizeCloudflareName(b.Name) &&
		(a.Type == string(dnsv1alpha1.RecordTypeCAA) || a.Content == b.Content) &&
		int32PtrValue(a.Priority) == int32PtrValue(b.Priority) &&
		int32PtrValue(a.TTL) == int32PtrValue(b.TTL) &&
		boolPtrValue(a.Proxied) == boolPtrValue(b.Proxied) &&
		a.Comment == b.Comment &&
		slices.Equal(cloudflareSortedTags(a.Tags), cloudflareSortedTags(b.Tags)) &&
		cloudflareCAAEqual(a.CAA, b.CAA)
}

func compareCloudflareDNSRecord(a, b CloudflareDNSRecord) int {
	for _, pair := range [][2]string{
		{a.Type, b.Type},
		{normalizeCloudflareName(a.Name), normalizeCloudflareName(b.Name)},
		{a.Content, b.Content},
		{fmt.Sprintf("%05d", int32PtrValue(a.Priority)), fmt.Sprintf("%05d", int32PtrValue(b.Priority))},
		{cloudflareCAAKey(a.CAA), cloudflareCAAKey(b.CAA)},
	} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	return 0
}

func cloudflareCAAEqual(a, b *CloudflareCAAData) bool {
	return cloudflareCAAKey(a) == cloudflareCAAKey(b)
}

func cloudflareCAAKey(value *CloudflareCAAData) string {
	if value == nil {
		return ""
	}
	return fmt.Sprintf("%d\x00%s\x00%s", value.Flags, value.Tag, value.Value)
}

func cloudflareSortedTags(tags []string) []string {
	out := slices.Clone(tags)
	slices.Sort(out)
	return out
}

func cloudflareSameNameConflict(recordType dnsv1alpha1.RecordType, records []CloudflareDNSRecord) string {
	for _, record := range records {
		currentType := dnsv1alpha1.RecordType(record.Type)
		if currentType == recordType {
			continue
		}
		if recordType == dnsv1alpha1.RecordTypeCNAME || currentType == dnsv1alpha1.RecordTypeCNAME {
			return "Cloudflare DNS record conflicts with same-name CNAME exclusivity"
		}
		if recordType == dnsv1alpha1.RecordTypeNS || currentType == dnsv1alpha1.RecordTypeNS {
			return "Cloudflare DNS record conflicts with same-name delegated NS exclusivity"
		}
	}
	return ""
}

func cloudflareRecordsByType(records []CloudflareDNSRecord, recordType string) []CloudflareDNSRecord {
	var out []CloudflareDNSRecord
	for _, record := range records {
		if record.Type == recordType {
			out = append(out, record)
		}
	}
	return out
}

func cloudflareRecordsByIDs(records []CloudflareDNSRecord, ids map[string]struct{}) ([]CloudflareDNSRecord, []string) {
	var out []CloudflareDNSRecord
	found := make(map[string]struct{}, len(ids))
	for _, record := range records {
		if _, ok := ids[record.ID]; ok {
			out = append(out, record)
			found[record.ID] = struct{}{}
		}
	}
	var missing []string
	for id := range ids {
		if _, ok := found[id]; !ok {
			missing = append(missing, id)
		}
	}
	return out, missing
}

func cloudflareRecordsExcludingIDs(records []CloudflareDNSRecord, ids map[string]struct{}) []CloudflareDNSRecord {
	var out []CloudflareDNSRecord
	for _, record := range records {
		if _, ok := ids[record.ID]; !ok {
			out = append(out, record)
		}
	}
	return out
}

func cloudflareRecordsHaveIDs(records []CloudflareDNSRecord, ids map[string]struct{}) bool {
	if len(records) != len(ids) {
		return false
	}
	for _, record := range records {
		if _, ok := ids[record.ID]; !ok {
			return false
		}
	}
	return true
}

func cloudflareStatusRecordIDs(recordSet *dnsv1alpha1.RecordSet) map[string]struct{} {
	statusData, err := cloudflareRecordSetStatusData(recordSet)
	if err != nil {
		return nil
	}
	out := make(map[string]struct{}, len(statusData.Records))
	for _, record := range statusData.Records {
		if record.ID != "" {
			out[record.ID] = struct{}{}
		}
	}
	return out
}

func cloudflareDNSRecordStatuses(records []CloudflareDNSRecord) []cloudflarev1alpha1.CloudflareDNSRecordStatus {
	statuses := make([]cloudflarev1alpha1.CloudflareDNSRecordStatus, 0, len(records))
	for _, record := range records {
		statuses = append(statuses, cloudflareDNSRecordStatus(record))
	}
	return statuses
}

func cloudflareDNSRecordStatus(record CloudflareDNSRecord) cloudflarev1alpha1.CloudflareDNSRecordStatus {
	return cloudflarev1alpha1.CloudflareDNSRecordStatus{
		ID: record.ID,
	}
}

func cloudflareDNSRecordStates(records []CloudflareDNSRecord) []cloudflarev1alpha1.CloudflareDNSRecordState {
	states := make([]cloudflarev1alpha1.CloudflareDNSRecordState, 0, len(records))
	for _, record := range records {
		states = append(states, cloudflareDNSRecordState(record))
	}
	return states
}

func cloudflareDNSRecordState(record CloudflareDNSRecord) cloudflarev1alpha1.CloudflareDNSRecordState {
	return cloudflarev1alpha1.CloudflareDNSRecordState{
		ID:        record.ID,
		Type:      record.Type,
		Name:      record.Name,
		Content:   record.Content,
		Priority:  record.Priority,
		TTL:       record.TTL,
		Proxied:   record.Proxied,
		Proxiable: record.Proxiable,
		Comment:   record.Comment,
		Tags:      slices.Clone(record.Tags),
	}
}

func cloudflareRecordStatusIDs(records []cloudflarev1alpha1.CloudflareDNSRecordStatus) []string {
	ids := make([]string, 0, len(records))
	for _, record := range records {
		if record.ID != "" {
			ids = append(ids, record.ID)
		}
	}
	return ids
}

func cloudflareRecordStatusIDsMatchAdoption(statuses []cloudflarev1alpha1.CloudflareDNSRecordStatus, adoptionIDs []string) bool {
	statusIDs := cloudflareRecordStatusIDs(statuses)
	if len(statusIDs) != len(adoptionIDs) {
		return false
	}
	expected := make(map[string]struct{}, len(adoptionIDs))
	for _, id := range adoptionIDs {
		expected[id] = struct{}{}
	}
	for _, id := range statusIDs {
		if _, ok := expected[id]; !ok {
			return false
		}
	}
	return true
}

func cloudflareRecordMatchesDeletingRecordSet(record CloudflareDNSRecord, recordSet *dnsv1alpha1.RecordSet, fullName string) bool {
	return record.Type == string(recordSet.Spec.Type) &&
		normalizeCloudflareName(record.Name) == normalizeCloudflareName(fullName)
}

func cloudflareRecordSetStatusData(recordSet *dnsv1alpha1.RecordSet) (cloudflarev1alpha1.CloudflareRecordSetStatusData, error) {
	if recordSet.Status.Provider == nil || len(recordSet.Status.Provider.Data.Raw) == 0 {
		return cloudflarev1alpha1.CloudflareRecordSetStatusData{}, nil
	}
	var data cloudflarev1alpha1.CloudflareRecordSetStatusData
	if err := json.Unmarshal(recordSet.Status.Provider.Data.Raw, &data); err != nil {
		return cloudflarev1alpha1.CloudflareRecordSetStatusData{}, fmt.Errorf("RecordSet status.provider.data must match Cloudflare schema: %w", err)
	}
	if len(data.Records) > 0 {
		if err := validateCloudflareRecordStatusIDs(data.Records, "RecordSet status.provider.data.records.id"); err != nil {
			return cloudflarev1alpha1.CloudflareRecordSetStatusData{}, err
		}
	}
	return data, nil
}

func cloudflareRecordSetState(recordSet *dnsv1alpha1.RecordSet) (cloudflarev1alpha1.CloudflareRecordSetState, error) {
	if recordSet.Status.Provider == nil || len(recordSet.Status.Provider.State.Raw) == 0 {
		return cloudflarev1alpha1.CloudflareRecordSetState{}, nil
	}
	var state cloudflarev1alpha1.CloudflareRecordSetState
	if err := json.Unmarshal(recordSet.Status.Provider.State.Raw, &state); err != nil {
		return cloudflarev1alpha1.CloudflareRecordSetState{}, fmt.Errorf("RecordSet status.provider.state must match Cloudflare schema: %w", err)
	}
	return state, nil
}

func cloudflareRecordSetAdoptionRef(recordSet *dnsv1alpha1.RecordSet) (cloudflarev1alpha1.CloudflareRecordSetAdoption, bool, error) {
	if len(recordSet.Spec.Adoption.Raw) == 0 {
		return cloudflarev1alpha1.CloudflareRecordSetAdoption{}, false, nil
	}
	var externalRef cloudflarev1alpha1.CloudflareRecordSetAdoption
	if err := json.Unmarshal(recordSet.Spec.Adoption.Raw, &externalRef); err != nil {
		return cloudflarev1alpha1.CloudflareRecordSetAdoption{}, true, fmt.Errorf("adoption must be an object with recordIDs: %w", err)
	}
	if len(externalRef.RecordIDs) == 0 {
		return cloudflarev1alpha1.CloudflareRecordSetAdoption{}, true, errors.New("adoption.recordIDs must not be empty")
	}
	if err := validateCloudflareUniqueIDs(externalRef.RecordIDs, "adoption.recordIDs"); err != nil {
		return cloudflarev1alpha1.CloudflareRecordSetAdoption{}, true, err
	}
	return externalRef, true, nil
}

func cloudflareFullRecordName(ownerName, domainName string) string {
	switch ownerName {
	case "@":
		return domainName
	case "*":
		return "*." + domainName
	default:
		return ownerName + "." + domainName
	}
}

func cloudflareZoneUnitOwnsRecordSet(recordSet *dnsv1alpha1.RecordSet, unit *dnsv1alpha1.ZoneUnit) (bool, bool) {
	if unit == nil {
		return false, false
	}
	refKey := cloudflareRecordSetClaimKey(recordSet.Namespace, recordSet.Name)
	for _, item := range unit.Spec.RecordSets {
		if cloudflareRecordSetClaimKey(item.RecordSetNamespace, item.RecordSetName) == refKey {
			if item.Name == recordSet.Spec.Name && item.Type == recordSet.Spec.Type {
				return true, false
			}
			return false, true
		}
	}
	for _, item := range unit.Spec.RecordSets {
		if item.Name != recordSet.Spec.Name {
			continue
		}
		if item.Type == recordSet.Spec.Type || item.Type == dnsv1alpha1.RecordTypeCNAME || recordSet.Spec.Type == dnsv1alpha1.RecordTypeCNAME {
			return false, true
		}
	}
	return false, false
}

func cloudflareRecordSetClaimKey(namespace, name string) string {
	return namespace + "\x00" + name
}

func normalizeCloudflareName(value string) string {
	return strings.TrimSuffix(value, ".")
}

func recordSetZoneKey(recordSet *dnsv1alpha1.RecordSet) (string, string) {
	namespace := recordSet.Namespace
	if recordSet.Spec.ZoneRef.Namespace != nil && *recordSet.Spec.ZoneRef.Namespace != "" {
		namespace = *recordSet.Spec.ZoneRef.Namespace
	}
	return namespace, recordSet.Spec.ZoneRef.Name
}

func fullRecordNamePatternMatch(pattern, recordName string) (bool, error) {
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return false, err
	}
	match := compiled.FindStringIndex(recordName)
	return match != nil && match[0] == 0 && match[1] == len(recordName), nil
}

func int32PtrValue(value *int32) int32 {
	if value == nil {
		return 0
	}
	return *value
}

func boolPtrValue(value *bool) bool {
	return value != nil && *value
}
