package conversion

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"

	conversionv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/conversion/v1alpha1"
	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	hostedv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/hosted/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

const conversionPath = "/apis/endpoint.hosted.dns.appthrust.io/v1alpha1/endpointrecordsetconversions"

func TestHandlerFramingAndCanonicalTargets(t *testing.T) {
	input := endpointv1alpha1.EndpointRecordSetConversionInput{Name: "*.project", Targets: []endpointv1alpha1.EndpointTarget{
		{Type: endpointv1alpha1.EndpointTargetTypeIPAddress, Value: "2001:0db8:0:0::2"},
		{Type: endpointv1alpha1.EndpointTargetTypeIPAddress, Value: "192.0.2.2"},
		{Type: endpointv1alpha1.EndpointTargetTypeIPAddress, Value: "192.0.2.1"},
		{Type: endpointv1alpha1.EndpointTargetTypeIPAddress, Value: "::ffff:192.0.2.1"},
		{Type: endpointv1alpha1.EndpointTargetTypeIPAddress, Value: "2001:db8::1"},
		{Type: endpointv1alpha1.EndpointTargetTypeIPAddress, Value: "2001:db8::2"},
	}}
	for _, ttl := range []uint32{hostedv1alpha1.DefaultTTL, 123, 86400} {
		review := conversionv1alpha1.EndpointRecordSetConversion{
			ObjectMeta: metav1.ObjectMeta{Name: "conversion", Labels: map[string]string{"test": "hosted"}},
			Spec:       conversionv1alpha1.EndpointRecordSetConversionSpec{UID: "request-1", Input: input},
		}
		got, response := requestConversion(t, NewHandler(ttl), review)
		if response.Code != http.StatusCreated || got.Status.UID != "request-1" || got.Status.Result.Status != "Success" || got.Status.Result.Reason != "Converted" || got.Status.Output == nil || got.Status.Result.Retryable {
			t.Fatalf("response = %d %#v", response.Code, got)
		}
		if got.APIVersion != "endpoint.hosted.dns.appthrust.io/v1alpha1" || got.Kind != "EndpointRecordSetConversion" || !reflect.DeepEqual(got.ObjectMeta, review.ObjectMeta) || !reflect.DeepEqual(got.Spec, review.Spec) {
			t.Fatalf("response framing = %#v", got)
		}
		if got.Status.Result.Message != "converted endpoint targets to hosted DNS record sets" {
			t.Fatalf("message = %q", got.Status.Result.Message)
		}
		fragments := got.Status.Output.Fragments
		if len(fragments) != 2 || fragments[0].Type != endpointv1alpha1.EndpointRecordSetTypeA || fragments[0].A == nil || fragments[1].Type != endpointv1alpha1.EndpointRecordSetTypeAAAA || fragments[1].AAAA == nil {
			t.Fatalf("fragments = %#v", fragments)
		}
		if !slices.Equal(fragments[0].A.Addresses, []string{"192.0.2.1", "192.0.2.2"}) || !slices.Equal(fragments[1].AAAA.Addresses, []string{"2001:db8::1", "2001:db8::2"}) {
			t.Fatalf("addresses = %v, %v", fragments[0].A.Addresses, fragments[1].AAAA.Addresses)
		}
		for _, fragment := range fragments {
			if fragment.Name != input.Name || fragment.TTL == nil || *fragment.TTL != int32(ttl) || len(fragment.Options.Raw) != 0 {
				t.Fatalf("fragment = %#v", fragment)
			}
		}
	}
}

