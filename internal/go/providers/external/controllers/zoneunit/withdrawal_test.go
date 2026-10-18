package zoneunit

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/appthrust/dns-api/internal/go/providers/external/probe"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func withdrawalItem(name, address string) dnsv1alpha1.ZoneUnitWithdrawal {
	return dnsv1alpha1.ZoneUnitWithdrawal{ClaimNamespace: "app", ClaimName: "retired", Claim: dnsv1alpha1.WithdrawalObjectReference{Namespace: "app", Name: "retired", UID: "claim-1"}, ObservedGeneration: 1, Name: name, Address: address, Allowed: true}
}

func withdrawalAnswer(request probe.ProbeRequest, address string, ttl uint32) (probe.ProbeResult, error) {
	result := probe.ProbeResult{}
	for _, server := range request.Policy.Resolver.Servers {
		view := probe.View{ID: "recursive/" + server}
		for _, family := range []string{"A", "AAAA"} {
			answer := probe.AnswerEvidence{Name: request.Name, QType: family, NegativeTTL: &ttl}
			if family == "A" && address != "" {
				answer.Addresses = []string{address}
				answer.PositiveTTL = &ttl
				view.A = []string{address}
			}
			view.Answers = append(view.Answers, answer)
		}
		result.Views = append(result.Views, view)
	}
	return result, nil
}

func addWithdrawal(t *testing.T, c client.Client, request ctrl.Request, item dnsv1alpha1.ZoneUnitWithdrawal) {
	t.Helper()
	var unit dnsv1alpha1.ZoneUnit
	if err := c.Get(context.Background(), request.NamespacedName, &unit); err != nil {
		t.Fatal(err)
	}
	unit.Spec.Withdrawals = append(unit.Spec.Withdrawals, item)
	unit.Generation++
	if err := c.Update(context.Background(), &unit); err != nil {
		t.Fatal(err)
	}
}

func assertWithdrawal(t *testing.T, unit dnsv1alpha1.ZoneUnit, status metav1.ConditionStatus, reason string) dnsv1alpha1.ZoneUnitWithdrawalStatus {
	t.Helper()
	if len(unit.Status.Withdrawals) != 1 {
		t.Fatalf("withdrawals=%#v", unit.Status.Withdrawals)
	}
	got := unit.Status.Withdrawals[0]
	condition := meta.FindStatusCondition(got.Conditions, "Withdrawn")
	if condition == nil || condition.Status != status || condition.Reason != reason {
		t.Fatalf("Withdrawn=%#v, want %s/%s", condition, status, reason)
	}
	return got
}

