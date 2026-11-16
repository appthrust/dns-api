package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	endpointcontroller "github.com/appthrust/dns-api/internal/go/apps/endpoint/controllers/endpointrecordset"
	corezoneunit "github.com/appthrust/dns-api/internal/go/core/controllers/zoneunit"
	route53 "github.com/appthrust/dns-api/internal/go/providers/route53/controllers/zoneunit"
	route53conversion "github.com/appthrust/dns-api/internal/go/providers/route53/conversion"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const publicationDNSContractDirectory = "testdata/publication-dns-contract/v1"

type publicationDNSContract struct {
	Scenario    string                `json:"scenario"`
	Description string                `json:"description"`
	Gateway     gatewayv1.Gateway     `json:"gateway"`
	Routes      []gatewayv1.HTTPRoute `json:"routes"`
	Expect      struct {
		DNS       string   `json:"dns"`
		Hostnames []string `json:"hostnames"`
	} `json:"expect"`
}

// TestPublicationDNSContract consumes only real, normalized Fleet output. The
// healthy publication is established through the complete consumer chain first;
// every scenario then starts with new clients and reconciler instances backed
// solely by those persisted Kubernetes objects and provider records.
func TestPublicationDNSContract(t *testing.T) {
	fixtures := publicationDNSFixtures(t)
	healthy := fixtures["healthy"]
	scheme := testScheme(t)
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, dnsv1alpha1.AddToScheme, route53v1alpha1.AddToScheme} {
		publicationDNSRequire(t, add(scheme))
	}
	server := httptest.NewServer(route53conversion.NewHandler())
	t.Cleanup(server.Close)
	zone := publicationDNSZone()
	objects := publicationDNSInfrastructure(zone)
	objects = append(objects, healthy.Gateway.DeepCopy())
	keys := make([]client.ObjectKey, 0, len(healthy.Routes))
	for i := range healthy.Routes {
		objects = append(objects, healthy.Routes[i].DeepCopy())
		keys = append(keys, client.ObjectKeyFromObject(&healthy.Routes[i]))
	}
	incarnation := 0
	cl := publicationDNSClient(scheme, objects, &incarnation)
	recordKey := client.ObjectKey{Namespace: zone.Namespace, Name: generatedGatewayEndpointRecordSetName(healthy.Gateway.Namespace, healthy.Gateway.Name)}
	provider := &publicationDNSProvider{publicationProvider: publicationProvider{records: map[string]route53.RecordSetResource{}}}
	// Reuse the ordinary (non-external) retention-test boundary. The source
	// controller creates the admission receipt; no receipt is synthesized here.
	r := &Reconciler{Client: cl, EndpointRecordSetNamespace: recordKey.Namespace}
	for _, key := range keys {
		reconcileRetentionRoute(t, r, key)
	}
	markUnprotected(t, r, recordKey)
	publicationDNSConverge(t, cl, provider, server.URL, keys, recordKey, client.ObjectKeyFromObject(zone))
	var before endpointv1alpha1.EndpointRecordSet
	publicationDNSRequire(t, cl.Get(t.Context(), recordKey, &before))
	if before.UID == "" || before.Annotations[testAdmissionReceiptAnnotation] == "" || !reflect.DeepEqual(before.Spec.Hostnames, healthy.Expect.Hostnames) {
		t.Fatalf("healthy fixture did not establish receipted DNS intent: %#v", before)
	}
	var children dnsv1alpha1.RecordSetList
	publicationDNSRequire(t, cl.List(t.Context(), &children))
	if len(children.Items) != len(healthy.Expect.Hostnames) || len(provider.records) != len(healthy.Expect.Hostnames) || provider.upserts != len(healthy.Expect.Hostnames) || provider.deletes != 0 {
		t.Fatalf("healthy publication did not reach Route 53: children=%d records=%d upserts=%d deletes=%d", len(children.Items), len(provider.records), provider.upserts, provider.deletes)
	}
	for _, child := range children.Items {
		if child.UID == "" {
			t.Fatal("healthy child RecordSet has no UID")
		}
	}
	stored := publicationDNSStoredObjects(t, cl)
	names := make([]string, 0, len(fixtures))
	for name := range fixtures {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			fixture := fixtures[name]
			// This is an API-store snapshot, not a warmed informer or a reused
			// reconciler. Preserve all persisted receipts, UIDs, finalizers and
			// child status, replacing only the producer-owned source objects.
			restartedObjects := make([]client.Object, 0, len(stored))
			for _, object := range stored {
				switch object.(type) {
				case *gatewayv1.Gateway, *gatewayv1.HTTPRoute:
					continue
				}
				restartedObjects = append(restartedObjects, object.DeepCopyObject().(client.Object))
			}
			restartedObjects = append(restartedObjects, fixture.Gateway.DeepCopy())
			for i := range fixture.Routes {
				route := fixture.Routes[i].DeepCopy()
				// DNS-owned finalizers survive for an existing incarnation; they
				// are not part of the normalized Fleet producer protocol.
				for _, object := range stored {
					previous, ok := object.(*gatewayv1.HTTPRoute)
					if ok && previous.UID == route.UID {
						route.Finalizers = append([]string(nil), previous.Finalizers...)
						break
					}
				}
				restartedObjects = append(restartedObjects, route)
			}
			restarted := publicationDNSClient(scheme, restartedObjects, &incarnation)
			restartedProvider := &publicationDNSProvider{publicationProvider: publicationProvider{records: map[string]route53.RecordSetResource{}, tags: provider.tags}}
			for key, record := range provider.records {
				restartedProvider.records[key] = record
			}
			// Include the deleted route's key: startup receipt discovery must
			// still clean up its old publication when the object is absent.
			publicationDNSConverge(t, restarted, restartedProvider, server.URL, keys, recordKey, client.ObjectKeyFromObject(zone))
			publicationDNSAssertOutcome(t, fixture, restarted, restartedProvider, &before, children.Items, provider.records, zone.Spec.DomainName)
		})
	}
}

