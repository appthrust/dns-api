package retirement

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func retirementFixture() *endpointv1alpha1.EndpointRecordSet {
	return &endpointv1alpha1.EndpointRecordSet{
		ObjectMeta: metav1.ObjectMeta{Name: "ers", Namespace: "dns", UID: "ers-uid", Generation: 5},
		Spec: endpointv1alpha1.EndpointRecordSetSpec{
			Hostnames: []string{"h1.example.com", "h2.example.com"},
			Targets: []endpointv1alpha1.EndpointTarget{{Type: endpointv1alpha1.EndpointTargetTypeIPAddress, Value: "192.0.2.1"}},
		},
	}
}

func TestWithdrawalSnapshotPreservesCorrelatedPairs(t *testing.T) {
	current := retirementFixture()
	desired := endpointv1alpha1.EndpointRecordSetSpec{Hostnames: []string{"h2.example.com"}, Targets: []endpointv1alpha1.EndpointTarget{{Type: endpointv1alpha1.EndpointTargetTypeIPAddress, Value: "192.0.2.2"}}}
	got, err := Append(current, desired)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.RetiredTargets) != 1 || len(got.RetiredHostnames) != 1 {
		t.Fatalf("snapshot = %#v", got)
	}
	if got.RetiredTargets[0].Address != "192.0.2.1" || !reflect.DeepEqual(got.RetiredTargets[0].Hostnames, current.Spec.Hostnames) ||
		got.RetiredHostnames[0].Hostname != "h1.example.com" || !reflect.DeepEqual(got.RetiredHostnames[0].Addresses, []string{"192.0.2.1"}) {
		t.Fatalf("lost old pair or invented (H1,V2): %#v", got)
	}
	if got.RetiredTargets[0].SourceGeneration != 5 || got.RetiredHostnames[0].SourceGeneration != 5 {
		t.Fatal("snapshot generation was not the pre-update ERS generation")
	}
	got.RetiredTargets[0].Hostnames[0] = "changed"
	if current.Spec.Hostnames[0] != "h1.example.com" {
		t.Fatal("snapshot aliases live source")
	}
	current.Generation++
	later, err := Append(current, desired)
	if err != nil || later.RetiredTargets[0].ID == got.RetiredTargets[0].ID {
		t.Fatalf("re-add/remove reused retirement identity: %v", err)
	}
}

func TestWithdrawalSnapshotOverflowRetainsOldIntent(t *testing.T) {
	for _, kind := range []string{"targets", "hostnames", "hostname snapshot", "address snapshot"} {
		t.Run(kind, func(t *testing.T) {
			current := retirementFixture()
			switch kind {
			case "targets":
				current.Spec.RetiredTargets = make([]endpointv1alpha1.RetiredTarget, MaxRetiredTargets)
			case "hostnames":
				current.Spec.RetiredHostnames = make([]endpointv1alpha1.RetiredHostname, MaxRetiredHostnames)
			case "hostname snapshot":
				for i := len(current.Spec.Hostnames); i <= MaxRetiredHostnames; i++ {
					current.Spec.Hostnames = append(current.Spec.Hostnames, fmt.Sprintf("h%d.example.com", i))
				}
			case "address snapshot":
				for i := 2; i <= MaxRetiredTargets+1; i++ {
					current.Spec.Targets = append(current.Spec.Targets, endpointv1alpha1.EndpointTarget{Type: endpointv1alpha1.EndpointTargetTypeIPAddress, Value: fmt.Sprintf("192.0.2.%d", i)})
				}
			}
			before := current.DeepCopy()
			_, err := Append(current, endpointv1alpha1.EndpointRecordSetSpec{})
			if !errors.Is(err, ErrCapturePending) || !reflect.DeepEqual(current, before) {
				t.Fatalf("overflow mutated intent or did not hold: %v", err)
			}
		})
	}
}

func TestWithdrawalSnapshotNonExternalAndStaleMarker(t *testing.T) {
	current := retirementFixture()
	current.Status.Conditions = []metav1.Condition{{Type: ProtectedCondition, Status: metav1.ConditionFalse, ObservedGeneration: current.Generation}}
	got, err := Append(current, endpointv1alpha1.EndpointRecordSetSpec{})
	if err != nil || len(got.RetiredTargets)+len(got.RetiredHostnames) != 0 {
		t.Fatalf("non-external retirement changed: %#v, %v", got, err)
	}
	current.Generation++
	got, err = Append(current, endpointv1alpha1.EndpointRecordSetSpec{})
	if err != nil || len(got.RetiredTargets) != 1 {
		t.Fatalf("stale marker dropped an obligation: %#v, %v", got, err)
	}
}