func TestWithdrawalHorizonConfirmationAndNoStatusLoop(t *testing.T) {
	resolver := &fakeResolver{answer: func(request probe.ProbeRequest) (probe.ProbeResult, error) {
		return withdrawalAnswer(request, "192.0.2.20", 60)
	}}
	r, c, now, request := newFixture(t, 0, resolver)
	addWithdrawal(t, c, request, withdrawalItem("www", "192.0.2.10"))
	_, first := reconcileOnce(t, r, request)
	pending := assertWithdrawal(t, first, metav1.ConditionFalse, "NegativeTTLNotElapsed")
	if pending.AbsentSince == nil || !pending.AbsentSince.Equal(timePointer(*now)) || pending.NotBefore == nil || !pending.NotBefore.Equal(timePointer(now.Add(time.Minute))) || pending.VerifiedAt != nil {
		t.Fatalf("window=%#v", pending)
	}
	_, same := reconcileOnce(t, r, request)
	if same.ResourceVersion != first.ResourceVersion || len(resolver.requests) != 1 {
		t.Fatal("unchanged status was patched or reprobed")
	}
	*now = now.Add(30 * time.Second)
	_, waiting := reconcileOnce(t, r, request)
	still := assertWithdrawal(t, waiting, metav1.ConditionFalse, "NegativeTTLNotElapsed")
	if !still.NotBefore.Equal(pending.NotBefore) {
		t.Fatal("identical negatives slid the horizon")
	}
	// A new reconciler restores the still-fresh identity-bound window.
	r2, err := NewReconciler(c, r.Options)
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(30 * time.Second)
	_, verified := reconcileOnce(t, r2, request)
	good := assertWithdrawal(t, verified, metav1.ConditionTrue, "WithdrawalVerified")
	if good.VerifiedAt == nil || good.VerifiedAt.Before(good.NotBefore) || good.ResultDigest == "" {
		t.Fatalf("unbound success=%#v", good)
	}
	assertProgrammed(t, verified.Status.Conditions, metav1.ConditionTrue, "Programmed")
	resolver.answer = func(req probe.ProbeRequest) (probe.ProbeResult, error) {
		return withdrawalAnswer(req, "192.0.2.10", 600)
	}
	*now = now.Add(30 * time.Second)
	_, reappeared := reconcileOnce(t, r2, request)
	old := assertWithdrawal(t, reappeared, metav1.ConditionFalse, "DNSStillResolves")
	if old.AbsentSince != nil || old.VerifiedAt != nil {
		t.Fatal("reappearance preserved success")
	}
	resolver.answer = func(probe.ProbeRequest) (probe.ProbeResult, error) {
		return probe.ProbeResult{}, errors.New("secret transport diagnostic")
	}
	*now = now.Add(30 * time.Second)
	_, failed := reconcileOnce(t, r2, request)
	assertWithdrawal(t, failed, metav1.ConditionUnknown, "DNSProbeFailed")
	resolver.answer = func(req probe.ProbeRequest) (probe.ProbeResult, error) { return withdrawalAnswer(req, "", 0) }
	*now = now.Add(30 * time.Second)
	_, afterFailure := reconcileOnce(t, r2, request)
	fresh := assertWithdrawal(t, afterFailure, metav1.ConditionFalse, "NegativeTTLNotElapsed")
	if !fresh.NotBefore.Equal(timePointer(old.LastProbeTime.Add(600 * time.Second))) {
		t.Fatal("failure discarded known positive horizon")
	}
}

func TestWithdrawalPolicyFloorAndWaiver(t *testing.T) {
	resolver := &fakeResolver{answer: func(req probe.ProbeRequest) (probe.ProbeResult, error) { return withdrawalAnswer(req, "", 0) }}
	r, c, now, request := newFixture(t, 0, resolver)
	addWithdrawal(t, c, request, withdrawalItem("www", "192.0.2.10"))
	reconcileOnce(t, r, request)
	*now = now.Add(30 * time.Second)
	reconcileOnce(t, r, request)
	var class dnsv1alpha1.ZoneClass
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "app", Name: "external"}, &class); err != nil {
		t.Fatal(err)
	}
	class.Spec.Parameters.Raw = []byte(`{"resolver":{"mode":"Recursive","servers":["192.0.2.54:53"]}}`)
	if err := c.Update(context.Background(), &class); err != nil {
		t.Fatal(err)
	}
	_, changed := reconcileOnce(t, r, request)
	assertWithdrawal(t, changed, metav1.ConditionUnknown, "PolicyChanged")
	*now = now.Add(30 * time.Second)
	_, weaker := reconcileOnce(t, r, request)
	assertWithdrawal(t, weaker, metav1.ConditionUnknown, "PolicyChanged")
	class.Spec.Parameters.Raw = []byte(`{"resolver":{"mode":"Recursive","servers":["192.0.2.53:53"]}}`)
	if err := c.Update(context.Background(), &class); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, r, request)
	*now = now.Add(30 * time.Second)
	_, restored := reconcileOnce(t, r, request)
	assertWithdrawal(t, restored, metav1.ConditionFalse, "NegativeTTLNotElapsed")
	resolver.answer = func(req probe.ProbeRequest) (probe.ProbeResult, error) {
		return withdrawalAnswer(req, "192.0.2.10", 60)
	}
	*now = now.Add(30 * time.Second)
	_, old := reconcileOnce(t, r, request)
	old.Spec.Withdrawals[0].Waiver = &dnsv1alpha1.ZoneUnitWithdrawalWaiver{Name: "emergency", UID: "waiver-1", Digest: "sha256:scope", ReservationRef: dnsv1alpha1.WithdrawalObjectReference{Namespace: "mgmt", Name: "reservation", UID: "reservation-1"}}
	if err := c.Update(context.Background(), &old); err != nil {
		t.Fatal(err)
	}
	_, waived := reconcileOnce(t, r, request)
	got := assertWithdrawal(t, waived, metav1.ConditionFalse, "DNSStillResolves")
	if !meta.IsStatusConditionTrue(got.Conditions, "OverrideAccepted") || got.WaiverRef == nil || got.WaiverRef.UID != "waiver-1" {
		t.Fatalf("waiver=%#v", got)
	}
	waived.Spec.Withdrawals[0].Waiver = nil
	if err := c.Update(context.Background(), &waived); err != nil {
		t.Fatal(err)
	}
	_, revoked := reconcileOnce(t, r, request)
	if revoked.Status.Withdrawals[0].WaiverRef != nil || meta.IsStatusConditionTrue(revoked.Status.Withdrawals[0].Conditions, "OverrideAccepted") {
		t.Fatal("deleted waiver retained")
	}
}

