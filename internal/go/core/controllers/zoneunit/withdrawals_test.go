package zoneunit

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func withdrawalFixture() (*dnsv1alpha1.Provider, *dnsv1alpha1.ZoneClass, *dnsv1alpha1.Zone, *dnsv1alpha1.RecordWithdrawal) {
	provider := route53ProviderForZoneUnitTest()
	provider.Spec.Versions[0].RecordWithdrawal = &dnsv1alpha1.ProviderRecordWithdrawal{Supported: true}
	class := route53ZoneClassForZoneUnitTest()
	class.Namespace = "app"
	class.Spec.IdentityRef = nil
	class.Spec.Parameters = runtime.RawExtension{Raw: []byte(`{}`)}
	z := zone("app", "example", "example.com")
	claim := &dnsv1alpha1.RecordWithdrawal{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "retired", UID: "claim-uid", Generation: 1, Labels: map[string]string{dnsv1alpha1.AddressLabelKey: "192.0.2.1"}}, Spec: dnsv1alpha1.RecordWithdrawalSpec{ZoneRef: dnsv1alpha1.ZoneReference{Name: z.Name}, Name: "www", Address: "192.0.2.1"}}
	return provider, class, z, claim
}

func TestWithdrawalCompositionAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*dnsv1alpha1.Provider, *dnsv1alpha1.Zone, *dnsv1alpha1.RecordWithdrawal)
		reason string
	}{
		{"accepted", func(*dnsv1alpha1.Provider, *dnsv1alpha1.Zone, *dnsv1alpha1.RecordWithdrawal) {}, "Accepted"},
		{"unsupported", func(p *dnsv1alpha1.Provider, _ *dnsv1alpha1.Zone, _ *dnsv1alpha1.RecordWithdrawal) {
			p.Spec.Versions[0].RecordWithdrawal = nil
		}, "UnsupportedProvider"},
		{"wildcard requires grant", func(_ *dnsv1alpha1.Provider, _ *dnsv1alpha1.Zone, c *dnsv1alpha1.RecordWithdrawal) { c.Spec.Name = "*" }, "NotAllowed"},
		{"invalid label", func(_ *dnsv1alpha1.Provider, _ *dnsv1alpha1.Zone, c *dnsv1alpha1.RecordWithdrawal) { c.Labels = nil }, "InvalidWithdrawal"},
		{"cross namespace denied", func(_ *dnsv1alpha1.Provider, _ *dnsv1alpha1.Zone, c *dnsv1alpha1.RecordWithdrawal) {
			c.Namespace = "tenant"
			c.Spec.ZoneRef.Namespace = ptr("app")
		}, "NotAllowed"},
		{"wildcard grant", func(_ *dnsv1alpha1.Provider, z *dnsv1alpha1.Zone, c *dnsv1alpha1.RecordWithdrawal) {
			c.Spec.Name = "*.apps"
			z.Spec.AllowedRecordSets = withdrawalGrant()
		}, "Accepted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, class, z, c := withdrawalFixture()
			tc.mutate(p, z, c)
			k8sClient := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(p, class, z, c, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "app", Labels: map[string]string{"dns": "allowed"}}}).WithStatusSubresource(&dnsv1alpha1.RecordWithdrawal{}, &dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneUnit{}, &dnsv1alpha1.RecordWithdrawalWaiver{}).Build()
			r := &ZoneUnitCompositionReconciler{Client: k8sClient}
			if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(z)}); err != nil {
				t.Fatal(err)
			}
			var got dnsv1alpha1.RecordWithdrawal
			if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(c), &got); err != nil {
				t.Fatal(err)
			}
			status := metav1.ConditionFalse
			if tc.reason == "Accepted" {
				status = metav1.ConditionTrue
			}
			assertCondition(t, got.Status.Conditions, "Accepted", status, tc.reason)
		})
	}
}

func withdrawalGrant() []dnsv1alpha1.AllowedRecordSet {
	return []dnsv1alpha1.AllowedRecordSet{{Namespaces: dnsv1alpha1.AllowedRecordSetNamespaces{Selector: metav1.LabelSelector{MatchLabels: map[string]string{"dns": "allowed"}}}, Records: []dnsv1alpha1.AllowedRecord{{Name: dnsv1alpha1.RecordNamePolicy{Pattern: `\*\.apps`}, Types: []dnsv1alpha1.RecordType{dnsv1alpha1.RecordTypeA}}}}}
}