func publicationDNSFixtures(t *testing.T) map[string]publicationDNSContract {
	t.Helper()
	required := []string{"apply-conflict", "gateway-rejected", "healthy", "hostname-removed", "leaf-policy-status-absent", "leaf-route-status-stale", "leaf-unreachable", "parentref-removed", "policy-rejected", "reconciler-restart", "route-deleted", "route-replaced"}
	manifest, err := os.ReadFile(filepath.Join(publicationDNSContractDirectory, "SHA256SUMS"))
	publicationDNSRequire(t, err)
	fixtures := make(map[string]publicationDNSContract, len(required))
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(manifest)), "\n") {
		fields := strings.Split(line, "  ")
		if len(fields) != 2 || filepath.Base(fields[1]) != fields[1] || !strings.HasSuffix(fields[1], ".json") {
			t.Fatalf("invalid contract SHA256SUMS entry: %q", line)
		}
		data, err := os.ReadFile(filepath.Join(publicationDNSContractDirectory, fields[1]))
		publicationDNSRequire(t, err)
		if fmt.Sprintf("%x", sha256.Sum256(data)) != fields[0] {
			t.Fatalf("contract checksum mismatch: %s", fields[1])
		}
		var fixture publicationDNSContract
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		publicationDNSRequire(t, decoder.Decode(&fixture))
		if fixture.Scenario+".json" != fields[1] || fixture.Description == "" {
			t.Fatalf("invalid contract scenario metadata: %s", fields[1])
		}
		fixtures[fixture.Scenario] = fixture
		names = append(names, fixture.Scenario)
	}
	sort.Strings(names)
	if !reflect.DeepEqual(required, names) {
		t.Fatalf("contract scenario inventory = %v, want %v", names, required)
	}
	files, err := filepath.Glob(filepath.Join(publicationDNSContractDirectory, "*.json"))
	publicationDNSRequire(t, err)
	if len(files) != len(names) {
		t.Fatal("unlisted contract fixture")
	}
	return fixtures
}

func publicationDNSClient(scheme *runtime.Scheme, objects []client.Object, incarnation *int) client.Client {
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).
		WithStatusSubresource(&gatewayv1.Gateway{}, &gatewayv1.HTTPRoute{}, &endpointv1alpha1.EndpointRecordSet{}, &dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneUnit{}, &dnsv1alpha1.RecordSet{}).
		WithInterceptorFuncs(interceptor.Funcs{Create: func(ctx context.Context, cl client.WithWatch, object client.Object, opts ...client.CreateOption) error {
			*incarnation++
			object.SetUID(types.UID(fmt.Sprintf("publication-contract-%d", *incarnation)))
			object.SetGeneration(1)
			return cl.Create(ctx, object, opts...)
		}}).Build()
}

