package conversion

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"strings"

	"github.com/appthrust/dns-api/internal/go/core/dnsprobe"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	conversionv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/conversion/v1alpha1"
	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	GroupName = "endpoint.external.dns.appthrust.io"
	Version   = "v1alpha1"
	Resource  = "endpointrecordsetconversions"
)

type Handler struct{}

func NewHandler() *Handler { return &Handler{} }

func (h *Handler) Register(mux interface{ Register(string, http.Handler) }) {
	mux.Register("/apis/"+GroupName+"/"+Version, h)
	mux.Register("/apis/"+GroupName+"/"+Version+"/"+Resource, h)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := "/apis/" + GroupName + "/" + Version
	switch {
	case r.Method == http.MethodGet && r.URL.Path == path:
		writeJSON(w, http.StatusOK, metav1.APIResourceList{
			TypeMeta:     metav1.TypeMeta{APIVersion: "v1", Kind: "APIResourceList"},
			GroupVersion: GroupName + "/" + Version,
			APIResources: []metav1.APIResource{{Name: Resource, Kind: "EndpointRecordSetConversion", Verbs: []string{"create"}}},
		})
	case r.Method == http.MethodPost && r.URL.Path == path+"/"+Resource:
		defer r.Body.Close()
		var request conversionv1alpha1.EndpointRecordSetConversion
		response := conversionv1alpha1.EndpointRecordSetConversion{TypeMeta: metav1.TypeMeta{APIVersion: GroupName + "/" + Version, Kind: "EndpointRecordSetConversion"}}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			response.Status.Result = failure("InvalidInput", "request body is malformed")
			writeJSON(w, http.StatusBadRequest, response)
			return
		}
		response.ObjectMeta, response.Spec, response.Status.UID = request.ObjectMeta, request.Spec, request.Spec.UID
		fragments, result := convertEndpoint(request.Spec.Input)
		response.Status.Result = result
		if len(fragments) > 0 {
			response.Status.Output = &endpointv1alpha1.EndpointRecordSetConversionOutput{Fragments: fragments}
		}
		writeJSON(w, http.StatusCreated, response)
	default:
		http.NotFound(w, r)
	}
}

func convertEndpoint(input endpointv1alpha1.EndpointRecordSetConversionInput) ([]endpointv1alpha1.RecordSetSpecFragment, conversionv1alpha1.EndpointRecordSetConversionResult) {
	if input.Name == "" {
		return nil, failure("InvalidInput", "input.name is required")
	}
	if len(input.Targets) == 0 {
		return nil, failure("InvalidInput", "input.targets must not be empty")
	}
	var ipv4, ipv6, hosts []string
	for _, target := range input.Targets {
		switch target.Type {
		case endpointv1alpha1.EndpointTargetTypeIPAddress:
			ip, err := netip.ParseAddr(target.Value)
			if err != nil || ip.Zone() != "" {
				return nil, failure("InvalidInput", fmt.Sprintf("target %q is not a valid IP address", target.Value))
			}
			ip = ip.Unmap()
			if ip.Is4() {
				ipv4 = append(ipv4, ip.String())
			} else {
				ipv6 = append(ipv6, ip.String())
			}
		case endpointv1alpha1.EndpointTargetTypeHostname:
			name := strings.TrimSuffix(strings.ToLower(target.Value), ".")
			if !dnsprobe.ValidName(name) {
				return nil, failure("InvalidInput", "hostname target must be a valid DNS name")
			}
			hosts = append(hosts, name)
		default:
			return nil, failure("InvalidInput", "unsupported target type "+string(target.Type))
		}
	}
	slices.Sort(ipv4)
	ipv4 = slices.Compact(ipv4)
	slices.Sort(ipv6)
	ipv6 = slices.Compact(ipv6)
	slices.Sort(hosts)
	hosts = slices.Compact(hosts)
	if len(hosts) > 0 && len(ipv4)+len(ipv6) > 0 {
		return nil, failure("UnsupportedTarget", "External endpoint conversion does not support mixed hostname and IP targets")
	}
	if len(hosts) > 1 {
		return nil, failure("UnsupportedTarget", "External endpoint conversion requires exactly one hostname target")
	}
	ttl := int32(300)
	if len(hosts) == 1 {
		if input.Name == "@" {
			return nil, failure("UnsupportedTarget", "External endpoint conversion does not support CNAME at the zone apex")
		}
		if input.PreferredRecordType != "" && input.PreferredRecordType != endpointv1alpha1.EndpointRecordSetTypeCNAME {
			return nil, failure("UnsupportedTarget", "preferredRecordType must be CNAME for a hostname target")
		}
		return []endpointv1alpha1.RecordSetSpecFragment{{Name: input.Name, Type: endpointv1alpha1.EndpointRecordSetTypeCNAME, TTL: &ttl, CNAME: &dnsv1alpha1.CNAMERecordSet{Target: hosts[0]}}}, success()
	}
	if input.PreferredRecordType != "" && !((input.PreferredRecordType == endpointv1alpha1.EndpointRecordSetTypeA && len(ipv6) == 0) || (input.PreferredRecordType == endpointv1alpha1.EndpointRecordSetTypeAAAA && len(ipv4) == 0)) {
		return nil, failure("UnsupportedTarget", "preferredRecordType is incompatible with IP targets")
	}
	fragments := make([]endpointv1alpha1.RecordSetSpecFragment, 0, 2)
	if len(ipv4) > 0 {
		fragments = append(fragments, endpointv1alpha1.RecordSetSpecFragment{Name: input.Name, Type: endpointv1alpha1.EndpointRecordSetTypeA, TTL: &ttl, A: &dnsv1alpha1.ARecordSet{Addresses: ipv4}})
	}
	if len(ipv6) > 0 {
		fragments = append(fragments, endpointv1alpha1.RecordSetSpecFragment{Name: input.Name, Type: endpointv1alpha1.EndpointRecordSetTypeAAAA, TTL: &ttl, AAAA: &dnsv1alpha1.AAAARecordSet{Addresses: ipv6}})
	}
	return fragments, success()
}

func failure(reason, message string) conversionv1alpha1.EndpointRecordSetConversionResult {
	return conversionv1alpha1.EndpointRecordSetConversionResult{Status: "Failure", Reason: reason, Message: message, Retryable: false}
}

func success() conversionv1alpha1.EndpointRecordSetConversionResult {
	return conversionv1alpha1.EndpointRecordSetConversionResult{Status: "Success", Reason: "Converted", Message: "converted endpoint targets to external DNS record sets"}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
