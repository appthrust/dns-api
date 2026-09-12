package route53

import (
	"context"
	"slices"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// legacyRecordSetReceipts returns the ZoneUnit.status.recordSets[] entries
// written before recordSetUID existed, keyed by claim namespace/name.
//
// Such an entry is the provider's own receipt that it programmed the record for
// that claim name, but it cannot prove which claim incarnation it belongs to.
// Callers must therefore pair it with fresh live evidence before treating the
// record as owned; a completed or payload-less entry carries no receipt at all.
func legacyRecordSetReceipts(unit *dnsv1alpha1.ZoneUnit) map[string]dnsv1alpha1.ZoneUnitRecordSetStatus {
	receipts := map[string]dnsv1alpha1.ZoneUnitRecordSetStatus{}
	for _, status := range unit.Status.RecordSets {
		if status.RecordSetUID != "" || status.DeletionCompleted || !providerStatusHasPayload(status.Provider) {
			continue
		}
		receipts[recordSetClaimKey(status.RecordSetNamespace, status.RecordSetName)] = status
	}
	return receipts
}

// legacyReceiptBindsRecordSet reports whether a UID-less receipt may be bound
// to the current claim incarnation. The receipt must name exactly the record
// identity the claim desires, and the live record must already equal the
// desired state: the migration then changes nothing in Route 53, it only
// records which incarnation owns the record. A live record that differs from
// desired stays a conflict, because a pre-upgrade receipt cannot distinguish an
// ordinary desired-value update from a recreated claim inheriting a stranger's
// record, and neither may be mutated on name/generation evidence alone.
func legacyReceiptBindsRecordSet(receipt dnsv1alpha1.ZoneUnitRecordSetStatus, desired, existing RecordSetResource) bool {
	data, err := route53RecordSetStatusDataFromProvider(receipt.Provider)
	if err != nil {
		return false
	}
	return normalizeHostedZoneID(data.HostedZoneID) == normalizeHostedZoneID(desired.HostedZoneID) &&
		normalizeRoute53RecordName(data.RecordName) == normalizeRoute53RecordName(desired.Name) &&
		data.RecordType == string(desired.Type) &&
		route53RecordSetEqual(existing, desired)
}

// pruneOrphanedRecordSetStatuses drops status.recordSets[] entries whose claim
// key has no spec item and which can no longer matter: cleanup completed, or
// the record they name is absent from the hosted zone. Without this the ledger
// grows with every deletion and pre-upgrade UID-less entries stay forever.
//
// An orphan whose record is still live is kept on purpose. Core can drop a
// spec item transiently (a claim rejected while its generation changed) and
// re-add it later; pruning that receipt would turn the provider's own record
// into a permanent ProviderConflict. The retained entry is also the only
// evidence of a leaked record.
//
// Returns true after a write so the caller re-observes the ZoneUnit instead of
// patching a stale snapshot. The unit is mutated only when a write is issued.
func (r *ZoneReconciler) pruneOrphanedRecordSetStatuses(ctx context.Context, unit *dnsv1alpha1.ZoneUnit, current map[string]RecordSetResource) (bool, error) {
	claims := make(map[string]struct{}, len(unit.Spec.RecordSets))
	for _, item := range unit.Spec.RecordSets {
		claims[recordSetClaimKey(item.RecordSetNamespace, item.RecordSetName)] = struct{}{}
	}
	prunable := func(status dnsv1alpha1.ZoneUnitRecordSetStatus) bool {
		if _, claimed := claims[recordSetClaimKey(status.RecordSetNamespace, status.RecordSetName)]; claimed {
			return false
		}
		if status.DeletionCompleted {
			return true
		}
		data, err := route53RecordSetStatusDataFromProvider(status.Provider)
		if err != nil || data.RecordType == "" || data.RecordName == "" {
			return true
		}
		_, live := current[recordSetIdentity(dnsv1alpha1.RecordType(data.RecordType), data.RecordName).key()]
		return !live
	}
	if !slices.ContainsFunc(unit.Status.RecordSets, prunable) {
		return false, nil
	}
	base := unit.DeepCopy()
	unit.Status.RecordSets = slices.DeleteFunc(unit.Status.RecordSets, prunable)
	if err := r.Status().Patch(ctx, unit, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	return true, nil
}
