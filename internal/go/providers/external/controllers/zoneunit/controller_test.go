package zoneunit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	probe "github.com/appthrust/dns-api/internal/go/core/dnsprobe"
	"github.com/appthrust/dns-api/internal/go/providers/external/manifest"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	externalv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/external/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type fakeResolver struct {
	requests []probe.ProbeRequest
	answer   func(probe.ProbeRequest) (probe.ProbeResult, error)
}

func (r *fakeResolver) Probe(_ context.Context, request probe.ProbeRequest) (probe.ProbeResult, error) {
	r.requests = append(r.requests, request)
	return r.answer(request)
}

func matching(request probe.ProbeRequest) (probe.ProbeResult, error) {
	ttl := uint32(120)
	view := probe.View{ID: "recursive/192.0.2.53:53", A: request.Expected.A, AAAA: request.Expected.AAAA, TTL: &ttl}
	if request.Expected.CNAME != "" {
		view.Chain = []string{request.Expected.CNAME}
		view.A = []string{"192.0.2.10"}
	}
	return probe.ProbeResult{Views: []probe.View{view}}, nil
}

func fixtureUnit(count int) *dnsv1alpha1.ZoneUnit {
	unit := &dnsv1alpha1.ZoneUnit{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "example", UID: "unit-1", Generation: 1}, Spec: dnsv1alpha1.ZoneUnitSpec{
		Provider: dnsv1alpha1.ProviderReference{Name: externalv1alpha1.ProviderName, Version: externalv1alpha1.ProviderVersion},
		Zone:     dnsv1alpha1.ZoneUnitZoneSpec{Ref: dnsv1alpha1.ObjectReference{Namespace: "app", Name: "example"}, ObservedGeneration: 1, DomainName: "example.test", ZoneClassRef: dnsv1alpha1.ObjectReference{Namespace: "app", Name: "external"}},
	}}
	ttl := int32(300)
	for i := range count {
		name := fmt.Sprintf("host-%03d", i)
		unit.Spec.RecordSets = append(unit.Spec.RecordSets, dnsv1alpha1.ZoneUnitRecordSetSpec{RecordSetNamespace: "app", RecordSetName: name, RecordSetUID: types.UID(name), ObservedGeneration: 1, Name: name, Type: dnsv1alpha1.RecordTypeA, TTL: &ttl, A: &dnsv1alpha1.ARecordSet{Addresses: []string{"192.0.2.10"}}})
	}
	return unit
}

func fixtureClass() *dnsv1alpha1.ZoneClass {
	return &dnsv1alpha1.ZoneClass{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "external", UID: "class-1", Generation: 1}, Spec: dnsv1alpha1.ZoneClassSpec{
		Provider: dnsv1alpha1.ProviderReference{Name: externalv1alpha1.ProviderName, Version: externalv1alpha1.ProviderVersion}, ControllerName: externalv1alpha1.ControllerName,
		Parameters: runtime.RawExtension{Raw: []byte(`{"resolver":{"mode":"Recursive","servers":["192.0.2.53:53"]}}`)},
	}, Status: dnsv1alpha1.ZoneClassStatus{Conditions: []metav1.Condition{{Type: "Accepted", Status: metav1.ConditionTrue, Reason: "Accepted", ObservedGeneration: 1, LastTransitionTime: metav1.NewTime(time.Unix(1, 0))}}}}
}

func newFixture(t *testing.T, count int, resolver *fakeResolver) (*ZoneReconciler, client.Client, *time.Time, ctrl.Request) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := dnsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	provider, err := manifest.Provider()
	if err != nil {
		t.Fatal(err)
	}
	provider.UID = "provider-1"
	unit := fixtureUnit(count)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(provider, fixtureClass(), unit, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "app"}}).WithStatusSubresource(&dnsv1alpha1.ZoneUnit{}, &dnsv1alpha1.ZoneClass{}).Build()
	now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	reconciler, err := NewReconciler(c, Options{Resolver: resolver, Clock: func() time.Time { return now }, Jitter: func() float64 { return 0 }})
	if err != nil {
		t.Fatal(err)
	}
	return reconciler, c, &now, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(unit)}
}

