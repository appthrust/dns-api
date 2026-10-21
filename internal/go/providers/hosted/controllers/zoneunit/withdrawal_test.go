package zoneunit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/appthrust/dns-api/internal/go/core/dnsprobe"
	"github.com/appthrust/dns-api/internal/go/providers/hosted/withdrawal"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	hostedv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/hosted/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func claim(index int) dnsv1alpha1.ZoneUnitWithdrawal {
	name := fmt.Sprintf("retire-%03d", index)
	return dnsv1alpha1.ZoneUnitWithdrawal{ClaimNamespace: "app", ClaimName: name, Claim: dnsv1alpha1.WithdrawalObjectReference{Namespace: "app", Name: name, UID: types.UID(name)}, ObservedGeneration: 1, Name: "www", Address: "192.0.2.10", Allowed: true}
}
func TestWithdrawalFairBudgetAndPersistedState(t *testing.T) {
	r, c, now, req := newFixture(t)
	_, unit := reconcileOnce(t, r, req)
	for i := range 65 {
		unit.Spec.Withdrawals = append(unit.Spec.Withdrawals, claim(i))
	}
	unit.Generation++
	if err := c.Update(context.Background(), unit); err != nil {
		t.Fatal(err)
	}
	evaluator := r.Options.Evaluator.(*fakeEvaluator)
	result, unit := reconcileOnce(t, r, req)
	if len(evaluator.inputs) != 32 || result.RequeueAfter != time.Second {
		t.Fatalf("calls=%d result=%+v", len(evaluator.inputs), result)
	}
	*now = now.Add(time.Second)
	reconcileOnce(t, r, req)
	*now = now.Add(time.Second)
	_, unit = reconcileOnce(t, r, req)
	if len(evaluator.inputs) != 65 {
		t.Fatalf("unfair probes=%d", len(evaluator.inputs))
	}
	for _, status := range unit.Status.Withdrawals {
		if status.Claim.UID == "" || status.ResultDigest == "" || status.LastProbeTime == nil || status.NextProbeTime == nil {
			t.Fatalf("unbound result=%+v", status)
		}
	}
	*now = now.Add(30 * time.Second)
	reconcileOnce(t, r, req)
	if evaluator.inputs[65].Previous == nil {
		t.Fatal("persisted evaluator history was not restored")
	}
}
func TestWithdrawalSeesServedOmissionAndRetainsPositiveHorizon(t *testing.T) {
	r, c, now, req := newFixture(t)
	reconcileOnce(t, r, req)
	serve(t, c, req, *now)
	_, unit := reconcileOnce(t, r, req)
	unit.Spec.RecordSets[0].DeletionRequested = true
	unit.Spec.Withdrawals = []dnsv1alpha1.ZoneUnitWithdrawal{claim(0)}
	unit.Generation++
	if err := c.Update(context.Background(), unit); err != nil {
		t.Fatal(err)
	}
	evaluator := r.Options.Evaluator.(*fakeEvaluator)
	_, unit = reconcileOnce(t, r, req)
	if len(evaluator.inputs) != 1 || evaluator.inputs[0].Served {
		t.Fatal("unserved omission authorized")
	}
	first := evaluator.inputs[0]
	if first.Previous == nil || !first.Previous.Positive["publication"].Equal(now.Add(120*time.Second)) {
		t.Fatalf("positive seed=%+v", first.Previous)
	}
	for _, record := range first.Snapshot.Records {
		if record.Name == "www" {
			t.Fatal("deleted ledger item passed as snapshot content")
		}
	}
	serve(t, c, req, *now)
	_, unit = reconcileOnce(t, r, req)
	if len(evaluator.inputs) != 2 || !evaluator.inputs[1].Served || evaluator.inputs[1].SOAMinimum != 300 {
		t.Fatalf("served input=%+v", evaluator.inputs)
	}
	unit.Spec.Withdrawals = nil
	unit.Generation++
	if err := c.Update(context.Background(), unit); err != nil {
		t.Fatal(err)
	}
	_, unit = reconcileOnce(t, r, req)
	if len(unit.Status.Withdrawals) != 0 || len(loadState(unit).Withdrawals) != 0 {
		t.Fatal("removed claims retained schedule/status")
	}
}
func TestWithdrawalStalePolicyResultDiscarded(t *testing.T) {
	r, c, _, req := newFixture(t)
	_, unit := reconcileOnce(t, r, req)
	unit.Spec.Withdrawals = []dnsv1alpha1.ZoneUnitWithdrawal{claim(0)}
	unit.Generation++
	if err := c.Update(context.Background(), unit); err != nil {
		t.Fatal(err)
	}
	evaluator := r.Options.Evaluator.(*fakeEvaluator)
	evaluator.evaluate = func(in withdrawal.Input) (withdrawal.Decision, error) {
		var current dnsv1alpha1.ZoneUnit
		if err := c.Get(context.Background(), req.NamespacedName, &current); err != nil {
			t.Fatal(err)
		}
		current.Generation++
		current.Spec.Withdrawals[0].Allowed = false
		if err := c.Update(context.Background(), &current); err != nil {
			t.Fatal(err)
		}
		return withdrawal.Decision{Withdrawn: metav1.ConditionTrue, Ready: metav1.ConditionTrue, Reason: "WithdrawalVerified", Message: "verified", NextProbe: in.Now.Add(30 * time.Second)}, nil
	}
	result, unit := reconcileOnce(t, r, req)
	if result.RequeueAfter != time.Second || len(unit.Status.Withdrawals) != 0 {
		t.Fatalf("stale result committed: %+v %+v", result, unit.Status.Withdrawals)
	}
}
func TestWithdrawalErrorsAndInvalidBindingFailClosed(t *testing.T) {
	r, c, _, req := newFixture(t)
	_, unit := reconcileOnce(t, r, req)
	unit.Spec.Withdrawals = []dnsv1alpha1.ZoneUnitWithdrawal{claim(0)}
	unit.Generation++
	if err := c.Update(context.Background(), unit); err != nil {
		t.Fatal(err)
	}
	evaluator := r.Options.Evaluator.(*fakeEvaluator)
	evaluator.evaluate = func(withdrawal.Input) (withdrawal.Decision, error) {
		return withdrawal.Decision{}, errors.New("private transport details")
	}
	_, unit = reconcileOnce(t, r, req)
	status := unit.Status.Withdrawals[0]
	assertCondition(t, status.Conditions, "Withdrawn", metav1.ConditionUnknown, "DNSProbeFailed")
	if status.VerifiedAt != nil {
		t.Fatal("failure retained proof")
	}
	unit.Spec.Zone.ZoneClassUID = "replaced-class"
	unit.Generation++
	if err := c.Update(context.Background(), unit); err != nil {
		t.Fatal(err)
	}
	_, unit = reconcileOnce(t, r, req)
	assertCondition(t, unit.Status.Withdrawals[0].Conditions, "Accepted", metav1.ConditionFalse, "InvalidWithdrawal")
	if len(evaluator.inputs) != 1 {
		t.Fatal("invalid class binding reached evaluator")
	}
}
func TestDelegationObservationDoesNotGateServing(t *testing.T) {
	r, c, now, req := newFixture(t)
	class := fixtureClass()
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(class), class); err != nil {
		t.Fatal(err)
	}
	class.Spec.Parameters.Raw = []byte(`{"nameServers":[{"name":"ns.example.test","addresses":["192.0.2.53"]}],"verification":{"delegation":{"resolver":{"mode":"Recursive","servers":["192.0.2.54:53"]}}}}`)
	if err := c.Update(context.Background(), class); err != nil {
		t.Fatal(err)
	}
	resolver := r.Options.Resolver.(*fakeResolver)
	resolver.answer = func(in dnsprobe.ProbeRequest) (dnsprobe.ProbeResult, error) {
		if !in.ZoneOnly || in.Name != "example.test" {
			t.Fatalf("delegation request=%+v", in)
		}
		return dnsprobe.ProbeResult{Zone: &dnsprobe.ZoneEvidence{Views: []dnsprobe.AuthorityObservation{{ID: "recursive/192.0.2.54:53", Nameservers: []string{"ns.other.test"}}}}}, nil
	}
	reconcileOnce(t, r, req)
	serve(t, c, req, *now)
	_, unit := reconcileOnce(t, r, req)
	assertCondition(t, unit.Status.Zone.Conditions, "Programmed", metav1.ConditionTrue, "Programmed")
	var data zoneData
	_ = json.Unmarshal(unit.Status.Zone.Provider.Data.Raw, &data)
	if data.Delegation == nil || data.Delegation.Matches || resolver.calls != 1 {
		t.Fatalf("delegation=%+v calls=%d", data.Delegation, resolver.calls)
	}
	*now = now.Add(300 * time.Second)
	serve(t, c, req, *now)
	resolver.answer = func(dnsprobe.ProbeRequest) (dnsprobe.ProbeResult, error) {
		return dnsprobe.ProbeResult{}, errors.New("private network details")
	}
	_, unit = reconcileOnce(t, r, req)
	assertCondition(t, unit.Status.Zone.Conditions, "Programmed", metav1.ConditionTrue, "Programmed")
	_ = json.Unmarshal(unit.Status.Zone.Provider.Data.Raw, &data)
	if data.Delegation.Error != "DNSProbeFailed" {
		t.Fatalf("failure=%+v", data.Delegation)
	}
}
func TestDeletingUnitKeepsWithdrawalFailClosed(t *testing.T) {
	r, c, _, req := newFixture(t)
	_, unit := reconcileOnce(t, r, req)
	unit.Spec.Withdrawals = []dnsv1alpha1.ZoneUnitWithdrawal{claim(0)}
	unit.Finalizers = []string{"test/retain"}
	unit.Generation++
	if err := c.Update(context.Background(), unit); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(context.Background(), unit); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, r, req)
	inputs := r.Options.Evaluator.(*fakeEvaluator).inputs
	if len(inputs) != 1 || inputs[0].Served || len(inputs[0].Snapshot.Records) != 0 {
		t.Fatalf("deleting withdrawal input=%+v", inputs)
	}
}
func TestServerExpiryBoundary(t *testing.T) {
	now := time.Unix(1000, 0)
	servers := []hostedv1alpha1.HostedZoneServerStatus{{Endpoint: "boundary", ObservedAt: metav1.NewTime(now.Add(-ServerFreshness))}, {Endpoint: "stale", ObservedAt: metav1.NewTime(now.Add(-ServerFreshness - time.Second))}}
	fresh := freshServers(servers, now)
	if len(fresh) != 1 || fresh[0].Endpoint != "boundary" {
		t.Fatalf("fresh=%+v", fresh)
	}
}