func publicationDNSConverge(t *testing.T, cl client.Client, provider *publicationDNSProvider, conversionURL string, routeKeys []client.ObjectKey, recordKey, zoneKey client.ObjectKey) {
	t.Helper()
	ignoredStatusWrites := 0
	cl = interceptor.NewClient(cl.(client.WithWatch), interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, subresource string, object client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			desired, isEndpoint := object.(*endpointv1alpha1.EndpointRecordSet)
			ignored := false
			if subresource == "status" && isEndpoint {
				var observed endpointv1alpha1.EndpointRecordSet
				publicationDNSRequire(t, c.Get(ctx, client.ObjectKeyFromObject(desired), &observed))
				candidate := desired.DeepCopy()
				publicationDNSNormalizeConvergence(&observed)
				publicationDNSNormalizeConvergence(candidate)
				before, err := json.Marshal(&observed)
				publicationDNSRequire(t, err)
				after, err := json.Marshal(candidate)
				publicationDNSRequire(t, err)
				ignored = bytes.Equal(before, after)
			}
			err := c.SubResource(subresource).Patch(ctx, object, patch, opts...)
			if err == nil && ignored {
				ignoredStatusWrites++
			}
			return err
		},
	})
	gateway := &Reconciler{Client: cl, EndpointRecordSetNamespace: recordKey.Namespace}
	endpoint := &endpointcontroller.Reconciler{Client: cl, RecordSetNamespace: recordKey.Namespace, RESTConfig: &rest.Config{Host: conversionURL}}
	composition := &corezoneunit.ZoneUnitCompositionReconciler{Client: cl}
	dns := &route53.ZoneReconciler{Client: cl, Provider: provider}
	var previous []byte
	for pass := range 16 {
		upserts, deletes := provider.upserts, provider.deletes
		for _, key := range routeKeys {
			reconcileRetentionRoute(t, gateway, key)
		}
		for _, step := range []struct {
			reconciler reconcile.Reconciler
			key        client.ObjectKey
		}{{endpoint, recordKey}, {composition, zoneKey}, {dns, zoneKey}} {
			_, err := step.reconciler.Reconcile(t.Context(), reconcile.Request{NamespacedName: step.key})
			publicationDNSRequire(t, err)
		}
		objects := publicationDNSStoredObjects(t, cl)
		for _, object := range objects {
			publicationDNSNormalizeConvergence(object)
		}
		current, err := json.Marshal(struct {
			Objects []client.Object
			Records map[string]route53.RecordSetResource
			Tags    map[string]string
			Actions []json.RawMessage
		}{objects, provider.records, provider.tags, provider.actions})
		publicationDNSRequire(t, err)
		if bytes.Equal(previous, current) && upserts == provider.upserts && deletes == provider.deletes {
			t.Logf("converged passes=%d ignored-status-only-writes=%d provider-upserts=%d provider-deletes=%d records=%d", pass+1, ignoredStatusWrites, provider.upserts, provider.deletes, len(provider.records))
			return
		}
		previous = current
	}
	t.Fatal("publication consumer chain did not converge in 16 passes")
}

func publicationDNSNormalizeConvergence(object client.Object) {
	object.SetResourceVersion("")
	object.SetManagedFields(nil)
	if endpoint, ok := object.(*endpointv1alpha1.EndpointRecordSet); ok {
		// APT-1185: hostname Resolved lastTransitionTime restamps each reconcile (endpointrecordset/controller.go:271,376,771-787).
		for i := range endpoint.Status.Hostnames {
			for j := range endpoint.Status.Hostnames[i].Conditions {
				endpoint.Status.Hostnames[i].Conditions[j].LastTransitionTime = metav1.Time{}
			}
		}
	}
}

func publicationDNSStoredObjects(t *testing.T, cl client.Client) []client.Object {
	t.Helper()
	var objects []client.Object
	for _, list := range []client.ObjectList{
		&gatewayv1.GatewayList{}, &gatewayv1.HTTPRouteList{}, &endpointv1alpha1.EndpointRecordSetList{}, &endpointv1alpha1.EndpointProviderCapabilityList{},
		&dnsv1alpha1.RecordSetList{}, &dnsv1alpha1.ZoneList{}, &dnsv1alpha1.ZoneUnitList{}, &dnsv1alpha1.ZoneClassList{}, &dnsv1alpha1.ProviderList{},
		&dnsv1alpha1.RecordWithdrawalList{}, &route53v1alpha1.Route53IdentityList{}, &corev1.NamespaceList{},
	} {
		publicationDNSRequire(t, cl.List(t.Context(), list))
		items, err := apimeta.ExtractList(list)
		publicationDNSRequire(t, err)
		for _, item := range items {
			objects = append(objects, item.(client.Object))
		}
	}
	return objects
}

