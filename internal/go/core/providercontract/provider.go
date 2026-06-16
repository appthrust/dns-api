package providercontract

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/appthrust/dns-api/internal/go/core/celruntime"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	"github.com/google/cel-go/cel"
	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiservervalidation "k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	"k8s.io/apimachinery/pkg/runtime"
)

type PayloadTarget string

const (
	TargetZoneClass PayloadTarget = "ZoneClass"
	TargetZone      PayloadTarget = "Zone"
	TargetRecordSet PayloadTarget = "RecordSet"
)

type Payload struct {
	Parameters runtime.RawExtension
	Adoption   runtime.RawExtension
	Options    runtime.RawExtension
}

type ConversionInput struct {
	Provider    *dnsv1alpha1.Provider
	Target      PayloadTarget
	FromVersion *dnsv1alpha1.ProviderVersion
	ToVersion   *dnsv1alpha1.ProviderVersion
	Payload     Payload
	Context     map[string]interface{}
}

func ConvertZoneClassParametersToStorage(provider *dnsv1alpha1.Provider, version *dnsv1alpha1.ProviderVersion, zoneClass *dnsv1alpha1.ZoneClass) (runtime.RawExtension, *PayloadError) {
	storageVersion, ok := StorageVersion(provider)
	if !ok {
		return runtime.RawExtension{}, payloadError(ProviderContractNotReady, "provider storage version is not resolved")
	}
	payload, err := ConvertProviderPayloadToStorage(ConversionInput{
		Provider:    provider,
		Target:      TargetZoneClass,
		FromVersion: version,
		ToVersion:   storageVersion,
		Payload: Payload{
			Parameters: zoneClass.Spec.Parameters,
		},
		Context: map[string]interface{}{
			"zoneClass": toUnstructured(zoneClass),
		},
	})
	if err != nil {
		return runtime.RawExtension{}, err
	}
	return payload.Parameters, nil
}

type PayloadErrorKind string

const (
	SchemaViolation              PayloadErrorKind = "SchemaViolation"
	ConversionRejected           PayloadErrorKind = "ConversionRejected"
	ConversionWebhookUnavailable PayloadErrorKind = "ConversionWebhookUnavailable"
	ConversionWebhookTimeout     PayloadErrorKind = "ConversionWebhookTimeout"
	ProviderContractNotReady     PayloadErrorKind = "ProviderContractNotReady"
)

type PayloadError struct {
	Kind    PayloadErrorKind
	Message string
}

func (e *PayloadError) Error() string {
	return e.Message
}

type Acceptance struct {
	Status  string
	Reason  string
	Message string
}

func StorageVersion(provider *dnsv1alpha1.Provider) (*dnsv1alpha1.ProviderVersion, bool) {
	if provider == nil {
		return nil, false
	}
	for index := range provider.Spec.Versions {
		version := &provider.Spec.Versions[index]
		if version.Storage {
			return version, true
		}
	}
	return nil, false
}

func ServedVersion(provider *dnsv1alpha1.Provider, name string) (*dnsv1alpha1.ProviderVersion, bool) {
	for index := range provider.Spec.Versions {
		version := &provider.Spec.Versions[index]
		if version.Name == name && version.Served {
			return version, true
		}
	}
	return nil, false
}

func SupportsRecordType(providerVersion *dnsv1alpha1.ProviderVersion, recordType dnsv1alpha1.RecordType) bool {
	return providerVersion != nil && slices.Contains(providerVersion.RecordSet.SupportedTypes, recordType)
}

