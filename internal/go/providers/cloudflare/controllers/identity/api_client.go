package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	cloudflarev1alpha1 "github.com/appthrust/dns-api/pkg/go/api/cloudflare/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type CloudflareAPIClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

func NewCloudflareAPIClient() *CloudflareAPIClient {
	return &CloudflareAPIClient{}
}

func (c *CloudflareAPIClient) ResolveIdentity(ctx context.Context, token string) (IdentityResolution, error) {
	tokenStatus, err := c.verifyToken(ctx, token)
	if err != nil {
		return IdentityResolution{}, err
	}
	if tokenStatus.Status != "" && tokenStatus.Status != "active" {
		return IdentityResolution{}, &identityReasonError{reason: "AccessTokenInactive", message: fmt.Sprintf("Cloudflare API token status is %q", tokenStatus.Status)}
	}
	account, err := c.resolveSingleAccount(ctx, token)
	if err != nil {
		return IdentityResolution{}, err
	}
	return IdentityResolution{
		Account:     account,
		AccessToken: tokenStatus,
	}, nil
}

func (c *CloudflareAPIClient) verifyToken(ctx context.Context, token string) (cloudflarev1alpha1.CloudflareAccessTokenStatus, error) {
	var response struct {
		Success bool `json:"success"`
		Result  struct {
			ID        string `json:"id"`
			Status    string `json:"status"`
			ExpiresOn string `json:"expires_on"`
			NotBefore string `json:"not_before"`
		} `json:"result"`
		Errors []cloudflareResponseInfo `json:"errors"`
	}
	if err := c.do(ctx, token, "/user/tokens/verify", &response); err != nil {
		return cloudflarev1alpha1.CloudflareAccessTokenStatus{}, err
	}
	if !response.Success {
		return cloudflarev1alpha1.CloudflareAccessTokenStatus{}, cloudflareAPIError("AccessTokenInvalid", response.Errors)
	}
	return cloudflarev1alpha1.CloudflareAccessTokenStatus{
		ID:        response.Result.ID,
		Status:    response.Result.Status,
		ExpiresOn: parseCloudflareTime(response.Result.ExpiresOn),
		NotBefore: parseCloudflareTime(response.Result.NotBefore),
	}, nil
}

func (c *CloudflareAPIClient) resolveSingleAccount(ctx context.Context, token string) (cloudflarev1alpha1.CloudflareAccountStatus, error) {
	var response struct {
		Success bool `json:"success"`
		Result  []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"result"`
		Errors []cloudflareResponseInfo `json:"errors"`
	}
	if err := c.do(ctx, token, "/accounts", &response); err != nil {
		return cloudflarev1alpha1.CloudflareAccountStatus{}, err
	}
	if !response.Success {
		return cloudflarev1alpha1.CloudflareAccountStatus{}, cloudflareAPIError("ReconcileError", response.Errors)
	}
	if len(response.Result) == 0 {
		return cloudflarev1alpha1.CloudflareAccountStatus{}, &identityReasonError{reason: "CloudflareAccountNotFound", message: "no Cloudflare account is visible through the token"}
	}
	if len(response.Result) > 1 {
		return cloudflarev1alpha1.CloudflareAccountStatus{}, &identityReasonError{reason: "CloudflareAccountAmbiguous", message: "more than one Cloudflare account is visible through the token"}
	}
	return cloudflarev1alpha1.CloudflareAccountStatus{
		ID:   response.Result[0].ID,
		Name: response.Result[0].Name,
		Type: response.Result[0].Type,
	}, nil
}

func (c *CloudflareAPIClient) do(ctx context.Context, token, path string, target any) error {
	return c.doJSON(ctx, token, http.MethodGet, path, nil, target)
}

func (c *CloudflareAPIClient) doJSON(ctx context.Context, token, method, path string, body any, target any) error {
	var requestBody *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return &identityReasonError{reason: "ReconcileError", message: err.Error()}
		}
		requestBody = bytes.NewReader(raw)
	} else {
		requestBody = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.baseURL(), "/")+path, requestBody)
	if err != nil {
		return &identityReasonError{reason: "ReconcileError", message: err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return &identityReasonError{reason: "ProviderUnavailable", message: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return &identityReasonError{reason: "AccessTokenInvalid", message: "Cloudflare API token verification failed"}
	}
	if resp.StatusCode == http.StatusForbidden {
		return &identityReasonError{reason: "ProviderAccessDenied", message: "Cloudflare API permission failure"}
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return &identityReasonError{reason: "ProviderUnavailable", message: fmt.Sprintf("Cloudflare API returned HTTP %d", resp.StatusCode)}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &identityReasonError{reason: "ReconcileError", message: fmt.Sprintf("Cloudflare API returned HTTP %d", resp.StatusCode)}
	}
	if target != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
			return &identityReasonError{reason: "ReconcileError", message: err.Error()}
		}
	}
	return nil
}

func (c *CloudflareAPIClient) baseURL() string {
	if c != nil && c.BaseURL != "" {
		return c.BaseURL
	}
	return "https://api.cloudflare.com/client/v4"
}

func (c *CloudflareAPIClient) httpClient() *http.Client {
	if c != nil && c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

type cloudflareResponseInfo struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func cloudflareAPIError(defaultReason string, errors []cloudflareResponseInfo) error {
	message := "Cloudflare API request failed"
	if len(errors) > 0 && errors[0].Message != "" {
		message = errors[0].Message
	}
	return &identityReasonError{reason: defaultReason, message: message}
}

func parseCloudflareTime(value string) *metav1.Time {
	if value == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil
	}
	mt := metav1.NewTime(parsed)
	return &mt
}