func TestWithdrawalWildcardRetainsPublicationWitness(t *testing.T) {
	resolver := &fakeResolver{answer: func(req probe.ProbeRequest) (probe.ProbeResult, error) {
		return withdrawalAnswer(req, "192.0.2.10", 3600)
	}}
	r, c, now, request := newFixture(t, 1, resolver)
	var unit dnsv1alpha1.ZoneUnit
	if err := c.Get(context.Background(), request.NamespacedName, &unit); err != nil {
		t.Fatal(err)
	}
	unit.Spec.RecordSets[0].Name = "*.project"
	if err := c.Update(context.Background(), &unit); err != nil {
		t.Fatal(err)
	}
	_, published := reconcileOnce(t, r, request)
	witness := resolver.requests[0].Name
	published.Spec.RecordSets = nil
	if err := c.Update(context.Background(), &published); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, r, request) // Core may remove the record before composing its independent claim.
	addWithdrawal(t, c, request, withdrawalItem("*.project", "192.0.2.10"))
	resolver.answer = func(req probe.ProbeRequest) (probe.ProbeResult, error) {
		if req.Name == witness {
			return withdrawalAnswer(req, "192.0.2.10", 3600)
		}
		return withdrawalAnswer(req, "", 0)
	}
	_, retiring := reconcileOnce(t, r, request)
	got := assertWithdrawal(t, retiring, metav1.ConditionFalse, "DNSStillResolves")
	if !got.SampledWildcard || len(got.CheckedNames) != 2 || !slices.Contains(got.CheckedNames, witness) {
		t.Fatalf("lost witness=%#v", got)
	}
	resolver.answer = func(req probe.ProbeRequest) (probe.ProbeResult, error) { return withdrawalAnswer(req, "", 0) }
	*now = now.Add(30 * time.Second)
	_, absent := reconcileOnce(t, r, request)
	pending := assertWithdrawal(t, absent, metav1.ConditionFalse, "NegativeTTLNotElapsed")
	if !pending.NotBefore.Equal(timePointer(got.LastProbeTime.Add(time.Hour))) {
		t.Fatal("3600-second horizon was capped")
	}
	recreated := withdrawalItem("*.project", "192.0.2.10")
	recreated.Claim.UID = "claim-2"
	if slices.Equal(withdrawalNames(&unit, recreated, publicationEvidence{}), withdrawalNames(&unit, withdrawalItem("*.project", "192.0.2.10"), publicationEvidence{})) {
		t.Fatal("recreated claim reused retirement witness")
	}
}

