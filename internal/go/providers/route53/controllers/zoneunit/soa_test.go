package route53

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const defaultSOA = "ns-1.awsdns-00.com. awsdns-hostmaster.amazon.com. 1 7200 900 1209600 86400"

func TestZoneReconcilerNegativeCacheTTL(t *testing.T) {
	for _, test := range []struct {
		name       string
		parameters string
		value      string
		adopt      bool
		private    bool
		wantWrites int
		wantTTL    int32
		malformed  bool
	}{
		{name: "absent", parameters: `{}`, value: defaultSOA},
		{name: "managed differs", parameters: `{"negativeCacheTTLSeconds":60}`, value: defaultSOA, wantWrites: 1, wantTTL: 60},
		{name: "already equal", parameters: `{"negativeCacheTTLSeconds":86400}`, value: defaultSOA, wantTTL: 86400},
		{name: "adopted public", parameters: `{"negativeCacheTTLSeconds":60}`, value: defaultSOA, adopt: true, wantWrites: 1, wantTTL: 60},
		{name: "adopted private", parameters: `{"zoneType":"Private","negativeCacheTTLSeconds":60}`, value: defaultSOA, adopt: true, private: true, wantWrites: 1, wantTTL: 60},
		{name: "zero", parameters: `{"negativeCacheTTLSeconds":0}`, value: defaultSOA, wantWrites: 1},
		{name: "maximum", parameters: `{"negativeCacheTTLSeconds":2147483647}`, value: defaultSOA, wantWrites: 1, wantTTL: 2147483647},
		{name: "malformed", parameters: `{"negativeCacheTTLSeconds":60}`, value: "private malformed value", malformed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			provider := newFakeProvider()
			hostedZone := HostedZone{ID: "ZSOA", Name: "apps.example.com", Private: test.private}
			provider.zones[hostedZone.ID] = hostedZone
			original := RecordSetResource{HostedZoneID: hostedZone.ID, Name: hostedZone.Name + ".", Type: "SOA", TTL: ptr(int64(900)), Values: []string{test.value}}
			key := recordKey(original.HostedZoneID, original.Name, original.Type)
			provider.records[key] = original
			class := route53ZoneClass("app", "route53", nil)
			class.Spec.Parameters = runtime.RawExtension{Raw: []byte(test.parameters)}
			unit := route53ZoneUnit("app", "apps-example-com", "app", class.Name)
			if test.adopt {
				unit.Spec.Zone.Adoption = runtime.RawExtension{Raw: []byte(`{"hostedZoneId":"ZSOA"}`)}
			} else {
				unit.Status.Provider = &dnsv1alpha1.ProviderStatus{State: runtime.RawExtension{Raw: []byte(`{"hostedZoneID":"ZSOA"}`)}}
			}
			k8sClient := fake.NewClientBuilder().WithScheme(testScheme(t)).
				WithObjects(route53Provider(), class, acceptedRoute53Identity("app", "route53-dev"), unit).
				WithStatusSubresource(&dnsv1alpha1.ZoneUnit{}, &dnsv1alpha1.ZoneClass{}, &route53v1alpha1.Route53Identity{}).Build()
			reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider}
			request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(unit)}
			reconcile := func() {
				t.Helper()
				if _, err := reconciler.Reconcile(ctx, request); err != nil {
					t.Fatal(err)
				}
				if err := k8sClient.Get(ctx, request.NamespacedName, unit); err != nil {
					t.Fatal(err)
				}
			}
			reconcile()
			wantReads := 1
			if test.parameters == `{}` {
				wantReads = 0
			}
			if len(provider.readRecords) != wantReads || (wantReads == 1 && provider.readRecords[0] != key) {
				t.Fatalf("SOA reads = %v, want %d apex reads", provider.readRecords, wantReads)
			}
			if len(provider.upserted) != test.wantWrites {
				t.Fatalf("SOA writes = %d, want %d", len(provider.upserted), test.wantWrites)
			}
			if test.malformed {
				condition := assertCondition(t, unit.Status.Zone.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ExternalResourceMismatch")
				if condition.Message != "Route 53 hosted zone apex SOA must contain exactly seven fields" {
					t.Fatalf("unsafe or unexpected SOA message: %q", condition.Message)
				}
				return
			}
			if test.wantWrites != 0 {
				state := mustZoneUnitStatusData(t, ctx, k8sClient, unit.Namespace, unit.Name)
				pending := state.PendingHostedZoneChange
				if pending == nil || pending.Operation != "NegativeCacheTTL" || pending.Status != route53v1alpha1.Route53ChangeStatusPending {
					t.Fatalf("pending change = %#v", pending)
				}
				assertCondition(t, unit.Status.Zone.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ProviderChangePending")
				var observed struct {
					Minimum int32 `json:"negativeCacheTTLSeconds"`
				}
				if err := json.Unmarshal(unit.Status.Zone.Provider.Data.Raw, &observed); err != nil || observed.Minimum != 86400 {
					t.Fatalf("submitted change must retain observed MINIMUM 86400: %#v, %v", observed, err)
				}
				reconcile()
				if len(provider.upserted) != 1 {
					t.Fatal("pending SOA change was resubmitted")
				}
				if len(provider.readRecords) != 1 || len(provider.listedRecordSets) != 0 {
					t.Fatal("pending hosted-zone change did not block SOA and child RecordSet observation")
				}
				provider.changes[pending.ID].Status = route53v1alpha1.Route53ChangeStatusInSync
				reconcile()
				reconcile()
				if len(provider.upserted) != 1 {
					t.Fatal("converged SOA was rewritten")
				}
				if state := mustZoneUnitStatusData(t, ctx, k8sClient, unit.Namespace, unit.Name); state.PendingHostedZoneChange != nil {
					t.Fatalf("completed pending change retained: %#v", state.PendingHostedZoneChange)
				}
			}
			assertCondition(t, unit.Status.Zone.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionTrue, "Programmed")
			var observed struct {
				Minimum int32 `json:"negativeCacheTTLSeconds"`
			}
			if err := json.Unmarshal(unit.Status.Zone.Provider.Data.Raw, &observed); err != nil || observed.Minimum != test.wantTTL {
				t.Fatalf("observed MINIMUM = %d, want %d; error %v", observed.Minimum, test.wantTTL, err)
			}
			actual := provider.records[key]
			if test.wantWrites == 0 {
				if !reflect.DeepEqual(actual, original) {
					t.Fatalf("SOA changed without an UPSERT: %#v", actual)
				}
			} else {
				want := "ns-1.awsdns-00.com. awsdns-hostmaster.amazon.com. 1 7200 900 1209600 " + strconv.FormatInt(int64(test.wantTTL), 10)
				if !reflect.DeepEqual(actual.Values, []string{want}) {
					t.Fatalf("SOA value = %v, want %q", actual.Values, want)
				}
				// The complete RRset must match apart from its final SOA field.
				actual.Values = original.Values
				if !reflect.DeepEqual(actual, original) {
					t.Fatalf("UPSERT changed other RRset fields: %#v", actual)
				}
			}
			if len(provider.created) != 0 || len(provider.deleted) != 0 {
				t.Fatal("SOA reconciliation changed the hosted zone lifecycle")
			}
			if test.wantWrites != 0 {
				provider.records[key] = original
				reconcile()
				if len(provider.upserted) != 2 || provider.upserted[1].Values[0] != provider.upserted[0].Values[0] {
					t.Fatal("later SOA drift did not converge")
				}
			}
		})
	}
}

