package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const admissionReceiptAnnotation = "gateway.endpoint.dns.appthrust.io/admission-receipt"

type admissionReceipt struct {
	Bindings []admissionReceiptBinding `json:"bindings"`
}

type admissionReceiptBinding struct {
	Hostnames       []string `json:"hostnames"`
	RouteNamespace  string   `json:"routeNamespace"`
	RouteName       string   `json:"routeName"`
	RouteUID        string   `json:"routeUID"`
	RouteGeneration int64    `json:"routeGeneration"`
	GatewayUID      string   `json:"gatewayUID"`
	GatewayClass    string   `json:"gatewayClass"`
	ParentRef       string   `json:"parentRef"`
	Listener        string   `json:"listener"`
	ListenerDigest  string   `json:"listenerDigest"`
}

func existingAdmissionReceipt(record *endpointv1alpha1.EndpointRecordSet) admissionReceipt {
	if record == nil || record.Annotations == nil {
		return admissionReceipt{}
	}
	encoded := record.Annotations[admissionReceiptAnnotation]
	if encoded == "" {
		return admissionReceipt{}
	}
	var receipt admissionReceipt
	if err := json.Unmarshal([]byte(encoded), &receipt); err != nil {
		return admissionReceipt{}
	}
	valid := receipt.Bindings[:0]
	for _, binding := range receipt.Bindings {
		if binding.valid() {
			valid = append(valid, binding)
		}
	}
	receipt.Bindings = valid
	return receipt
}

func (binding admissionReceiptBinding) valid() bool {
	if binding.RouteNamespace == "" || binding.RouteName == "" || binding.RouteUID == "" ||
		binding.RouteGeneration <= 0 || binding.GatewayUID == "" || binding.GatewayClass == "" ||
		binding.ParentRef == "" || binding.Listener == "" || binding.ListenerDigest == "" || len(binding.Hostnames) == 0 {
		return false
	}
	for _, hostname := range binding.Hostnames {
		if hostname == "" {
			return false
		}
	}
	return true
}

func newAdmissionReceiptBinding(route *gatewayv1.HTTPRoute, gateway *gatewayv1.Gateway, binding routeGatewayBinding) (admissionReceiptBinding, bool) {
	if route.UID == "" || route.Generation <= 0 || gateway.UID == "" || gateway.Spec.GatewayClassName == "" {
		return admissionReceiptBinding{}, false
	}
	listenerDigest := gatewayListenerDigest(binding.listener)
	if listenerDigest == "" {
		return admissionReceiptBinding{}, false
	}
	return admissionReceiptBinding{
		RouteNamespace:  route.Namespace,
		RouteName:       route.Name,
		RouteUID:        string(route.UID),
		RouteGeneration: route.Generation,
		GatewayUID:      string(gateway.UID),
		GatewayClass:    string(gateway.Spec.GatewayClassName),
		ParentRef:       parentRefKey(route.Namespace, binding.ref),
		Listener:        string(binding.listener.Name),
		ListenerDigest:  listenerDigest,
	}, true
}

func (receipt admissionReceipt) matchingBinding(route *gatewayv1.HTTPRoute, gateway *gatewayv1.Gateway, binding routeGatewayBinding, hostname string) (admissionReceiptBinding, bool) {
	listenerDigest := gatewayListenerDigest(binding.listener)
	if route.UID == "" || route.Generation <= 0 || gateway.UID == "" || listenerDigest == "" {
		return admissionReceiptBinding{}, false
	}
	parentRef := parentRefKey(route.Namespace, binding.ref)
	for _, candidate := range receipt.Bindings {
		if candidate.RouteNamespace != route.Namespace ||
			candidate.RouteName != route.Name ||
			candidate.RouteUID != string(route.UID) ||
			candidate.RouteGeneration != route.Generation ||
			candidate.GatewayUID != string(gateway.UID) ||
			candidate.GatewayClass != string(gateway.Spec.GatewayClassName) ||
			candidate.ParentRef != parentRef ||
			candidate.Listener != string(binding.listener.Name) ||
			candidate.ListenerDigest != listenerDigest ||
			!receiptHasHostname(candidate, hostname) {
			continue
		}
		return candidate, true
	}
	return admissionReceiptBinding{}, false
}

func (receipt admissionReceipt) conflictsCurrentBinding(route *gatewayv1.HTTPRoute, gateway *gatewayv1.Gateway, binding routeGatewayBinding, hostname string) bool {
	listenerDigest := gatewayListenerDigest(binding.listener)
	if route.UID == "" || route.Generation <= 0 || gateway.UID == "" || listenerDigest == "" {
		return false
	}
	parentRef := parentRefKey(route.Namespace, binding.ref)
	conflict := false
	for _, candidate := range receipt.Bindings {
		if candidate.RouteNamespace != route.Namespace ||
			candidate.RouteName != route.Name ||
			candidate.RouteUID != string(route.UID) ||
			candidate.RouteGeneration != route.Generation ||
			candidate.ParentRef != parentRef ||
			candidate.Listener != string(binding.listener.Name) ||
			!receiptHasHostname(candidate, hostname) {
			continue
		}
		if candidate.GatewayUID == string(gateway.UID) &&
			candidate.GatewayClass == string(gateway.Spec.GatewayClassName) &&
			candidate.ListenerDigest == listenerDigest {
			return false
		}
		conflict = true
	}
	return conflict
}