func reconcileOnce(t *testing.T, r *ZoneReconciler, request ctrl.Request) (ctrl.Result, dnsv1alpha1.ZoneUnit) {
	t.Helper()
	result, err := r.Reconcile(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	var unit dnsv1alpha1.ZoneUnit
	if err := r.Get(context.Background(), request.NamespacedName, &unit); err != nil {
		t.Fatal(err)
	}
	return result, unit
}

func assertProgrammed(t *testing.T, conditions []metav1.Condition, status metav1.ConditionStatus, reason string) *metav1.Condition {
	t.Helper()
	condition := meta.FindStatusCondition(conditions, "Programmed")
	if condition == nil || condition.Status != status || condition.Reason != reason {
		t.Fatalf("Programmed = %#v, want %s/%s", condition, status, reason)
	}
	return condition
}

func TestMismatchRecoveryDriftAndNoStatusLoop(t *testing.T) {
	resolver := &fakeResolver{answer: func(request probe.ProbeRequest) (probe.ProbeResult, error) {
		result, err := matching(request)
		result.Views[0].A = []string{"192.0.2.99"}
		return result, err
	}}
	r, _, now, request := newFixture(t, 1, resolver)
	_, first := reconcileOnce(t, r, request)
	assertProgrammed(t, first.Status.Zone.Conditions, metav1.ConditionTrue, "VerificationConfigured")
	assertProgrammed(t, first.Status.RecordSets[0].Conditions, metav1.ConditionFalse, "ResolutionMismatch")
	_, unchanged := reconcileOnce(t, r, request)
	if len(resolver.requests) != 1 || unchanged.ResourceVersion != first.ResourceVersion {
		t.Fatalf("unchanged status triggered work or a status patch: probes=%d (want 1), resourceVersion=%q -> %q", len(resolver.requests), first.ResourceVersion, unchanged.ResourceVersion)
	}
	resolver.answer = matching
	*now = now.Add(30 * time.Second)
	_, ready := reconcileOnce(t, r, request)
	condition := assertProgrammed(t, ready.Status.RecordSets[0].Conditions, metav1.ConditionTrue, "ResolutionVerified")
	transition := condition.LastTransitionTime
	*now = now.Add(120 * time.Second)
	_, readyAgain := reconcileOnce(t, r, request)
	if !assertProgrammed(t, readyAgain.Status.RecordSets[0].Conditions, metav1.ConditionTrue, "ResolutionVerified").LastTransitionTime.Equal(&transition) {
		t.Fatal("success timestamp changed transition time")
	}
	resolver.answer = func(probe.ProbeRequest) (probe.ProbeResult, error) {
		return probe.ProbeResult{}, errors.New("private raw transport details")
	}
	*now = now.Add(120 * time.Second)
	_, failed := reconcileOnce(t, r, request)
	assertProgrammed(t, failed.Status.RecordSets[0].Conditions, metav1.ConditionUnknown, "DNSProbeFailed")
	assertProgrammed(t, failed.Status.Conditions, metav1.ConditionUnknown, "ProviderUnavailable")
	if failed.Status.RecordSets[0].RecordSetUID != "host-000" || failed.Status.RecordSets[0].ObservedGeneration != 1 {
		t.Fatal("unbound receipt")
	}
}

func TestPolicyAndSiblingInvalidation(t *testing.T) {
	resolver := &fakeResolver{answer: matching}
	r, c, _, request := newFixture(t, 1, resolver)
	reconcileOnce(t, r, request)
	var class dnsv1alpha1.ZoneClass
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "app", Name: "external"}, &class); err != nil {
		t.Fatal(err)
	}
	class.Spec.Parameters.Raw = []byte(`{"resolver":{"mode":"Recursive","servers":["192.0.2.54:53"]}}`)
	if err := c.Update(context.Background(), &class); err != nil {
		t.Fatal(err)
	}
	resolver.answer = func(request probe.ProbeRequest) (probe.ProbeResult, error) {
		result, err := matching(request)
		result.Views[0].ID = "recursive/192.0.2.54:53"
		return result, err
	}
	_, unit := reconcileOnce(t, r, request)
	if len(resolver.requests) != 2 {
		t.Fatal("policy change retained previous success")
	}
	family := unit.Spec.RecordSets[0]
	family.RecordSetName, family.RecordSetUID, family.Type, family.A, family.AAAA = "v6", "v6-uid", dnsv1alpha1.RecordTypeAAAA, nil, &dnsv1alpha1.AAAARecordSet{Addresses: []string{"2001:db8::1"}}
	unit.Spec.RecordSets = append(unit.Spec.RecordSets, family)
	unit.Generation++
	if err := c.Update(context.Background(), &unit); err != nil {
		t.Fatal(err)
	}
	_, got := reconcileOnce(t, r, request)
	if len(resolver.requests) != 3 || len(resolver.requests[2].Expected.AAAA) != 1 {
		t.Fatal("sibling family did not invalidate combined observation")
	}
	for _, receipt := range got.Status.RecordSets {
		assertProgrammed(t, receipt.Conditions, metav1.ConditionTrue, "ResolutionVerified")
	}
}

