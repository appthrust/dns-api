package conversion

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	endpointconversionv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/conversion/v1alpha1"
	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	GroupName = "endpoint.route53.dns.appthrust.io"
	Version   = "v1alpha1"
	Resource  = "endpointrecordsetconversions"
)

type Handler struct{}

func NewHandler() *Handler {
	return &Handler{}
}

func (h *Handler) Register(mux interface {
	Register(path string, hook http.Handler)
}) {
	mux.Register("/apis/"+GroupName+"/"+Version, h)
	mux.Register("/apis/"+GroupName+"/"+Version+"/"+Resource, h)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/apis/"+GroupName+"/"+Version:
		writeJSON(w, http.StatusOK, metav1.APIResourceList{
			TypeMeta:     metav1.TypeMeta{APIVersion: "v1", Kind: "APIResourceList"},
			GroupVersion: GroupName + "/" + Version,
			APIResources: []metav1.APIResource{{
				Name:       Resource,
				Namespaced: false,
				Kind:       "EndpointRecordSetConversion",
				Verbs:      []string{"create"},
			}},
		})
	case r.Method == http.MethodPost && r.URL.Path == "/apis/"+GroupName+"/"+Version+"/"+Resource:
		h.createConversion(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) createConversion(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var conversion endpointconversionv1alpha1.EndpointRecordSetConversion
	if err := json.NewDecoder(r.Body).Decode(&conversion); err != nil {
		writeJSON(w, http.StatusBadRequest, failureConversion("", "MalformedRequest", "request body is malformed: "+err.Error(), false))
		return
	}
	response := endpointconversionv1alpha1.EndpointRecordSetConversion{
		TypeMeta: metav1.TypeMeta{
			APIVersion: GroupName + "/" + Version,
			Kind:       "EndpointRecordSetConversion",
		},
		ObjectMeta: conversion.ObjectMeta,
		Spec:       conversion.Spec,
	}
	response.Status.UID = conversion.Spec.UID
	fragments, result := convertEndpoint(conversion.Spec.Input)
	response.Status.Result = result
	if len(fragments) > 0 {
		response.Status.Output = &endpointv1alpha1.EndpointRecordSetConversionOutput{Fragments: fragments}
	}
	writeJSON(w, http.StatusCreated, response)
}

func convertEndpoint(input endpointv1alpha1.EndpointRecordSetConversionInput) ([]endpointv1alpha1.RecordSetSpecFragment, endpointconversionv1alpha1.EndpointRecordSetConversionResult) {
	if input.Name == "" {
		return nil, failureResult("InvalidInput", "input.name is required", false)
	}
	if len(input.Targets) == 0 {
		return nil, failureResult("InvalidInput", "input.targets must not be empty", false)
	}
	var hostnameTargets []string
	var ipv4 []string
	var ipv6 []string
	for _, target := range input.Targets {
		value := strings.TrimSuffix(target.Value, ".")
		switch target.Type {
		case endpointv1alpha1.EndpointTargetTypeHostname:
			hostnameTargets = appendUnique(hostnameTargets, value)
		case endpointv1alpha1.EndpointTargetTypeIPAddress:
			ip := net.ParseIP(value)
			if ip == nil {
				return nil, failureResult("InvalidInput", fmt.Sprintf("target %q is not a valid IP address", target.Value), false)
			}
			if ip.To4() != nil {
				ipv4 = appendUnique(ipv4, value)
			} else {
				ipv6 = appendUnique(ipv6, value)
			}
		default:
			return nil, failureResult("InvalidInput", "unsupported target type "+string(target.Type), false)
		}
	}
	if len(hostnameTargets) > 0 && (len(ipv4) > 0 || len(ipv6) > 0) {
		return nil, failureResult("UnsupportedTarget", "Route 53 endpoint conversion does not support mixed hostname and IP targets", false)
	}
	if len(hostnameTargets) > 1 {
		return nil, failureResult("UnsupportedTarget", "multiple hostname targets require routing policy, which is not supported yet", false)
	}
	if len(hostnameTargets) == 1 {
		return route53AliasFragments(input.Name, hostnameTargets[0])
	}
	fragments := make([]endpointv1alpha1.RecordSetSpecFragment, 0, 2)
	ttl := int32(300)
	if len(ipv4) > 0 {
		fragments = append(fragments, endpointv1alpha1.RecordSetSpecFragment{
			Type: endpointv1alpha1.EndpointRecordSetTypeA,
			Name: input.Name,
			TTL:  &ttl,
			A:    &dnsv1alpha1.ARecordSet{Addresses: ipv4},
		})
	}
	if len(ipv6) > 0 {
		fragments = append(fragments, endpointv1alpha1.RecordSetSpecFragment{
			Type: endpointv1alpha1.EndpointRecordSetTypeAAAA,
			Name: input.Name,
			TTL:  &ttl,
			AAAA: &dnsv1alpha1.AAAARecordSet{Addresses: ipv6},
		})
	}
	if len(fragments) == 0 {
		return nil, failureResult("UnsupportedTarget", "no supported Route 53 target was found", false)
	}
	return fragments, successResult("Converted", "converted endpoint targets to Route 53 record sets")
}

func route53AliasFragments(name, targetValue string) ([]endpointv1alpha1.RecordSetSpecFragment, endpointconversionv1alpha1.EndpointRecordSetConversionResult) {
	target := normalizeRoute53AliasDNSName(targetValue)
	hostedZoneID := route53CanonicalHostedZone(target)
	if hostedZoneID == "" {
		return nil, failureResult("UnsupportedTarget", fmt.Sprintf("Route 53 alias hosted zone ID is unknown for endpoint target %q", targetValue), false)
	}
	options, err := json.Marshal(route53v1alpha1.Route53RecordSetOptions{
		Alias: &route53v1alpha1.Route53AliasTarget{
			DNSName:              target,
			HostedZoneID:         hostedZoneID,
			EvaluateTargetHealth: true,
		},
	})
	if err != nil {
		return nil, failureResult("InternalError", "failed to encode Route 53 alias options: "+err.Error(), true)
	}
	a := endpointv1alpha1.RecordSetSpecFragment{
		Type:    endpointv1alpha1.EndpointRecordSetTypeA,
		Name:    name,
		Options: runtime.RawExtension{Raw: options},
	}
	aaaa := a
	aaaa.Type = endpointv1alpha1.EndpointRecordSetTypeAAAA
	return []endpointv1alpha1.RecordSetSpecFragment{a, aaaa}, successResult("Converted", "converted hostname endpoint target to Route 53 alias A and AAAA record sets")
}

func appendUnique(items []string, item string) []string {
	for _, existing := range items {
		if existing == item {
			return items
		}
	}
	return append(items, item)
}

func successResult(reason, message string) endpointconversionv1alpha1.EndpointRecordSetConversionResult {
	return endpointconversionv1alpha1.EndpointRecordSetConversionResult{
		Status:  "Success",
		Reason:  reason,
		Message: message,
	}
}

func failureResult(reason, message string, retryable bool) endpointconversionv1alpha1.EndpointRecordSetConversionResult {
	return endpointconversionv1alpha1.EndpointRecordSetConversionResult{
		Status:    "Failure",
		Reason:    reason,
		Message:   message,
		Retryable: retryable,
	}
}

func failureConversion(uid, reason, message string, retryable bool) endpointconversionv1alpha1.EndpointRecordSetConversion {
	return endpointconversionv1alpha1.EndpointRecordSetConversion{
		TypeMeta: metav1.TypeMeta{
			APIVersion: GroupName + "/" + Version,
			Kind:       "EndpointRecordSetConversion",
		},
		Status: endpointconversionv1alpha1.EndpointRecordSetConversionStatus{
			UID:    uid,
			Result: failureResult(reason, message, retryable),
		},
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
