package gateway

import (
	"net/http/httptest"
	"reflect"
	"testing"

	endpointcontroller "github.com/appthrust/dns-api/internal/go/apps/endpoint/controllers/endpointrecordset"
	corezoneunit "github.com/appthrust/dns-api/internal/go/core/controllers/zoneunit"
	route53 "github.com/appthrust/dns-api/internal/go/providers/route53/controllers/zoneunit"
	route53conversion "github.com/appthrust/dns-api/internal/go/providers/route53/conversion"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestGatewayListenerReceiptContinuity(t *testing.T) {
	for _, test := range []struct {
		name              string
		routeNamespace    string
		retain            bool
		pendingConversion bool
		change            func(*gatewayv1.Gateway, *gatewayv1.HTTPRoute)
		afterPending      func(*testing.T, *Reconciler, client.ObjectKey, client.ObjectKey)
	}{
		{name: "SameNamespaceAccepted", retain: true},
		{
			name: "SameNamespaceAcceptedAddressObservationPending", retain: true,
			change: func(gateway *gatewayv1.Gateway, _ *gatewayv1.HTTPRoute) {
				gateway.Status.Addresses = nil
			},
		},
		{name: "SameNamespaceAcceptedConversionObservationPending", retain: true, pendingConversion: true},
		{
			name: "CrossNamespaceRejected", routeNamespace: "app",
			change: func(_ *gatewayv1.Gateway, route *gatewayv1.HTTPRoute) {
				route.Status.Parents[0].Conditions[0].Status = metav1.ConditionFalse
			},
		},
		{name: "CrossNamespaceStillAcceptedCannotRefresh", routeNamespace: "app"},
		{
			name: "ListenerRemoved",
			change: func(gateway *gatewayv1.Gateway, _ *gatewayv1.HTTPRoute) {
				gateway.Spec.Listeners = nil
			},
		},
		{
			name: "GatewayClassChanged",
			change: func(gateway *gatewayv1.Gateway, _ *gatewayv1.HTTPRoute) {
				gateway.Spec.GatewayClassName = "another-controller"
			},
		},
		{
			name: "RouteGenerationNotAccepted",
			change: func(_ *gatewayv1.Gateway, route *gatewayv1.HTTPRoute) {
				route.Generation++
			},
		},
		{
			name: "RouteRejected",
			change: func(_ *gatewayv1.Gateway, route *gatewayv1.HTTPRoute) {
				route.Status.Parents[0].Conditions[0].Status = metav1.ConditionFalse
			},
		},
		{
			name: "SameNamespaceUnknownThenAccepted", retain: true,
			change: func(_ *gatewayv1.Gateway, route *gatewayv1.HTTPRoute) {
				route.Status.Parents[0].Conditions[0].Status = metav1.ConditionUnknown
			},
		},
		{
			name: "UnknownAddressLossThenChangedTarget", retain: true,
			change: func(gateway *gatewayv1.Gateway, route *gatewayv1.HTTPRoute) {
				route.Status.Parents[0].Conditions[0].Status = metav1.ConditionUnknown
				gateway.Status.Addresses = nil
			},
			afterPending: func(t *testing.T, r *Reconciler, _, gatewayKey client.ObjectKey) {
				setGatewayAddresses(t, r, gatewayKey, []gatewayv1.GatewayStatusAddress{{Value: "192.0.2.2"}})
			},
		},
		{
			name: "UnknownThenRejected", retain: true,
			change: func(_ *gatewayv1.Gateway, route *gatewayv1.HTTPRoute) {
				route.Status.Parents[0].Conditions[0].Status = metav1.ConditionUnknown
			},
			afterPending: func(t *testing.T, r *Reconciler, routeKey, _ client.ObjectKey) {
				setRouteAcceptance(t, r, routeKey, metav1.ConditionFalse)
			},
		},
		{
			name: "UnknownThenHostnameMismatch", retain: true,
			change: func(_ *gatewayv1.Gateway, route *gatewayv1.HTTPRoute) {
				route.Status.Parents[0].Conditions[0].Status = metav1.ConditionUnknown
			},
			afterPending: func(t *testing.T, r *Reconciler, _, gatewayKey client.ObjectKey) {
				var gateway gatewayv1.Gateway
				publicationDNSRequire(t, r.Get(t.Context(), gatewayKey, &gateway))
				hostname := gatewayv1.Hostname("other.project.example")
				gateway.Spec.Listeners[0].Hostname = &hostname
				gateway.Generation++
				publicationDNSRequire(t, r.Update(t.Context(), &gateway))
			},
		},
		{
			name: "CrossNamespaceUnknown", routeNamespace: "app",
			change: func(_ *gatewayv1.Gateway, route *gatewayv1.HTTPRoute) {
				route.Status.Parents[0].Conditions[0].Status = metav1.ConditionUnknown
			},
		},
		{
			name: "UnknownWithChangedTarget",
			change: func(gateway *gatewayv1.Gateway, route *gatewayv1.HTTPRoute) {
				route.Status.Parents[0].Conditions[0].Status = metav1.ConditionUnknown
				gateway.Status.Addresses[0].Value = "192.0.2.2"
			},
		},
		{
			name: "UnknownWithRemovedListener",
			change: func(gateway *gatewayv1.Gateway, route *gatewayv1.HTTPRoute) {
				route.Status.Parents[0].Conditions[0].Status = metav1.ConditionUnknown
				gateway.Spec.Listeners = nil
			},
		},
		{
			name: "UnknownWithRejectedKind",
			change: func(gateway *gatewayv1.Gateway, route *gatewayv1.HTTPRoute) {
				route.Status.Parents[0].Conditions[0].Status = metav1.ConditionUnknown
				gateway.Spec.Listeners[0].AllowedRoutes.Kinds = []gatewayv1.RouteGroupKind{{Kind: "TCPRoute"}}
			},
		},
		{
			name: "UnknownWithHTTPSChangedToTLS",
			change: func(gateway *gatewayv1.Gateway, route *gatewayv1.HTTPRoute) {
				route.Status.Parents[0].Conditions[0].Status = metav1.ConditionUnknown
				gateway.Spec.Listeners[0].Protocol = gatewayv1.TLSProtocolType
			},
		},
		{
			name: "HostnameNoLongerMatches",
			change: func(gateway *gatewayv1.Gateway, _ *gatewayv1.HTTPRoute) {
				hostname := gatewayv1.Hostname("other.project.example")
				gateway.Spec.Listeners[0].Hostname = &hostname
			},
		},
		{
			name: "HTTPRouteKindNoLongerAllowed",
			change: func(gateway *gatewayv1.Gateway, _ *gatewayv1.HTTPRoute) {
				gateway.Spec.Listeners[0].AllowedRoutes.Kinds = []gatewayv1.RouteGroupKind{{Kind: "TCPRoute"}}
			},
		},
		{
			name: "SelectorCannotRefresh",
			change: func(gateway *gatewayv1.Gateway, _ *gatewayv1.HTTPRoute) {
				selector := gatewayv1.NamespacesFromSelector
				gateway.Spec.Listeners[0].AllowedRoutes.Namespaces = &gatewayv1.RouteNamespaces{
					From: &selector, Selector: &metav1.LabelSelector{},
				}
			},
		},
		{
			name: "ChangedTargetCannotRefresh",
			change: func(gateway *gatewayv1.Gateway, _ *gatewayv1.HTTPRoute) {
				gateway.Status.Addresses[0].Value = "192.0.2.2"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			scheme := testScheme(t)
			for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, dnsv1alpha1.AddToScheme, route53v1alpha1.AddToScheme} {
				publicationDNSRequire(t, add(scheme))
			}
			gateway := receiptSyncGateway("public", "listener-continuity-gateway", "192.0.2.1", "https")
			gateway.Spec.Listeners[0].Protocol = gatewayv1.HTTPSProtocolType
			gateway.Spec.Listeners[0].Port = 443
			namespace := test.routeNamespace
			if namespace == "" {
				namespace = gateway.Namespace
			}
			route := retentionRoute(namespace, "ha", "listener-continuity-route", "ha.project.example", metav1.ConditionTrue)
			zone := publicationDNSZone()
			objects := append(publicationDNSInfrastructure(zone), gateway, route)
			incarnation := 0
			cl := publicationDNSClient(scheme, objects, &incarnation)
			routeKey := client.ObjectKeyFromObject(route)
			recordKey := client.ObjectKey{Namespace: zone.Namespace, Name: generatedGatewayEndpointRecordSetName(gateway.Namespace, gateway.Name)}
			r := &Reconciler{Client: cl, EndpointRecordSetNamespace: recordKey.Namespace}
			reconcileRetentionRoute(t, r, routeKey)
			markUnprotected(t, r, recordKey)
			server := httptest.NewServer(route53conversion.NewHandler())
			t.Cleanup(server.Close)
			provider := &publicationDNSProvider{publicationProvider: publicationProvider{records: map[string]route53.RecordSetResource{}}}
			publicationDNSConverge(t, cl, provider, server.URL, []client.ObjectKey{routeKey}, recordKey, client.ObjectKeyFromObject(zone))

			var before endpointv1alpha1.EndpointRecordSet
			publicationDNSRequire(t, cl.Get(ctx, recordKey, &before))
			oldReceipt := existingAdmissionReceipt(&before)
			if before.UID == "" || len(oldReceipt.Bindings) != 1 || oldReceipt.Bindings[0].ListenerDigest != gatewayListenerDigest(gateway.Spec.Listeners[0]) {
				t.Fatalf("initial admission receipt was not minted by the reconciler: %#v", oldReceipt)
			}
			var children dnsv1alpha1.RecordSetList
			publicationDNSRequire(t, cl.List(ctx, &children))
			if len(children.Items) != 1 || children.Items[0].UID == "" || provider.upserts != 1 || provider.deletes != 0 || len(provider.records) != 1 {
				t.Fatalf("initial publication did not reach DNS: children=%d upserts=%d deletes=%d records=%d", len(children.Items), provider.upserts, provider.deletes, len(provider.records))
			}
			records := make(map[string]route53.RecordSetResource, len(provider.records))
			for name, record := range provider.records {
				records[name] = record
			}
			provider.upserts, provider.deletes = 0, 0

			publicationDNSRequire(t, cl.Get(ctx, client.ObjectKeyFromObject(gateway), gateway))
			publicationDNSRequire(t, cl.Get(ctx, routeKey, route))
			same := gatewayv1.NamespacesFromSame
			gateway.Spec.Listeners[0].AllowedRoutes.Namespaces.From = &same
			gateway.Generation++
			if test.change != nil {
				test.change(gateway, route)
			}
			gatewayStatus, routeStatus := gateway.Status, route.Status
			publicationDNSRequire(t, cl.Update(ctx, gateway))
			gateway.Status = gatewayStatus
			publicationDNSRequire(t, cl.Status().Update(ctx, gateway))
			publicationDNSRequire(t, cl.Update(ctx, route))
			route.Status = routeStatus
			publicationDNSRequire(t, cl.Status().Update(ctx, route))
			if test.pendingConversion {
				publicationDNSRequire(t, cl.Delete(ctx, &endpointv1alpha1.EndpointProviderCapability{ObjectMeta: metav1.ObjectMeta{Name: "route53"}}))
			}

			// Restart the source reconciler: only the persisted receipt may
			// authorize continuity across the listener policy change.
			r = &Reconciler{Client: cl, EndpointRecordSetNamespace: recordKey.Namespace}
			reconcileRetentionRoute(t, r, routeKey)
			var after endpointv1alpha1.EndpointRecordSet
			publicationDNSRequire(t, cl.Get(ctx, recordKey, &after))
			if test.retain {
				if after.UID != before.UID || !after.DeletionTimestamp.IsZero() || !reflect.DeepEqual(after.Spec, before.Spec) {
					t.Fatalf("listener-only change disrupted publication: before=%#v after=%#v", before, after)
				}
				refreshed := existingAdmissionReceipt(&after)
				newDigest := gatewayListenerDigest(gateway.Spec.Listeners[0])
				if oldReceipt.Bindings[0].ListenerDigest == newDigest {
					t.Fatal("test did not change the listener digest")
				}
				if routeParentAcceptance(route, route.Spec.ParentRefs[0]) == parentAcceptanceUnknown {
					if after.Annotations[admissionReceiptAnnotation] != before.Annotations[admissionReceiptAnnotation] ||
						after.ResourceVersion != before.ResourceVersion {
						t.Fatal("Unknown admission rewrote the established receipt")
					}
				} else {
					oldReceipt.Bindings[0].ListenerDigest = newDigest
				}
				if !reflect.DeepEqual(refreshed, oldReceipt) {
					t.Fatalf("receipt does not match its admission state: got=%#v want=%#v", refreshed, oldReceipt)
				}
				revision := after.ResourceVersion
				reconcileRetentionRoute(t, r, routeKey)
				publicationDNSRequire(t, cl.Get(ctx, recordKey, &after))
				if after.ResourceVersion != revision {
					t.Fatal("refreshed receipt was rewritten on replay")
				}
			} else if after.DeletionTimestamp.IsZero() {
				t.Fatalf("revoked binding retained the established intent: %#v", after)
			}

			// Advance only consumers of this decision. Fresh source admission
			// after deletion remains a separate Gateway-controller contract.
			endpoint := &endpointcontroller.Reconciler{Client: cl, RecordSetNamespace: recordKey.Namespace, RESTConfig: &rest.Config{Host: server.URL}}
			composition := &corezoneunit.ZoneUnitCompositionReconciler{Client: cl}
			dns := &route53.ZoneReconciler{Client: cl, Provider: provider}
			for range 8 {
				for _, step := range []struct {
					reconciler reconcile.Reconciler
					key        client.ObjectKey
				}{{endpoint, recordKey}, {composition, client.ObjectKeyFromObject(zone)}, {dns, client.ObjectKeyFromObject(zone)}} {
					_, err := step.reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: step.key})
					publicationDNSRequire(t, err)
				}
			}
			fixture := publicationDNSContract{}
			fixture.Expect.DNS = "withdraw-all"
			fixture.Expect.Hostnames = before.Spec.Hostnames
			if test.retain {
				fixture.Expect.DNS = "retain"
			}
			publicationDNSAssertOutcome(t, fixture, cl, provider, &before, children.Items, records, zone.Spec.DomainName)
			if test.retain {
				var child dnsv1alpha1.RecordSet
				publicationDNSRequire(t, cl.Get(ctx, client.ObjectKeyFromObject(&children.Items[0]), &child))
				if child.ResourceVersion != children.Items[0].ResourceVersion {
					t.Fatal("listener-only receipt refresh wrote an established RecordSet")
				}
				if test.afterPending != nil {
					// First prove the old publication survived the pending
					// observation, then observe the revocation independently.
					test.afterPending(t, r, routeKey, client.ObjectKeyFromObject(gateway))
					r = &Reconciler{Client: cl, EndpointRecordSetNamespace: recordKey.Namespace}
					reconcileRetentionRoute(t, r, routeKey)
					publicationDNSRequire(t, cl.Get(ctx, recordKey, &after))
					if after.DeletionTimestamp.IsZero() {
						t.Fatalf("revocation after pending continuity retained the intent: %#v", after)
					}
					fixture.Expect.DNS = "withdraw-all"
					publicationDNSConverge(t, cl, provider, server.URL, []client.ObjectKey{routeKey}, recordKey, client.ObjectKeyFromObject(zone))
					publicationDNSAssertOutcome(t, fixture, cl, provider, &before, children.Items, records, zone.Spec.DomainName)
					return
				}
				if routeParentAcceptance(route, route.Spec.ParentRefs[0]) == parentAcceptanceUnknown {
					// Current acceptance, not the pending observation above,
					// authorizes replacing the old listener digest.
					setRouteAcceptance(t, r, routeKey, metav1.ConditionTrue)
					r = &Reconciler{Client: cl, EndpointRecordSetNamespace: recordKey.Namespace}
					reconcileRetentionRoute(t, r, routeKey)
					publicationDNSRequire(t, cl.Get(ctx, recordKey, &after))
					oldReceipt.Bindings[0].ListenerDigest = gatewayListenerDigest(gateway.Spec.Listeners[0])
					if !reflect.DeepEqual(existingAdmissionReceipt(&after), oldReceipt) {
						t.Fatalf("current admission did not refresh the pending receipt: %#v", existingAdmissionReceipt(&after))
					}
					publicationDNSConverge(t, cl, provider, server.URL, []client.ObjectKey{routeKey}, recordKey, client.ObjectKeyFromObject(zone))
					publicationDNSAssertOutcome(t, fixture, cl, provider, &before, children.Items, records, zone.Spec.DomainName)
					publicationDNSRequire(t, cl.Get(ctx, client.ObjectKeyFromObject(&children.Items[0]), &child))
					if child.ResourceVersion != children.Items[0].ResourceVersion {
						t.Fatal("Unknown-to-Accepted transition wrote an established RecordSet")
					}
				}
			}
		})
	}
}