func ConvertProviderPayloadToStorage(input ConversionInput) (Payload, *PayloadError) {
	if input.Provider == nil || input.FromVersion == nil || input.ToVersion == nil {
		return Payload{}, payloadError(ProviderContractNotReady, "provider contract version is not resolved")
	}
	defaultedInput, err := defaultPayload(input.Payload, input.FromVersion, input.Target)
	if err != nil {
		return Payload{}, payloadError(SchemaViolation, err.Error())
	}
	if err := validatePayload(defaultedInput, input.FromVersion, input.Target, false); err != nil {
		return Payload{}, payloadError(SchemaViolation, err.Error())
	}
	if input.FromVersion.Name == input.ToVersion.Name {
		return defaultedInput, nil
	}

	conversion := conversionForTarget(input.FromVersion, input.Target)
	if strings.TrimSpace(conversion.ToStorage.CEL) != "" {
		input.Payload = defaultedInput
		converted, err := evaluateConversionCEL(input)
		if err != nil {
			return Payload{}, payloadError(ConversionRejected, err.Error())
		}
		storagePayload, err := defaultPayload(converted, input.ToVersion, input.Target)
		if err != nil {
			return Payload{}, payloadError(ConversionRejected, err.Error())
		}
		if err := validatePayload(storagePayload, input.ToVersion, input.Target, false); err != nil {
			return Payload{}, payloadError(ConversionRejected, "converted payload does not match storage schema: "+err.Error())
		}
		return storagePayload, nil
	}
	if conversion.ToStorage.Webhook != nil {
		input.Payload = defaultedInput
		converted, webhookErr := callProviderConversionWebhook(input, conversion.ToStorage.Webhook)
		if webhookErr != nil {
			return Payload{}, webhookErr
		}
		storagePayload, defaultErr := defaultPayload(converted, input.ToVersion, input.Target)
		if defaultErr != nil {
			return Payload{}, payloadError(ConversionRejected, defaultErr.Error())
		}
		if err := validatePayload(storagePayload, input.ToVersion, input.Target, false); err != nil {
			return Payload{}, payloadError(ConversionRejected, "converted payload does not match storage schema: "+err.Error())
		}
		return storagePayload, nil
	}
	return Payload{}, payloadError(ConversionRejected, fmt.Sprintf("provider version %s does not define conversion to %s", input.FromVersion.Name, input.ToVersion.Name))
}

func PayloadErrorToAcceptance(err *PayloadError) Acceptance {
	if err == nil {
		return Acceptance{Status: "True", Reason: "Accepted"}
	}
	switch err.Kind {
	case SchemaViolation:
		return Acceptance{Status: "False", Reason: "InvalidProviderPayload", Message: err.Message}
	case ConversionRejected:
		return Acceptance{Status: "False", Reason: "ProviderConversionFailed", Message: err.Message}
	case ConversionWebhookUnavailable, ConversionWebhookTimeout:
		return Acceptance{Status: "Unknown", Reason: "ProviderConversionUnavailable", Message: err.Message}
	case ProviderContractNotReady:
		return Acceptance{Status: "Unknown", Reason: "ProviderStorageVersionNotResolved", Message: err.Message}
	default:
		return Acceptance{Status: "Unknown", Reason: "ProviderConversionFailed", Message: err.Message}
	}
}

func ValidatePayload(payload Payload, version *dnsv1alpha1.ProviderVersion, target PayloadTarget) error {
	return validatePayload(payload, version, target, false)
}

func payloadError(kind PayloadErrorKind, message string) *PayloadError {
	return &PayloadError{Kind: kind, Message: message}
}

type conversionReview struct {
	APIVersion string                     `json:"apiVersion"`
	Kind       string                     `json:"kind"`
	Request    *conversionReviewRequest   `json:"request,omitempty"`
	Response   *conversionReviewResponse  `json:"response,omitempty"`
	Payload    map[string]json.RawMessage `json:"payload,omitempty"`
}

type conversionReviewRequest struct {
	UID         string                     `json:"uid"`
	Provider    conversionProviderRef      `json:"provider"`
	Target      string                     `json:"target"`
	FromVersion string                     `json:"fromVersion"`
	ToVersion   string                     `json:"toVersion"`
	Payload     map[string]json.RawMessage `json:"payload,omitempty"`
	Context     map[string]interface{}     `json:"context,omitempty"`
}

type conversionProviderRef struct {
	Name string `json:"name"`
}

type conversionReviewResponse struct {
	UID    string                 `json:"uid"`
	Result conversionReviewResult `json:"result"`
}

type conversionReviewResult struct {
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
	Message   string `json:"message,omitempty"`
	Retryable bool   `json:"retryable,omitempty"`
}