func TestWithdrawalRequiresAllFreshServersPastOmission(t *testing.T) {
	r, c, now, req := newFixture(t)
	reconcileOnce(t, r, req)
	serve(t, c, req, *now)
	_, unit := reconcileOnce(t, r, req)
	before := snapshotFor(t, c, req)
	unit.Spec.RecordSets[0].DeletionRequested = true
	unit.Spec.Withdrawals = []dnsv1alpha1.ZoneUnitWithdrawal{claim(0)}
	unit.Generation++
	if err := c.Update(context.Background(), unit); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, r, req)
	omitted := snapshotFor(t, c, req)
	omitted.Status.Servers = []hostedv1alpha1.HostedZoneServerStatus{
		{Endpoint: "dns/fast", Serial: omitted.Spec.Serial, ContentDigest: omitted.Spec.ContentDigest, ObservedAt: metav1.NewTime(*now)},
		{Endpoint: "dns/slow", Serial: before.Spec.Serial, ContentDigest: before.Spec.ContentDigest, ObservedAt: metav1.NewTime(*now)},
	}
	if err := c.Status().Update(context.Background(), omitted); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(30 * time.Second)
	reconcileOnce(t, r, req)
	evaluator := r.Options.Evaluator.(*fakeEvaluator)
	if evaluator.inputs[len(evaluator.inputs)-1].Served {
		t.Fatal("fast replica alone authorized withdrawal")
	}
	serve(t, c, req, *now)
	_, unit = reconcileOnce(t, r, req)
	if !evaluator.inputs[len(evaluator.inputs)-1].Served {
		t.Fatal("all replicas past omission did not authorize snapshot term")
	}
	sibling := unit.Spec.RecordSets[0]
	sibling.RecordSetName, sibling.RecordSetUID, sibling.Name, sibling.DeletionRequested = "other", "other-uid", "other", false
	unit.Spec.RecordSets = append(unit.Spec.RecordSets, sibling)
	unit.Generation++
	if err := c.Update(context.Background(), unit); err != nil {
		t.Fatal(err)
	}
	_, unit = reconcileOnce(t, r, req)
	assertCondition(t, unit.Status.Zone.Conditions, "Programmed", metav1.ConditionFalse, "ServingPending")
	if !evaluator.inputs[len(evaluator.inputs)-1].Served {
		t.Fatal("unrelated pending revision retracted already served omission")
	}
}

