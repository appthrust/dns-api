package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	cloudflarev1alpha1 "github.com/appthrust/dns-api/pkg/go/api/cloudflare/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type CloudflareAPIClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

func NewCloudflareAPIClient() *CloudflareAPIClient {
	return &CloudflareAPIClient{}
}

type CloudflareZoneAPI struct {
	client *CloudflareAPIClient
	token  string
}

func (c *CloudflareAPIClient) ProviderForIdentity(ctx context.Context, k8sClient client.Client, identity *cloudflarev1alpha1.CloudflareIdentity) (ZoneProvider, error) {
	token, err := c.readAccessToken(ctx, k8sClient, identity)
	if err != nil {
		return nil, err
	}
	return &CloudflareZoneAPI{client: c, token: token}, nil
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
	if resp.StatusCode == http.StatusNotFound {
		return &identityReasonError{reason: "ExternalResourceNotFound", message: "Cloudflare resource was not found"}
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return &identityReasonError{reason: "AccessTokenInvalid", message: "Cloudflare API token verification failed"}
	}
	if resp.StatusCode == http.StatusForbidden {
		return &identityReasonError{reason: "ProviderAccessDenied", message: "Cloudflare API permission failure"}
	}
	if resp.StatusCode == http.StatusBadRequest {
		return &identityReasonError{reason: "ProviderInvalidRequest", message: "Cloudflare API rejected the request"}
	}
	if resp.StatusCode == http.StatusConflict {
		return &identityReasonError{reason: "ProviderConflict", message: "Cloudflare API conflict"}
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

func (c *CloudflareAPIClient) readAccessToken(ctx context.Context, k8sClient client.Client, identity *cloudflarev1alpha1.CloudflareIdentity) (string, error) {
	ref := identity.Spec.AccessToken.SecretRef
	var secret corev1.Secret
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: identity.Namespace, Name: ref.Name}, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return "", &identityReasonError{reason: "SecretNotFound", message: "referenced Secret does not exist"}
		}
		return "", &identityReasonError{reason: "ReconcileError", message: err.Error()}
	}
	raw, ok := secret.Data[ref.Key]
	if !ok || strings.TrimSpace(string(raw)) == "" {
		return "", &identityReasonError{reason: "AccessTokenUnavailable", message: "referenced Secret key does not exist or is empty"}
	}
	return strings.TrimSpace(string(raw)), nil
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

func (p *CloudflareZoneAPI) GetZone(ctx context.Context, id string) (CloudflareZone, error) {
	var response cloudflareZoneResponse
	if err := p.client.do(ctx, p.token, "/zones/"+url.PathEscape(id), &response); err != nil {
		return CloudflareZone{}, err
	}
	if !response.Success {
		return CloudflareZone{}, cloudflareAPIError("ReconcileError", response.Errors)
	}
	return cloudflareZoneFromResponse(response.Result), nil
}

func (p *CloudflareZoneAPI) ListZonesByName(ctx context.Context, accountID, name string) ([]CloudflareZone, error) {
	query := url.Values{}
	query.Set("account.id", accountID)
	query.Set("name", name)
	var response cloudflareZoneListResponse
	if err := p.client.do(ctx, p.token, "/zones?"+query.Encode(), &response); err != nil {
		return nil, err
	}
	if !response.Success {
		return nil, cloudflareAPIError("ReconcileError", response.Errors)
	}
	zones := make([]CloudflareZone, 0, len(response.Result))
	for _, result := range response.Result {
		zones = append(zones, cloudflareZoneFromResponse(result))
	}
	return zones, nil
}

func (p *CloudflareZoneAPI) CreateZone(ctx context.Context, accountID, name string) (CloudflareZone, error) {
	body := map[string]any{
		"account":    map[string]string{"id": accountID},
		"jump_start": false,
		"name":       name,
		"type":       "full",
	}
	var response cloudflareZoneResponse
	if err := p.client.doJSON(ctx, p.token, http.MethodPost, "/zones", body, &response); err != nil {
		return CloudflareZone{}, err
	}
	if !response.Success {
		return CloudflareZone{}, cloudflareAPIError("ReconcileError", response.Errors)
	}
	return cloudflareZoneFromResponse(response.Result), nil
}

func (p *CloudflareZoneAPI) DeleteZone(ctx context.Context, id string) error {
	var response struct {
		Success bool                     `json:"success"`
		Errors  []cloudflareResponseInfo `json:"errors"`
	}
	if err := p.client.doJSON(ctx, p.token, http.MethodDelete, "/zones/"+url.PathEscape(id), nil, &response); err != nil {
		return err
	}
	if !response.Success {
		return cloudflareAPIError("ReconcileError", response.Errors)
	}
	return nil
}