func callProviderConversionWebhook(input ConversionInput, webhook *dnsv1alpha1.ProviderConversionWebhook) (Payload, *PayloadError) {
	uid := conversionUID()
	request := conversionReview{
		APIVersion: "dns.appthrust.io/v1alpha1",
		Kind:       "ProviderConversionReview",
		Request: &conversionReviewRequest{
			UID:         uid,
			Provider:    conversionProviderRef{Name: input.Provider.Name},
			Target:      string(input.Target),
			FromVersion: input.FromVersion.Name,
			ToVersion:   input.ToVersion.Name,
			Payload:     payloadToRawMap(input.Payload, input.Target),
			Context:     input.Context,
		},
	}
	body, err := json.Marshal(request)
	if err != nil {
		return Payload{}, payloadError(ConversionRejected, err.Error())
	}

	client, err := conversionWebhookHTTPClient(webhook)
	if err != nil {
		return Payload{}, payloadError(ConversionWebhookUnavailable, err.Error())
	}
	timeout := time.Duration(conversionWebhookTimeoutSeconds(webhook)) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, conversionWebhookURL(webhook), bytes.NewReader(body))
	if err != nil {
		return Payload{}, payloadError(ConversionRejected, err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return Payload{}, payloadError(ConversionWebhookTimeout, err.Error())
		}
		return Payload{}, payloadError(ConversionWebhookUnavailable, err.Error())
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Payload{}, payloadError(ConversionWebhookUnavailable, err.Error())
	}
	if resp.StatusCode >= 500 {
		return Payload{}, payloadError(ConversionWebhookUnavailable, fmt.Sprintf("provider conversion webhook returned HTTP %d", resp.StatusCode))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Payload{}, payloadError(ConversionRejected, fmt.Sprintf("provider conversion webhook returned HTTP %d", resp.StatusCode))
	}

	var review conversionReview
	if err := json.Unmarshal(rawBody, &review); err != nil {
		return Payload{}, payloadError(ConversionRejected, "provider conversion webhook response is malformed: "+err.Error())
	}
	if review.APIVersion != "dns.appthrust.io/v1alpha1" || review.Kind != "ProviderConversionReview" || review.Response == nil {
		return Payload{}, payloadError(ConversionRejected, "provider conversion webhook response kind is unsupported")
	}
	if review.Response.UID != uid {
		return Payload{}, payloadError(ConversionRejected, "provider conversion webhook response uid does not match request uid")
	}
	switch review.Response.Result.Status {
	case "Success":
		if len(review.Payload) == 0 {
			return Payload{}, payloadError(ConversionRejected, "provider conversion webhook success response requires payload")
		}
		return rawMapToPayload(review.Payload), nil
	case "Failure":
		message := review.Response.Result.Message
		if message == "" {
			message = review.Response.Result.Reason
		}
		if message == "" {
			message = "provider conversion webhook returned failure"
		}
		if review.Response.Result.Retryable {
			return Payload{}, payloadError(ConversionWebhookUnavailable, message)
		}
		return Payload{}, payloadError(ConversionRejected, message)
	default:
		return Payload{}, payloadError(ConversionRejected, "provider conversion webhook result.status must be Success or Failure")
	}
}

func conversionWebhookHTTPClient(webhook *dnsv1alpha1.ProviderConversionWebhook) (*http.Client, error) {
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(webhook.ClientConfig.CABundle) {
		return nil, fmt.Errorf("provider conversion webhook caBundle does not contain a PEM certificate")
	}
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		},
	}, nil
}

func conversionWebhookURL(webhook *dnsv1alpha1.ProviderConversionWebhook) string {
	service := webhook.ClientConfig.Service
	path := service.Path
	if path == "" {
		path = "/convert"
	}
	port := int32(443)
	if service.Port != nil {
		port = *service.Port
	}
	return fmt.Sprintf("https://%s.%s.svc:%d%s", service.Name, service.Namespace, port, path)
}

func conversionWebhookTimeoutSeconds(webhook *dnsv1alpha1.ProviderConversionWebhook) int32 {
	if webhook.TimeoutSeconds == nil {
		return 10
	}
	return *webhook.TimeoutSeconds
}

func conversionUID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}