func TestListenerAllowsReceiptRefresh(t *testing.T) {
	gateway := &gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "platform"}}
	group := gatewayv1.Group(gatewayv1.GroupName)
	otherGroup := gatewayv1.Group("other.example")
	for _, test := range []struct {
		name      string
		namespace string
		policy    gatewayv1.FromNamespaces
		protocol  gatewayv1.ProtocolType
		kinds     []gatewayv1.RouteGroupKind
		want      bool
	}{
		{name: "DefaultSameNamespace", namespace: "platform", want: true},
		{name: "DefaultCrossNamespace", namespace: "app"},
		{name: "AllCrossNamespace", namespace: "app", policy: gatewayv1.NamespacesFromAll, want: true},
		{name: "SameNamespace", namespace: "platform", policy: gatewayv1.NamespacesFromSame, want: true},
		{name: "SameCrossNamespace", namespace: "app", policy: gatewayv1.NamespacesFromSame},
		{name: "SelectorSameNamespace", namespace: "platform", policy: gatewayv1.NamespacesFromSelector},
		{name: "ExplicitHTTPRoute", namespace: "platform", kinds: []gatewayv1.RouteGroupKind{{Group: &group, Kind: "HTTPRoute"}}, want: true},
		{name: "DefaultGroupHTTPRoute", namespace: "platform", kinds: []gatewayv1.RouteGroupKind{{Kind: "HTTPRoute"}}, want: true},
		{name: "WrongKind", namespace: "platform", kinds: []gatewayv1.RouteGroupKind{{Kind: "TCPRoute"}}},
		{name: "WrongGroup", namespace: "platform", kinds: []gatewayv1.RouteGroupKind{{Group: &otherGroup, Kind: "HTTPRoute"}}},
		{name: "EmptyKindsHTTP", namespace: "platform", protocol: gatewayv1.HTTPProtocolType, want: true},
		{name: "EmptyKindsTLS", namespace: "platform", protocol: gatewayv1.TLSProtocolType},
		{name: "EmptyKindsTCP", namespace: "platform", protocol: gatewayv1.TCPProtocolType},
		{name: "EmptyKindsUDP", namespace: "platform", protocol: gatewayv1.UDPProtocolType},
	} {
		t.Run(test.name, func(t *testing.T) {
			route := &gatewayv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Namespace: test.namespace}}
			listener := gatewayv1.Listener{Protocol: gatewayv1.HTTPSProtocolType, AllowedRoutes: &gatewayv1.AllowedRoutes{Kinds: test.kinds}}
			if test.protocol != "" {
				listener.Protocol = test.protocol
			}
			if test.policy != "" {
				listener.AllowedRoutes.Namespaces = &gatewayv1.RouteNamespaces{From: &test.policy}
			}
			if got := listenerAllowsReceiptRefresh(route, gateway, listener); got != test.want {
				t.Fatalf("listenerAllowsReceiptRefresh = %t, want %t", got, test.want)
			}
		})
	}
}

