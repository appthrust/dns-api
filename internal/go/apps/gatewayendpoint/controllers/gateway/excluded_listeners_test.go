package gateway

import (
	"reflect"
	"testing"

	endpoint "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestManagedListenersDoNotPublishDNS(t *testing.T) {
	ctx := t.Context()
	wildcard := gatewayv1.Hostname("*.appthrust.app")
	public := &gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "public", Namespace: "platform", UID: "gateway", Generation: 1}, Spec: gatewayv1.GatewaySpec{
		GatewayClassName: "envoy", Listeners: []gatewayv1.Listener{{Name: "https-platform"}, {Name: "https-managed", Hostname: &wildcard}, {Name: "http-managed", Hostname: &wildcard}},
	}, Status: gatewayv1.GatewayStatus{Addresses: []gatewayv1.GatewayStatusAddress{{Value: "pmc.elb.example.test"}}}}
	routes := []*gatewayv1.HTTPRoute{
		acceptedHTTPRoute("platform", "console", "platform", "https-platform", "console.appthrust.com"),
		acceptedHTTPRoute("platform", "redirect", "platform", "http-managed", "*.appthrust.app"),
		acceptedHTTPRoute("dashboards", "acme", "platform", "https-managed", "acme.appthrust.app"),
	}
	objects := []client.Object{public}
	for _, route := range routes {
		route.UID, route.Generation = types.UID(route.Name), 1
		route.Status.Parents[0].Conditions[0].ObservedGeneration = route.Generation
		objects = append(objects, route)
	}
	r := &Reconciler{Client: fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objects...).Build()}
	before, err := r.endpointRecordSetsForGateway(ctx, public, "dns", nil)
	if err != nil || len(before) != 1 || len(before[0].Spec.Hostnames) != 3 {
		t.Fatalf("baseline publication: %#v %v", before, err)
	}
	public.Annotations = map[string]string{excludedListenersAnnotation: " https-managed, http-managed,https-managed "}
	for _, existing := range []*endpoint.EndpointRecordSet{nil, &before[0]} {
		records, err := r.endpointRecordSetsForGateway(ctx, public, "dns", existing)
		if err != nil || len(records) != 1 || !reflect.DeepEqual(records[0].Spec.Hostnames, []string{"console.appthrust.com"}) {
			t.Fatalf("managed hosts must never be emitted or retained: %#v %v", records, err)
		}
	}
	// Even during an address outage, prior admission cannot retain an excluded host.
	public.Status.Addresses = nil
	records, err := r.endpointRecordSetsForGateway(ctx, public, "dns", &before[0])
	if err != nil || len(records) != 1 || !reflect.DeepEqual(records[0].Spec.Hostnames, []string{"console.appthrust.com"}) {
		t.Fatalf("excluded receipt retained during outage: %#v %v", records, err)
	}
	public.Annotations[excludedListenersAnnotation] = "https-platform,https-managed,http-managed"
	records, err = r.endpointRecordSetsForGateway(ctx, public, "dns", &before[0])
	if err != nil || len(records) != 0 {
		t.Fatalf("all excluded listeners must withdraw gateway publication: %#v %v", records, err)
	}
}
