package zoneunit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/appthrust/dns-api/internal/go/core/dnsprobe"
	"github.com/appthrust/dns-api/internal/go/providers/hosted/manifest"
	"github.com/appthrust/dns-api/internal/go/providers/hosted/withdrawal"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	hostedv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/hosted/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

// controller-runtime v0.23's fake does not implement CRD SSA. This adapter
// exercises the exact production Apply boundary and retains independent status.
// API-server merge ownership is intentionally not claimed by these fake tests.
type snapshotClient struct {
	client.Client
	applies int
	reject  error
	t       *testing.T
}

func (c *snapshotClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	snapshot, ok := obj.(*hostedv1alpha1.HostedZone)
	if !ok || patch.Type() != types.ApplyPatchType {
		return c.Client.Patch(ctx, obj, patch, opts...)
	}
	c.applies++
	options := &client.PatchOptions{}
	for _, option := range opts {
		option.ApplyToPatch(options)
	}
	if options.FieldManager != "hosted-controller" {
		c.t.Fatalf("fieldManager=%q", options.FieldManager)
	}
	if len(snapshot.Status.Servers) != 0 {
		c.t.Fatal("controller applied server status")
	}
	if c.reject != nil {
		return c.reject
	}
	var live hostedv1alpha1.HostedZone
	err := c.Client.Get(ctx, client.ObjectKeyFromObject(obj), &live)
	if apierrors.IsNotFound(err) {
		return c.Client.Create(ctx, obj)
	}
	if err != nil {
		return err
	}
	snapshot.ResourceVersion = live.ResourceVersion
	snapshot.Status = live.Status
	return c.Client.Update(ctx, obj)
}

type fakeResolver struct {
	calls  int
	answer func(dnsprobe.ProbeRequest) (dnsprobe.ProbeResult, error)
}

func (f *fakeResolver) Probe(_ context.Context, in dnsprobe.ProbeRequest) (dnsprobe.ProbeResult, error) {
	f.calls++
	if f.answer != nil {
		return f.answer(in)
	}
	return dnsprobe.ProbeResult{}, nil
}

func (f *fakeResolver) EffectivePolicy(policy dnsprobe.Policy) (dnsprobe.Policy, error) {
	if policy.Mode == "" || policy.Mode == "Cluster" {
		policy.Mode = "Cluster"
		policy.Servers = []string{"192.0.2.53:53"}
	}
	return policy, nil
}

type fakeEvaluator struct {
	inputs   []withdrawal.Input
	evaluate func(withdrawal.Input) (withdrawal.Decision, error)
}

