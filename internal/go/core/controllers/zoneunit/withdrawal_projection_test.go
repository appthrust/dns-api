package zoneunit

import (
	"context"
	"testing"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestWithdrawalProjectionRejectsStaleBindings(t *testing.T) {
	_, _, _, claim := withdrawalFixture()
	item := dnsv1alpha1.ZoneUnitWithdrawal{ClaimNamespace: claim.Namespace, ClaimName: claim.Name, Claim: withdrawalObjectRef(claim), ObservedGeneration: claim.Generation, Name: claim.Spec.Name, Address: claim.Spec.Address, Allowed: true}
	for _, tc := range []struct {
		name   string
		mutate func(*dnsv1alpha1.ZoneUnit)
		want   bool
	}{
		{"current", func(*dnsv1alpha1.ZoneUnit) {}, true},
		{"claim UID", func(u *dnsv1alpha1.ZoneUnit) { u.Status.Withdrawals[0].Claim.UID = "other" }, false},
		{"claim generation", func(u *dnsv1alpha1.ZoneUnit) { u.Status.Withdrawals[0].ObservedGeneration++ }, false},
		{"condition generation", func(u *dnsv1alpha1.ZoneUnit) { u.Status.Withdrawals[0].Conditions[0].ObservedGeneration++ }, false},
		{"unit generation", func(u *dnsv1alpha1.ZoneUnit) { u.Status.ObservedGeneration-- }, false},
		{"missing class UID", func(u *dnsv1alpha1.ZoneUnit) { u.Spec.Zone.ZoneClassUID = "" }, false},
		{"missing class generation", func(u *dnsv1alpha1.ZoneUnit) { u.Spec.Zone.ZoneClassObservedGeneration = 0 }, false},
		{"authorization revoked", func(u *dnsv1alpha1.ZoneUnit) { u.Spec.Withdrawals[0].Allowed = false }, false},
		{"missing item", func(u *dnsv1alpha1.ZoneUnit) { u.Spec.Withdrawals = nil }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unit := &dnsv1alpha1.ZoneUnit{ObjectMeta: metav1.ObjectMeta{Generation: 10}, Spec: dnsv1alpha1.ZoneUnitSpec{Zone: dnsv1alpha1.ZoneUnitZoneSpec{ZoneClassUID: "class", ZoneClassObservedGeneration: 1}, Withdrawals: []dnsv1alpha1.ZoneUnitWithdrawal{item}}, Status: dnsv1alpha1.ZoneUnitStatus{ObservedGeneration: 10, Withdrawals: []dnsv1alpha1.ZoneUnitWithdrawalStatus{{ClaimNamespace: claim.Namespace, ClaimName: claim.Name, Claim: item.Claim, ObservedGeneration: claim.Generation, ResultDigest: "current", Conditions: []metav1.Condition{{Type: "Withdrawn", Status: metav1.ConditionTrue, Reason: "WithdrawalVerified", Message: "exact provider detail", ObservedGeneration: claim.Generation}, {Type: "Ready", Status: metav1.ConditionTrue, Reason: "WithdrawalVerified", ObservedGeneration: claim.Generation}}}}}}
			tc.mutate(unit)
			status := dnsv1alpha1.RecordWithdrawalStatus{}
			projectWithdrawalResult(&status, claim, item, unit)
			if (status.ResultDigest != "") == tc.want {
				return
			}
			t.Fatalf("projected=%#v want=%v", status, tc.want)
		})
	}
}

func TestWithdrawalWaiverMissingClaimInvalidatesAcceptance(t *testing.T) {
	waiver := &dnsv1alpha1.RecordWithdrawalWaiver{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "waiver", Generation: 1}, Spec: dnsv1alpha1.RecordWithdrawalWaiverSpec{WithdrawalRef: dnsv1alpha1.WithdrawalObjectReference{Namespace: "app", Name: "missing", UID: "gone"}}, Status: dnsv1alpha1.RecordWithdrawalWaiverStatus{ObservedGeneration: 1, Conditions: []metav1.Condition{{Type: "Accepted", Status: metav1.ConditionTrue, Reason: "Accepted", ObservedGeneration: 1}}}}
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithStatusSubresource(waiver).WithObjects(waiver).Build()
	r := &RecordWithdrawalWaiverReconciler{Client: c}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(waiver)}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(waiver), waiver); err != nil {
		t.Fatal(err)
	}
	assertCondition(t, waiver.Status.Conditions, "Accepted", metav1.ConditionFalse, "InvalidWithdrawal")
}