func TestHostnameConversionAndFailures(t *testing.T) {
	host := endpointv1alpha1.EndpointTarget{Type: endpointv1alpha1.EndpointTargetTypeHostname, Value: "TARGET.EXAMPLE."}
	ip := endpointv1alpha1.EndpointTarget{Type: endpointv1alpha1.EndpointTargetTypeIPAddress, Value: "192.0.2.1"}
	ipv6 := endpointv1alpha1.EndpointTarget{Type: endpointv1alpha1.EndpointTargetTypeIPAddress, Value: "2001:db8::1"}
	cases := []struct {
		name            string
		input           endpointv1alpha1.EndpointRecordSetConversionInput
		reason, message string
	}{
		{"hostname", endpointv1alpha1.EndpointRecordSetConversionInput{Name: "www", Targets: []endpointv1alpha1.EndpointTarget{host, {Type: endpointv1alpha1.EndpointTargetTypeHostname, Value: "target.example"}}}, "Converted", "converted endpoint targets to hosted DNS record sets"},
		{"hostname preference", endpointv1alpha1.EndpointRecordSetConversionInput{Name: "www", Targets: []endpointv1alpha1.EndpointTarget{host}, PreferredRecordType: endpointv1alpha1.EndpointRecordSetTypeCNAME}, "Converted", "converted endpoint targets to hosted DNS record sets"},
		{"apex", endpointv1alpha1.EndpointRecordSetConversionInput{Name: "@", Targets: []endpointv1alpha1.EndpointTarget{host}}, "UnsupportedTarget", "Hosted endpoint conversion does not support CNAME at the zone apex"},
		{"mixed", endpointv1alpha1.EndpointRecordSetConversionInput{Name: "www", Targets: []endpointv1alpha1.EndpointTarget{host, ip}}, "UnsupportedTarget", "Hosted endpoint conversion does not support mixed hostname and IP targets"},
		{"multiple", endpointv1alpha1.EndpointRecordSetConversionInput{Name: "www", Targets: []endpointv1alpha1.EndpointTarget{host, {Type: endpointv1alpha1.EndpointTargetTypeHostname, Value: "other.example"}}}, "UnsupportedTarget", "Hosted endpoint conversion requires exactly one hostname target"},
		{"invalid address", endpointv1alpha1.EndpointRecordSetConversionInput{Name: "www", Targets: []endpointv1alpha1.EndpointTarget{{Type: endpointv1alpha1.EndpointTargetTypeIPAddress, Value: "bad"}}}, "InvalidInput", "target \"bad\" is not a valid IP address"},
		{"scoped address", endpointv1alpha1.EndpointRecordSetConversionInput{Name: "www", Targets: []endpointv1alpha1.EndpointTarget{{Type: endpointv1alpha1.EndpointTargetTypeIPAddress, Value: "fe80::1%eth0"}}}, "InvalidInput", "target \"fe80::1%eth0\" is not a valid IP address"},
		{"invalid hostname", endpointv1alpha1.EndpointRecordSetConversionInput{Name: "www", Targets: []endpointv1alpha1.EndpointTarget{{Type: endpointv1alpha1.EndpointTargetTypeHostname, Value: "bad_name.example"}}}, "InvalidInput", "hostname target must be a valid DNS name"},
		{"address as hostname", endpointv1alpha1.EndpointRecordSetConversionInput{Name: "www", Targets: []endpointv1alpha1.EndpointTarget{{Type: endpointv1alpha1.EndpointTargetTypeHostname, Value: "192.0.2.1"}}}, "InvalidInput", "hostname target must be a valid DNS name"},
		{"unknown target type", endpointv1alpha1.EndpointRecordSetConversionInput{Name: "www", Targets: []endpointv1alpha1.EndpointTarget{{Type: "Other", Value: "target.example"}}}, "InvalidInput", "unsupported target type Other"},
		{"hostname incompatible preference", endpointv1alpha1.EndpointRecordSetConversionInput{Name: "www", Targets: []endpointv1alpha1.EndpointTarget{host}, PreferredRecordType: endpointv1alpha1.EndpointRecordSetTypeA}, "UnsupportedTarget", "preferredRecordType must be CNAME for a hostname target"},
		{"IP CNAME preference", endpointv1alpha1.EndpointRecordSetConversionInput{Name: "www", Targets: []endpointv1alpha1.EndpointTarget{ip}, PreferredRecordType: endpointv1alpha1.EndpointRecordSetTypeCNAME}, "UnsupportedTarget", "preferredRecordType is incompatible with IP targets"},
		{"IPv4 AAAA preference", endpointv1alpha1.EndpointRecordSetConversionInput{Name: "www", Targets: []endpointv1alpha1.EndpointTarget{ip}, PreferredRecordType: endpointv1alpha1.EndpointRecordSetTypeAAAA}, "UnsupportedTarget", "preferredRecordType is incompatible with IP targets"},
		{"IPv6 A preference", endpointv1alpha1.EndpointRecordSetConversionInput{Name: "www", Targets: []endpointv1alpha1.EndpointTarget{ipv6}, PreferredRecordType: endpointv1alpha1.EndpointRecordSetTypeA}, "UnsupportedTarget", "preferredRecordType is incompatible with IP targets"},
		{"dual stack A preference", endpointv1alpha1.EndpointRecordSetConversionInput{Name: "www", Targets: []endpointv1alpha1.EndpointTarget{ip, ipv6}, PreferredRecordType: endpointv1alpha1.EndpointRecordSetTypeA}, "UnsupportedTarget", "preferredRecordType is incompatible with IP targets"},
		{"dual stack AAAA preference", endpointv1alpha1.EndpointRecordSetConversionInput{Name: "www", Targets: []endpointv1alpha1.EndpointTarget{ip, ipv6}, PreferredRecordType: endpointv1alpha1.EndpointRecordSetTypeAAAA}, "UnsupportedTarget", "preferredRecordType is incompatible with IP targets"},
		{"missing name", endpointv1alpha1.EndpointRecordSetConversionInput{Targets: []endpointv1alpha1.EndpointTarget{ip}}, "InvalidInput", "input.name is required"},
		{"missing targets", endpointv1alpha1.EndpointRecordSetConversionInput{Name: "www"}, "InvalidInput", "input.targets must not be empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, ttl := range []uint32{hostedv1alpha1.DefaultTTL, 123} {
				review := conversionv1alpha1.EndpointRecordSetConversion{Spec: conversionv1alpha1.EndpointRecordSetConversionSpec{UID: "request-1", Input: tc.input}}
				got, response := requestConversion(t, NewHandler(ttl), review)
				result := got.Status.Result
				if response.Code != http.StatusCreated || got.Status.UID != review.Spec.UID || result.Reason != tc.reason || result.Retryable || result.Message != tc.message {
					t.Fatalf("response = %d %#v", response.Code, got)
				}
				if tc.reason == "Converted" {
					if result.Status != "Success" || got.Status.Output == nil || len(got.Status.Output.Fragments) != 1 {
						t.Fatalf("response = %#v", got)
					}
					fragment := got.Status.Output.Fragments[0]
					if fragment.Name != tc.input.Name || fragment.Type != endpointv1alpha1.EndpointRecordSetTypeCNAME || fragment.CNAME == nil || fragment.CNAME.Target != "target.example" || fragment.TTL == nil || *fragment.TTL != int32(ttl) || len(fragment.Options.Raw) != 0 {
						t.Fatalf("fragment = %#v", fragment)
					}
				} else if result.Status != "Failure" || got.Status.Output != nil {
					t.Fatalf("failure emitted output: %#v", got)
				}
			}
		})
	}
}

