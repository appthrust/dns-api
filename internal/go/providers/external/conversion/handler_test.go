package conversion

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	conversionv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/conversion/v1alpha1"
	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
)

func TestHandlerFramingAndCanonicalTargets(t *testing.T) {
	input := endpointv1alpha1.EndpointRecordSetConversionInput{Name: "*.project", Targets: []endpointv1alpha1.EndpointTarget{
		{Type: endpointv1alpha1.EndpointTargetTypeIPAddress, Value: "2001:0db8:0:0::1"},
		{Type: endpointv1alpha1.EndpointTargetTypeIPAddress, Value: "192.0.2.2"},
		{Type: endpointv1alpha1.EndpointTargetTypeIPAddress, Value: "192.0.2.1"},
		{Type: endpointv1alpha1.EndpointTargetTypeIPAddress, Value: "2001:db8::1"},
	}}
	review := conversionv1alpha1.EndpointRecordSetConversion{Spec: conversionv1alpha1.EndpointRecordSetConversionSpec{UID: "request-1", Input: input}}
	body, _ := json.Marshal(review)
	response := httptest.NewRecorder()
	NewHandler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/apis/"+GroupName+"/"+Version+"/"+Resource, bytes.NewReader(body)))
	var got conversionv1alpha1.EndpointRecordSetConversion
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusCreated || got.Status.UID != "request-1" || got.Status.Result.Reason != "Converted" || got.Status.Output == nil {
		t.Fatalf("response = %d %#v", response.Code, got)
	}
	fragments := got.Status.Output.Fragments
	if len(fragments) != 2 || fragments[0].A.Addresses[0] != "192.0.2.1" || len(fragments[1].AAAA.Addresses) != 1 || fragments[1].AAAA.Addresses[0] != "2001:db8::1" {
		t.Fatalf("fragments = %#v", fragments)
	}
	for _, fragment := range fragments {
		if fragment.Name != input.Name || fragment.TTL == nil || *fragment.TTL != 300 || len(fragment.Options.Raw) != 0 {
			t.Fatalf("fragment = %#v", fragment)
		}
	}
}

func TestHostnameConversionAndFailures(t *testing.T) {
	host := endpointv1alpha1.EndpointTarget{Type: endpointv1alpha1.EndpointTargetTypeHostname, Value: "TARGET.EXAMPLE."}
	ip := endpointv1alpha1.EndpointTarget{Type: endpointv1alpha1.EndpointTargetTypeIPAddress, Value: "192.0.2.1"}
	cases := []struct {
		name            string
		input           endpointv1alpha1.EndpointRecordSetConversionInput
		reason, message string
	}{
		{"hostname", endpointv1alpha1.EndpointRecordSetConversionInput{Name: "www", Targets: []endpointv1alpha1.EndpointTarget{host, host}}, "Converted", ""},
		{"apex", endpointv1alpha1.EndpointRecordSetConversionInput{Name: "@", Targets: []endpointv1alpha1.EndpointTarget{host}}, "UnsupportedTarget", "External endpoint conversion does not support CNAME at the zone apex"},
		{"mixed", endpointv1alpha1.EndpointRecordSetConversionInput{Name: "www", Targets: []endpointv1alpha1.EndpointTarget{host, ip}}, "UnsupportedTarget", "External endpoint conversion does not support mixed hostname and IP targets"},
		{"multiple", endpointv1alpha1.EndpointRecordSetConversionInput{Name: "www", Targets: []endpointv1alpha1.EndpointTarget{host, {Type: endpointv1alpha1.EndpointTargetTypeHostname, Value: "other.example"}}}, "UnsupportedTarget", "External endpoint conversion requires exactly one hostname target"},
		{"invalid address", endpointv1alpha1.EndpointRecordSetConversionInput{Name: "www", Targets: []endpointv1alpha1.EndpointTarget{{Type: endpointv1alpha1.EndpointTargetTypeIPAddress, Value: "bad"}}}, "InvalidInput", ""},
		{"preference", endpointv1alpha1.EndpointRecordSetConversionInput{Name: "www", Targets: []endpointv1alpha1.EndpointTarget{ip}, PreferredRecordType: endpointv1alpha1.EndpointRecordSetTypeCNAME}, "UnsupportedTarget", ""},
		{"missing", endpointv1alpha1.EndpointRecordSetConversionInput{}, "InvalidInput", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fragments, result := convertEndpoint(tc.input)
			if result.Reason != tc.reason || result.Retryable || (tc.message != "" && result.Message != tc.message) {
				t.Fatalf("result = %#v", result)
			}
			if result.Status == "Success" {
				if len(fragments) != 1 || fragments[0].CNAME.Target != "target.example" || *fragments[0].TTL != 300 {
					t.Fatalf("fragments = %#v", fragments)
				}
			} else if len(fragments) != 0 {
				t.Fatal("failure emitted fragments")
			}
		})
	}
}

func TestDiscoveryAndMalformedRequest(t *testing.T) {
	for _, tc := range []struct {
		method, suffix, body string
		code                 int
	}{
		{http.MethodGet, "", "", http.StatusOK},
		{http.MethodGet, "/" + Resource, "", http.StatusNotFound},
		{http.MethodPost, "/" + Resource, "{", http.StatusBadRequest},
	} {
		response := httptest.NewRecorder()
		NewHandler().ServeHTTP(response, httptest.NewRequest(tc.method, "/apis/"+GroupName+"/"+Version+tc.suffix, bytes.NewBufferString(tc.body)))
		if response.Code != tc.code {
			t.Fatalf("status = %d want %d", response.Code, tc.code)
		}
	}
}