func TestAdmissionReceiptListenerRefreshIdentity(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*admissionReceiptBinding)
	}{
		{name: "RouteNamespace", change: func(binding *admissionReceiptBinding) { binding.RouteNamespace = "other" }},
		{name: "RouteName", change: func(binding *admissionReceiptBinding) { binding.RouteName = "other" }},
		{name: "RouteUID", change: func(binding *admissionReceiptBinding) { binding.RouteUID = "other" }},
		{name: "RouteGeneration", change: func(binding *admissionReceiptBinding) { binding.RouteGeneration++ }},
		{name: "GatewayUID", change: func(binding *admissionReceiptBinding) { binding.GatewayUID = "other" }},
		{name: "GatewayClass", change: func(binding *admissionReceiptBinding) { binding.GatewayClass = "other" }},
		{name: "ParentRef", change: func(binding *admissionReceiptBinding) { binding.ParentRef = "other" }},
		{name: "Listener", change: func(binding *admissionReceiptBinding) { binding.Listener = "other" }},
		{name: "Hostname", change: func(binding *admissionReceiptBinding) { binding.Hostnames = []string{"other.project.example"} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			route := retentionRoute("platform", "ha", "route", "ha.project.example", metav1.ConditionTrue)
			gateway := receiptSyncGateway("public", "gateway", "192.0.2.1", "https")
			binding := routeGatewayBindings(route, gateway)[0]
			previous, ok := newAdmissionReceiptBinding(route, gateway, binding)
			if !ok {
				t.Fatal("could not construct the previous binding")
			}
			previous.Hostnames = []string{"ha.project.example"}
			same := gatewayv1.NamespacesFromSame
			binding.listener = *binding.listener.DeepCopy()
			binding.listener.AllowedRoutes.Namespaces.From = &same
			receipt := admissionReceipt{Bindings: []admissionReceiptBinding{previous}}
			if _, ok := receipt.matchingBinding(route, gateway, binding, "ha.project.example", true); !ok {
				t.Fatal("listener-only change did not match")
			}
			test.change(&receipt.Bindings[0])
			if _, ok := receipt.matchingBinding(route, gateway, binding, "ha.project.example", true); ok {
				t.Fatal("listener refresh inherited a different identity")
			}
		})
	}
}
