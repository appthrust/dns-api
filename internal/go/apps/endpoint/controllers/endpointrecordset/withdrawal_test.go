package endpointrecordset

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"testing"

	"github.com/appthrust/dns-api/internal/go/apps/endpoint/retirement"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func withdrawalFixture(t *testing.T, supported bool, intercept interceptor.Funcs) (*Reconciler, *endpointv1alpha1.EndpointRecordSet, *dnsv1alpha1.Zone) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := endpointv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := dnsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	ref := dnsv1alpha1.ProviderReference{Name: "test.dns.appthrust.io", Version: "v1alpha1"}
	provider := &dnsv1alpha1.Provider{ObjectMeta: metav1.ObjectMeta{Name: ref.Name}, Spec: dnsv1alpha1.ProviderSpec{Versions: []dnsv1alpha1.ProviderVersion{{Name: ref.Version, RecordWithdrawal: &dnsv1alpha1.ProviderRecordWithdrawal{Supported: supported}}}}}
	zone := &dnsv1alpha1.Zone{ObjectMeta: metav1.ObjectMeta{Namespace: "dns", Name: "example", UID: "zone-uid"}, Spec: dnsv1alpha1.ZoneSpec{DomainName: "example.com", Provider: ref}}
	capability := &endpointv1alpha1.EndpointProviderCapability{ObjectMeta: metav1.ObjectMeta{Name: "test"}, Spec: endpointv1alpha1.EndpointProviderCapabilitySpec{Provider: ref}}
	ers := &endpointv1alpha1.EndpointRecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "dns", Name: "ers", UID: "ers-uid", Generation: 8, Finalizers: []string{generatedRecordSetsFinalizer}},
		Spec: endpointv1alpha1.EndpointRecordSetSpec{
			Hostnames: []string{"h2.example.com"},
			Targets: []endpointv1alpha1.EndpointTarget{{Type: endpointv1alpha1.EndpointTargetTypeIPAddress, Value: "192.0.2.2"}},
			RetiredTargets: []endpointv1alpha1.RetiredTarget{{ID: "target-old", SourceGeneration: 7, Address: "192.0.2.1", Hostnames: []string{"h1.example.com", "h2.example.com"}}},
			RetiredHostnames: []endpointv1alpha1.RetiredHostname{{ID: "hostname-old", SourceGeneration: 7, Hostname: "h1.example.com", Addresses: []string{"192.0.2.1"}}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&endpointv1alpha1.EndpointRecordSet{}, &dnsv1alpha1.RecordWithdrawal{}).
		WithObjects(provider, zone, capability, ers).WithInterceptorFuncs(intercept).Build()
	return &Reconciler{Client: c, Scheme: scheme, conversionFunc: func(_ context.Context, _ *endpointv1alpha1.EndpointProviderCapability, input endpointv1alpha1.EndpointRecordSetConversionInput) ([]endpointv1alpha1.RecordSetSpecFragment, string, error) {
		if len(input.Targets) != 1 {
			t.Fatal("retirement invoked conversion without a nonempty pair snapshot")
		}
		return []endpointv1alpha1.RecordSetSpecFragment{{Type: endpointv1alpha1.EndpointRecordSetTypeA, Name: input.Name, A: &dnsv1alpha1.ARecordSet{Addresses: []string{input.Targets[0].Value}}}}, "", nil
	}}, ers, zone
}

func getWithdrawalERS(t *testing.T, c client.Client, key client.ObjectKey) *endpointv1alpha1.EndpointRecordSet {
	t.Helper()
	var ers endpointv1alpha1.EndpointRecordSet
	if err := c.Get(context.Background(), key, &ers); err != nil {
		t.Fatal(err)
	}
	return &ers
}

func listWithdrawalClaims(t *testing.T, c client.Client) []dnsv1alpha1.RecordWithdrawal {
	t.Helper()
	var claims dnsv1alpha1.RecordWithdrawalList
	if err := c.List(context.Background(), &claims); err != nil {
		t.Fatal(err)
	}
	return claims.Items
}

