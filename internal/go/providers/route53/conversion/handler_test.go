package conversion

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	endpointconversionv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/conversion/v1alpha1"
	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestHandlerConvertsELBHostnameToAliasAAndAAAA(t *testing.T) {
	review := endpointconversionv1alpha1.EndpointRecordSetConversion{
		TypeMeta: metav1.TypeMeta{APIVersion: GroupName + "/" + Version, Kind: "EndpointRecordSetConversion"},
		Spec: endpointconversionv1alpha1.EndpointRecordSetConversionSpec{
			UID: "request-1",
			Input: endpointv1alpha1.EndpointRecordSetConversionInput{
				Hostname: "api.example.com",
				Name:     "api",
				Zone:     endpointv1alpha1.EndpointRecordSetConversionZone{DomainName: "example.com"},
				Targets: []endpointv1alpha1.EndpointTarget{{
					Type:  endpointv1alpha1.EndpointTargetTypeHostname,
					Value: "k8s-public-123456.ap-northeast-1.elb.amazonaws.com",
				}},
			},
		},
	}
	body, err := json.Marshal(review)
	if err != nil {
		t.Fatalf("marshal review: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/apis/"+GroupName+"/"+Version+"/"+Resource, bytes.NewReader(body))
	response := httptest.NewRecorder()

	NewHandler().ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var got endpointconversionv1alpha1.EndpointRecordSetConversion
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Status.UID != "request-1" {
		t.Fatalf("uid = %q, want request-1", got.Status.UID)
	}
	if got.Status.Result.Status != "Success" {
		t.Fatalf("result = %#v, want success", got.Status.Result)
	}
	if got.Status.Output == nil || len(got.Status.Output.Fragments) != 2 {
		t.Fatalf("fragments = %#v, want 2", got.Status.Output)
	}
	if got.Status.Output.Fragments[0].Type != endpointv1alpha1.EndpointRecordSetTypeA || got.Status.Output.Fragments[1].Type != endpointv1alpha1.EndpointRecordSetTypeAAAA {
		t.Fatalf("record set types = %q, %q", got.Status.Output.Fragments[0].Type, got.Status.Output.Fragments[1].Type)
	}
	if got.Status.Output.Fragments[0].TTL != nil || got.Status.Output.Fragments[0].CNAME != nil {
		t.Fatalf("A fragment = %#v, want no ttl and no cname", got.Status.Output.Fragments[0])
	}
	var options route53v1alpha1.Route53RecordSetOptions
	if err := json.Unmarshal(got.Status.Output.Fragments[0].Options.Raw, &options); err != nil {
		t.Fatalf("decode options: %v", err)
	}
	if options.Alias == nil ||
		options.Alias.DNSName != "dualstack.k8s-public-123456.ap-northeast-1.elb.amazonaws.com." ||
		options.Alias.HostedZoneID != "Z14GRHDCWA56QT" ||
		options.Alias.EvaluateTargetHealth {
		t.Fatalf("alias = %#v", options.Alias)
	}
}

func TestHandlerPreservesExistingELBDualstackAliasPrefix(t *testing.T) {
	review := endpointconversionv1alpha1.EndpointRecordSetConversion{
		TypeMeta: metav1.TypeMeta{APIVersion: GroupName + "/" + Version, Kind: "EndpointRecordSetConversion"},
		Spec: endpointconversionv1alpha1.EndpointRecordSetConversionSpec{
			UID: "request-1",
			Input: endpointv1alpha1.EndpointRecordSetConversionInput{
				Hostname: "api.example.com",
				Name:     "api",
				Zone:     endpointv1alpha1.EndpointRecordSetConversionZone{DomainName: "example.com"},
				Targets: []endpointv1alpha1.EndpointTarget{{
					Type:  endpointv1alpha1.EndpointTargetTypeHostname,
					Value: "dualstack.k8s-public-123456.ap-northeast-1.elb.amazonaws.com.",
				}},
			},
		},
	}
	body, err := json.Marshal(review)
	if err != nil {
		t.Fatalf("marshal review: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/apis/"+GroupName+"/"+Version+"/"+Resource, bytes.NewReader(body))
	response := httptest.NewRecorder()

	NewHandler().ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var got endpointconversionv1alpha1.EndpointRecordSetConversion
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Status.Output == nil || len(got.Status.Output.Fragments) == 0 {
		t.Fatalf("fragments = %#v, want output", got.Status.Output)
	}
	var options route53v1alpha1.Route53RecordSetOptions
	if err := json.Unmarshal(got.Status.Output.Fragments[0].Options.Raw, &options); err != nil {
		t.Fatalf("decode options: %v", err)
	}
	if options.Alias == nil || options.Alias.DNSName != "dualstack.k8s-public-123456.ap-northeast-1.elb.amazonaws.com." {
		t.Fatalf("alias = %#v", options.Alias)
	}
}

func TestHandlerRejectsUnknownHostnameTarget(t *testing.T) {
	review := endpointconversionv1alpha1.EndpointRecordSetConversion{
		Spec: endpointconversionv1alpha1.EndpointRecordSetConversionSpec{
			UID: "request-1",
			Input: endpointv1alpha1.EndpointRecordSetConversionInput{
				Hostname: "api.example.com",
				Name:     "api",
				Zone:     endpointv1alpha1.EndpointRecordSetConversionZone{DomainName: "example.com"},
				Targets: []endpointv1alpha1.EndpointTarget{{
					Type:  endpointv1alpha1.EndpointTargetTypeHostname,
					Value: "lb.example.net",
				}},
			},
		},
	}
	body, err := json.Marshal(review)
	if err != nil {
		t.Fatalf("marshal review: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/apis/"+GroupName+"/"+Version+"/"+Resource, bytes.NewReader(body))
	response := httptest.NewRecorder()

	NewHandler().ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var got endpointconversionv1alpha1.EndpointRecordSetConversion
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Status.Result.Status != "Failure" || got.Status.Result.Reason != "UnsupportedTarget" {
		t.Fatalf("result = %#v, want UnsupportedTarget failure", got.Status.Result)
	}
	if got.Status.Output != nil && len(got.Status.Output.Fragments) != 0 {
		t.Fatalf("fragments = %d, want 0", len(got.Status.Output.Fragments))
	}
}