func publicationDNSAssertOutcome(t *testing.T, fixture publicationDNSContract, cl client.Client, provider *publicationDNSProvider, before *endpointv1alpha1.EndpointRecordSet, children []dnsv1alpha1.RecordSet, records map[string]route53.RecordSetResource, domain string) {
	t.Helper()
	removed := map[string]bool{}
	switch fixture.Expect.DNS {
	case "retain":
		if !reflect.DeepEqual(fixture.Expect.Hostnames, before.Spec.Hostnames) {
			t.Fatal("retain fixture must name every established hostname")
		}
		// Count actions, not just final records: DELETE followed by UPSERT is
		// a withdrawal regression even when the final DNS snapshot looks right.
		if provider.deletes != 0 {
			t.Fatalf("retained publication sent Route 53 DELETE: %v", provider.deletedNames)
		}
	case "withdraw-hostnames", "withdraw-all":
		for _, hostname := range fixture.Expect.Hostnames {
			removed[hostname] = true
		}
	default:
		t.Fatalf("unknown DNS expectation %q", fixture.Expect.DNS)
	}
	var expected []string
	for _, hostname := range before.Spec.Hostnames {
		if !removed[hostname] {
			expected = append(expected, hostname)
		}
	}
	if fixture.Expect.DNS == "withdraw-all" && len(expected) != 0 {
		t.Fatal("withdraw-all fixture does not cover all established hostnames")
	}
	var after endpointv1alpha1.EndpointRecordSet
	err := cl.Get(t.Context(), client.ObjectKeyFromObject(before), &after)
	if len(expected) == 0 {
		if !apierrors.IsNotFound(err) {
			t.Fatalf("withdrawn EndpointRecordSet remains: error=%v object=%#v", err, after)
		}
	} else {
		publicationDNSRequire(t, err)
		if after.UID != before.UID || !after.DeletionTimestamp.IsZero() || !reflect.DeepEqual(after.Spec.Hostnames, expected) || !reflect.DeepEqual(after.Spec.Targets, before.Spec.Targets) {
			t.Fatalf("unexpected EndpointRecordSet transition: expected=%v before=%#v after=%#v", expected, before, after)
		}
		if fixture.Expect.DNS == "retain" && !reflect.DeepEqual(after.Spec, before.Spec) {
			t.Fatal("retained EndpointRecordSet spec changed")
		}
	}
	var current dnsv1alpha1.RecordSetList
	publicationDNSRequire(t, cl.List(t.Context(), &current))
	if len(current.Items) != len(expected) {
		t.Fatalf("child RecordSets = %d, want %d", len(current.Items), len(expected))
	}
	for _, child := range children {
		hostname := strings.TrimSuffix(child.Spec.Name+"."+domain, ".")
		var found dnsv1alpha1.RecordSet
		err := cl.Get(t.Context(), client.ObjectKeyFromObject(&child), &found)
		if removed[hostname] {
			if !apierrors.IsNotFound(err) {
				t.Fatalf("withdrawn child %s remains: %v", hostname, err)
			}
			continue
		}
		publicationDNSRequire(t, err)
		if found.UID != child.UID || !found.DeletionTimestamp.IsZero() || !reflect.DeepEqual(found.Spec, child.Spec) {
			t.Fatalf("retained child %s changed identity or spec: before=%#v after=%#v", hostname, child, found)
		}
	}
	if len(provider.records) != len(expected) || provider.deletes != len(removed) {
		t.Fatalf("provider outcome: records=%d deletes=%d; want records=%d deletes=%d", len(provider.records), provider.deletes, len(expected), len(removed))
	}
	for name, beforeRecord := range records {
		afterRecord, exists := provider.records[name]
		if removed[strings.TrimSuffix(name, ".")] {
			if exists {
				t.Fatalf("withdrawn Route 53 name remains: %s", name)
			}
		} else if !exists || !reflect.DeepEqual(afterRecord, beforeRecord) {
			t.Fatalf("retained Route 53 record changed: %s", name)
		}
	}
	for _, name := range provider.deletedNames {
		if !removed[strings.TrimSuffix(name, ".")] {
			t.Fatalf("Route 53 DELETE targeted an unaffected hostname: %s", name)
		}
	}
}

type publicationDNSProvider struct {
	publicationProvider
	deletedNames []string
	actions      []json.RawMessage
}