func (f *fakeEvaluator) Evaluate(_ context.Context, in withdrawal.Input) (withdrawal.Decision, error) {
	f.inputs = append(f.inputs, in)
	if f.evaluate != nil {
		return f.evaluate(in)
	}
	return withdrawal.Decision{Withdrawn: metav1.ConditionFalse, Ready: metav1.ConditionFalse, Reason: "NegativeTTLNotElapsed", Message: "cache horizon pending", NextProbe: in.Now.Add(30 * time.Second), State: &withdrawal.State{Identity: "test", LastProbe: in.Now}, RequiredViews: []string{"recursive/192.0.2.53:53"}, CheckedNames: []string{"www.example.test"}}, nil
}
func fixtureClass() *dnsv1alpha1.ZoneClass {
	return &dnsv1alpha1.ZoneClass{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "hosted", UID: "class-1", Generation: 1}, Spec: dnsv1alpha1.ZoneClassSpec{ControllerName: hostedv1alpha1.ControllerName, Provider: dnsv1alpha1.ProviderReference{Name: hostedv1alpha1.ProviderName, Version: hostedv1alpha1.ProviderVersion}, Parameters: runtime.RawExtension{Raw: []byte(`{"nameServers":[{"name":"ns.example.test","addresses":["192.0.2.53"]}],"verification":{"views":["192.0.2.53:53"]}}`)}}, Status: dnsv1alpha1.ZoneClassStatus{Conditions: []metav1.Condition{{Type: "Accepted", Status: metav1.ConditionTrue, Reason: "Accepted", ObservedGeneration: 1, LastTransitionTime: metav1.NewTime(time.Unix(1, 0))}}}}
}
func fixtureUnit() *dnsv1alpha1.ZoneUnit {
	ttl := int32(120)
	return &dnsv1alpha1.ZoneUnit{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "zone", UID: "unit-1", Generation: 1}, Spec: dnsv1alpha1.ZoneUnitSpec{Provider: dnsv1alpha1.ProviderReference{Name: hostedv1alpha1.ProviderName, Version: hostedv1alpha1.ProviderVersion}, Zone: dnsv1alpha1.ZoneUnitZoneSpec{Ref: dnsv1alpha1.ObjectReference{Namespace: "app", Name: "zone"}, DomainName: "example.test", ObservedGeneration: 1, ZoneClassRef: dnsv1alpha1.ObjectReference{Namespace: "app", Name: "hosted"}, ZoneClassUID: "class-1", ZoneClassObservedGeneration: 1}, RecordSets: []dnsv1alpha1.ZoneUnitRecordSetSpec{{RecordSetNamespace: "app", RecordSetName: "www", RecordSetUID: "record-1", ObservedGeneration: 1, Name: "www", Type: dnsv1alpha1.RecordTypeA, TTL: &ttl, A: &dnsv1alpha1.ARecordSet{Addresses: []string{"192.0.2.10"}}}}}}
}
func newFixture(t *testing.T) (*ZoneReconciler, *snapshotClient, *time.Time, ctrl.Request) {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{dnsv1alpha1.AddToScheme, hostedv1alpha1.AddToScheme, corev1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	provider, err := manifest.Provider()
	if err != nil {
		t.Fatal(err)
	}
	c := &snapshotClient{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(provider, fixtureClass(), fixtureUnit()).WithStatusSubresource(&dnsv1alpha1.ZoneUnit{}, &dnsv1alpha1.ZoneClass{}, &hostedv1alpha1.HostedZone{}).Build(), t: t}
	now := time.Unix(1000, 0).UTC()
	r, err := NewReconciler(c, Options{Resolver: &fakeResolver{}, Evaluator: &fakeEvaluator{}, DefaultViews: []string{"192.0.2.53:53"}, Clock: func() time.Time { return now }, Jitter: func() float64 { return 0 }})
	if err != nil {
		t.Fatal(err)
	}
	return r, c, &now, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(fixtureUnit())}
}
func reconcileOnce(t *testing.T, r *ZoneReconciler, req ctrl.Request) (ctrl.Result, *dnsv1alpha1.ZoneUnit) {
	t.Helper()
	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	var unit dnsv1alpha1.ZoneUnit
	if err := r.Get(context.Background(), req.NamespacedName, &unit); err != nil {
		t.Fatal(err)
	}
	return result, &unit
}
func snapshotFor(t *testing.T, c client.Client, req ctrl.Request) *hostedv1alpha1.HostedZone {
	t.Helper()
	var snapshot hostedv1alpha1.HostedZone
	if err := c.Get(context.Background(), req.NamespacedName, &snapshot); err != nil {
		t.Fatal(err)
	}
	return &snapshot
}
func serve(t *testing.T, c client.Client, req ctrl.Request, now time.Time) {
	t.Helper()
	snapshot := snapshotFor(t, c, req)
	snapshot.Status.Servers = []hostedv1alpha1.HostedZoneServerStatus{{Endpoint: "dns/server", Serial: snapshot.Spec.Serial, ContentDigest: snapshot.Spec.ContentDigest, ObservedAt: metav1.NewTime(now)}}
	if err := c.Status().Update(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
}
func assertCondition(t *testing.T, conditions []metav1.Condition, kind string, status metav1.ConditionStatus, reason string) {
	t.Helper()
	got := meta.FindStatusCondition(conditions, kind)
	if got == nil || got.Status != status || got.Reason != reason {
		t.Fatalf("%s=%+v want %s/%s", kind, got, status, reason)
	}
}
func TestServingPendingProgrammedAndStableSerial(t *testing.T) {
	r, c, now, req := newFixture(t)
	_, unit := reconcileOnce(t, r, req)
	assertCondition(t, unit.Status.Zone.Conditions, "Programmed", metav1.ConditionFalse, "ServingPending")
	snapshot := snapshotFor(t, c, req)
	if !ownedSnapshot(snapshot, unit) {
		t.Fatal("snapshot owner missing")
	}
	if !slices.Equal(unit.Status.Zone.NameServers, []string{"ns.example.test"}) {
		t.Fatal("nameservers missing")
	}
	if got := meta.FindStatusCondition(unit.Status.Zone.Conditions, "Programmed").Message; got != pendingMessage(snapshot.Spec.Serial) {
		t.Fatal(got)
	}
	serve(t, c, req, *now)
	_, unit = reconcileOnce(t, r, req)
	assertCondition(t, unit.Status.Zone.Conditions, "Programmed", metav1.ConditionTrue, "Programmed")
	assertCondition(t, unit.Status.RecordSets[0].Conditions, "Programmed", metav1.ConditionTrue, "Programmed")
	rv := unit.ResourceVersion
	_, stable := reconcileOnce(t, r, req)
	if stable.ResourceVersion != rv || c.applies != 1 {
		t.Fatalf("no-op changed rv/apply count: %s/%s %d\nbefore=%#v\nafter=%#v", rv, stable.ResourceVersion, c.applies, unit.Status, stable.Status)
	}
	sibling := unit.Spec.RecordSets[0]
	sibling.RecordSetName = "other"
	sibling.RecordSetUID = "other-uid"
	sibling.Name = "other"
	unit.Spec.RecordSets = append(unit.Spec.RecordSets, sibling)
	unit.Generation++
	if err := c.Update(context.Background(), unit); err != nil {
		t.Fatal(err)
	}
	_, unit = reconcileOnce(t, r, req)
	assertCondition(t, unit.Status.Zone.Conditions, "Programmed", metav1.ConditionFalse, "ServingPending")
	assertCondition(t, unit.Status.RecordSets[0].Conditions, "Programmed", metav1.ConditionTrue, "Programmed")
	assertCondition(t, unit.Status.RecordSets[1].Conditions, "Programmed", metav1.ConditionFalse, "ServingPending")
	if got := snapshotFor(t, c, req).Spec.Serial; got != snapshot.Spec.Serial+1 {
		t.Fatalf("serial=%d", got)
	}
}
func TestDeletionCompletedRequiresServedOmission(t *testing.T) {
	r, c, now, req := newFixture(t)
	reconcileOnce(t, r, req)
	serve(t, c, req, *now)
	_, unit := reconcileOnce(t, r, req)
	unit.Spec.RecordSets[0].DeletionRequested = true
	unit.Generation++
	if err := c.Update(context.Background(), unit); err != nil {
		t.Fatal(err)
	}
	_, unit = reconcileOnce(t, r, req)
	if unit.Status.RecordSets[0].DeletionCompleted {
		t.Fatal("unserved omission completed cleanup")
	}
	snapshot := snapshotFor(t, c, req)
	for _, record := range snapshot.Spec.Records {
		if record.Name == "www" {
			t.Fatal("ledger record retained")
		}
	}
	serve(t, c, req, *now)
	_, unit = reconcileOnce(t, r, req)
	if !unit.Status.RecordSets[0].DeletionCompleted {
		t.Fatal("served omission did not complete cleanup")
	}
	unit.Spec.RecordSets[0].RecordSetUID = "recreated"
	unit.Spec.RecordSets[0].DeletionRequested = false
	unit.Generation++
	if err := c.Update(context.Background(), unit); err != nil {
		t.Fatal(err)
	}
	_, unit = reconcileOnce(t, r, req)
	if unit.Status.RecordSets[0].DeletionCompleted {
		t.Fatal("cleanup leaked across UID")
	}
}
func TestSnapshotRejectionAndShadowDeletion(t *testing.T) {
	r, c, _, req := newFixture(t)
	c.reject = errors.New(strings.Repeat("拒", 1000))
	_, unit := reconcileOnce(t, r, req)
	assertCondition(t, unit.Status.Zone.Conditions, "Programmed", metav1.ConditionFalse, "SnapshotRejected")
	if len(meta.FindStatusCondition(unit.Status.Zone.Conditions, "Programmed").Message) > 2048 {
		t.Fatal("unbounded API error")
	}
	c.reject = nil
	reconcileOnce(t, r, req)
	snapshot := snapshotFor(t, c, req)
	snapshot.OwnerReferences[0].UID = "foreign"
	if err := c.Update(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	_, unit = reconcileOnce(t, r, req)
	assertCondition(t, unit.Status.Zone.Conditions, "Programmed", metav1.ConditionFalse, "SnapshotRejected")
	if err := c.Get(context.Background(), req.NamespacedName, &hostedv1alpha1.HostedZone{}); !apierrors.IsNotFound(err) {
		t.Fatalf("shadow not deleted: %v", err)
	}
	reconcileOnce(t, r, req)
	if !ownedSnapshot(snapshotFor(t, c, req), unit) {
		t.Fatal("shadow did not recover")
	}
}
func TestStaleServersIgnoredWithoutStatusMutation(t *testing.T) {
	r, c, now, req := newFixture(t)
	reconcileOnce(t, r, req)
	serve(t, c, req, *now)
	snapshot := snapshotFor(t, c, req)
	version := snapshot.ResourceVersion
	*now = now.Add(ServerFreshness + time.Second)
	_, unit := reconcileOnce(t, r, req)
	assertCondition(t, unit.Status.Zone.Conditions, "Programmed", metav1.ConditionFalse, "ServingPending")
	snapshot = snapshotFor(t, c, req)
	if len(snapshot.Status.Servers) != 1 || snapshot.ResourceVersion != version {
		t.Fatal("controller mutated server-owned status")
	}
	var data zoneData
	if err := json.Unmarshal(unit.Status.Zone.Provider.Data.Raw, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Servers) != 0 {
		t.Fatal("stale server included in projection")
	}
	for i := range 70 {
		snapshot.Status.Servers = append(snapshot.Status.Servers, hostedv1alpha1.HostedZoneServerStatus{Endpoint: fmt.Sprintf("dns/pod-%03d", i), Serial: snapshot.Spec.Serial, ContentDigest: snapshot.Spec.ContentDigest, ObservedAt: metav1.NewTime(*now)})
	}
	if err := c.Status().Update(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	_, unit = reconcileOnce(t, r, req)
	_ = json.Unmarshal(unit.Status.Zone.Provider.Data.Raw, &data)
	if len(data.Servers) != 64 {
		t.Fatalf("preview=%d", len(data.Servers))
	}
	assertCondition(t, unit.Status.Zone.Conditions, "Programmed", metav1.ConditionTrue, "Programmed")
}
func TestInvalidParametersAndDeletingUnit(t *testing.T) {
	r, c, _, req := newFixture(t)
	_, unit := reconcileOnce(t, r, req)
	class := fixtureClass()
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(class), class); err != nil {
		t.Fatal(err)
	}
	class.Spec.Parameters.Raw = []byte(`{"nameServers":[]}`)
	if err := c.Update(context.Background(), class); err != nil {
		t.Fatal(err)
	}
	_, unit = reconcileOnce(t, r, req)
	assertCondition(t, unit.Status.Zone.Conditions, "Accepted", metav1.ConditionFalse, "InvalidProviderPayload")
	unit.Finalizers = []string{"test/retain"}
	if err := c.Update(context.Background(), unit); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(context.Background(), unit); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, r, req)
	if err := c.Get(context.Background(), req.NamespacedName, &hostedv1alpha1.HostedZone{}); !apierrors.IsNotFound(err) {
		t.Fatalf("deleting unit retained snapshot: %v", err)
	}
}
func TestSnapshotWatchPredicate(t *testing.T) {
	old := &hostedv1alpha1.HostedZone{ObjectMeta: metav1.ObjectMeta{Generation: 1}}
	current := old.DeepCopy()
	p := snapshotPredicate()
	if p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: current}) {
		t.Fatal("unchanged status enqueued")
	}
	current.Status.Servers = []hostedv1alpha1.HostedZoneServerStatus{{Endpoint: "dns/server", Serial: 1}}
	if !p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: current}) {
		t.Fatal("serving acknowledgement lost")
	}
	old = current.DeepCopy()
	current.Status.Servers[0].ObservedAt = metav1.NewTime(time.Unix(2, 0))
	if !p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: current}) {
		t.Fatal("heartbeat affects staleness and must enqueue")
	}
}