func TestIPConversionCompatiblePreferences(t *testing.T) {
	for _, tc := range []struct {
		address    string
		recordType endpointv1alpha1.EndpointRecordSetType
	}{
		{"192.0.2.1", endpointv1alpha1.EndpointRecordSetTypeA},
		{"2001:db8::1", endpointv1alpha1.EndpointRecordSetTypeAAAA},
	} {
		for _, preference := range []endpointv1alpha1.EndpointRecordSetType{"", tc.recordType} {
			input := endpointv1alpha1.EndpointRecordSetConversionInput{Name: "@", Targets: []endpointv1alpha1.EndpointTarget{{Type: endpointv1alpha1.EndpointTargetTypeIPAddress, Value: tc.address}}, PreferredRecordType: preference}
			got, _ := requestConversion(t, NewHandler(123), conversionv1alpha1.EndpointRecordSetConversion{Spec: conversionv1alpha1.EndpointRecordSetConversionSpec{UID: "request-1", Input: input}})
			if got.Status.Result.Status != "Success" || got.Status.Output == nil || len(got.Status.Output.Fragments) != 1 {
				t.Fatalf("address %s preference %s: %#v", tc.address, preference, got)
			}
			fragment := got.Status.Output.Fragments[0]
			if fragment.Type != tc.recordType || fragment.Name != "@" || fragment.TTL == nil || *fragment.TTL != 123 {
				t.Fatalf("fragment = %#v", fragment)
			}
		}
	}
}

func TestDiscoveryAndMalformedRequest(t *testing.T) {
	server := webhook.NewServer(webhook.Options{})
	NewHandler(hostedv1alpha1.DefaultTTL).Register(server)
	discoveryPath := "/apis/endpoint.hosted.dns.appthrust.io/v1alpha1"
	for _, tc := range []struct {
		method, path, body string
		code               int
	}{
		{http.MethodGet, discoveryPath, "", http.StatusOK},
		{http.MethodGet, conversionPath, "", http.StatusNotFound},
		{http.MethodPost, discoveryPath, "", http.StatusNotFound},
		{http.MethodPost, conversionPath, "{", http.StatusBadRequest},
		{http.MethodDelete, conversionPath, "", http.StatusNotFound},
		{http.MethodGet, "/apis/endpoint.external.dns.appthrust.io/v1alpha1", "", http.StatusNotFound},
	} {
		response := httptest.NewRecorder()
		server.WebhookMux().ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(tc.body)))
		if response.Code != tc.code {
			t.Fatalf("%s %s status = %d want %d", tc.method, tc.path, response.Code, tc.code)
		}
		switch tc.code {
		case http.StatusOK:
			var got metav1.APIResourceList
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			want := metav1.APIResourceList{
				TypeMeta:     metav1.TypeMeta{APIVersion: "v1", Kind: "APIResourceList"},
				GroupVersion: "endpoint.hosted.dns.appthrust.io/v1alpha1",
				APIResources: []metav1.APIResource{{Name: "endpointrecordsetconversions", Kind: "EndpointRecordSetConversion", Verbs: []string{"create"}}},
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("discovery = %#v, want %#v", got, want)
			}
		case http.StatusBadRequest:
			var got conversionv1alpha1.EndpointRecordSetConversion
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.APIVersion != "endpoint.hosted.dns.appthrust.io/v1alpha1" || got.Kind != "EndpointRecordSetConversion" || got.Status.Result.Status != "Failure" || got.Status.Result.Reason != "InvalidInput" || got.Status.Result.Message != "request body is malformed" || got.Status.Result.Retryable || got.Status.Output != nil {
				t.Fatalf("malformed response = %#v", got)
			}
		}
	}
}

func requestConversion(t *testing.T, handler http.Handler, review conversionv1alpha1.EndpointRecordSetConversion) (conversionv1alpha1.EndpointRecordSetConversion, *httptest.ResponseRecorder) {
	t.Helper()
	body, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, conversionPath, bytes.NewReader(body)))
	if response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("Content-Type = %q", response.Header().Get("Content-Type"))
	}
	var got conversionv1alpha1.EndpointRecordSetConversion
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got, response
}
