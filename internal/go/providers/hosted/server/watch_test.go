package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	hostedv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/hosted/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

type appliedStatus struct {
	patch  clienttesting.PatchAction
	object hostedv1alpha1.HostedZone
}

func fakeSnapshotClient(t *testing.T, objects ...runtime.Object) (*dynamicfake.FakeDynamicClient, <-chan appliedStatus) {
	t.Helper()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{hostedZones: "HostedZoneList"}, objects...)
	patches := make(chan appliedStatus, 100)
	// The dynamic fake has no CRD structured-merge schema. Capture the actual
	// status apply request rather than pretending merge-patch proves SSA.
	client.PrependReactor("patch", "hostedzones", func(action clienttesting.Action) (bool, runtime.Object, error) {
		patch := action.(clienttesting.PatchAction)
		var object hostedv1alpha1.HostedZone
		if err := json.Unmarshal(patch.GetPatch(), &object); err != nil {
			t.Error(err)
		}
		patches <- appliedStatus{patch: patch, object: object}
		return true, zoneObject(t, &object), nil
	})
	return client, patches
}

func zoneObject(t *testing.T, zone *hostedv1alpha1.HostedZone) *unstructured.Unstructured {
	t.Helper()
	zone = zone.DeepCopy()
	zone.APIVersion = hostedv1alpha1.SchemeGroupVersion.String()
	zone.Kind = "HostedZone"
	object, err := json.Marshal(zone)
	if err != nil {
		t.Fatal(err)
	}
	result := new(unstructured.Unstructured)
	if err := result.UnmarshalJSON(object); err != nil {
		t.Fatal(err)
	}
	result.SetAPIVersion(hostedv1alpha1.SchemeGroupVersion.String())
	result.SetKind("HostedZone")
	return result
}

func receiveStatus(t *testing.T, patches <-chan appliedStatus) appliedStatus {
	t.Helper()
	select {
	case patch := <-patches:
		return patch
	case <-time.After(3 * time.Second):
		t.Fatal("no serving status apply")
		return appliedStatus{}
	}
}

func waitUntil(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for !condition() {
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("condition did not become true")
		}
	}
}

func startWatcher(t *testing.T, w *snapshotWatcher) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); w.run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("watcher did not stop")
		}
	})
}