func TestPolicyChangePreservesWithdrawalFloor(t *testing.T) {
	r, c, _, req := newFixture(t)
	_, unit := reconcileOnce(t, r, req)
	unit.Spec.Withdrawals = []dnsv1alpha1.ZoneUnitWithdrawal{claim(0)}
	unit.Generation++
	if err := c.Update(context.Background(), unit); err != nil {
		t.Fatal(err)
	}
	evaluator := r.Options.Evaluator.(*fakeEvaluator)
	evaluator.evaluate = func(in withdrawal.Input) (withdrawal.Decision, error) {
		return withdrawal.Decision{Withdrawn: metav1.ConditionFalse, Ready: metav1.ConditionFalse, Reason: "NegativeTTLNotElapsed", Message: "waiting", NextProbe: in.Now.Add(30 * time.Second), State: &withdrawal.State{Identity: "bound", Views: []string{"recursive/192.0.2.53:53"}, SOAMinimum: 300}}, nil
	}
	reconcileOnce(t, r, req)
	class := fixtureClass()
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(class), class); err != nil {
		t.Fatal(err)
	}
	class.Spec.Parameters.Raw = []byte(`{"nameServers":[{"name":"ns.example.test","addresses":["192.0.2.53"]}],"verification":{"views":["192.0.2.54:53"]}}`)
	if err := c.Update(context.Background(), class); err != nil {
		t.Fatal(err)
	}
	_, unit = reconcileOnce(t, r, req)
	last := evaluator.inputs[len(evaluator.inputs)-1]
	if last.Previous == nil || len(last.Previous.Views) != 1 || last.Previous.Views[0] != "recursive/192.0.2.53:53" {
		t.Fatalf("policy floor discarded: %+v", last.Previous)
	}
	assertCondition(t, unit.Status.Withdrawals[0].Conditions, "Withdrawn", metav1.ConditionUnknown, "PolicyChanged")
}