func TestBoundedFairPassAndDeletion(t *testing.T) {
	resolver := &fakeResolver{answer: matching}
	r, c, now, request := newFixture(t, 65, resolver)
	result, unit := reconcileOnce(t, r, request)
	if len(resolver.requests) != 32 || result.RequeueAfter != time.Second {
		t.Fatalf("requests=%d result=%#v", len(resolver.requests), result)
	}
	assertProgrammed(t, unit.Status.RecordSets[64].Conditions, metav1.ConditionUnknown, "Reconciling")
	*now = now.Add(time.Second)
	reconcileOnce(t, r, request)
	*now = now.Add(time.Second)
	_, unit = reconcileOnce(t, r, request)
	if len(resolver.requests) != 65 {
		t.Fatalf("requests=%d, want each name once", len(resolver.requests))
	}
	unit.Spec.RecordSets[0].DeletionRequested = true
	denied := false
	unit.Spec.RecordSets[1].Allowed = &denied
	unit.Generation++
	if err := c.Update(context.Background(), &unit); err != nil {
		t.Fatal(err)
	}
	_, got := reconcileOnce(t, r, request)
	if !got.Status.RecordSets[0].DeletionCompleted || got.Status.RecordSets[0].RecordSetUID != unit.Spec.RecordSets[0].RecordSetUID {
		t.Fatal("deletion receipt missing incarnation")
	}
	if len(resolver.requests) != 65 {
		t.Fatal("denied/deleted names were probed")
	}
	unit = got
	unit.Spec.RecordSets[0].RecordSetUID = "replacement"
	unit.Spec.RecordSets[0].DeletionRequested = false
	unit.Generation++
	if err := c.Update(context.Background(), &unit); err != nil {
		t.Fatal(err)
	}
	_, got = reconcileOnce(t, r, request)
	if got.Status.RecordSets[0].DeletionCompleted || got.Status.RecordSets[0].RecordSetUID != "replacement" {
		t.Fatal("old deletion finalized replacement")
	}
}

func TestStaleAsynchronousResultsDiscarded(t *testing.T) {
	resolver := &fakeResolver{}
	r, c, _, request := newFixture(t, 1, resolver)
	resolver.answer = func(input probe.ProbeRequest) (probe.ProbeResult, error) {
		var unit dnsv1alpha1.ZoneUnit
		if err := c.Get(context.Background(), request.NamespacedName, &unit); err != nil {
			t.Fatal(err)
		}
		unit.Spec.RecordSets[0].ObservedGeneration++
		unit.Spec.RecordSets[0].A.Addresses = []string{"192.0.2.20"}
		unit.Generation++
		if err := c.Update(context.Background(), &unit); err != nil {
			t.Fatal(err)
		}
		return matching(input)
	}
	result, unit := reconcileOnce(t, r, request)
	if result.RequeueAfter != time.Second || len(unit.Status.RecordSets) != 0 {
		t.Fatalf("stale result committed: %#v", unit.Status)
	}
}

