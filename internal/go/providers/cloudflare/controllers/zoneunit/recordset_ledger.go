package cloudflare

import (
	"context"
	"fmt"
	"slices"

	cloudflarev1alpha1 "github.com/appthrust/dns-api/pkg/go/api/cloudflare/v1alpha1"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// legacyRecordSetReceipts returns the ZoneUnit.status.recordSets[] entries
// written before recordSetUID existed, keyed by claim namespace/name.
//
// Such an entry is the provider's own receipt that it created the listed
// record IDs for that claim name, but it cannot prove which claim incarnation
// they belong to. Callers must pair it with fresh live evidence before treating
// the records as owned; a completed or ID-less entry carries no receipt.
func legacyRecordSetReceipts(unit *dnsv1alpha1.ZoneUnit) map[string]dnsv1alpha1.ZoneUnitRecordSetStatus {
	receipts := map[string]dnsv1alpha1.ZoneUnitRecordSetStatus{}
	for _, status := range unit.Status.RecordSets {
		if status.RecordSetUID != "" || status.DeletionCompleted {
			continue
		}
		data, err := cloudflareRecordSetStatusDataFromProvider(status.Provider)
		if err != nil || len(cloudflareRecordStatusIDs(data.Records)) == 0 {
			continue
		}
		receipts[cloudflareRecordSetClaimKey(status.RecordSetNamespace, status.RecordSetName)] = status
	}
	return receipts
}

// legacyReceiptRecords returns the live records a UID-less receipt may bind to
// the current claim incarnation. Every listed record of the claim's type must
// carry an ID from the receipt, and those records must already equal the
// desired state: the migration then changes nothing in Cloudflare, it only
// records which incarnation owns the IDs. A foreign same-type record or a live
// value that differs from desired stays a conflict, because a pre-upgrade
// receipt cannot distinguish an ordinary desired-value update from a recreated
// claim inheriting a stranger's records.
func legacyReceiptRecords(receipt dnsv1alpha1.ZoneUnitRecordSetStatus, recordSet *dnsv1alpha1.RecordSet, fullName string, listed []CloudflareDNSRecord) ([]CloudflareDNSRecord, bool) {
	data, err := cloudflareRecordSetStatusDataFromProvider(receipt.Provider)
	if err != nil {
		return nil, false
	}
	ids := cloudflareRecordStatusIDs(data.Records)
	if len(ids) == 0 {
		return nil, false
	}
	options, err := cloudflareRecordSetOptions(recordSet)
	if err != nil || validateCloudflareRecordSetOptions(recordSet, options) != "" {
		return nil, false
	}
	desired, err := desiredCloudflareDNSRecords(recordSet, fullName, options)
	if err != nil {
		return nil, false
	}
	sameType := cloudflareRecordsByType(listed, string(recordSet.Spec.Type))
	if len(sameType) == 0 {
		return nil, false
	}
	for _, record := range sameType {
		if !slices.Contains(ids, record.ID) {
			return nil, false
		}
	}
	if !cloudflareDNSRecordSetEqual(sameType, desired) {
		return nil, false
	}
	return sameType, true
}

// bindLegacyReceiptForDeletion binds a pre-upgrade receipt to a deleting claim
// when the retained desired values still match the live records. Deletion is
// the one mutation a claim's own receipt must keep authorizing after the UID
// cutover; otherwise every pre-upgrade claim would be stuck in deletion. A
// minimal cleanup item without values cannot match and stays a conflict.
func (r *recordSetReconciler) bindLegacyReceiptForDeletion(ctx context.Context, recordSet *dnsv1alpha1.RecordSet, ctxData cloudflareRecordSetContext, observed []CloudflareDNSRecord) (bool, error) {
	if ctxData.LegacyReceipt == nil {
		return false, nil
	}
	bound, ok := legacyReceiptRecords(*ctxData.LegacyReceipt, recordSet, ctxData.FullName, observed)
	if !ok {
		return false, nil
	}
	if err := r.patchCloudflareRecordSetStatus(ctx, recordSet, func(data *cloudflarev1alpha1.CloudflareRecordSetStatusData, state *cloudflarev1alpha1.CloudflareRecordSetState) {
		data.Records = cloudflareDNSRecordStatuses(bound)
		state.Records = cloudflareDNSRecordStates(bound)
	}); err != nil {
		return false, err
	}
	r.recordEvent(recordSet, corev1.EventTypeNormal, "CloudflareRecordSetLegacyOwnershipBound", fmt.Sprintf("pre-upgrade Cloudflare ownership receipt was bound to deleting RecordSet %s", recordSet.UID))
	return true, nil
}

// pruneOrphanedRecordSetStatuses drops status.recordSets[] entries whose claim
// key has no spec item and which can no longer matter: cleanup completed, or
// no record ID is recorded. Without this the ledger grows with every deletion
// and pre-upgrade UID-less entries stay forever.
//
// An orphan that still records IDs is kept on purpose. Core can drop a spec
// item transiently (a claim rejected while its generation changed) and re-add
// it later; pruning that receipt would turn the provider's own records into a
// permanent ProviderConflict. Confirming absence would need one Cloudflare
// read per recorded ID on every reconcile, so the retained entry stays as the
// only evidence of a leaked record.
//
// Returns true after a write so the caller re-observes the ZoneUnit. The
// caller's unit may already trail zone-status writes from the same pass, so
// the patch is based on a fresh observation rather than that snapshot.
func (r *recordSetReconciler) pruneOrphanedRecordSetStatuses(ctx context.Context, unit *dnsv1alpha1.ZoneUnit) (bool, error) {
	var observed dnsv1alpha1.ZoneUnit
	if err := r.Get(ctx, client.ObjectKeyFromObject(unit), &observed); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	claims := make(map[string]struct{}, len(observed.Spec.RecordSets))
	for _, item := range observed.Spec.RecordSets {
		claims[cloudflareRecordSetClaimKey(item.RecordSetNamespace, item.RecordSetName)] = struct{}{}
	}
	prunable := func(status dnsv1alpha1.ZoneUnitRecordSetStatus) bool {
		if _, claimed := claims[cloudflareRecordSetClaimKey(status.RecordSetNamespace, status.RecordSetName)]; claimed {
			return false
		}
		if status.DeletionCompleted {
			return true
		}
		data, err := cloudflareRecordSetStatusDataFromProvider(status.Provider)
		return err != nil || len(cloudflareRecordStatusIDs(data.Records)) == 0
	}
	if !slices.ContainsFunc(observed.Status.RecordSets, prunable) {
		return false, nil
	}
	base := observed.DeepCopy()
	observed.Status.RecordSets = slices.DeleteFunc(observed.Status.RecordSets, prunable)
	if err := r.Status().Patch(ctx, &observed, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	return true, nil
}