func payloadToRawMap(payload Payload, target PayloadTarget) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	switch target {
	case TargetZoneClass:
		if len(payload.Parameters.Raw) > 0 {
			out["parameters"] = json.RawMessage(payload.Parameters.Raw)
		}
	case TargetZone:
		if len(payload.Adoption.Raw) > 0 {
			out["adoption"] = json.RawMessage(payload.Adoption.Raw)
		}
	case TargetRecordSet:
		if len(payload.Options.Raw) > 0 {
			out["options"] = json.RawMessage(payload.Options.Raw)
		}
		if len(payload.Adoption.Raw) > 0 {
			out["adoption"] = json.RawMessage(payload.Adoption.Raw)
		}
	}
	return out
}

func rawMapToPayload(raw map[string]json.RawMessage) Payload {
	return Payload{
		Parameters: runtime.RawExtension{Raw: raw["parameters"]},
		Adoption:   runtime.RawExtension{Raw: raw["adoption"]},
		Options:    runtime.RawExtension{Raw: raw["options"]},
	}
}

func conversionForTarget(version *dnsv1alpha1.ProviderVersion, target PayloadTarget) dnsv1alpha1.ProviderConversion {
	switch target {
	case TargetZoneClass:
		return version.ZoneClass.Conversion
	case TargetZone:
		return version.Zone.Conversion
	case TargetRecordSet:
		return version.RecordSet.Conversion
	default:
		return dnsv1alpha1.ProviderConversion{}
	}
}

func validatePayload(payload Payload, version *dnsv1alpha1.ProviderVersion, target PayloadTarget, validateMissing bool) error {
	if version == nil {
		return fmt.Errorf("provider version is not resolved")
	}
	switch target {
	case TargetZoneClass:
		return validateRawBySchema(payload.Parameters, version.ZoneClass.Schemas.Parameters, "parameters", validateMissing)
	case TargetZone:
		return validateRawBySchema(payload.Adoption, version.Zone.Schemas.Adoption, "adoption", validateMissing)
	case TargetRecordSet:
		if err := validateRawBySchema(payload.Options, version.RecordSet.Schemas.Options, "options", validateMissing); err != nil {
			return err
		}
		return validateRawBySchema(payload.Adoption, version.RecordSet.Schemas.Adoption, "adoption", validateMissing)
	default:
		return fmt.Errorf("unsupported provider payload target %q", target)
	}
}

func defaultPayload(payload Payload, version *dnsv1alpha1.ProviderVersion, target PayloadTarget) (Payload, error) {
	if version == nil {
		return Payload{}, fmt.Errorf("provider version is not resolved")
	}
	switch target {
	case TargetZoneClass:
		parameters, err := defaultRawBySchema(payload.Parameters, version.ZoneClass.Schemas.Parameters, "parameters")
		if err != nil {
			return Payload{}, err
		}
		payload.Parameters = parameters
		return payload, nil
	case TargetZone:
		adoption, err := defaultRawBySchema(payload.Adoption, version.Zone.Schemas.Adoption, "adoption")
		if err != nil {
			return Payload{}, err
		}
		payload.Adoption = adoption
		return payload, nil
	case TargetRecordSet:
		options, err := defaultRawBySchema(payload.Options, version.RecordSet.Schemas.Options, "options")
		if err != nil {
			return Payload{}, err
		}
		adoption, err := defaultRawBySchema(payload.Adoption, version.RecordSet.Schemas.Adoption, "adoption")
		if err != nil {
			return Payload{}, err
		}
		payload.Options = options
		payload.Adoption = adoption
		return payload, nil
	default:
		return Payload{}, fmt.Errorf("unsupported provider payload target %q", target)
	}
}

func defaultRawBySchema(value runtime.RawExtension, schema *dnsv1alpha1.ProviderOpenAPISchema, path string) (runtime.RawExtension, error) {
	if schema == nil || len(rawExtensionBytes(schema.OpenAPIV3Schema)) == 0 {
		return value, nil
	}
	schemaV1, err := openAPISchema(schema, path)
	if err != nil {
		return runtime.RawExtension{}, err
	}
	raw := rawExtensionBytes(value)
	if len(raw) == 0 {
		if schemaV1.Default != nil && len(schemaV1.Default.Raw) > 0 {
			raw = schemaV1.Default.Raw
		} else if schemaHasDefault(&schemaV1) {
			raw = []byte(`{}`)
		} else {
			return value, nil
		}
	}
	var object interface{}
	if err := json.Unmarshal(raw, &object); err != nil {
		return runtime.RawExtension{}, fmt.Errorf("%s must be valid JSON: %w", path, err)
	}
	defaulted := applySchemaDefaults(object, &schemaV1)
	defaultedRaw, err := json.Marshal(defaulted)
	if err != nil {
		return runtime.RawExtension{}, fmt.Errorf("%s defaults could not be encoded: %w", path, err)
	}
	if string(defaultedRaw) == "null" {
		return runtime.RawExtension{}, nil
	}
	return runtime.RawExtension{Raw: defaultedRaw}, nil
}