func TestWithdrawalCreateBeforePruneSurvivesCrash(t *testing.T) {
	ctx := context.Background()
	crash := true
	r, ers, _ := withdrawalFixture(t, true, interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
		if _, ok := obj.(*endpointv1alpha1.EndpointRecordSet); ok && crash {
			crash = false
			return errors.New("simulated crash after claim persistence")
		}
		return c.Patch(ctx, obj, patch, opts...)
	}})
	if _, err := r.Reconcile(ctx, ctrlRequest(ers)); err == nil || !strings.Contains(err.Error(), "simulated crash") {
		t.Fatalf("first reconcile = %v", err)
	}
	current := getWithdrawalERS(t, r.Client, client.ObjectKeyFromObject(ers))
	if !hasRetirements(current) {
		t.Fatal("crash discarded source obligations")
	}
	claims := listWithdrawalClaims(t, r.Client)
	if len(claims) != 2 {
		t.Fatalf("claims = %d, want deduplicated (H1,V1), (H2,V1)", len(claims))
	}
	for _, claim := range claims {
		if claim.Spec.Address != "192.0.2.1" || claim.Labels[dnsv1alpha1.AddressLabelKey] != "192.0.2.1" || claim.Annotations[withdrawalSourceGenerationAnnotation] != "7" || len(claim.OwnerReferences) != 0 || len(claim.Finalizers) != 0 {
			t.Fatalf("incorrect retained claim: %#v", claim)
		}
	}
	restarted := &Reconciler{Client: r.Client, Scheme: r.Scheme, conversionFunc: r.conversionFunc}
	if _, err := restarted.Reconcile(ctx, ctrlRequest(ers)); err != nil {
		t.Fatal(err)
	}
	current = getWithdrawalERS(t, r.Client, client.ObjectKeyFromObject(ers))
	if hasRetirements(current) || len(listWithdrawalClaims(t, r.Client)) != 2 || current.Spec.Targets[0].Value != "192.0.2.2" {
		t.Fatal("restart failed idempotent create-then-prune")
	}
}

func TestWithdrawalPruneConflictPreservesConcurrentEntry(t *testing.T) {
	ctx := context.Background()
	interleave := true
	r, ers, _ := withdrawalFixture(t, true, interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
		if _, ok := obj.(*endpointv1alpha1.EndpointRecordSet); ok && interleave {
			interleave = false
			var current endpointv1alpha1.EndpointRecordSet
			if err := c.Get(ctx, client.ObjectKeyFromObject(obj), &current); err != nil { return err }
			current.Spec.RetiredTargets = append(current.Spec.RetiredTargets, endpointv1alpha1.RetiredTarget{ID: "concurrent", SourceGeneration: 8, Address: "192.0.2.3", Hostnames: []string{"h2.example.com"}})
			if err := c.Update(ctx, &current); err != nil { return err }
		}
		return c.Patch(ctx, obj, patch, opts...)
	}})
	if _, err := r.Reconcile(ctx, ctrlRequest(ers)); !apierrors.IsConflict(err) {
		t.Fatalf("prune = %v, want resourceVersion conflict", err)
	}
	current := getWithdrawalERS(t, r.Client, client.ObjectKeyFromObject(ers))
	if len(current.Spec.RetiredTargets) != 2 {
		t.Fatal("concurrent entry was lost")
	}
	if _, err := r.Reconcile(ctx, ctrlRequest(ers)); err != nil { t.Fatal(err) }
	if len(listWithdrawalClaims(t, r.Client)) != 3 || hasRetirements(getWithdrawalERS(t, r.Client, client.ObjectKeyFromObject(ers))) {
		t.Fatal("retry did not capture concurrent entry")
	}
}