func TestWildcardWitnessAndBackoff(t *testing.T) {
	unit := fixtureUnit(2)
	unit.Spec.RecordSets[0].Name, unit.Spec.RecordSets[1].Name = "*.project", "*.project"
	first := groupNames(unit, "policy-a")[0]
	slices.Reverse(unit.Spec.RecordSets)
	second := groupNames(unit, "policy-b")[0]
	if first.CheckedName != second.CheckedName || !first.SampledWildcard || first.Fingerprint == second.Fingerprint {
		t.Fatal("unstable witness or policy fingerprint")
	}
	unit.Spec.RecordSets[0].ObservedGeneration++
	if groupNames(unit, "policy-b")[0].CheckedName == first.CheckedName {
		t.Fatal("changed intent reused witness")
	}
	now := time.Unix(100, 0)
	state := schedule{}
	failure := probe.Failed("www.example", "view", "timeout")
	for _, want := range []time.Duration{30, 60, 120, 300, 300} {
		state = advance(state, "same", failure, now, 0)
		if state.Next.Sub(now) != want*time.Second {
			t.Fatalf("backoff=%v want=%v", state.Next.Sub(now), want)
		}
	}
	state = advance(state, "changed", failure, now, 0)
	if state.Failures != 1 || state.Next.Sub(now) != 30*time.Second {
		t.Fatal("changed input did not reset backoff")
	}
	zero, high, minute := uint32(0), uint32(3600), uint32(60)
	for _, tc := range []struct {
		decision probe.Decision
		jitter   float64
		want     time.Duration
	}{
		{probe.Decision{Status: metav1.ConditionTrue, TTL: &zero}, -1, 30 * time.Second},
		{probe.Decision{Status: metav1.ConditionTrue, TTL: &high}, 1, 300 * time.Second},
		{probe.Decision{Status: metav1.ConditionFalse, Reason: "DNSNoData", NegativeTTL: &minute}, 0, time.Minute},
		{probe.Decision{Status: metav1.ConditionTrue, TTL: &minute}, 1, 66 * time.Second},
	} {
		if got := nextInterval(tc.decision, 0, tc.jitter); got != tc.want {
			t.Fatalf("interval=%v want=%v", got, tc.want)
		}
	}
}

func TestAggregateAllowlistAndFalsePrecedence(t *testing.T) {
	unit := fixtureUnit(2)
	prepareReceipts(unit)
	now := time.Unix(100, 0)
	setCondition(&unit.Status.Zone.Conditions, "Programmed", metav1.ConditionTrue, "VerificationConfigured", "", 1, now)
	setCondition(&unit.Status.RecordSets[0].Conditions, "Programmed", metav1.ConditionUnknown, "DNSProbeFailed", "", 1, now)
	setCondition(&unit.Status.RecordSets[1].Conditions, "Programmed", metav1.ConditionFalse, "DNSNameNotFound", "", 1, now)
	setAggregate(unit, now)
	assertProgrammed(t, unit.Status.Conditions, metav1.ConditionFalse, "ExternalResourceNotFound")
	for _, reason := range []string{"DNSNoData", "ResolutionPartial", "ResolutionMismatch", "ResolutionInconsistent", "NameserverMismatch"} {
		status, mapped := UnitReason(probe.Decision{Status: metav1.ConditionFalse, Reason: reason})
		if status != metav1.ConditionFalse || mapped != "ExternalResourceMismatch" {
			t.Fatalf("mapping=%s/%s", status, mapped)
		}
	}
	data := publicData(probe.Policy{Mode: probe.ModeRecursive}, "www.example", false, probe.Decision{}, now, now.Add(time.Minute))
	var public externalv1alpha1.ExternalVerificationData
	if err := json.Unmarshal(data.Data.Raw, &public); err != nil {
		t.Fatal(err)
	}
	if public.NegativeTTLSeconds != nil || public.NegativeExpiryTime != nil {
		t.Fatal("invented negative TTL")
	}
}

func TestZoneAuthorityEvidenceSharedAndBlocksRecords(t *testing.T) {
	resolver := &fakeResolver{}
	r, c, now, request := newFixture(t, 2, resolver)
	var class dnsv1alpha1.ZoneClass
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "app", Name: "external"}, &class); err != nil {
		t.Fatal(err)
	}
	class.Spec.Parameters.Raw = []byte(`{"resolver":{"mode":"Recursive","servers":["192.0.2.53:53"]},"expectedNameservers":["ns.example.test"]}`)
	if err := c.Update(context.Background(), &class); err != nil {
		t.Fatal(err)
	}
	wrong := false
	zoneCalls := 0
	resolver.answer = func(input probe.ProbeRequest) (probe.ProbeResult, error) {
		if input.ZoneOnly {
			zoneCalls++
			ns := "ns.example.test"
			if wrong {
				ns = "wrong.example.test"
			}
			return probe.ProbeResult{Zone: &probe.ZoneEvidence{
				Zone: input.Zone, PolicyFingerprint: probe.Fingerprint(input.Policy), Authorities: []string{"192.0.2.54:53"},
				Views: []probe.AuthorityObservation{
					{ID: "recursive/192.0.2.53:53", Nameservers: []string{ns}, SOAOwner: input.Zone},
					{ID: "authority/192.0.2.54:53", Nameservers: []string{ns}, SOAOwner: input.Zone, Authoritative: true},
				},
			}}, nil
		}
		if input.Evidence == nil {
			t.Fatal("per-Zone evidence was not shared")
		}
		result, err := matching(input)
		authority := result.Views[0]
		authority.ID, authority.Authoritative = "authority/192.0.2.54:53/via/192.0.2.53:53", true
		result.Views = append(result.Views, authority)
		return result, err
	}
	_, unit := reconcileOnce(t, r, request)
	if zoneCalls != 1 || len(resolver.requests) != 3 {
		t.Fatalf("zoneCalls=%d probes=%d", zoneCalls, len(resolver.requests))
	}
	assertProgrammed(t, unit.Status.Zone.Conditions, metav1.ConditionTrue, "NameserversVerified")
	for _, receipt := range unit.Status.RecordSets {
		assertProgrammed(t, receipt.Conditions, metav1.ConditionTrue, "ResolutionVerified")
	}
	wrong = true
	*now = now.Add(30 * time.Second)
	_, unit = reconcileOnce(t, r, request)
	if zoneCalls != 2 || len(resolver.requests) != 4 {
		t.Fatal("failed authority check still probed child records")
	}
	for _, receipt := range unit.Status.RecordSets {
		assertProgrammed(t, receipt.Conditions, metav1.ConditionFalse, "NameserverMismatch")
	}
	if len(unit.Status.Zone.NameServers) != 1 || unit.Status.Zone.NameServers[0] != "wrong.example.test" {
		t.Fatal("expected NS was published as observed evidence")
	}
}