func applySchemaDefaults(value interface{}, schema *apiextensionsv1.JSONSchemaProps) interface{} {
	if value == nil {
		if defaultValue, ok := schemaDefaultValue(schema); ok {
			return defaultValue
		}
		return nil
	}
	switch typed := value.(type) {
	case map[string]interface{}:
		for name, property := range schema.Properties {
			if current, ok := typed[name]; ok {
				typed[name] = applySchemaDefaults(current, &property)
				continue
			}
			if defaultValue, ok := schemaDefaultValue(&property); ok {
				typed[name] = defaultValue
			}
		}
		return typed
	case []interface{}:
		if schema.Items == nil || schema.Items.Schema == nil {
			return typed
		}
		for index := range typed {
			typed[index] = applySchemaDefaults(typed[index], schema.Items.Schema)
		}
		return typed
	default:
		return value
	}
}

func schemaDefaultValue(schema *apiextensionsv1.JSONSchemaProps) (interface{}, bool) {
	if schema == nil || schema.Default == nil || len(schema.Default.Raw) == 0 {
		return nil, false
	}
	var value interface{}
	if err := json.Unmarshal(schema.Default.Raw, &value); err != nil {
		return nil, false
	}
	return value, true
}

func schemaHasDefault(schema *apiextensionsv1.JSONSchemaProps) bool {
	if schema == nil {
		return false
	}
	if schema.Default != nil && len(schema.Default.Raw) > 0 {
		return true
	}
	for _, property := range schema.Properties {
		if schemaHasDefault(&property) {
			return true
		}
	}
	if schema.Items != nil && schema.Items.Schema != nil {
		return schemaHasDefault(schema.Items.Schema)
	}
	return false
}

func validateRawBySchema(value runtime.RawExtension, schema *dnsv1alpha1.ProviderOpenAPISchema, path string, validateMissing bool) error {
	if schema == nil || len(rawExtensionBytes(schema.OpenAPIV3Schema)) == 0 {
		return nil
	}
	raw := rawExtensionBytes(value)
	if len(raw) == 0 && !validateMissing {
		return nil
	}
	if len(raw) == 0 {
		raw = []byte(`{}`)
	}
	validator, err := schemaValidator(schema, path)
	if err != nil {
		return err
	}
	var object interface{}
	if err := json.Unmarshal(raw, &object); err != nil {
		return fmt.Errorf("%s must be valid JSON: %w", path, err)
	}
	if errs := apiservervalidation.ValidateCustomResource(nil, object, validator); len(errs) > 0 {
		return fmt.Errorf("%s does not match provider schema: %s", path, errs.ToAggregate().Error())
	}
	return nil
}

func schemaValidator(schema *dnsv1alpha1.ProviderOpenAPISchema, path string) (apiservervalidation.SchemaValidator, error) {
	schemaV1, err := openAPISchema(schema, path)
	if err != nil {
		return nil, err
	}
	var schemaInternal apiextensions.JSONSchemaProps
	if err := apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(&schemaV1, &schemaInternal, nil); err != nil {
		return nil, fmt.Errorf("%s schema is invalid: %w", path, err)
	}
	validator, _, err := apiservervalidation.NewSchemaValidator(&schemaInternal)
	if err != nil {
		return nil, fmt.Errorf("%s schema is invalid: %w", path, err)
	}
	return validator, nil
}

func openAPISchema(schema *dnsv1alpha1.ProviderOpenAPISchema, path string) (apiextensionsv1.JSONSchemaProps, error) {
	var schemaV1 apiextensionsv1.JSONSchemaProps
	if err := json.Unmarshal(rawExtensionBytes(schema.OpenAPIV3Schema), &schemaV1); err != nil {
		return schemaV1, fmt.Errorf("%s schema is invalid: %w", path, err)
	}
	return schemaV1, nil
}