func TestSOARoundTrip(t *testing.T) {
	for _, value := range []string{
		defaultSOA,
		"ns.example. hostmaster.example. 4294967295 0010 020 0030 0",
		"ns.example. hostmaster.example. 42 10 20 30 2147483647",
	} {
		t.Run(value, func(t *testing.T) {
			soa, err := parseSOA(value)
			if err != nil {
				t.Fatal(err)
			}
			if got := soa.withMinimum(soa.minimum); got != value {
				t.Fatalf("round trip = %q, want %q", got, value)
			}
			before := strings.Fields(value)
			after := strings.Fields(soa.withMinimum(60))
			if !reflect.DeepEqual(before[:6], after[:6]) || after[6] != "60" {
				t.Fatalf("SOA replacement changed other fields: %v", after)
			}
		})
	}
}

func TestSOAMalformed(t *testing.T) {
	for _, value := range []string{
		"",
		"ns.example. hostmaster.example. 1 7200 900 1209600",
		defaultSOA + " extra",
		"ns.example. hostmaster.example. 1 7200 900 1209600 not-a-number",
		"ns.example. hostmaster.example. 1 7200 900 1209600 -1",
		"ns.example. hostmaster.example. 1 7200 900 1209600 2147483648",
	} {
		t.Run(value, func(t *testing.T) {
			if _, err := parseSOA(value); err == nil {
				t.Fatal("malformed SOA accepted")
			}
		})
	}
}