func TestWithdrawalFreshnessAndDigest(t *testing.T) {
	for _, mode := range []string{"stale", "clock reversal", "missing private state", "UID recreation"} {
		t.Run(mode, func(t *testing.T) {
			resolver := &fakeResolver{answer: func(req probe.ProbeRequest) (probe.ProbeResult, error) { return withdrawalAnswer(req, "", 0) }}
			r, c, now, request := newFixture(t, 0, resolver)
			addWithdrawal(t, c, request, withdrawalItem("www", "192.0.2.10"))
			reconcileOnce(t, r, request)
			*now = now.Add(30 * time.Second)
			_, unit := reconcileOnce(t, r, request)
			good := assertWithdrawal(t, unit, metav1.ConditionTrue, "WithdrawalVerified")
			copy := good
			copy.Conditions = slices.Clone(good.Conditions)
			slices.Reverse(copy.Conditions)
			for i := range copy.Conditions {
				copy.Conditions[i].LastTransitionTime = metav1.Now()
			}
			if withdrawalResultDigest(&unit, unit.Spec.Withdrawals[0], copy) != good.ResultDigest {
				t.Fatal("digest depends on condition order/transition times")
			}
			switch mode {
			case "stale":
				*now = now.Add(331 * time.Second)
			case "clock reversal":
				*now = now.Add(-time.Minute)
			case "missing private state":
				unit.Status.Provider = nil
				if err := c.Status().Update(context.Background(), &unit); err != nil {
					t.Fatal(err)
				}
			case "UID recreation":
				unit.Spec.Withdrawals[0].Claim.UID = "claim-2"
				if err := c.Update(context.Background(), &unit); err != nil {
					t.Fatal(err)
				}
			}
			_, got := reconcileOnce(t, r, request)
			reason := "NegativeTTLNotElapsed"
			status := metav1.ConditionFalse
			if mode == "missing private state" {
				reason = "PolicyChanged"
				status = metav1.ConditionUnknown
			}
			assertWithdrawal(t, got, status, reason)
		})
	}
}

func TestWithdrawalFairBudgetAndDenied(t *testing.T) {
	resolver := &fakeResolver{answer: func(req probe.ProbeRequest) (probe.ProbeResult, error) { return withdrawalAnswer(req, "", 0) }}
	r, c, now, request := newFixture(t, 40, resolver)
	var unit dnsv1alpha1.ZoneUnit
	if err := c.Get(context.Background(), request.NamespacedName, &unit); err != nil {
		t.Fatal(err)
	}
	for i := range 40 {
		item := withdrawalItem("www", "192.0.2.10")
		item.ClaimName = fmt.Sprintf("claim-%02d", i)
		item.Claim.Name = item.ClaimName
		item.Claim.UID = types.UID(item.ClaimName)
		unit.Spec.Withdrawals = append(unit.Spec.Withdrawals, item)
	}
	unit.Spec.Withdrawals[0].Allowed = false
	if err := c.Update(context.Background(), &unit); err != nil {
		t.Fatal(err)
	}
	_, first := reconcileOnce(t, r, request)
	if len(resolver.requests) != MaxNamesPerPass || first.Status.Withdrawals[0].LastProbeTime != nil {
		t.Fatal("budget/denied boundary")
	}
	for range 2 {
		*now = now.Add(time.Second)
		reconcileOnce(t, r, request)
	}
	if len(resolver.requests) != 79 {
		t.Fatalf("starved work: probes=%d", len(resolver.requests))
	}
}

func TestWithdrawalLateObservationAndInputMutation(t *testing.T) {
	for _, mode := range []string{"late", "changed"} {
		t.Run(mode, func(t *testing.T) {
			resolver := &fakeResolver{answer: func(req probe.ProbeRequest) (probe.ProbeResult, error) { return withdrawalAnswer(req, "", 0) }}
			r, c, now, request := newFixture(t, 0, resolver)
			addWithdrawal(t, c, request, withdrawalItem("www", "192.0.2.10"))
			reconcileOnce(t, r, request)
			*now = now.Add(30 * time.Second)
			resolver.answer = func(req probe.ProbeRequest) (probe.ProbeResult, error) {
				if mode == "late" {
					*now = now.Add(301 * time.Second)
				} else {
					var unit dnsv1alpha1.ZoneUnit
					if err := c.Get(context.Background(), request.NamespacedName, &unit); err != nil {
						t.Fatal(err)
					}
					unit.Spec.Withdrawals[0].Claim.UID = "replacement"
					if err := c.Update(context.Background(), &unit); err != nil {
						t.Fatal(err)
					}
				}
				return withdrawalAnswer(req, "", 0)
			}
			result, unit := reconcileOnce(t, r, request)
			if mode == "late" {
				assertWithdrawal(t, unit, metav1.ConditionUnknown, "DNSProbeFailed")
			} else if result.RequeueAfter != time.Second || unit.Status.Withdrawals[0].VerifiedAt != nil {
				t.Fatal("stale result committed")
			}
		})
	}
}