func evaluateConversionCEL(input ConversionInput) (Payload, error) {
	self, err := payloadToObject(input.Payload, input.Target)
	if err != nil {
		return Payload{}, err
	}
	declarations := []cel.EnvOption{
		cel.Variable("self", cel.DynType),
		cel.Variable("target", cel.StringType),
		cel.Variable("provider", cel.DynType),
		cel.Variable("fromVersion", cel.DynType),
		cel.Variable("toVersion", cel.DynType),
	}
	for key := range input.Context {
		declarations = append(declarations, cel.Variable(key, cel.DynType))
	}
	activation := map[string]interface{}{
		"self":        self,
		"target":      string(input.Target),
		"provider":    toUnstructured(input.Provider),
		"fromVersion": toUnstructured(input.FromVersion),
		"toVersion":   toUnstructured(input.ToVersion),
	}
	for key, value := range input.Context {
		activation[key] = value
	}
	object, err := celruntime.EvalObject(conversionForTarget(input.FromVersion, input.Target).ToStorage.CEL, activation, declarations...)
	if err != nil {
		return Payload{}, err
	}
	return objectToPayload(object, input.Target)
}

func payloadToObject(payload Payload, target PayloadTarget) (map[string]interface{}, error) {
	object := map[string]interface{}{}
	switch target {
	case TargetZoneClass:
		value, err := rawToObject(payload.Parameters)
		if err != nil {
			return nil, fmt.Errorf("parameters must be valid JSON: %w", err)
		}
		object["parameters"] = value
	case TargetZone:
		value, err := rawToObject(payload.Adoption)
		if err != nil {
			return nil, fmt.Errorf("adoption must be valid JSON: %w", err)
		}
		object["adoption"] = value
	case TargetRecordSet:
		options, err := rawToObject(payload.Options)
		if err != nil {
			return nil, fmt.Errorf("options must be valid JSON: %w", err)
		}
		adoption, err := rawToObject(payload.Adoption)
		if err != nil {
			return nil, fmt.Errorf("adoption must be valid JSON: %w", err)
		}
		object["options"] = options
		object["adoption"] = adoption
	default:
		return nil, fmt.Errorf("unsupported provider payload target %q", target)
	}
	return object, nil
}

func rawToObject(value runtime.RawExtension) (interface{}, error) {
	raw := rawExtensionBytes(value)
	if len(raw) == 0 {
		raw = []byte(`{}`)
	}
	var object interface{}
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	return object, nil
}

func objectToPayload(object map[string]interface{}, target PayloadTarget) (Payload, error) {
	var payload Payload
	switch target {
	case TargetZoneClass:
		raw, err := objectFieldRaw(object, "parameters")
		if err != nil {
			return Payload{}, err
		}
		payload.Parameters = raw
	case TargetZone:
		raw, err := objectFieldRaw(object, "adoption")
		if err != nil {
			return Payload{}, err
		}
		payload.Adoption = raw
	case TargetRecordSet:
		options, err := objectFieldRaw(object, "options")
		if err != nil {
			return Payload{}, err
		}
		adoption, err := objectFieldRaw(object, "adoption")
		if err != nil {
			return Payload{}, err
		}
		payload.Options = options
		payload.Adoption = adoption
	default:
		return Payload{}, fmt.Errorf("unsupported provider payload target %q", target)
	}
	return payload, nil
}

func objectFieldRaw(object map[string]interface{}, field string) (runtime.RawExtension, error) {
	value, ok := object[field]
	if !ok || value == nil {
		return runtime.RawExtension{}, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return runtime.RawExtension{}, fmt.Errorf("%s could not be encoded: %w", field, err)
	}
	if string(raw) == "null" {
		return runtime.RawExtension{}, nil
	}
	return runtime.RawExtension{Raw: raw}, nil
}

func toUnstructured(value interface{}) map[string]interface{} {
	object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(value)
	if err != nil {
		return map[string]interface{}{}
	}
	return object
}

func rawExtensionBytes(value runtime.RawExtension) []byte {
	if len(value.Raw) > 0 {
		return value.Raw
	}
	if value.Object != nil {
		raw, err := json.Marshal(value.Object)
		if err == nil {
			return raw
		}
	}
	return nil
}