func receiptHasHostname(binding admissionReceiptBinding, hostname string) bool {
	for _, candidate := range binding.Hostnames {
		if candidate == hostname {
			return true
		}
	}
	return false
}

func (receipt admissionReceipt) referencesRoute(namespace, name string) bool {
	for _, binding := range receipt.Bindings {
		if binding.RouteNamespace == namespace && binding.RouteName == name {
			return true
		}
	}
	return false
}

func addReceiptHostname(bindings map[string]admissionReceiptBinding, binding admissionReceiptBinding, hostname string) {
	key := admissionReceiptBindingKey(binding)
	current, found := bindings[key]
	if !found {
		current = binding
		current.Hostnames = nil
	}
	if !receiptHasHostname(current, hostname) {
		current.Hostnames = append(current.Hostnames, hostname)
	}
	bindings[key] = current
}

func retainReceiptHostnames(bindings map[string]admissionReceiptBinding, hostnames map[string]struct{}) {
	for key, binding := range bindings {
		retained := binding.Hostnames[:0]
		for _, hostname := range binding.Hostnames {
			if _, ok := hostnames[hostname]; ok {
				retained = append(retained, hostname)
			}
		}
		if len(retained) == 0 {
			delete(bindings, key)
			continue
		}
		binding.Hostnames = retained
		bindings[key] = binding
	}
}

func encodeAdmissionReceipt(bindings map[string]admissionReceiptBinding) string {
	if len(bindings) == 0 {
		return ""
	}
	receipt := admissionReceipt{Bindings: make([]admissionReceiptBinding, 0, len(bindings))}
	for _, binding := range bindings {
		sort.Strings(binding.Hostnames)
		receipt.Bindings = append(receipt.Bindings, binding)
	}
	sort.Slice(receipt.Bindings, func(i, j int) bool {
		return admissionReceiptBindingKey(receipt.Bindings[i]) < admissionReceiptBindingKey(receipt.Bindings[j])
	})
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func admissionReceiptBindingKey(binding admissionReceiptBinding) string {
	return strings.Join([]string{
		binding.RouteNamespace,
		binding.RouteName,
		binding.RouteUID,
		strconv.FormatInt(binding.RouteGeneration, 10),
		binding.GatewayUID,
		binding.GatewayClass,
		binding.ParentRef,
		binding.Listener,
		binding.ListenerDigest,
	}, "\x00")
}

func gatewayListenerDigest(listener gatewayv1.Listener) string {
	encoded, err := json.Marshal(listener)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func (r *Reconciler) existingGatewayEndpointRecordSet(ctx context.Context, namespace string, gateway *gatewayv1.Gateway) (*endpointv1alpha1.EndpointRecordSet, error) {
	var record endpointv1alpha1.EndpointRecordSet
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: generatedGatewayEndpointRecordSetName(gateway.Namespace, gateway.Name)}, &record); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if record.Labels[managedByLabel] != managedByValue ||
		record.Labels[generatedLabelGatewayNamespace] != gateway.Namespace ||
		record.Labels[generatedLabelGatewayName] != gateway.Name {
		return nil, nil
	}
	return &record, nil
}

func (r *Reconciler) gatewayKeysFromAdmissionReceipts(ctx context.Context, namespace, routeNamespace, routeName string) ([]client.ObjectKey, error) {
	var records endpointv1alpha1.EndpointRecordSetList
	if err := r.List(ctx, &records, client.InNamespace(namespace), client.MatchingLabels{managedByLabel: managedByValue}); err != nil {
		return nil, err
	}
	keys := map[client.ObjectKey]struct{}{}
	for i := range records.Items {
		record := &records.Items[i]
		labels := record.Labels
		key := client.ObjectKey{Namespace: labels[generatedLabelGatewayNamespace], Name: labels[generatedLabelGatewayName]}
		if key.Namespace == "" || key.Name == "" || !existingAdmissionReceipt(record).referencesRoute(routeNamespace, routeName) {
			continue
		}
		keys[key] = struct{}{}
	}
	result := make([]client.ObjectKey, 0, len(keys))
	for key := range keys {
		result = append(result, key)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Namespace == result[j].Namespace {
			return result[i].Name < result[j].Name
		}
		return result[i].Namespace < result[j].Namespace
	})
	return result, nil
}