func TestEveryFreshServerMustAcknowledge(t *testing.T) {
	r, c, now, req := newFixture(t)
	_, unit := reconcileOnce(t, r, req)
	first := snapshotFor(t, c, req)
	serve(t, c, req, *now)
	_, unit = reconcileOnce(t, r, req)
	unit.Spec.RecordSets[0].A.Addresses = []string{"192.0.2.20"}
	unit.Spec.RecordSets[0].ObservedGeneration++
	unit.Generation++
	if err := c.Update(context.Background(), unit); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, r, req)
	current := snapshotFor(t, c, req)
	current.Status.Servers = []hostedv1alpha1.HostedZoneServerStatus{
		{Endpoint: "dns/fast", Serial: current.Spec.Serial, ContentDigest: current.Spec.ContentDigest, ObservedAt: metav1.NewTime(*now)},
		{Endpoint: "dns/slow", Serial: first.Spec.Serial, ContentDigest: first.Spec.ContentDigest, ObservedAt: metav1.NewTime(*now)},
	}
	if err := c.Status().Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	_, unit = reconcileOnce(t, r, req)
	assertCondition(t, unit.Status.Zone.Conditions, "Programmed", metav1.ConditionFalse, "ServingPending")
	assertCondition(t, unit.Status.RecordSets[0].Conditions, "Programmed", metav1.ConditionFalse, "ServingPending")
	current = snapshotFor(t, c, req)
	current.Status.Servers[1].Serial = current.Spec.Serial
	current.Status.Servers[1].ContentDigest = current.Spec.ContentDigest
	if err := c.Status().Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	_, unit = reconcileOnce(t, r, req)
	assertCondition(t, unit.Status.Zone.Conditions, "Programmed", metav1.ConditionTrue, "Programmed")
	assertCondition(t, unit.Status.RecordSets[0].Conditions, "Programmed", metav1.ConditionTrue, "Programmed")
}