func TestNamespacePolicyChangeInvalidatesSuccess(t *testing.T) {
	resolver := &fakeResolver{answer: matching}
	r, c, _, request := newFixture(t, 1, resolver)
	reconcileOnce(t, r, request)
	var namespace corev1.Namespace
	if err := c.Get(context.Background(), client.ObjectKey{Name: "app"}, &namespace); err != nil {
		t.Fatal(err)
	}
	namespace.Labels = map[string]string{"policy": "changed"}
	if err := c.Update(context.Background(), &namespace); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, r, request)
	if len(resolver.requests) != 2 {
		t.Fatal("namespace-policy fingerprint was not invalidated")
	}
}

func TestStaleConditionGenerationCannotUseScheduledSuccess(t *testing.T) {
	resolver := &fakeResolver{answer: matching}
	r, c, _, request := newFixture(t, 1, resolver)
	_, unit := reconcileOnce(t, r, request)
	for i := range unit.Status.RecordSets[0].Conditions {
		if unit.Status.RecordSets[0].Conditions[i].Type == "Programmed" {
			unit.Status.RecordSets[0].Conditions[i].ObservedGeneration = 0
		}
	}
	if err := c.Status().Update(context.Background(), &unit); err != nil {
		t.Fatal(err)
	}
	_, current := reconcileOnce(t, r, request)
	if len(resolver.requests) != 2 {
		t.Fatal("stale condition reused a future scheduling receipt")
	}
	condition := assertProgrammed(t, current.Status.RecordSets[0].Conditions, metav1.ConditionTrue, "ResolutionVerified")
	if condition.ObservedGeneration != 1 {
		t.Fatal("stale result was rebadged instead of reprobed")
	}
}

func TestUnchangedSchedulerJSONDoesNotPatch(t *testing.T) {
	for _, probeFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("probeFails=%t", probeFails), func(t *testing.T) {
			resolver := &fakeResolver{answer: matching}
			if probeFails {
				resolver.answer = func(probe.ProbeRequest) (probe.ProbeResult, error) {
					return probe.ProbeResult{}, errors.New("transport")
				}
			}
			r, c, now, request := newFixture(t, 1, resolver)
			_, first := reconcileOnce(t, r, request)

			// Model an API round-trip that changes JSON formatting/key order,
			// but neither desired inputs nor the persisted scheduling receipt.
			var state map[string]any
			if err := json.Unmarshal(first.Status.Provider.State.Raw, &state); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.MarshalIndent(state, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			first.Status.Provider.State.Raw = encoded
			if err := c.Status().Update(context.Background(), &first); err != nil {
				t.Fatal(err)
			}
			version := first.ResourceVersion
			for range 3 {
				*now = now.Add(time.Second)
				_, unchanged := reconcileOnce(t, r, request)
				if len(resolver.requests) != 1 || unchanged.ResourceVersion != version {
					t.Fatalf("unchanged scheduling receipt: probes=%d (want 1), resourceVersion=%q -> %q", len(resolver.requests), version, unchanged.ResourceVersion)
				}
			}
		})
	}
}
