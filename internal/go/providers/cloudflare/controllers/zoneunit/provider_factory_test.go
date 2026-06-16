package cloudflare

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCloudflareAPIClientResolveIdentityObservesSingleAccount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user/tokens/verify":
			_, _ = w.Write([]byte(`{"success":true,"result":{"id":"token-id","status":"active"}}`))
		case "/accounts":
			_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"023e105f4ecef8ad9ca31a8372d0c354","name":"ci","type":"standard"}]}`))
		default:
			t.Fatalf("unexpected Cloudflare API path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := &CloudflareAPIClient{BaseURL: server.URL, HTTPClient: server.Client()}
	resolution, err := client.ResolveIdentity(context.Background(), "raw-token")
	if err != nil {
		t.Fatalf("ResolveIdentity returned error: %v", err)
	}
	if resolution.Account.ID != "023e105f4ecef8ad9ca31a8372d0c354" {
		t.Fatalf("account ID = %q", resolution.Account.ID)
	}
	if resolution.Account.Name != "ci" || resolution.Account.Type != "standard" {
		t.Fatalf("account = %#v", resolution.Account)
	}
	if resolution.AccessToken.ID != "token-id" || resolution.AccessToken.Status != "active" {
		t.Fatalf("access token = %#v", resolution.AccessToken)
	}
}

func TestCloudflareAPIClientResolveIdentityRejectsAmbiguousAccounts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user/tokens/verify":
			_, _ = w.Write([]byte(`{"success":true,"result":{"id":"token-id","status":"active"}}`))
		case "/accounts":
			_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"11111111111111111111111111111111"},{"id":"22222222222222222222222222222222"}]}`))
		default:
			t.Fatalf("unexpected Cloudflare API path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := &CloudflareAPIClient{BaseURL: server.URL, HTTPClient: server.Client()}
	_, err := client.ResolveIdentity(context.Background(), "raw-token")
	reasonErr, ok := err.(*identityReasonError)
	if !ok {
		t.Fatalf("error = %#v, want identityReasonError", err)
	}
	if reasonErr.reason != "CloudflareAccountAmbiguous" {
		t.Fatalf("reason = %q", reasonErr.reason)
	}
}