func TestWithdrawalContinuesDuringUnitDeletion(t *testing.T) {
	resolver := &fakeResolver{answer: func(req probe.ProbeRequest) (probe.ProbeResult, error) {
		return withdrawalAnswer(req, "192.0.2.10", 60)
	}}
	r, c, _, request := newFixture(t, 0, resolver)
	addWithdrawal(t, c, request, withdrawalItem("www", "192.0.2.10"))
	var unit dnsv1alpha1.ZoneUnit
	if err := c.Get(context.Background(), request.NamespacedName, &unit); err != nil {
		t.Fatal(err)
	}
	unit.Finalizers = []string{"core-retention"}
	if err := c.Update(context.Background(), &unit); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(context.Background(), &unit); err != nil {
		t.Fatal(err)
	}
	_, got := reconcileOnce(t, r, request)
	assertWithdrawal(t, got, metav1.ConditionFalse, "DNSStillResolves")
}

func TestWithdrawalWindowExtendsNeverShortens(t *testing.T) {
	now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	status := dnsv1alpha1.ZoneUnitWithdrawalStatus{}
	stored := withdrawalState{}
	for _, ttl := range []uint32{60, 120, 30} {
		decision := probe.WithdrawalDecision{Decision: probe.Decision{Status: metav1.ConditionTrue}, Cache: []probe.WithdrawalCacheEvidence{{Name: "www.example.test", View: "recursive/a", QType: "AAAA", NegativeTTL: &ttl}}}
		got := withdrawalWindow(&status, &stored, decision, now)
		if got.Reason != "NegativeTTLNotElapsed" {
			t.Fatalf("unexpected decision %#v", got)
		}
		status.LastProbeTime = timePointer(now)
		now = now.Add(30 * time.Second)
	}
	if !status.NotBefore.Equal(timePointer(status.AbsentSince.Add(120 * time.Second))) {
		t.Fatal("changed answer shortened or slid the horizon")
	}
}

func TestWithdrawalSubsecondHorizonSurvivesStatusPrecision(t *testing.T) {
	start := time.Date(2026, 10, 18, 0, 0, 0, 900000000, time.UTC)
	ttl := uint32(60)
	decision := probe.WithdrawalDecision{Decision: probe.Decision{Status: metav1.ConditionTrue}, Cache: []probe.WithdrawalCacheEvidence{{Name: "www.example.test", View: "recursive/a", QType: "A", NegativeTTL: &ttl}}}
	status := dnsv1alpha1.ZoneUnitWithdrawalStatus{}
	state := withdrawalState{}
	withdrawalWindow(&status, &state, decision, start)
	// metav1.Time JSON drops fractional seconds; the private anchor must not.
	status.AbsentSince = timePointer(start.Truncate(time.Second))
	status.LastProbeTime = timePointer(start.Truncate(time.Second))
	if status.NotBefore.Nanosecond() != 0 || !status.NotBefore.Equal(timePointer(start.Truncate(time.Second).Add(61*time.Second))) {
		t.Fatal("public horizon rounded down")
	}
	if got := withdrawalWindow(&status, &state, decision, start.Truncate(time.Second).Add(60*time.Second)); got.Status == metav1.ConditionTrue {
		t.Fatal("subsecond expiry released early")
	}
	if got := withdrawalWindow(&status, &state, decision, start.Truncate(time.Second).Add(61*time.Second)); got.Status != metav1.ConditionTrue {
		t.Fatal("post-horizon confirming probe did not verify")
	}
}

