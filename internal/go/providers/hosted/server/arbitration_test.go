package server

import (
	"context"
	"testing"

	"github.com/miekg/dns"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestOnlyZoneUnitOwnedSnapshotsAreAdmitted(t *testing.T) {
	for _, tc := range []struct {
		name   string
		owners []metav1.OwnerReference
	}{
		{"missing", nil},
		{"wrong API", []metav1.OwnerReference{{APIVersion: "other/v1", Kind: "ZoneUnit", UID: "unit-1"}}},
		{"wrong kind", []metav1.OwnerReference{{APIVersion: "dns.appthrust.io/v1alpha1", Kind: "Zone", UID: "unit-1"}}},
		{"missing UID", []metav1.OwnerReference{{APIVersion: "dns.appthrust.io/v1alpha1", Kind: "ZoneUnit"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			index := newZoneIndex(10, newMetrics())
			zone := testZone()
			zone.OwnerReferences = tc.owners
			if err := index.replace(zone); err == nil || err.Error() != "snapshot has no ZoneUnit owner reference" {
				t.Fatalf("admission error = %v", err)
			}
			if index.lookup("www.example.test.") != nil || metricValue(t, index.metrics.rejected.WithLabelValues("unowned")) != 1 {
				t.Fatal("unowned snapshot was not rejected and counted")
			}
		})
	}
}

func TestInformerOwnershipRemovalStopsServingAndRetractsAcknowledgement(t *testing.T) {
	zone := testZone()
	zone.Generation = 1
	client, patches := fakeSnapshotClient(t, zoneObject(t, zone))
	index := newZoneIndex(10, newMetrics())
	w, err := newSnapshotWatcher(client, "dns", "dns/server-1", index)
	if err != nil {
		t.Fatal(err)
	}
	startWatcher(t, w)
	assertOwnApply(t, receiveStatus(t, patches), 7)
	zone.OwnerReferences = nil // Metadata-only update does not advance generation.
	if _, err := client.Resource(hostedZones).Namespace("dns").Update(context.Background(), zoneObject(t, zone), metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	removed := receiveStatus(t, patches)
	if len(removed.object.Status.Servers) != 0 {
		t.Fatal("unowned snapshot retained a serving acknowledgement")
	}
	if index.lookup("www.example.test.") != nil || metricValue(t, index.metrics.rejected.WithLabelValues("unowned")) != 1 {
		t.Fatal("removing ownership did not remove authority")
	}
	zone.OwnerReferences = testZone().OwnerReferences
	if _, err := client.Resource(hostedZones).Namespace("dns").Update(context.Background(), zoneObject(t, zone), metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	assertOwnApply(t, receiveStatus(t, patches), 7)
	if index.lookup("www.example.test.") == nil {
		t.Fatal("restored ownership was ignored")
	}
}

func TestCrossNamespaceApexConflictRefusesBothAndRecovers(t *testing.T) {
	first := testZone()
	second := testZone()
	second.Namespace = "other"
	second.UID = "zone-2"
	client, patches := fakeSnapshotClient(t, zoneObject(t, first), zoneObject(t, second))
	index := newZoneIndex(1, newMetrics())
	w, err := newSnapshotWatcher(client, "", "dns/server-1", index)
	if err != nil {
		t.Fatal(err)
	}
	startWatcher(t, w)
	waitUntil(t, w.ready.Load)
	assertRefused(t, index, "www.example.test.")
	if metricValue(t, index.metrics.rejected.WithLabelValues("apex_conflict")) == 0 || metricValue(t, index.metrics.loaded) != 0 {
		t.Fatal("conflict not counted or still loaded")
	}
	select {
	case patch := <-patches:
		t.Fatalf("conflicting snapshot was acknowledged: %#v", patch.object)
	default:
	}
	if err := client.Resource(hostedZones).Namespace("other").Delete(context.Background(), second.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	assertOwnApply(t, receiveStatus(t, patches), 7)
	if index.lookup("www.example.test.") == nil {
		t.Fatal("sole survivor was not loaded after conflict deletion")
	}
	// A second contender must quarantine an already acknowledged zone even
	// when admitting another compiled snapshot would exceed MaxZones.
	if _, err := client.Resource(hostedZones).Namespace("other").Create(context.Background(), zoneObject(t, second), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	removed := receiveStatus(t, patches)
	if len(removed.object.Status.Servers) != 0 {
		t.Fatal("conflicted snapshot acknowledgement was not retracted")
	}
	assertRefused(t, index, "www.example.test.")
	second.Spec.DomainName = "different.test"
	if _, err := client.Resource(hostedZones).Namespace("other").Update(context.Background(), zoneObject(t, second), metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	// The sole original owner recovers on rename as well as deletion. It may
	// compete with the renamed zone for capacity, so free that unrelated slot.
	if err := client.Resource(hostedZones).Namespace("other").Delete(context.Background(), second.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return index.lookup("www.example.test.") != nil })
}

func TestConflictingChildApexDoesNotFallThroughToParent(t *testing.T) {
	index := newZoneIndex(10, newMetrics())
	parent := testZone()
	parent.Spec.DomainName = "test"
	parent.Name = "parent"
	if err := index.replace(parent); err != nil {
		t.Fatal(err)
	}
	first := testZone()
	if err := index.replace(first); err != nil {
		t.Fatal(err)
	}
	second := first.DeepCopy()
	second.Namespace = "other"
	if err := index.replace(second); err == nil {
		t.Fatal("duplicate accepted")
	}
	assertRefused(t, index, "www.example.test.")
	if index.lookup("www.test.") == nil {
		t.Fatal("conflict removed unrelated parent authority")
	}
}

func assertRefused(t *testing.T, index *zoneIndex, name string) {
	t.Helper()
	request := new(dns.Msg)
	request.SetQuestion(name, dns.TypeA)
	response, _ := (&handler{index: index}).answer(request)
	if response.Rcode != dns.RcodeRefused || len(response.Answer) != 0 {
		t.Fatalf("conflicted query = %v", response)
	}
}

func TestUnownedSnapshotCannotQuarantineAnOwnedApex(t *testing.T) {
	owned := testZone()
	unowned := owned.DeepCopy()
	unowned.Namespace = "other"
	unowned.OwnerReferences = nil
	client, patches := fakeSnapshotClient(t, zoneObject(t, owned), zoneObject(t, unowned))
	index := newZoneIndex(10, newMetrics())
	w, err := newSnapshotWatcher(client, "", "dns/server-1", index)
	if err != nil {
		t.Fatal(err)
	}
	startWatcher(t, w)
	assertOwnApply(t, receiveStatus(t, patches), 7)
	if index.lookup("www.example.test.") == nil {
		t.Fatal("unowned snapshot denied another namespace's authority")
	}
	if metricValue(t, index.metrics.rejected.WithLabelValues("apex_conflict")) != 0 || metricValue(t, index.metrics.rejected.WithLabelValues("unowned")) != 1 {
		t.Fatal("incorrect rejection reason")
	}
}
