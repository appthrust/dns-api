package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"reflect"
	"regexp"

	"github.com/appthrust/dns-api/internal/go/core/declaration"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	recordWithdrawalWebhookPath       = "/validate-dns-appthrust-io-v1alpha1-recordwithdrawal"
	recordWithdrawalWaiverWebhookPath = "/validate-dns-appthrust-io-v1alpha1-recordwithdrawalwaiver"
)

var withdrawalDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func (v *CoreValidator) validateWithdrawalRequest(ctx context.Context, req admission.Request) error {
	var claim dnsv1alpha1.RecordWithdrawal
	if err := v.decoder.Decode(req, &claim); err != nil {
		return err
	}
	if req.SubResource == "status" {
		return v.validateWithdrawalStatus(ctx, &claim)
	}
	if err := declaration.ValidateWithdrawal(&claim); err != nil {
		return err
	}
	if req.Operation == admissionv1.Update {
		var old dnsv1alpha1.RecordWithdrawal
		if err := json.Unmarshal(req.OldObject.Raw, &old); err != nil {
			return fmt.Errorf("old RecordWithdrawal object could not be decoded: %w", err)
		}
		if !reflect.DeepEqual(old.Spec, claim.Spec) {
			return fmt.Errorf("spec is immutable")
		}
	}
	return nil
}

func (v *CoreValidator) validateWaiverRequest(req admission.Request) error {
	var waiver dnsv1alpha1.RecordWithdrawalWaiver
	if err := v.decoder.Decode(req, &waiver); err != nil {
		return err
	}
	if req.SubResource == "status" {
		if err := validateObservedGeneration(waiver.Status.ObservedGeneration, waiver.Generation, "status.observedGeneration"); err != nil {
			return err
		}
		if err := validateConditionObservedGenerations(waiver.Status.Conditions, waiver.Generation, "status.conditions"); err != nil {
			return err
		}
		return validateStatusConditions(waiver.Status.Conditions, "status.conditions", withdrawalAcceptanceReasons())
	}
	if err := declaration.ValidateWithdrawalWaiver(&waiver); err != nil {
		return err
	}
	if !withdrawalDigestPattern.MatchString(waiver.Spec.ClaimSpecDigest) {
		return fmt.Errorf("spec.claimSpecDigest must be a sha256 digest")
	}
	if req.Operation == admissionv1.Update {
		var old dnsv1alpha1.RecordWithdrawalWaiver
		if err := json.Unmarshal(req.OldObject.Raw, &old); err != nil {
			return fmt.Errorf("old RecordWithdrawalWaiver object could not be decoded: %w", err)
		}
		if !reflect.DeepEqual(old.Spec, waiver.Spec) {
			return fmt.Errorf("spec is immutable")
		}
	}
	return nil
}

func withdrawalAcceptanceReasons() conditionReasonSet {
	return conditionReasonSet{dnsv1alpha1.ConditionAccepted: {metav1.ConditionTrue: {"Accepted": {}}, metav1.ConditionFalse: {"InvalidWithdrawal": {}, "NotAllowed": {}, "UnsupportedProvider": {}}}}
}

func withdrawalConditionReasons() conditionReasonSet {
	set := withdrawalAcceptanceReasons()
	for _, reason := range []string{"DNSStillResolves", "NegativeTTLNotElapsed"} {
		addConditionReason(set, dnsv1alpha1.ConditionWithdrawn, metav1.ConditionFalse, reason)
		addConditionReason(set, dnsv1alpha1.ConditionReady, metav1.ConditionFalse, reason)
	}
	for _, reason := range []string{"DNSProbeFailed", "PolicyChanged"} {
		addConditionReason(set, dnsv1alpha1.ConditionWithdrawn, metav1.ConditionUnknown, reason)
		addConditionReason(set, dnsv1alpha1.ConditionReady, metav1.ConditionFalse, reason)
	}
	addConditionReason(set, dnsv1alpha1.ConditionWithdrawn, metav1.ConditionTrue, "WithdrawalVerified")
	addConditionReason(set, dnsv1alpha1.ConditionReady, metav1.ConditionTrue, "WithdrawalVerified")
	addConditionReason(set, dnsv1alpha1.ConditionOverrideAccepted, metav1.ConditionTrue, "OperatorAuthorizedRelease")
	return set
}