func TestWithdrawalEnvtestCompositionProjectionWaiverAndRetention(t *testing.T) {
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	if assets == "" {
		t.Skip("KUBEBUILDER_ASSETS is required for the real CRD integration test")
	}
	environment := &envtest.Environment{BinaryAssetsDirectory: assets, CRDDirectoryPaths: []string{filepath.Join("..", "..", "..", "..", "..", "app", "operator", "config", "crd", "bases")}, ErrorIfCRDPathMissing: true, ControlPlaneStartTimeout: 30 * time.Second, ControlPlaneStopTimeout: 15 * time.Second}
	t.Cleanup(func() {
		if err := environment.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})
	cfg, err := environment.Start()
	if err != nil {
		t.Fatal(err)
	}
	scheme := testScheme(t)
	if err := endpointv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	p, class, z, claim := withdrawalFixture()
	claim.UID = ""
	claim.Generation = 0
	claim.Spec.Name = "*.apps"
	z.Spec.AllowedRecordSets = withdrawalGrant()
	for _, obj := range []client.Object{&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "app", Labels: map[string]string{"dns": "allowed"}}}, p, class, z, claim} {
		if err := c.Create(ctx, obj); err != nil {
			t.Fatalf("create %T: %v", obj, err)
		}
	}
	r := &ZoneUnitCompositionReconciler{Client: c}
	key := client.ObjectKeyFromObject(z)
	reconcile := func() {
		t.Helper()
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatal(err)
		}
	}
	readClaim := func() {
		t.Helper()
		if err := c.Get(ctx, client.ObjectKeyFromObject(claim), claim); err != nil {
			t.Fatal(err)
		}
	}
	readUnit := func() *dnsv1alpha1.ZoneUnit {
		t.Helper()
		u := &dnsv1alpha1.ZoneUnit{}
		if err := c.Get(ctx, key, u); err != nil {
			t.Fatal(err)
		}
		return u
	}
	reconcile()
	readClaim()
	assertCondition(t, claim.Status.Conditions, "Accepted", metav1.ConditionTrue, "Accepted")
	unit := readUnit()
	if len(unit.Spec.Withdrawals) != 1 || unit.Spec.Withdrawals[0].Claim.UID != claim.UID || !unit.Spec.Withdrawals[0].Allowed {
		t.Fatalf("composition=%#v", unit.Spec.Withdrawals)
	}
	oldSpec := claim.DeepCopy()
	claim.Spec.Address = "192.0.2.2"
	if err := c.Update(ctx, claim); !apierrors.IsInvalid(err) {
		t.Fatalf("mutable claim admitted: %v", err)
	}
	claim = oldSpec
	invalidUnit := unit.DeepCopy()
	invalidUnit.Spec.Withdrawals[0].ClaimName = "different"
	if err := c.Update(ctx, invalidUnit); !apierrors.IsInvalid(err) {
		t.Fatalf("inconsistent scalar key admitted: %v", err)
	}
	waiver := &dnsv1alpha1.RecordWithdrawalWaiver{ObjectMeta: metav1.ObjectMeta{Namespace: claim.Namespace, Name: "risk"}, Spec: dnsv1alpha1.RecordWithdrawalWaiverSpec{WithdrawalRef: withdrawalObjectRef(claim), ClaimSpecDigest: dnsv1alpha1.WithdrawalSpecDigest(claim.Spec), Address: claim.Spec.Address, ReservationRef: dnsv1alpha1.WithdrawalObjectReference{Namespace: "mgmt", Name: "reservation", UID: "reservation-uid"}, Reason: "explicit operator risk acceptance", TicketRef: "change-1", AcceptStaleDNSRisk: true}}
	if err := c.Create(ctx, waiver); err != nil {
		t.Fatal(err)
	}
	if _, err := (&RecordWithdrawalWaiverReconciler{Client: c}).Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(waiver)}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(waiver), waiver); err != nil {
		t.Fatal(err)
	}
	assertCondition(t, waiver.Status.Conditions, "Accepted", metav1.ConditionTrue, "Accepted")
	reconcile()
	unit = readUnit()
	if unit.Spec.Withdrawals[0].Waiver == nil {
		t.Fatal("exact waiver not composed")
	}
	changedWaiver := waiver.DeepCopy()
	changedWaiver.Spec.ReservationRef.UID = "replacement"
	if err := c.Update(ctx, changedWaiver); !apierrors.IsInvalid(err) {
		t.Fatalf("mutable waiver admitted: %v", err)
	}
	stamp := metav1.NewTime(time.Date(2026, 10, 18, 1, 0, 0, 0, time.UTC))
	item := unit.Spec.Withdrawals[0]
	result := dnsv1alpha1.ZoneUnitWithdrawalStatus{ClaimNamespace: claim.Namespace, ClaimName: claim.Name, Claim: withdrawalObjectRef(claim), ObservedGeneration: claim.Generation, PolicyFingerprint: "policy", CheckedNames: []string{"witness.apps.example.com"}, RequiredViews: []string{"recursive"}, SampledWildcard: true, LastProbeTime: &stamp, NextProbeTime: &stamp, ResultDigest: "result", WaiverRef: &dnsv1alpha1.RecordWithdrawalWaiverReference{Name: waiver.Name, UID: waiver.UID, Digest: item.Waiver.Digest, ReservationRef: waiver.Spec.ReservationRef}, Conditions: []metav1.Condition{{Type: "Withdrawn", Status: metav1.ConditionFalse, Reason: "DNSStillResolves", Message: "DNS witness.apps.example.com still resolves to retired address 192.0.2.1 in recursive (A).", ObservedGeneration: claim.Generation, LastTransitionTime: stamp}, {Type: "Ready", Status: metav1.ConditionFalse, Reason: "DNSStillResolves", Message: "provider detail", ObservedGeneration: claim.Generation, LastTransitionTime: stamp}, {Type: "OverrideAccepted", Status: metav1.ConditionTrue, Reason: "OperatorAuthorizedRelease", Message: "explicit risk", ObservedGeneration: claim.Generation, LastTransitionTime: stamp}}}
	unit.Status.ObservedGeneration = unit.Generation
	unit.Status.Withdrawals = []dnsv1alpha1.ZoneUnitWithdrawalStatus{result}
	unit.Status.Zone = &dnsv1alpha1.ZoneUnitZoneStatus{Conditions: []metav1.Condition{
		{Type: "Accepted", Status: metav1.ConditionTrue, Reason: "Accepted", ObservedGeneration: z.Generation, LastTransitionTime: stamp},
		{Type: "Programmed", Status: metav1.ConditionTrue, Reason: "Programmed", ObservedGeneration: z.Generation, LastTransitionTime: stamp},
	}}
	if err := c.Status().Update(ctx, unit); err != nil {
		t.Fatal(err)
	}
	reconcile()
	readClaim()
	condition := assertCondition(t, claim.Status.Conditions, "Withdrawn", metav1.ConditionFalse, "DNSStillResolves")
	if condition.Message != result.Conditions[0].Message {
		t.Fatalf("message lost: %s", condition.Message)
	}
	assertCondition(t, claim.Status.Conditions, "OverrideAccepted", metav1.ConditionTrue, "OperatorAuthorizedRelease")
	if err := c.Get(ctx, key, z); err != nil {
		t.Fatal(err)
	}
	assertCondition(t, z.Status.Conditions, "Programmed", metav1.ConditionTrue, "Programmed")
	// A policy edit fences old observations before the verifier runs again.
	class.Spec.Parameters = runtime.RawExtension{Raw: []byte(`{"policy":"changed"}`)}
	if err := c.Update(ctx, class); err != nil {
		t.Fatal(err)
	}
	reconcile()
	readClaim()
	assertCondition(t, claim.Status.Conditions, "Withdrawn", metav1.ConditionUnknown, "DNSProbeFailed")
	unit = readUnit()
	if unit.Spec.Zone.ZoneClassUID != class.UID || unit.Spec.Zone.ZoneClassObservedGeneration != class.Generation {
		t.Fatal("Unit did not bind the new Class generation")
	}
	// Retained claims pin deletion without marking their provider Unit deleting.
	if err := c.Get(ctx, key, z); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, z); err != nil {
		t.Fatal(err)
	}
	reconcile()
	unit = readUnit()
	if !unit.DeletionTimestamp.IsZero() {
		t.Fatal("pending withdrawal allowed Unit deletion")
	}
	if err := c.Get(ctx, key, z); err != nil {
		t.Fatal(err)
	}
	gate := assertCondition(t, z.Status.Conditions, "Programmed", metav1.ConditionFalse, "ProviderChangePending")
	if gate.Message == "" {
		t.Fatal("missing dependency detail")
	}
	// A same-name recreation cannot consume the old result or waiver.
	if err := c.Delete(ctx, claim); err != nil {
		t.Fatal(err)
	}
	replacement := oldSpec.DeepCopy()
	replacement.ResourceVersion = ""
	replacement.UID = ""
	replacement.Generation = 0
	replacement.CreationTimestamp = metav1.Time{}
	replacement.Status = dnsv1alpha1.RecordWithdrawalStatus{}
	if err := c.Create(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	claim = replacement
	reconcile()
	readClaim()
	assertCondition(t, claim.Status.Conditions, "Withdrawn", metav1.ConditionUnknown, "DNSProbeFailed")
	unit = readUnit()
	if unit.Spec.Withdrawals[0].Waiver != nil {
		t.Fatal("old waiver attached to recreated claim")
	}
	if _, err := (&RecordWithdrawalWaiverReconciler{Client: c}).Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(waiver)}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(waiver), waiver); err != nil {
		t.Fatal(err)
	}
	assertCondition(t, waiver.Status.Conditions, "Accepted", metav1.ConditionFalse, "InvalidWithdrawal")
	// CEL admits retire-only intent but never an empty unprotected ERS.
	ers := &endpointv1alpha1.EndpointRecordSet{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "retire-only"}, Spec: endpointv1alpha1.EndpointRecordSetSpec{RetiredTargets: []endpointv1alpha1.RetiredTarget{{ID: "capture", SourceGeneration: 1, Address: "192.0.2.1", Hostnames: []string{"www.example.com"}}}}}
	if err := c.Create(ctx, ers); err != nil {
		t.Fatal(err)
	}
	ers.Spec.RetiredTargets = nil
	if err := c.Update(ctx, ers); !apierrors.IsInvalid(err) {
		t.Fatalf("empty ERS admitted: %v", err)
	}
}