func TestWithdrawalDirectDeleteCapturesLiveAndRetired(t *testing.T) {
	ctx := context.Background()
	r, ers, _ := withdrawalFixture(t, true, interceptor.Funcs{})
	if err := r.Delete(ctx, ers); err != nil { t.Fatal(err) }
	if _, err := r.Reconcile(ctx, ctrlRequest(ers)); err != nil { t.Fatal(err) }
	var current endpointv1alpha1.EndpointRecordSet
	if err := r.Get(ctx, client.ObjectKeyFromObject(ers), &current); !apierrors.IsNotFound(err) {
		t.Fatalf("ERS after capture = %v", err)
	}
	claims := listWithdrawalClaims(t, r.Client)
	if len(claims) != 3 { t.Fatalf("claims = %d, want old pairs plus (H2,V2)", len(claims)) }
}

func TestWithdrawalRetireOnlyAcknowledgesWithoutEmptyPublication(t *testing.T) {
	ctx := context.Background()
	r, ers, _ := withdrawalFixture(t, true, interceptor.Funcs{})
	ers.Spec.Hostnames = nil
	ers.Spec.Targets = nil
	if err := r.Update(ctx, ers); err != nil { t.Fatal(err) }
	if _, err := r.Reconcile(ctx, ctrlRequest(ers)); err != nil { t.Fatal(err) }
	current := getWithdrawalERS(t, r.Client, client.ObjectKeyFromObject(ers))
	ready := meta.FindStatusCondition(current.Status.Conditions, "Ready")
	if !retirement.Captured(current) || !hasRetirements(current) || ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != "Retiring" {
		t.Fatalf("retire-only status = %#v", current.Status)
	}
	before := current.ResourceVersion
	if _, err := r.Reconcile(ctx, ctrlRequest(ers)); err != nil { t.Fatal(err) }
	if getWithdrawalERS(t, r.Client, client.ObjectKeyFromObject(ers)).ResourceVersion != before {
		t.Fatal("stable capture acknowledgment caused status churn")
	}
	if err := retirement.RetireOrDelete(ctx, r.Client, current); err != nil { t.Fatal(err) }
	if _, err := r.Reconcile(ctx, ctrlRequest(ers)); err != nil { t.Fatal(err) }
	if len(listWithdrawalClaims(t, r.Client)) != 2 { t.Fatal("deletion changed retirement identity") }
}

func TestWithdrawalCollisionAndCreateFailureRetainIntent(t *testing.T) {
	for _, failure := range []string{"shape", "source", "create"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			r, ers, zone := withdrawalFixture(t, true, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if _, ok := obj.(*dnsv1alpha1.RecordWithdrawal); ok && failure == "create" { return fmt.Errorf("create denied") }
				return c.Create(ctx, obj, opts...)
			}})
			if failure != "create" {
				claim := withdrawalForPair(ers, "dns", zone, "h1.example.com", netip.MustParseAddr("192.0.2.1"), 7)
				if failure == "shape" { claim.Spec.Address = "192.0.2.99" } else { claim.Annotations[withdrawalSourceUIDAnnotation] = "foreign" }
				if err := r.Create(ctx, claim); err != nil { t.Fatal(err) }
			}
			if _, err := r.Reconcile(ctx, ctrlRequest(ers)); err == nil { t.Fatal("unsafe capture succeeded") }
			current := getWithdrawalERS(t, r.Client, client.ObjectKeyFromObject(ers))
			ready := meta.FindStatusCondition(current.Status.Conditions, "Ready")
			if !hasRetirements(current) || ready == nil || ready.Reason != retirement.PendingCondition { t.Fatal("failure discarded or hid capture obligation") }
		})
	}
}

func TestWithdrawalNonExternalPrunesWithoutClaims(t *testing.T) {
	r, ers, _ := withdrawalFixture(t, false, interceptor.Funcs{})
	if _, err := r.Reconcile(context.Background(), ctrlRequest(ers)); err != nil { t.Fatal(err) }
	if len(listWithdrawalClaims(t, r.Client)) != 0 || hasRetirements(getWithdrawalERS(t, r.Client, client.ObjectKeyFromObject(ers))) {
		t.Fatal("non-external retirement gained an external wait")
	}
}