func validateWithdrawalConditions(conditions []metav1.Condition, generation int64, path string) error {
	if err := validateStatusConditions(conditions, path, withdrawalConditionReasons()); err != nil {
		return err
	}
	if err := validateConditionObservedGenerations(conditions, generation, path); err != nil {
		return err
	}
	seen := make(map[string]bool, len(conditions))
	for _, c := range conditions {
		if seen[c.Type] {
			return fmt.Errorf("%s contains duplicate condition type %s", path, c.Type)
		}
		seen[c.Type] = true
		if len(c.Message) > 2048 {
			return fmt.Errorf("%s message exceeds 2048 bytes", path)
		}
	}
	return nil
}

func (v *CoreValidator) validateWithdrawalStatus(ctx context.Context, claim *dnsv1alpha1.RecordWithdrawal) error {
	if err := validateObservedGeneration(claim.Status.ObservedGeneration, claim.Generation, "status.observedGeneration"); err != nil {
		return err
	}
	if err := validateWithdrawalConditions(claim.Status.Conditions, claim.Generation, "status.conditions"); err != nil {
		return err
	}
	if claim.Status.Zone != nil {
		namespace := claim.Namespace
		if claim.Spec.ZoneRef.Namespace != nil {
			namespace = *claim.Spec.ZoneRef.Namespace
		}
		if claim.Status.Zone.Namespace != namespace || claim.Status.Zone.Name != claim.Spec.ZoneRef.Name || claim.Status.Zone.UID == "" {
			return fmt.Errorf("status.zone must match spec.zoneRef with a nonempty UID")
		}
		if claim.Status.Unit != nil && (claim.Status.Unit.Namespace != namespace || claim.Status.Unit.Name != claim.Spec.ZoneRef.Name || claim.Status.Unit.UID == "") {
			return fmt.Errorf("status.unit must match the resolved Zone with a nonempty UID")
		}
	}
	if len(claim.Status.CheckedNames) > 256 || len(claim.Status.RequiredViews) > 256 {
		return fmt.Errorf("withdrawal checkedNames and requiredViews must not exceed 256 items")
	}
	if claim.Status.Provider != nil {
		if len(rawExtensionBytes(claim.Status.Provider.State)) > 0 {
			return fmt.Errorf("status.provider.state is private to the ZoneUnit")
		}
		if len(rawExtensionBytes(claim.Status.Provider.Data)) > 0 {
			if claim.Status.ProviderRef == nil {
				return fmt.Errorf("status.providerRef is required for provider data")
			}
			_, version, err := v.resolveProviderVersion(ctx, *claim.Status.ProviderRef, false)
			if err != nil {
				return err
			}
			if version.RecordWithdrawal == nil || !version.RecordWithdrawal.Supported {
				return fmt.Errorf("Provider does not support record withdrawal")
			}
			return validateRawExtensionBySchema(claim.Status.Provider.Data, version.RecordWithdrawal.Schemas.StatusProviderData, "status.provider.data")
		}
	}
	return nil
}

func validateProviderWithdrawal(capability *dnsv1alpha1.ProviderRecordWithdrawal, path string) error {
	if capability == nil {
		return nil
	}
	if err := validateExtensionOpenAPISchema(capability.Schemas.StatusProviderData, path+".schemas.statusProviderData.openAPIV3Schema"); err != nil {
		return err
	}
	core := withdrawalAcceptanceReasons()
	core[dnsv1alpha1.ConditionWithdrawn] = nil
	core[dnsv1alpha1.ConditionReady] = nil
	core[dnsv1alpha1.ConditionOverrideAccepted] = nil
	if err := validateProviderConditionReasons(capability.AdditionalConditionReasons, path+".additionalConditionReasons", core); err != nil {
		return err
	}
	for _, reason := range capability.AdditionalConditionReasons {
		if !hasConditionReason(withdrawalConditionReasons(), reason.ConditionType, reason.Status, reason.Reason) {
			return fmt.Errorf("%s condition reason %s is not part of the withdrawal contract", path, reason.Reason)
		}
	}
	return nil
}