func TestInformerPublishesSnapshotAndAppliesOnlyOwnStatus(t *testing.T) {
	zone := testZone()
	zone.Status.Servers = []hostedv1alpha1.HostedZoneServerStatus{{Endpoint: "dns/other", Serial: 6}}
	client, patches := fakeSnapshotClient(t, zoneObject(t, zone))
	index := newZoneIndex(10, newMetrics())
	w, err := newSnapshotWatcher(client, "dns", "dns/server-1", index)
	if err != nil {
		t.Fatal(err)
	}
	startWatcher(t, w)
	initial := receiveStatus(t, patches)
	assertOwnApply(t, initial, 7)
	if !w.ready.Load() {
		t.Fatal("initial list did not make server ready")
	}
	first := index.lookup("www.example.test.")
	if first == nil || first.serial != 7 {
		t.Fatalf("initial snapshot = %#v", first)
	}

	zone.Spec.Serial = 8
	zone.Spec.ContentDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	zone.Spec.Records[3].Values[0] = "192.0.2.99"
	if _, err := client.Resource(hostedZones).Namespace("dns").Update(context.Background(), zoneObject(t, zone), metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	updated := receiveStatus(t, patches)
	assertOwnApply(t, updated, 8)
	second := index.lookup("www.example.test.")
	if first == second || first.serial != 7 || second.serial != 8 || first.owners["www.example.test."][1][0].String() == second.owners["www.example.test."][1][0].String() {
		t.Fatal("snapshot was mutated instead of atomically replaced")
	}

	// Status-only events must neither recompile nor immediately acknowledge
	// themselves, otherwise status writes create an unbounded feedback loop.
	zone.Status.Servers = updated.object.Status.Servers
	if _, err := client.Resource(hostedZones).Namespace("dns").UpdateStatus(context.Background(), zoneObject(t, zone), metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := client.Resource(hostedZones).Namespace("dns").Delete(context.Background(), zone.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return index.lookup("www.example.test.") == nil })
	if !w.ready.Load() {
		t.Fatal("empty authority became unready")
	}
	for _, action := range client.Actions() {
		if action.GetNamespace() != "dns" {
			t.Fatalf("watch escaped namespace: %#v", action)
		}
	}
}

func assertOwnApply(t *testing.T, applied appliedStatus, serial uint32) {
	t.Helper()
	patch := applied.patch
	if patch.GetPatchType() != types.ApplyPatchType || patch.GetSubresource() != "status" || patch.GetName() != "example" || patch.GetNamespace() != "dns" {
		t.Fatalf("patch contract = %#v", patch)
	}
	options := patch.(interface{ GetPatchOptions() metav1.PatchOptions }).GetPatchOptions()
	if options.FieldManager != "hosted-server/server-1" || options.Force != nil {
		t.Fatalf("apply options = %#v", options)
	}
	object := applied.object
	if object.UID != "zone-1" || object.APIVersion != hostedv1alpha1.SchemeGroupVersion.String() || object.Kind != "HostedZone" || len(object.Status.Servers) != 1 {
		t.Fatalf("apply object = %#v", object)
	}
	status := object.Status.Servers[0]
	if status.Endpoint != "dns/server-1" || status.Serial != serial || status.ObservedAt.IsZero() || status.ContentDigest == "" {
		t.Fatalf("own status = %#v", status)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(patch.GetPatch(), &fields); err != nil {
		t.Fatal(err)
	}
	if _, exists := fields["spec"]; exists {
		t.Fatal("server tried to own snapshot spec")
	}
}

func TestInitialEmptyListIsReady(t *testing.T) {
	client, _ := fakeSnapshotClient(t)
	w, err := newSnapshotWatcher(client, "", "dns/server-1", newZoneIndex(10, newMetrics()))
	if err != nil {
		t.Fatal(err)
	}
	startWatcher(t, w)
	waitUntil(t, w.ready.Load)
	for _, action := range client.Actions() {
		if action.GetNamespace() != "" {
			t.Fatalf("empty scope is not cluster-wide: %#v", action)
		}
	}
}

func TestWatchLossSuppressesReportsAndPreservesServing(t *testing.T) {
	for _, event := range []string{"EOF", "ERROR", "STOP"} {
		t.Run(event, func(t *testing.T) {
			client, patches := fakeSnapshotClient(t)
			index := newZoneIndex(10, newMetrics())
			if err := index.replace(testZone()); err != nil {
				t.Fatal(err)
			}
			w, err := newSnapshotWatcher(client, "dns", "dns/server-1", index)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			w.connection = ctx
			upstream := watch.NewRaceFreeFake()
			tracked := &trackedWatch{upstream: upstream, result: make(chan watch.Event), cancel: cancel}
			go tracked.forward(ctx)
			reported := make(map[*zone]time.Time)
			now := time.Now()
			w.reportDue(context.Background(), reported, now)
			assertOwnApply(t, receiveStatus(t, patches), 7)
			switch event {
			case "EOF":
				upstream.Stop()
			case "ERROR":
				upstream.Error(&metav1.Status{Status: "Failure"})
			case "STOP":
				tracked.Stop()
			}
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
				t.Fatal("watch loss not detected")
			}
			w.reportDue(context.Background(), reported, now.Add(time.Hour))
			select {
			case patch := <-patches:
				t.Fatalf("reported after watch loss: %#v", patch)
			default:
			}
			if index.lookup("www.example.test.") == nil {
				t.Fatal("watch loss dropped serving snapshot")
			}
			// A newly connected watch resumes heartbeat acknowledgement.
			reconnected, stop := context.WithCancel(context.Background())
			defer stop()
			w.connection = reconnected
			w.reportDue(context.Background(), reported, now.Add(time.Hour))
			assertOwnApply(t, receiveStatus(t, patches), 7)
		})
	}
}

func TestShutdownRelinquishesOnlyOwnSSAFields(t *testing.T) {
	client, patches := fakeSnapshotClient(t)
	index := newZoneIndex(10, newMetrics())
	if err := index.replace(testZone()); err != nil {
		t.Fatal(err)
	}
	w, err := newSnapshotWatcher(client, "dns", "dns/server-1", index)
	if err != nil {
		t.Fatal(err)
	}
	w.connection = context.Background()
	w.reportDue(context.Background(), make(map[*zone]time.Time), time.Now())
	assertOwnApply(t, receiveStatus(t, patches), 7)
	w.removeOwnStatus(context.Background())
	applied := receiveStatus(t, patches)
	if applied.patch.GetPatchType() != types.ApplyPatchType || applied.patch.GetSubresource() != "status" {
		t.Fatalf("cleanup patch = %#v", applied.patch)
	}
	options := applied.patch.(interface{ GetPatchOptions() metav1.PatchOptions }).GetPatchOptions()
	if options.FieldManager != "hosted-server/server-1" || options.Force != nil {
		t.Fatalf("cleanup manager = %#v", options)
	}
	var body map[string]any
	if err := json.Unmarshal(applied.patch.GetPatch(), &body); err != nil {
		t.Fatal(err)
	}
	servers := body["status"].(map[string]any)["servers"].([]any)
	if len(servers) != 0 || applied.object.UID != "zone-1" {
		t.Fatalf("cleanup body = %s", applied.patch.GetPatch())
	}
}

func TestHealthReadinessBoundary(t *testing.T) {
	var ready atomic.Bool
	handler := healthHandler(&ready)
	for _, tc := range []struct {
		path  string
		ready bool
		code  int
	}{{"/healthz", false, 200}, {"/readyz", false, 503}, {"/readyz", true, 200}, {"/healthz", true, 200}} {
		ready.Store(tc.ready)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if recorder.Code != tc.code {
			t.Fatalf("%s ready=%v: %d", tc.path, tc.ready, recorder.Code)
		}
	}
}

func TestHeartbeatEveryThirtySeconds(t *testing.T) {
	client, patches := fakeSnapshotClient(t)
	index := newZoneIndex(10, newMetrics())
	if err := index.replace(testZone()); err != nil {
		t.Fatal(err)
	}
	w, err := newSnapshotWatcher(client, "dns", "dns/server-1", index)
	if err != nil {
		t.Fatal(err)
	}
	w.connection = context.Background()
	reported := make(map[*zone]time.Time)
	now := time.Now()
	w.reportDue(context.Background(), reported, now)
	assertOwnApply(t, receiveStatus(t, patches), 7)
	w.reportDue(context.Background(), reported, now.Add(30*time.Second-time.Millisecond))
	select {
	case <-patches:
		t.Fatal("unchanged snapshot was acknowledged before 30 seconds")
	default:
	}
	w.reportDue(context.Background(), reported, now.Add(30*time.Second))
	assertOwnApply(t, receiveStatus(t, patches), 7)
}