func TestWithdrawalDeniedHistoryAndIPv6(t *testing.T) {
	const address = "2001:db8::1"
	resolver := &fakeResolver{answer: func(req probe.ProbeRequest) (probe.ProbeResult, error) {
		result, err := withdrawalAnswer(req, "", 3600)
		for i := range result.Views {
			result.Views[i].AAAA = []string{address}
			result.Views[i].Answers[1].Addresses = []string{address}
			ttl := uint32(3600)
			result.Views[i].Answers[1].PositiveTTL = &ttl
		}
		return result, err
	}}
	r, c, now, request := newFixture(t, 0, resolver)
	addWithdrawal(t, c, request, withdrawalItem("www", address))
	_, unit := reconcileOnce(t, r, request)
	old := assertWithdrawal(t, unit, metav1.ConditionFalse, "DNSStillResolves")
	unit.Spec.Withdrawals[0].Allowed = false
	if err := c.Update(context.Background(), &unit); err != nil {
		t.Fatal(err)
	}
	_, denied := reconcileOnce(t, r, request)
	if len(resolver.requests) != 1 || !meta.IsStatusConditionFalse(denied.Status.Withdrawals[0].Conditions, "Accepted") {
		t.Fatal("denied withdrawal was probed or accepted")
	}
	denied.Spec.Withdrawals[0].Allowed = true
	if err := c.Update(context.Background(), &denied); err != nil {
		t.Fatal(err)
	}
	resolver.answer = func(req probe.ProbeRequest) (probe.ProbeResult, error) { return withdrawalAnswer(req, "", 0) }
	*now = now.Add(30 * time.Second)
	_, restored := reconcileOnce(t, r, request)
	got := assertWithdrawal(t, restored, metav1.ConditionFalse, "NegativeTTLNotElapsed")
	if !got.NotBefore.Equal(timePointer(old.LastProbeTime.Add(time.Hour))) {
		t.Fatal("denial discarded IPv6 positive horizon")
	}
}

func TestWithdrawalBindingRejectionNoStatusLoop(t *testing.T) {
	resolver := &fakeResolver{answer: func(req probe.ProbeRequest) (probe.ProbeResult, error) {
		return withdrawalAnswer(req, "192.0.2.10", 3600)
	}}
	r, c, now, request := newFixture(t, 0, resolver)
	addWithdrawal(t, c, request, withdrawalItem("www", "192.0.2.10"))
	_, unit := reconcileOnce(t, r, request)
	old := assertWithdrawal(t, unit, metav1.ConditionFalse, "DNSStillResolves")
	var class dnsv1alpha1.ZoneClass
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "app", Name: "external"}, &class); err != nil {
		t.Fatal(err)
	}
	class.Status.Conditions[0].Status = metav1.ConditionFalse
	if err := c.Status().Update(context.Background(), &class); err != nil {
		t.Fatal(err)
	}
	_, rejected := reconcileOnce(t, r, request)
	assertWithdrawal(t, rejected, metav1.ConditionUnknown, "DNSProbeFailed")
	*now = now.Add(time.Second)
	_, same := reconcileOnce(t, r, request)
	if same.ResourceVersion != rejected.ResourceVersion {
		t.Fatal("unchanged rejected binding patched status")
	}
	class.Status.Conditions[0].Status = metav1.ConditionTrue
	if err := c.Status().Update(context.Background(), &class); err != nil {
		t.Fatal(err)
	}
	resolver.answer = func(req probe.ProbeRequest) (probe.ProbeResult, error) { return withdrawalAnswer(req, "", 0) }
	_, restored := reconcileOnce(t, r, request)
	got := assertWithdrawal(t, restored, metav1.ConditionFalse, "NegativeTTLNotElapsed")
	if !got.NotBefore.Equal(timePointer(old.LastProbeTime.Add(time.Hour))) {
		t.Fatal("binding rejection discarded known positive horizon")
	}
}