func validateUnitWithdrawals(unit *dnsv1alpha1.ZoneUnit, old *dnsv1alpha1.ZoneUnit) error {
	if len(unit.Spec.Withdrawals) > 256 {
		return fmt.Errorf("spec.withdrawals exceeds 256 items")
	}
	seen := map[string]bool{}
	for i, item := range unit.Spec.Withdrawals {
		key := recordSetClaimKey(item.ClaimNamespace, item.ClaimName)
		if item.ClaimNamespace == "" || item.ClaimName == "" || item.Claim.UID == "" || item.ClaimNamespace != item.Claim.Namespace || item.ClaimName != item.Claim.Name || item.ObservedGeneration < 1 || seen[key] {
			return fmt.Errorf("spec.withdrawals[%d] has invalid or duplicate claim identity", i)
		}
		seen[key] = true
		claim := dnsv1alpha1.RecordWithdrawal{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{dnsv1alpha1.AddressLabelKey: addressLabel(item.Address)}}, Spec: dnsv1alpha1.RecordWithdrawalSpec{ZoneRef: dnsv1alpha1.ZoneReference{Name: unit.Spec.Zone.Ref.Name}, Name: item.Name, Address: item.Address}}
		if err := declaration.ValidateWithdrawal(&claim); err != nil {
			return fmt.Errorf("spec.withdrawals[%d]: %w", i, err)
		}
		if item.Waiver != nil && (item.Waiver.Name == "" || item.Waiver.UID == "" || !withdrawalDigestPattern.MatchString(item.Waiver.Digest) || item.Waiver.ReservationRef.UID == "") {
			return fmt.Errorf("spec.withdrawals[%d].waiver must include exact immutable scope", i)
		}
		if old != nil {
			for _, previous := range old.Spec.Withdrawals {
				if previous.Claim == item.Claim && (previous.Name != item.Name || previous.Address != item.Address || previous.ObservedGeneration != item.ObservedGeneration) {
					return fmt.Errorf("spec.withdrawals[%d] immutable claim shape changed", i)
				}
			}
		}
	}
	return nil
}

func (v *CoreValidator) validateUnitWithdrawalStatus(unit *dnsv1alpha1.ZoneUnit, version *dnsv1alpha1.ProviderVersion) error {
	if len(unit.Status.Withdrawals) > 256 {
		return fmt.Errorf("status.withdrawals exceeds 256 items")
	}
	seen := map[string]bool{}
	for i, item := range unit.Status.Withdrawals {
		path := fmt.Sprintf("status.withdrawals[%d]", i)
		key := recordSetClaimKey(item.ClaimNamespace, item.ClaimName)
		if item.ClaimNamespace == "" || item.ClaimName == "" || item.Claim.UID == "" || item.ClaimNamespace != item.Claim.Namespace || item.ClaimName != item.Claim.Name || item.ObservedGeneration < 1 || seen[key] {
			return fmt.Errorf("%s has invalid or duplicate claim identity", path)
		}
		seen[key] = true
		if err := validateWithdrawalConditions(item.Conditions, item.ObservedGeneration, path+".conditions"); err != nil {
			return err
		}
		if item.Provider != nil && len(rawExtensionBytes(item.Provider.Data)) > 0 {
			if version.RecordWithdrawal == nil || !version.RecordWithdrawal.Supported {
				return fmt.Errorf("Provider does not support record withdrawal")
			}
			if err := validateRawExtensionBySchema(item.Provider.Data, version.RecordWithdrawal.Schemas.StatusProviderData, path+".provider.data"); err != nil {
				return err
			}
		}
	}
	return nil
}

func addressLabel(address string) string {
	ip, _ := netip.ParseAddr(address)
	return dnsv1alpha1.AddressLabelValue(ip)
}