func (p *CloudflareZoneAPI) ListDNSRecords(ctx context.Context, zoneID, name string) ([]CloudflareDNSRecord, error) {
	query := url.Values{}
	if name != "" {
		query.Set("name", name)
	}
	var response cloudflareDNSRecordListResponse
	if err := p.client.do(ctx, p.token, "/zones/"+url.PathEscape(zoneID)+"/dns_records?"+query.Encode(), &response); err != nil {
		return nil, err
	}
	if !response.Success {
		return nil, cloudflareAPIError("ReconcileError", response.Errors)
	}
	records := make([]CloudflareDNSRecord, 0, len(response.Result))
	for _, result := range response.Result {
		records = append(records, cloudflareDNSRecordFromResponse(result))
	}
	return records, nil
}

func (p *CloudflareZoneAPI) GetDNSRecord(ctx context.Context, zoneID, recordID string) (CloudflareDNSRecord, error) {
	var response cloudflareDNSRecordResponse
	if err := p.client.do(ctx, p.token, "/zones/"+url.PathEscape(zoneID)+"/dns_records/"+url.PathEscape(recordID), &response); err != nil {
		return CloudflareDNSRecord{}, err
	}
	if !response.Success {
		return CloudflareDNSRecord{}, cloudflareAPIError("ReconcileError", response.Errors)
	}
	return cloudflareDNSRecordFromResponse(response.Result), nil
}

func (p *CloudflareZoneAPI) CreateDNSRecord(ctx context.Context, zoneID string, record CloudflareDNSRecord) (CloudflareDNSRecord, error) {
	body := cloudflareDNSRecordRequest(record)
	var response cloudflareDNSRecordResponse
	if err := p.client.doJSON(ctx, p.token, http.MethodPost, "/zones/"+url.PathEscape(zoneID)+"/dns_records", body, &response); err != nil {
		return CloudflareDNSRecord{}, err
	}
	if !response.Success {
		return CloudflareDNSRecord{}, cloudflareAPIError("ReconcileError", response.Errors)
	}
	return cloudflareDNSRecordFromResponse(response.Result), nil
}

func (p *CloudflareZoneAPI) PatchDNSRecord(ctx context.Context, zoneID, recordID string, record CloudflareDNSRecord) (CloudflareDNSRecord, error) {
	body := cloudflareDNSRecordRequest(record)
	var response cloudflareDNSRecordResponse
	if err := p.client.doJSON(ctx, p.token, http.MethodPatch, "/zones/"+url.PathEscape(zoneID)+"/dns_records/"+url.PathEscape(recordID), body, &response); err != nil {
		return CloudflareDNSRecord{}, err
	}
	if !response.Success {
		return CloudflareDNSRecord{}, cloudflareAPIError("ReconcileError", response.Errors)
	}
	return cloudflareDNSRecordFromResponse(response.Result), nil
}

func (p *CloudflareZoneAPI) DeleteDNSRecord(ctx context.Context, zoneID, recordID string) error {
	var response struct {
		Success bool                     `json:"success"`
		Errors  []cloudflareResponseInfo `json:"errors"`
	}
	if err := p.client.doJSON(ctx, p.token, http.MethodDelete, "/zones/"+url.PathEscape(zoneID)+"/dns_records/"+url.PathEscape(recordID), nil, &response); err != nil {
		return err
	}
	if !response.Success {
		return cloudflareAPIError("ReconcileError", response.Errors)
	}
	return nil
}

func (p *CloudflareZoneAPI) BatchDNSRecords(ctx context.Context, zoneID string, batch CloudflareDNSRecordBatch) (CloudflareDNSRecordBatch, error) {
	body := cloudflareDNSRecordBatchRequest(batch)
	var response cloudflareDNSRecordBatchResponse
	if err := p.client.doJSON(ctx, p.token, http.MethodPost, "/zones/"+url.PathEscape(zoneID)+"/dns_records/batch", body, &response); err != nil {
		return CloudflareDNSRecordBatch{}, err
	}
	if !response.Success {
		return CloudflareDNSRecordBatch{}, cloudflareAPIError("ReconcileError", response.Errors)
	}
	return cloudflareDNSRecordBatchFromResponse(response.Result), nil
}

type cloudflareZoneResponse struct {
	Success bool                     `json:"success"`
	Result  cloudflareZoneResult     `json:"result"`
	Errors  []cloudflareResponseInfo `json:"errors"`
}

type cloudflareZoneListResponse struct {
	Success bool                     `json:"success"`
	Result  []cloudflareZoneResult   `json:"result"`
	Errors  []cloudflareResponseInfo `json:"errors"`
}

type cloudflareZoneResult struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Status      string   `json:"status"`
	Type        string   `json:"type"`
	NameServers []string `json:"name_servers"`
	Account     struct {
		ID string `json:"id"`
	} `json:"account"`
}

type cloudflareDNSRecordResponse struct {
	Success bool                      `json:"success"`
	Result  cloudflareDNSRecordResult `json:"result"`
	Errors  []cloudflareResponseInfo  `json:"errors"`
}

type cloudflareDNSRecordListResponse struct {
	Success bool                        `json:"success"`
	Result  []cloudflareDNSRecordResult `json:"result"`
	Errors  []cloudflareResponseInfo    `json:"errors"`
}