func TestWithdrawalOverflowReportsPending(t *testing.T) {
	r, ers, _ := withdrawalFixture(t, true, interceptor.Funcs{})
	for len(ers.Spec.RetiredTargets) <= retirement.MaxRetiredTargets {
		ers.Spec.RetiredTargets = append(ers.Spec.RetiredTargets, ers.Spec.RetiredTargets[0])
	}
	if err := r.Update(context.Background(), ers); err != nil { t.Fatal(err) }
	if _, err := r.Reconcile(context.Background(), ctrlRequest(ers)); !errors.Is(err, retirement.ErrCapturePending) { t.Fatalf("overflow = %v", err) }
	current := getWithdrawalERS(t, r.Client, client.ObjectKeyFromObject(ers))
	if len(current.Spec.RetiredTargets) != 65 || len(listWithdrawalClaims(t, r.Client)) != 0 || meta.FindStatusCondition(current.Status.Conditions, "Ready").Reason != retirement.PendingCondition {
		t.Fatal("overflow was truncated or acknowledged")
	}
}

func TestWithdrawalResolutionUsesPublicationFallback(t *testing.T) {
	ctx := context.Background()
	r, ers, zone := withdrawalFixture(t, true, interceptor.Funcs{})
	narrow := zone.DeepCopy()
	narrow.Name = "narrow"
	narrow.UID = "narrow-zone"
	narrow.ResourceVersion = ""
	narrow.Spec.DomainName = "h1.example.com"
	if err := r.Create(ctx, narrow); err != nil { t.Fatal(err) }
	baseConversion := r.conversionFunc
	r.conversionFunc = func(ctx context.Context, capability *endpointv1alpha1.EndpointProviderCapability, input endpointv1alpha1.EndpointRecordSetConversionInput) ([]endpointv1alpha1.RecordSetSpecFragment, string, error) {
		if input.Zone.DomainName == narrow.Spec.DomainName { return nil, "conversion rejected narrow zone", nil }
		return baseConversion(ctx, capability, input)
	}
	if _, err := r.Reconcile(ctx, ctrlRequest(ers)); err != nil { t.Fatal(err) }
	for _, claim := range listWithdrawalClaims(t, r.Client) {
		if claim.Spec.ZoneRef.Name != zone.Name { t.Fatalf("retirement bypassed publication fallback: %#v", claim.Spec) }
	}
}

func TestWithdrawalProtectionMarkerFollowsCapability(t *testing.T) {
	ctx := context.Background()
	r, ers, zone := withdrawalFixture(t, true, interceptor.Funcs{})
	ers.Spec.RetiredTargets = nil
	ers.Spec.RetiredHostnames = nil
	ers.Status.Conditions = []metav1.Condition{{Type:retirement.ProtectedCondition, Status:metav1.ConditionFalse, ObservedGeneration:ers.Generation}}
	status := endpointv1alpha1.EndpointRecordSetStatus{Hostnames:[]endpointv1alpha1.EndpointRecordSetHostnameStatus{{Hostname:"h2.example.com", Zone:&endpointv1alpha1.EndpointRecordSetZoneStatus{Ref:dnsv1alpha1.ObjectReference{Namespace:zone.Namespace, Name:zone.Name}, DomainName:zone.Spec.DomainName, Provider:zone.Spec.Provider}}}}
	r.annotateWithdrawalStatus(ctx, ers, &status)
	if !meta.IsStatusConditionTrue(status.Conditions, retirement.ProtectedCondition) || !protectionNeedsBarrier(ers, status) { t.Fatal("new protected publication lacks a source-retention barrier") }
	ers.Status = status
	r.annotateWithdrawalStatus(ctx, ers, &status)
	if protectionNeedsBarrier(ers, status) { t.Fatal("stable protected marker does not converge") }
}