func TestOrphanSnapshotDeleted(t *testing.T) {
	r, c, _, req := newFixture(t)
	orphan := &hostedv1alpha1.HostedZone{ObjectMeta: metav1.ObjectMeta{Namespace: req.Namespace, Name: "orphan"}}
	if err := c.Create(context.Background(), orphan); err != nil {
		t.Fatal(err)
	}
	orphanRequest := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(orphan)}
	if _, err := r.Reconcile(context.Background(), orphanRequest); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), orphanRequest.NamespacedName, &hostedv1alpha1.HostedZone{}); !apierrors.IsNotFound(err) {
		t.Fatalf("orphan retained: %v", err)
	}
}

func TestDefaultDelegationAlwaysReported(t *testing.T) {
	r, _, _, req := newFixture(t)
	resolver := r.Options.Resolver.(*fakeResolver)
	resolver.answer = func(in dnsprobe.ProbeRequest) (dnsprobe.ProbeResult, error) {
		if in.Policy.Mode != "Cluster" || len(in.Policy.Servers) != 1 {
			t.Fatalf("default delegation policy=%+v", in.Policy)
		}
		// Shape of a real probe: the recursive view, then the authority view
		// reached through glue; both must agree and the authority answers AA.
		return dnsprobe.ProbeResult{Zone: &dnsprobe.ZoneEvidence{
			Zone:        in.Zone,
			Authorities: []string{"192.0.2.10:53"},
			Views: []dnsprobe.AuthorityObservation{
				{ID: "recursive/" + in.Policy.Servers[0], Nameservers: []string{"NS.EXAMPLE.TEST."}, SOAOwner: in.Zone},
				{ID: "authority/192.0.2.10:53", Nameservers: []string{"ns.example.test"}, SOAOwner: in.Zone, Authoritative: true},
			},
		}}, nil
	}
	_, unit := reconcileOnce(t, r, req)
	var data zoneData
	if err := json.Unmarshal(unit.Status.Zone.Provider.Data.Raw, &data); err != nil {
		t.Fatal(err)
	}
	if data.Delegation == nil || !data.Delegation.Matches || data.Delegation.Error != "" || !slices.Equal(data.Delegation.ObservedNameServers, []string{"ns.example.test"}) {
		t.Fatalf("delegation=%+v", data.Delegation)
	}
}
