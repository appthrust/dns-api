package declaration

import (
	"context"
	"fmt"
	"net/netip"
	"strings"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func ValidateWithdrawal(claim *dnsv1alpha1.RecordWithdrawal) error {
	if len(claim.OwnerReferences) != 0 || len(claim.Finalizers) != 0 {
		return fmt.Errorf("withdrawals must not have ownerReferences or finalizers")
	}
	if len(validation.IsDNS1123Subdomain(claim.Spec.ZoneRef.Name)) != 0 {
		return fmt.Errorf("spec.zoneRef.name must be a valid object name")
	}
	if claim.Spec.ZoneRef.Namespace != nil && len(validation.IsDNS1123Label(*claim.Spec.ZoneRef.Namespace)) != 0 {
		return fmt.Errorf("spec.zoneRef.namespace must be a valid namespace")
	}
	name := claim.Spec.Name
	if name != "@" && name != "*" {
		if strings.HasPrefix(name, "*.") {
			name = strings.TrimPrefix(name, "*.")
		}
		if len(validation.IsDNS1123Subdomain(name)) != 0 || len(claim.Spec.Name) > 253 {
			return fmt.Errorf("spec.name must be a canonical relative DNS owner")
		}
	}
	address, err := netip.ParseAddr(claim.Spec.Address)
	if err != nil || address.Zone() != "" || address.Is4In6() || address.String() != claim.Spec.Address {
		return fmt.Errorf("spec.address must be a canonical unscoped and unmapped IP address")
	}
	if claim.Labels[dnsv1alpha1.AddressLabelKey] != dnsv1alpha1.AddressLabelValue(address) {
		return fmt.Errorf("metadata.labels[%s] must match spec.address", dnsv1alpha1.AddressLabelKey)
	}
	return nil
}

func ValidateWithdrawalWaiver(waiver *dnsv1alpha1.RecordWithdrawalWaiver) error {
	if len(waiver.OwnerReferences) != 0 || len(waiver.Finalizers) != 0 {
		return fmt.Errorf("waivers must not have ownerReferences or finalizers")
	}
	for _, ref := range []dnsv1alpha1.WithdrawalObjectReference{waiver.Spec.WithdrawalRef, waiver.Spec.ReservationRef} {
		if len(validation.IsDNS1123Label(ref.Namespace)) != 0 || len(validation.IsDNS1123Subdomain(ref.Name)) != 0 || ref.UID == "" {
			return fmt.Errorf("withdrawalRef and reservationRef require exact namespace, name and UID")
		}
	}
	if waiver.Spec.WithdrawalRef.Namespace != waiver.Namespace {
		return fmt.Errorf("spec.withdrawalRef.namespace must match waiver namespace")
	}
	ip, err := netip.ParseAddr(waiver.Spec.Address)
	if err != nil || ip.Zone() != "" || ip.Is4In6() || ip.String() != waiver.Spec.Address {
		return fmt.Errorf("spec.address must be a canonical unscoped and unmapped IP address")
	}
	if !waiver.Spec.AcceptStaleDNSRisk {
		return fmt.Errorf("spec.acceptStaleDNSRisk must be true")
	}
	if strings.TrimSpace(waiver.Spec.Reason) == "" || len(waiver.Spec.Reason) > 2048 || strings.TrimSpace(waiver.Spec.TicketRef) == "" || len(waiver.Spec.TicketRef) > 1024 {
		return fmt.Errorf("spec.reason and spec.ticketRef must be nonempty and bounded")
	}
	return nil
}

func WithdrawalWaiverMatches(waiver *dnsv1alpha1.RecordWithdrawalWaiver, claim *dnsv1alpha1.RecordWithdrawal) bool {
	return ValidateWithdrawalWaiver(waiver) == nil && waiver.DeletionTimestamp.IsZero() && waiver.UID != "" && waiver.Namespace == claim.Namespace && waiver.Spec.WithdrawalRef == (dnsv1alpha1.WithdrawalObjectReference{Namespace: claim.Namespace, Name: claim.Name, UID: claim.UID}) && waiver.Spec.ClaimSpecDigest == dnsv1alpha1.WithdrawalSpecDigest(claim.Spec) && waiver.Spec.Address == claim.Spec.Address
}

func WithdrawalAllowedByZone(ctx context.Context, c client.Client, claim *dnsv1alpha1.RecordWithdrawal, zone *dnsv1alpha1.Zone) (bool, string) {
	ip, err := netip.ParseAddr(claim.Spec.Address)
	if err != nil {
		return false, "Withdrawal address is invalid"
	}
	recordType := dnsv1alpha1.RecordTypeA
	if ip.Is6() {
		recordType = dnsv1alpha1.RecordTypeAAAA
	}
	record := dnsv1alpha1.RecordSet{ObjectMeta: metav1.ObjectMeta{Namespace: claim.Namespace}, Spec: dnsv1alpha1.RecordSetSpec{Name: claim.Spec.Name, Type: recordType}}
	return recordSetAllowedByZone(ctx, c, &record, zone, !strings.HasPrefix(claim.Spec.Name, "*"))
}
