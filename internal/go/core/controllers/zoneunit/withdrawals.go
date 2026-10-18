package zoneunit

import (
	"context"
	"slices"
	"strings"

	"github.com/appthrust/dns-api/internal/go/core/declaration"
	"github.com/appthrust/dns-api/internal/go/core/providercontract"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// +kubebuilder:rbac:groups=dns.appthrust.io,resources=recordwithdrawals;recordwithdrawalwaivers,verbs=create;delete;get;list;watch;patch;update
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=recordwithdrawals/status;recordwithdrawalwaivers/status,verbs=get;patch;update
func (r *ZoneUnitCompositionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	claims, err := r.withdrawalsForZone(ctx, req.NamespacedName)
	if err != nil {
		return ctrl.Result{}, err
	}
	result, err := r.reconcileZone(ctx, req, claims)
	if err != nil {
		return result, err
	}
	return result, r.reconcileWithdrawals(ctx, req.NamespacedName, claims)
}

func withdrawalZoneKey(claim *dnsv1alpha1.RecordWithdrawal) client.ObjectKey {
	namespace := claim.Namespace
	if claim.Spec.ZoneRef.Namespace != nil && *claim.Spec.ZoneRef.Namespace != "" {
		namespace = *claim.Spec.ZoneRef.Namespace
	}
	return client.ObjectKey{Namespace: namespace, Name: claim.Spec.ZoneRef.Name}
}

func (r *ZoneUnitCompositionReconciler) withdrawalsForZone(ctx context.Context, key client.ObjectKey) ([]dnsv1alpha1.RecordWithdrawal, error) {
	var list dnsv1alpha1.RecordWithdrawalList
	if err := r.List(ctx, &list); err != nil {
		return nil, err
	}
	claims := make([]dnsv1alpha1.RecordWithdrawal, 0)
	for _, claim := range list.Items {
		if withdrawalZoneKey(&claim) == key {
			claims = append(claims, claim)
		}
	}
	slices.SortFunc(claims, func(a, b dnsv1alpha1.RecordWithdrawal) int {
		return strings.Compare(recordSetClaimKey(a.Namespace, a.Name), recordSetClaimKey(b.Namespace, b.Name))
	})
	return claims, nil
}

func (r *ZoneUnitCompositionReconciler) mapWithdrawalToZone(_ context.Context, obj client.Object) []reconcile.Request {
	claim := obj.(*dnsv1alpha1.RecordWithdrawal)
	return []reconcile.Request{{NamespacedName: withdrawalZoneKey(claim)}}
}

func (r *ZoneUnitCompositionReconciler) mapWaiverToZone(ctx context.Context, obj client.Object) []reconcile.Request {
	waiver := obj.(*dnsv1alpha1.RecordWithdrawalWaiver)
	var claim dnsv1alpha1.RecordWithdrawal
	if err := r.Get(ctx, client.ObjectKey{Namespace: waiver.Spec.WithdrawalRef.Namespace, Name: waiver.Spec.WithdrawalRef.Name}, &claim); err != nil {
		return nil
	}
	return []reconcile.Request{{NamespacedName: withdrawalZoneKey(&claim)}}
}

func (r *ZoneUnitCompositionReconciler) reconcileWithdrawals(ctx context.Context, key client.ObjectKey, claims []dnsv1alpha1.RecordWithdrawal) error {
	var zone dnsv1alpha1.Zone
	var unit dnsv1alpha1.ZoneUnit
	zoneErr := r.Get(ctx, key, &zone)
	if err := client.IgnoreNotFound(zoneErr); err != nil {
		return err
	}
	unitErr := r.Get(ctx, key, &unit)
	if err := client.IgnoreNotFound(unitErr); err != nil {
		return err
	}
	var version *dnsv1alpha1.ProviderVersion
	bindingValid := false
	zoneAllowed := true
	if zoneErr == nil {
		class, found, err := r.zoneClassForZone(ctx, &zone)
		if err != nil {
			return err
		}
		if found && class.Spec.Provider == zone.Spec.Provider {
			allowed, err := declaration.ZoneAllowedByClass(ctx, r.Client, class, zone.Namespace)
			if err != nil {
				return err
			}
			zoneAllowed = allowed
			var provider dnsv1alpha1.Provider
			if err := r.Get(ctx, client.ObjectKey{Name: class.Spec.Provider.Name}, &provider); err != nil {
				if client.IgnoreNotFound(err) != nil {
					return err
				}
			} else {
				input, served := providercontract.ServedVersion(&provider, zone.Spec.Provider.Version)
				storage, stored := providercontract.StorageVersion(&provider)
				if served && stored && input.RecordWithdrawal != nil && input.RecordWithdrawal.Supported {
					version = storage
				}
				bindingValid = allowed && served && stored && unitErr == nil && unit.DeletionTimestamp.IsZero() &&
					unit.Spec.Zone.Ref == (dnsv1alpha1.ObjectReference{Namespace: zone.Namespace, Name: zone.Name}) &&
					unit.Spec.Zone.ObservedGeneration == zone.Generation && unit.Spec.Zone.DomainName == zone.Spec.DomainName &&
					unit.Spec.Zone.ZoneClassRef == (dnsv1alpha1.ObjectReference{Namespace: class.Namespace, Name: class.Name}) &&
					unit.Spec.Zone.ZoneClassUID == class.UID && unit.Spec.Zone.ZoneClassObservedGeneration == class.Generation &&
					unit.Spec.Provider == (dnsv1alpha1.ProviderReference{Name: provider.Name, Version: storage.Name})
			}
		}
	}
	var waivers dnsv1alpha1.RecordWithdrawalWaiverList
	if len(claims) > 0 {
		if err := r.List(ctx, &waivers); err != nil {
			return err
		}
		slices.SortFunc(waivers.Items, func(a, b dnsv1alpha1.RecordWithdrawalWaiver) int {
			return strings.Compare(recordSetClaimKey(a.Namespace, a.Name), recordSetClaimKey(b.Namespace, b.Name))
		})
	}
	items := make([]dnsv1alpha1.ZoneUnitWithdrawal, 0, len(claims))
	if len(claims) > 256 {
		// Refuse the new inventory as a whole, retaining previous identities and
		// provider cache history instead of truncating obligations.
		items = append(items, unit.Spec.Withdrawals...)
		for i := range items {
			items[i].Allowed = false
			items[i].Waiver = nil
		}
	}
	for i := range claims {
		claim := &claims[i]
		reason, message := "Accepted", "Withdrawal binding, grants and provider capability are accepted"
		if !zoneAllowed {
			reason, message = "NotAllowed", "Zone namespace is not allowed by the referenced ZoneClass"
		} else if !bindingValid || len(claims) > 256 {
			reason, message = "InvalidWithdrawal", "Withdrawal Zone binding is unavailable or exceeds the 256 claim limit"
		} else if version == nil || version.RecordWithdrawal == nil || !version.RecordWithdrawal.Supported {
			reason, message = "UnsupportedProvider", "The bound Provider version does not support record withdrawal"
		} else if err := declaration.ValidateWithdrawal(claim); err != nil {
			reason, message = "InvalidWithdrawal", err.Error()
		} else if len(claim.Spec.Name)+len(zone.Spec.DomainName)+1 > 253 && claim.Spec.Name != "@" {
			reason, message = "InvalidWithdrawal", "Withdrawal owner and Zone domain exceed the maximum DNS name length"
		} else if allowed, detail := declaration.WithdrawalAllowedByZone(ctx, r.Client, claim, &zone); !allowed {
			reason, message = "NotAllowed", detail
		}
		item := dnsv1alpha1.ZoneUnitWithdrawal{ClaimNamespace: claim.Namespace, ClaimName: claim.Name, Claim: withdrawalObjectRef(claim), ObservedGeneration: claim.Generation, Name: claim.Spec.Name, Address: claim.Spec.Address, Allowed: reason == "Accepted"}
		for j := range waivers.Items {
			waiver := &waivers.Items[j]
			if waiver.Spec.WithdrawalRef.Namespace != claim.Namespace || waiver.Spec.WithdrawalRef.Name != claim.Name {
				continue
			}
			valid := declaration.WithdrawalWaiverMatches(waiver, claim)
			if valid && item.Allowed && item.Waiver == nil {
				item.Waiver = &dnsv1alpha1.ZoneUnitWithdrawalWaiver{Name: waiver.Name, UID: waiver.UID, Digest: dnsv1alpha1.WithdrawalWaiverDigest(waiver.Spec), ReservationRef: waiver.Spec.ReservationRef}
			}
		}
		if len(claims) <= 256 && declaration.ValidateWithdrawal(claim) == nil {
			items = append(items, item)
		}
		status := dnsv1alpha1.RecordWithdrawalStatus{ObservedGeneration: claim.Generation}
		if zoneErr == nil {
			ref := withdrawalObjectRef(&zone)
			status.Zone = &ref
		}
		if unitErr == nil {
			ref := withdrawalObjectRef(&unit)
			status.Unit = &ref
			status.ProviderRef = &unit.Spec.Provider
		}
		accepted := metav1.ConditionFalse
		if item.Allowed {
			accepted = metav1.ConditionTrue
		}
		status.Conditions = append(status.Conditions, metav1.Condition{Type: string(dnsv1alpha1.ConditionAccepted), Status: accepted, Reason: reason, Message: message, ObservedGeneration: claim.Generation})
		if item.Allowed && unitErr == nil {
			projectWithdrawalResult(&status, claim, item, &unit)
		}
		if meta.FindStatusCondition(status.Conditions, string(dnsv1alpha1.ConditionWithdrawn)) == nil {
			status.Conditions = append(status.Conditions,
				metav1.Condition{Type: string(dnsv1alpha1.ConditionWithdrawn), Status: metav1.ConditionUnknown, Reason: "DNSProbeFailed", Message: "No current identity-bound withdrawal observation is available", ObservedGeneration: claim.Generation},
				metav1.Condition{Type: string(dnsv1alpha1.ConditionReady), Status: metav1.ConditionFalse, Reason: "DNSProbeFailed", Message: "No current identity-bound withdrawal observation is available", ObservedGeneration: claim.Generation})
		}
		if err := r.setWithdrawalStatus(ctx, claim, status); err != nil {
			return err
		}
	}
	if unitErr != nil || !unit.DeletionTimestamp.IsZero() || equality.Semantic.DeepEqual(unit.Spec.Withdrawals, items) || len(unit.Spec.Withdrawals) == 0 && len(items) == 0 {
		return nil
	}
	base := unit.DeepCopy()
	unit.Spec.Withdrawals = items
	return r.Patch(ctx, &unit, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
}

func withdrawalObjectRef(obj client.Object) dnsv1alpha1.WithdrawalObjectReference {
	return dnsv1alpha1.WithdrawalObjectReference{Namespace: obj.GetNamespace(), Name: obj.GetName(), UID: obj.GetUID()}
}

func projectWithdrawalResult(status *dnsv1alpha1.RecordWithdrawalStatus, claim *dnsv1alpha1.RecordWithdrawal, desired dnsv1alpha1.ZoneUnitWithdrawal, unit *dnsv1alpha1.ZoneUnit) {
	// Both spec and result must refer to this claim incarnation and current policy
	// admission. A cached result may never cross a changed waiver or desired item.
	if unit.Status.ObservedGeneration != unit.Generation || unit.Spec.Zone.ZoneClassUID == "" || unit.Spec.Zone.ZoneClassObservedGeneration < 1 {
		return
	}
	if !slices.ContainsFunc(unit.Spec.Withdrawals, func(item dnsv1alpha1.ZoneUnitWithdrawal) bool { return equality.Semantic.DeepEqual(item, desired) }) {
		return
	}
	for i := range unit.Status.Withdrawals {
		result := &unit.Status.Withdrawals[i]
		if result.Claim != desired.Claim || result.ClaimNamespace != claim.Namespace || result.ClaimName != claim.Name || result.ObservedGeneration != claim.Generation {
			continue
		}
		withdrawn := conditionAtGeneration(result.Conditions, string(dnsv1alpha1.ConditionWithdrawn), claim.Generation)
		ready := conditionAtGeneration(result.Conditions, string(dnsv1alpha1.ConditionReady), claim.Generation)
		if withdrawn == nil || ready == nil {
			return
		}
		status.PolicyFingerprint, status.CheckedNames, status.RequiredViews, status.SampledWildcard = result.PolicyFingerprint, slices.Clone(result.CheckedNames), slices.Clone(result.RequiredViews), result.SampledWildcard
		status.AbsentSince, status.NotBefore, status.LastProbeTime, status.NextProbeTime, status.VerifiedAt = result.AbsentSince.DeepCopy(), result.NotBefore.DeepCopy(), result.LastProbeTime.DeepCopy(), result.NextProbeTime.DeepCopy(), result.VerifiedAt.DeepCopy()
		status.ResultDigest = result.ResultDigest
		if result.Provider != nil {
			status.Provider = &dnsv1alpha1.ProviderStatus{Data: *result.Provider.Data.DeepCopy()}
		}
		status.Conditions = append(status.Conditions, *withdrawn, *ready)
		if desired.Waiver != nil && result.WaiverRef != nil && result.WaiverRef.Name == desired.Waiver.Name && result.WaiverRef.UID == desired.Waiver.UID && result.WaiverRef.Digest == desired.Waiver.Digest && result.WaiverRef.ReservationRef == desired.Waiver.ReservationRef {
			if override := conditionAtGeneration(result.Conditions, string(dnsv1alpha1.ConditionOverrideAccepted), claim.Generation); override != nil && override.Status == metav1.ConditionTrue && override.Reason == "OperatorAuthorizedRelease" {
				status.WaiverRef = result.WaiverRef.DeepCopy()
				status.Conditions = append(status.Conditions, *override)
			}
		}
		return
	}
}

func (r *ZoneUnitCompositionReconciler) setWithdrawalStatus(ctx context.Context, claim *dnsv1alpha1.RecordWithdrawal, status dnsv1alpha1.RecordWithdrawalStatus) error {
	base := claim.DeepCopy()
	conditions := append([]metav1.Condition(nil), claim.Status.Conditions...)
	for _, condition := range status.Conditions {
		meta.SetStatusCondition(&conditions, condition)
	}
	if status.WaiverRef == nil {
		meta.RemoveStatusCondition(&conditions, string(dnsv1alpha1.ConditionOverrideAccepted))
	}
	status.Conditions = conditions
	claim.Status = status
	if equality.Semantic.DeepEqual(base.Status, status) {
		return nil
	}
	return client.IgnoreNotFound(r.Status().Patch(ctx, claim, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})))
}