type cloudflareDNSRecordBatchResponse struct {
	Success bool                           `json:"success"`
	Result  cloudflareDNSRecordBatchResult `json:"result"`
	Errors  []cloudflareResponseInfo       `json:"errors"`
}

type cloudflareDNSRecordBatchResult struct {
	Deletes []cloudflareDNSRecordResult `json:"deletes"`
	Patches []cloudflareDNSRecordResult `json:"patches"`
	Posts   []cloudflareDNSRecordResult `json:"posts"`
}

type cloudflareDNSRecordResult struct {
	ID        string   `json:"id"`
	Type      string   `json:"type"`
	Name      string   `json:"name"`
	Content   string   `json:"content"`
	Priority  *int32   `json:"priority"`
	TTL       *int32   `json:"ttl"`
	Proxied   *bool    `json:"proxied"`
	Proxiable *bool    `json:"proxiable"`
	Comment   string   `json:"comment"`
	Tags      []string `json:"tags"`
	Data      struct {
		Flags *int32 `json:"flags"`
		Tag   string `json:"tag"`
		Value string `json:"value"`
	} `json:"data"`
}

func cloudflareDNSRecordFromResponse(result cloudflareDNSRecordResult) CloudflareDNSRecord {
	record := CloudflareDNSRecord{
		ID:        result.ID,
		Type:      result.Type,
		Name:      result.Name,
		Content:   result.Content,
		Priority:  result.Priority,
		TTL:       result.TTL,
		Proxied:   result.Proxied,
		Proxiable: result.Proxiable,
		Comment:   result.Comment,
		Tags:      result.Tags,
	}
	if result.Data.Flags != nil || result.Data.Tag != "" || result.Data.Value != "" {
		record.CAA = &CloudflareCAAData{
			Flags: valueOrZero(result.Data.Flags),
			Tag:   result.Data.Tag,
			Value: result.Data.Value,
		}
	}
	return record
}

func cloudflareDNSRecordBatchRequest(batch CloudflareDNSRecordBatch) map[string]any {
	body := map[string]any{}
	if len(batch.Deletes) > 0 {
		deletes := make([]map[string]any, 0, len(batch.Deletes))
		for _, record := range batch.Deletes {
			deletes = append(deletes, map[string]any{"id": record.ID})
		}
		body["deletes"] = deletes
	}
	if len(batch.Patches) > 0 {
		patches := make([]map[string]any, 0, len(batch.Patches))
		for _, record := range batch.Patches {
			item := cloudflareDNSRecordRequest(record)
			item["id"] = record.ID
			patches = append(patches, item)
		}
		body["patches"] = patches
	}
	if len(batch.Posts) > 0 {
		posts := make([]map[string]any, 0, len(batch.Posts))
		for _, record := range batch.Posts {
			posts = append(posts, cloudflareDNSRecordRequest(record))
		}
		body["posts"] = posts
	}
	return body
}

func cloudflareDNSRecordBatchFromResponse(result cloudflareDNSRecordBatchResult) CloudflareDNSRecordBatch {
	return CloudflareDNSRecordBatch{
		Deletes: cloudflareDNSRecordsFromResponse(result.Deletes),
		Patches: cloudflareDNSRecordsFromResponse(result.Patches),
		Posts:   cloudflareDNSRecordsFromResponse(result.Posts),
	}
}

func cloudflareDNSRecordsFromResponse(results []cloudflareDNSRecordResult) []CloudflareDNSRecord {
	records := make([]CloudflareDNSRecord, 0, len(results))
	for _, result := range results {
		records = append(records, cloudflareDNSRecordFromResponse(result))
	}
	return records
}

func cloudflareDNSRecordRequest(record CloudflareDNSRecord) map[string]any {
	body := map[string]any{
		"type":    record.Type,
		"name":    record.Name,
		"content": record.Content,
		"ttl":     valueOrDefault(record.TTL, 1),
	}
	if record.Priority != nil {
		body["priority"] = *record.Priority
	}
	if record.Proxied != nil {
		body["proxied"] = *record.Proxied
	}
	if record.Comment != "" {
		body["comment"] = record.Comment
	}
	if len(record.Tags) > 0 {
		body["tags"] = record.Tags
	}
	if record.CAA != nil {
		body["data"] = map[string]any{
			"flags": record.CAA.Flags,
			"tag":   record.CAA.Tag,
			"value": record.CAA.Value,
		}
	}
	return body
}

func valueOrZero(value *int32) int32 {
	if value == nil {
		return 0
	}
	return *value
}

func valueOrDefault(value *int32, fallback int32) int32 {
	if value == nil {
		return fallback
	}
	return *value
}

func cloudflareZoneFromResponse(result cloudflareZoneResult) CloudflareZone {
	return CloudflareZone{
		ID:          result.ID,
		AccountID:   result.Account.ID,
		Name:        result.Name,
		Status:      result.Status,
		Type:        result.Type,
		NameServers: result.NameServers,
	}
}