func (p *publicationDNSProvider) GetHostedZone(context.Context, string) (route53.HostedZone, error) {
	return route53.HostedZone{ID: "ZPUBLICATION", Name: "project.example"}, nil
}

func (p *publicationDNSProvider) ChangeRecordSets(ctx context.Context, id string, changes []route53.RecordSetChange) (*route53v1alpha1.Route53Change, error) {
	actions, err := json.Marshal(changes)
	if err != nil {
		return nil, err
	}
	p.actions = append(p.actions, actions)
	for _, change := range changes {
		if change.Action == route53.RecordSetChangeActionDelete {
			p.deletedNames = append(p.deletedNames, change.RecordSet.Name)
		}
	}
	return p.publicationProvider.ChangeRecordSets(ctx, id, changes)
}

func publicationDNSZone() *dnsv1alpha1.Zone {
	return &dnsv1alpha1.Zone{
		ObjectMeta: metav1.ObjectMeta{Namespace: "dns-system", Name: "project", UID: "publication-zone", Generation: 1},
		Spec: dnsv1alpha1.ZoneSpec{
			DomainName: "project.example", Provider: route53v1alpha1.ProviderRef,
			ZoneClassRef: dnsv1alpha1.ZoneClassReference{Name: "route53"},
			Adoption:     runtime.RawExtension{Raw: []byte(`{"hostedZoneId":"ZPUBLICATION"}`)},
			AllowedRecordSets: []dnsv1alpha1.AllowedRecordSet{{
				Namespaces: dnsv1alpha1.AllowedRecordSetNamespaces{Selector: metav1.LabelSelector{}},
				Records: []dnsv1alpha1.AllowedRecord{{
					Name: dnsv1alpha1.RecordNamePolicy{Pattern: ".*"}, Types: []dnsv1alpha1.RecordType{dnsv1alpha1.RecordTypeA},
				}},
			}},
		},
	}
}

func publicationDNSInfrastructure(zone *dnsv1alpha1.Zone) []client.Object {
	return []client.Object{
		zone,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: zone.Namespace}},
		&dnsv1alpha1.Provider{
			ObjectMeta: metav1.ObjectMeta{Name: route53v1alpha1.ProviderName},
			Spec: dnsv1alpha1.ProviderSpec{Versions: []dnsv1alpha1.ProviderVersion{{
				Name: route53v1alpha1.ProviderVersion, Served: true, Storage: true,
				RecordSet: dnsv1alpha1.ProviderRecordSet{SupportedTypes: []dnsv1alpha1.RecordType{dnsv1alpha1.RecordTypeA}},
			}}},
		},
		&dnsv1alpha1.ZoneClass{
			ObjectMeta: metav1.ObjectMeta{Namespace: zone.Namespace, Name: "route53", Generation: 1},
			Spec: dnsv1alpha1.ZoneClassSpec{
				Provider: route53v1alpha1.ProviderRef, ControllerName: route53.DefaultControllerName,
				IdentityRef: &dnsv1alpha1.LocalObjectReference{Name: "route53"},
				Parameters:  runtime.RawExtension{Raw: []byte(`{}`)},
				AllowedZones: dnsv1alpha1.ZoneClassAllowedZones{
					Namespaces: dnsv1alpha1.NamespacePolicy{From: dnsv1alpha1.NamespacesFromSame},
				},
			},
			Status: dnsv1alpha1.ZoneClassStatus{Conditions: []metav1.Condition{{
				Type: "Accepted", Status: metav1.ConditionTrue, ObservedGeneration: 1,
			}}},
		},
		&route53v1alpha1.Route53Identity{
			ObjectMeta: metav1.ObjectMeta{Namespace: zone.Namespace, Name: "route53", Generation: 1},
			Status: route53v1alpha1.Route53IdentityStatus{Conditions: []metav1.Condition{
				{Type: "Accepted", Status: metav1.ConditionTrue, ObservedGeneration: 1},
				{Type: "Ready", Status: metav1.ConditionTrue, ObservedGeneration: 1},
			}},
		},
		&endpointv1alpha1.EndpointProviderCapability{
			ObjectMeta: metav1.ObjectMeta{Name: "route53"},
			Spec: endpointv1alpha1.EndpointProviderCapabilitySpec{
				Provider: route53v1alpha1.ProviderRef,
				Conversion: endpointv1alpha1.EndpointRecordSetConversionAPI{
					Group: route53conversion.GroupName, Version: route53conversion.Version, Resource: route53conversion.Resource,
				},
			},
		},
	}
}

func publicationDNSRequire(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
