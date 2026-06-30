package gateway

import (
	"context"
	"net"
	"sort"
	"strings"

	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

type parentInfo struct {
	ref       gatewayv1.ParentReference
	gateway   gatewayv1.Gateway
	listener  gatewayv1.Listener
	addresses []gatewayv1.GatewayStatusAddress
}

func (r *Reconciler) acceptedParents(ctx context.Context, route *gatewayv1.HTTPRoute) ([]parentInfo, error) {
	acceptedKeys := acceptedParentRefKeys(route)
	parents := make([]parentInfo, 0, len(route.Spec.ParentRefs))
	for _, ref := range route.Spec.ParentRefs {
		if _, ok := acceptedKeys[parentRefKey(route.Namespace, ref)]; !ok {
			continue
		}
		namespace := route.Namespace
		if ref.Namespace != nil {
			namespace = string(*ref.Namespace)
		}
		var gateway gatewayv1.Gateway
		if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: string(ref.Name)}, &gateway); err != nil {
			return nil, err
		}
		for _, listener := range gateway.Spec.Listeners {
			if ref.SectionName != nil && listener.Name != *ref.SectionName {
				continue
			}
			parents = append(parents, parentInfo{
				ref:       ref,
				gateway:   gateway,
				listener:  listener,
				addresses: gateway.Status.Addresses,
			})
		}
	}
	return parents, nil
}

func allRouteParentRefsAccepted(route *gatewayv1.HTTPRoute) bool {
	if len(route.Spec.ParentRefs) == 0 {
		return false
	}
	acceptedKeys := acceptedParentRefKeys(route)
	for _, ref := range route.Spec.ParentRefs {
		if _, ok := acceptedKeys[parentRefKey(route.Namespace, ref)]; !ok {
			return false
		}
	}
	return true
}

func acceptedParentRefKeys(route *gatewayv1.HTTPRoute) map[string]struct{} {
	acceptedKeys := map[string]struct{}{}
	for _, parent := range route.Status.Parents {
		for _, condition := range parent.Conditions {
			if condition.Type == string(gatewayv1.RouteConditionAccepted) && condition.Status == "True" {
				acceptedKeys[parentRefKey(route.Namespace, parent.ParentRef)] = struct{}{}
			}
		}
	}
	return acceptedKeys
}

func parentRefKey(routeNamespace string, ref gatewayv1.ParentReference) string {
	namespace := routeNamespace
	if ref.Namespace != nil {
		namespace = string(*ref.Namespace)
	}
	section := ""
	if ref.SectionName != nil {
		section = string(*ref.SectionName)
	}
	return namespace + "/" + string(ref.Name) + "/" + section
}

func effectiveHostnames(route *gatewayv1.HTTPRoute, parents []parentInfo) []string {
	hostnames := map[string]struct{}{}
	for _, parent := range parents {
		listenerHostname := ""
		if parent.listener.Hostname != nil {
			listenerHostname = string(*parent.listener.Hostname)
		}
		if len(route.Spec.Hostnames) == 0 {
			if listenerHostname != "" {
				hostnames[listenerHostname] = struct{}{}
			}
			continue
		}
		for _, routeHostname := range route.Spec.Hostnames {
			hostname := string(routeHostname)
			if listenerHostname == "" {
				hostnames[hostname] = struct{}{}
				continue
			}
			if matched, ok := hostnameIntersection(hostname, listenerHostname); ok {
				hostnames[matched] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(hostnames))
	for hostname := range hostnames {
		result = append(result, hostname)
	}
	sort.Strings(result)
	return result
}

func effectiveHostnamesForParent(route *gatewayv1.HTTPRoute, parent parentInfo) []string {
	listenerHostname := ""
	if parent.listener.Hostname != nil {
		listenerHostname = string(*parent.listener.Hostname)
	}
	hostnames := map[string]struct{}{}
	if len(route.Spec.Hostnames) == 0 {
		if listenerHostname != "" {
			hostnames[listenerHostname] = struct{}{}
		}
	} else {
		for _, routeHostname := range route.Spec.Hostnames {
			hostname := string(routeHostname)
			if listenerHostname == "" {
				hostnames[hostname] = struct{}{}
				continue
			}
			if matched, ok := hostnameIntersection(hostname, listenerHostname); ok {
				hostnames[matched] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(hostnames))
	for hostname := range hostnames {
		result = append(result, strings.TrimSuffix(hostname, "."))
	}
	sort.Strings(result)
	return result
}

func hostnameIntersection(routeHostname, listenerHostname string) (string, bool) {
	if routeHostname == listenerHostname {
		return routeHostname, true
	}
	if strings.HasPrefix(listenerHostname, "*.") {
		return routeHostname, strings.HasSuffix(routeHostname, strings.TrimPrefix(listenerHostname, "*"))
	}
	if strings.HasPrefix(routeHostname, "*.") {
		return listenerHostname, strings.HasSuffix(listenerHostname, strings.TrimPrefix(routeHostname, "*"))
	}
	return "", false
}

func endpointTargets(addresses []gatewayv1.GatewayStatusAddress) []endpointv1alpha1.EndpointTarget {
	seen := map[string]struct{}{}
	targets := make([]endpointv1alpha1.EndpointTarget, 0, len(addresses))
	for _, address := range addresses {
		value := strings.TrimSuffix(string(address.Value), ".")
		if value == "" {
			continue
		}
		targetType := endpointv1alpha1.EndpointTargetTypeHostname
		if net.ParseIP(value) != nil {
			targetType = endpointv1alpha1.EndpointTargetTypeIPAddress
		}
		key := string(targetType) + "\x00" + value
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		targets = append(targets, endpointv1alpha1.EndpointTarget{Type: targetType, Value: value})
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].Type == targets[j].Type {
			return targets[i].Value < targets[j].Value
		}
		return targets[i].Type < targets[j].Type
	})
	return targets
}

func appendUnique(items []string, item string) []string {
	for _, existing := range items {
		if existing == item {
			return items
		}
	}
	return append(items, item)
}
