package endpointrecordset

import (
	"context"
	"reflect"
	"strings"
	"testing"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestUnmanagedHostnameDoesNotBlockResolvedSibling(t *testing.T) {
	mode := "success"
	r, ers, base, writes := continuityFixture(t, &mode)
	for _, child := range continuityChildren(t, base) {
		if err := base.Delete(t.Context(), &child); err != nil {
			t.Fatal(err)
		}
	}
	ers.Spec.Hostnames = []string{"a.example.com", "unmanaged.other.test"}
	ers.Generation++
	if err := base.Update(t.Context(), ers); err != nil {
		t.Fatal(err)
	}
	ers.Status = endpointv1alpha1.EndpointRecordSetStatus{}
	if err := base.Status().Update(t.Context(), ers); err != nil {
		t.Fatal(err)
	}
	ttl := int32(120)
	r.conversionFunc = func(_ context.Context, _ *endpointv1alpha1.EndpointProviderCapability, input endpointv1alpha1.EndpointRecordSetConversionInput) ([]endpointv1alpha1.RecordSetSpecFragment, string, error) {
		fragments := continuityFragments(input.Name)
		for i := range fragments {
			fragments[i].TTL = &ttl
		}
		return fragments, "", nil
	}
	for _, desiredTTL := range []int32{120, 240} {
		ttl = desiredTTL
		if _, err := r.Reconcile(t.Context(), ctrlRequest(ers)); err != nil {
			t.Fatal(err)
		}
		children := continuityChildren(t, base)
		if len(children) != 2 {
			t.Fatalf("children=%d, want A's two records only", len(children))
		}
		for _, child := range children {
			if child.Spec.Name != "a" || child.Spec.TTL == nil || *child.Spec.TTL != desiredTTL {
				t.Fatalf("resolved sibling failed to converge: %+v", child.Spec)
			}
		}
		assertMixedHostnameStatus(t, base, ers, "unmanaged.other.test", 0)
	}
	if *writes != 4 {
		t.Fatalf("writes=%d, want two creates then two updates", *writes)
	}
}

// Give B its own Zone and capability, so its dependency can disappear while A's
// successful observations and stable generated identities remain unchanged.
func mixedHostnameFixture(t *testing.T, mode *string) (*Reconciler, *endpointv1alpha1.EndpointRecordSet, client.WithWatch, *int, *dnsv1alpha1.Zone, *endpointv1alpha1.EndpointProviderCapability) {
	t.Helper()
	r, ers, base, writes := continuityFixture(t, mode)
	var zone dnsv1alpha1.Zone
	if err := base.Get(t.Context(), client.ObjectKey{Namespace: "dns", Name: "zone"}, &zone); err != nil {
		t.Fatal(err)
	}
	zone.Name, zone.ResourceVersion = "other", ""
	zone.Spec.DomainName = "other.test"
	zone.Spec.Provider.Name = "other.provider.test"
	if err := base.Create(t.Context(), &zone); err != nil {
		t.Fatal(err)
	}
	provider := &dnsv1alpha1.Provider{ObjectMeta: metav1.ObjectMeta{Name: zone.Spec.Provider.Name}, Spec: dnsv1alpha1.ProviderSpec{Versions: []dnsv1alpha1.ProviderVersion{{Name: zone.Spec.Provider.Version}}}}
	if err := base.Create(t.Context(), provider); err != nil {
		t.Fatal(err)
	}
	var capability endpointv1alpha1.EndpointProviderCapability
	if err := base.Get(t.Context(), client.ObjectKey{Name: "conversion"}, &capability); err != nil {
		t.Fatal(err)
	}
	capability.Name, capability.ResourceVersion = "other-conversion", ""
	capability.Spec.Provider = zone.Spec.Provider
	if err := base.Create(t.Context(), &capability); err != nil {
		t.Fatal(err)
	}
	for _, child := range continuityChildren(t, base) {
		if child.Spec.Name == "z" {
			if err := base.Delete(t.Context(), &child); err != nil {
				t.Fatal(err)
			}
		}
	}
	ers.Spec.Hostnames = []string{"a.example.com", "z.other.test"}
	if err := base.Update(t.Context(), ers); err != nil {
		t.Fatal(err)
	}
	for _, fragment := range continuityFragments("z") {
		child := r.recordSetFromFragment(t.Context(), ers, ers.Namespace, &zone, fragment)
		child.UID, child.Generation = types.UID(child.Name), 1
		child.Status.ObservedGeneration = 1
		child.Status.Conditions = readyGeneratedRecordSetStatus(child.Name, 1).Conditions
		if err := base.Create(t.Context(), &child); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.Reconcile(t.Context(), ctrlRequest(ers)); err != nil {
		t.Fatal(err)
	}
	if err := base.Get(t.Context(), client.ObjectKeyFromObject(ers), ers); err != nil {
		t.Fatal(err)
	}
	assertEndpointCondition(t, ers.Status.Conditions, "Ready", metav1.ConditionTrue, ers.Generation)
	*writes = 0
	return r, ers, base, writes, &zone, &capability
}

func TestPendingHostnameRetainsChildrenWhileSiblingConverges(t *testing.T) {
	for _, pending := range []string{"no-zone", "no-capability", "ambiguous-capability"} {
		t.Run(pending, func(t *testing.T) {
			mode := "success"
			r, ers, base, writes, zone, capability := mixedHostnameFixture(t, &mode)
			before := map[string]dnsv1alpha1.RecordSet{}
			for _, child := range continuityChildren(t, base) {
				before[child.Name] = child
			}
			switch pending {
			case "no-zone":
				if err := base.Delete(t.Context(), zone); err != nil {
					t.Fatal(err)
				}
			case "no-capability":
				if err := base.Delete(t.Context(), capability); err != nil {
					t.Fatal(err)
				}
			case "ambiguous-capability":
				duplicate := capability.DeepCopy()
				duplicate.Name, duplicate.ResourceVersion = "duplicate-conversion", ""
				if err := base.Create(t.Context(), duplicate); err != nil {
					t.Fatal(err)
				}
			}
			ttl := int32(120)
			r.conversionFunc = func(_ context.Context, _ *endpointv1alpha1.EndpointProviderCapability, input endpointv1alpha1.EndpointRecordSetConversionInput) ([]endpointv1alpha1.RecordSetSpecFragment, string, error) {
				if input.Hostname != "a.example.com" {
					t.Fatalf("pending hostname reached conversion: %s", input.Hostname)
				}
				fragments := continuityFragments(input.Name)
				for i := range fragments {
					fragments[i].TTL = &ttl
				}
				return fragments, "", nil
			}
			for _, desiredTTL := range []int32{120, 240} {
				ttl = desiredTTL
				// Reconstruct the controller over persisted status between passes.
				current := *r
				if _, err := current.Reconcile(t.Context(), ctrlRequest(ers)); err != nil {
					t.Fatal(err)
				}
				children := continuityChildren(t, base)
				if len(children) != 4 {
					t.Fatalf("children=%d, want all four", len(children))
				}
				for _, child := range children {
					old, exists := before[child.Name]
					if !exists || child.UID == "" || child.UID != old.UID {
						t.Fatalf("child identity changed: %+v", child)
					}
					if child.Spec.Name == "z" {
						if !reflect.DeepEqual(old, child) {
							t.Fatal("pending B was mutated")
						}
					} else if child.Spec.TTL == nil || *child.Spec.TTL != desiredTTL {
						t.Fatalf("A failed to converge: %+v", child.Spec)
					}
				}
				assertMixedHostnameStatus(t, base, ers, "z.other.test", 2)
			}
			if *writes != 4 {
				t.Fatalf("writes=%d, want only A's four updates", *writes)
			}
			// Retention is an exclusion from prune, not desired state: an externally
			// deleted pending child must not be recreated on the next pass.
			for _, child := range continuityChildren(t, base) {
				if child.Spec.Name == "z" {
					if err := base.Delete(t.Context(), &child); err != nil {
						t.Fatal(err)
					}
					break
				}
			}
			if _, err := r.Reconcile(t.Context(), ctrlRequest(ers)); err != nil {
				t.Fatal(err)
			}
			if len(continuityChildren(t, base)) != 3 || *writes != 4 {
				t.Fatal("pending recreated a missing child")
			}
		})
	}
}

func TestPendingHostnameBecomesExplicitlyRejectedAndPrunes(t *testing.T) {
	mode := "success"
	r, ers, base, writes, zone, _ := mixedHostnameFixture(t, &mode)
	if err := base.Delete(t.Context(), zone); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(t.Context(), ctrlRequest(ers)); err != nil {
		t.Fatal(err)
	}
	if len(continuityChildren(t, base)) != 4 || *writes != 0 {
		t.Fatal("pending did not retain B")
	}
	zone.ResourceVersion = ""
	if err := base.Create(t.Context(), zone); err != nil {
		t.Fatal(err)
	}
	mode = "rejected"
	for range 2 {
		if _, err := r.Reconcile(t.Context(), ctrlRequest(ers)); err != nil {
			t.Fatal(err)
		}
	}
	children := continuityChildren(t, base)
	if len(children) != 2 || *writes != 2 {
		t.Fatalf("rejected B not pruned: children=%d writes=%d", len(children), *writes)
	}
	for _, child := range children {
		if child.Spec.Name != "a" {
			t.Fatal("rejected B remained")
		}
	}
}

func assertMixedHostnameStatus(t *testing.T, c client.Client, ers *endpointv1alpha1.EndpointRecordSet, pendingHostname string, retainedCount int) {
	t.Helper()
	var current endpointv1alpha1.EndpointRecordSet
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(ers), &current); err != nil {
		t.Fatal(err)
	}
	if len(current.Status.Hostnames) != 2 {
		t.Fatalf("hostnames=%d", len(current.Status.Hostnames))
	}
	for _, hostname := range current.Status.Hostnames {
		condition := meta.FindStatusCondition(hostname.Conditions, "Resolved")
		if condition == nil {
			t.Fatal("missing hostname condition")
		}
		if hostname.Hostname == pendingHostname {
			if condition.Status != metav1.ConditionFalse || condition.Reason != "ZoneNotResolved" || len(hostname.RecordSets) != retainedCount {
				t.Fatalf("pending status=%+v", hostname)
			}
			if !strings.Contains(condition.Message, "Zone") && !strings.Contains(condition.Message, "EndpointProviderCapability") {
				t.Fatalf("missing pending diagnostic: %s", condition.Message)
			}
		} else if condition.Status != metav1.ConditionTrue || len(hostname.RecordSets) != 2 {
			t.Fatalf("resolved evidence regressed: %+v", hostname)
		}
	}
	for conditionType, reason := range map[string]string{"Resolved": "HostnameNotResolved", "Accepted": "NotResolved", "Programmed": "NotAccepted", "Ready": "NotProgrammed"} {
		condition := meta.FindStatusCondition(current.Status.Conditions, conditionType)
		if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != reason {
			t.Fatalf("aggregate %s=%+v", conditionType, condition)
		}
	}
}

func TestPendingZoneCandidateStillAllowsResolvedFallback(t *testing.T) {
	mode := "success"
	r, ers, base, writes := continuityFixture(t, &mode)
	before := continuityChildren(t, base)
	zone := &dnsv1alpha1.Zone{
		ObjectMeta: metav1.ObjectMeta{Namespace: "dns", Name: "more-specific"},
		Spec: dnsv1alpha1.ZoneSpec{
			DomainName: "z.example.com",
			Provider:   dnsv1alpha1.ProviderReference{Name: "unobserved.provider.test", Version: "v1alpha1"},
		},
	}
	if err := base.Create(t.Context(), zone); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(t.Context(), ctrlRequest(ers)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, continuityChildren(t, base)) || *writes != 0 {
		t.Fatal("pending candidate blocked or changed the resolved fallback")
	}
	if err := base.Get(t.Context(), client.ObjectKeyFromObject(ers), ers); err != nil {
		t.Fatal(err)
	}
	assertEndpointCondition(t, ers.Status.Conditions, "Ready", metav1.ConditionTrue, ers.Generation)
}
