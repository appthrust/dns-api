package gateway

import (
	"net"
	"sort"
	"strconv"
	"strings"

	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

type routeGatewayBinding struct {
	ref      gatewayv1.ParentReference
	listener gatewayv1.Listener
}

type parentAcceptance uint8

const (
	parentAcceptanceUnknown parentAcceptance = iota
	parentAcceptanceAccepted
	parentAcceptanceRejected
)

func routeGatewayBindings(route *gatewayv1.HTTPRoute, gateway *gatewayv1.Gateway) []routeGatewayBinding {
	bindings := make([]routeGatewayBinding, 0, len(route.Spec.ParentRefs))
	for _, ref := range route.Spec.ParentRefs {
		if !parentRefTargetsGateway(route.Namespace, ref, gateway) {
			continue
		}
		for _, listener := range gateway.Spec.Listeners {
			if ref.SectionName != nil && listener.Name != *ref.SectionName {
				continue
			}
			if ref.Port != nil && listener.Port != *ref.Port {
				continue
			}
			bindings = append(bindings, routeGatewayBinding{ref: ref, listener: listener})
		}
	}
	return bindings
}

func parentRefTargetsGateway(routeNamespace string, ref gatewayv1.ParentReference, gateway *gatewayv1.Gateway) bool {
	if ref.Group != nil && string(*ref.Group) != "gateway.networking.k8s.io" {
		return false
	}
	if ref.Kind != nil && string(*ref.Kind) != "Gateway" {
		return false
	}
	namespace := routeNamespace
	if ref.Namespace != nil {
		namespace = string(*ref.Namespace)
	}
	return namespace == gateway.Namespace && string(ref.Name) == gateway.Name
}

func allRouteParentRefsAccepted(route *gatewayv1.HTTPRoute) bool {
	if len(route.Spec.ParentRefs) == 0 {
		return false
	}
	for _, ref := range route.Spec.ParentRefs {
		if routeParentAcceptance(route, ref) != parentAcceptanceAccepted {
			return false
		}
	}
	return true
}

func routeParentAcceptance(route *gatewayv1.HTTPRoute, ref gatewayv1.ParentReference) parentAcceptance {
	acceptance := parentAcceptanceUnknown
	for _, parent := range route.Status.Parents {
		if parentRefKey(route.Namespace, parent.ParentRef) != parentRefKey(route.Namespace, ref) {
			continue
		}
		for _, condition := range parent.Conditions {
			if condition.Type != string(gatewayv1.RouteConditionAccepted) || condition.ObservedGeneration != route.Generation {
				continue
			}
			switch condition.Status {
			case "False":
				return parentAcceptanceRejected
			case "True":
				acceptance = parentAcceptanceAccepted
			}
		}
	}
	return acceptance
}
func gatewayAcceptance(gateway *gatewayv1.Gateway) parentAcceptance {
	acceptance := parentAcceptanceUnknown
	for _, condition := range gateway.Status.Conditions {
		if condition.Type != string(gatewayv1.GatewayConditionAccepted) || condition.ObservedGeneration != gateway.Generation {
			continue
		}
		switch condition.Status {
		case "False":
			return parentAcceptanceRejected
		case "True":
			acceptance = parentAcceptanceAccepted
		}
	}
	return acceptance
}

func listenerAllowsUnknownRetention(route *gatewayv1.HTTPRoute, gateway *gatewayv1.Gateway, listener gatewayv1.Listener) bool {
	if listener.AllowedRoutes == nil || listener.AllowedRoutes.Namespaces == nil || listener.AllowedRoutes.Namespaces.From == nil {
		return route.Namespace == gateway.Namespace
	}
	switch *listener.AllowedRoutes.Namespaces.From {
	case gatewayv1.NamespacesFromAll:
		return true
	case gatewayv1.NamespacesFromSame:
		return route.Namespace == gateway.Namespace
	default:
		// Selector policy would require a namespace read to re-prove attachment.
		return false
	}
}

func parentRefKey(routeNamespace string, ref gatewayv1.ParentReference) string {
	namespace := routeNamespace
	if ref.Namespace != nil {
		namespace = string(*ref.Namespace)
	}
	group := "gateway.networking.k8s.io"
	if ref.Group != nil {
		group = string(*ref.Group)
	}
	kind := "Gateway"
	if ref.Kind != nil {
		kind = string(*ref.Kind)
	}
	section := ""
	if ref.SectionName != nil {
		section = string(*ref.SectionName)
	}
	port := ""
	if ref.Port != nil {
		port = strconv.Itoa(int(*ref.Port))
	}
	return strings.Join([]string{group, kind, namespace, string(ref.Name), section, port}, "\x00")
}

func effectiveHostnamesForListener(route *gatewayv1.HTTPRoute, listener gatewayv1.Listener) []string {
	listenerHostname := ""
	if listener.Hostname != nil {
		listenerHostname = string(*listener.Hostname)
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
