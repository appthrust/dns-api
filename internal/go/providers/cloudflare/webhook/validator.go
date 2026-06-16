package webhook

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	cloudflarev1alpha1 "github.com/appthrust/dns-api/pkg/go/api/cloudflare/v1alpha1"
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlwebhook "sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const cloudflareIdentityWebhookPath = "/validate-cloudflare-dns-appthrust-io-v1alpha1-cloudflareidentity"

var cloudflareIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

type Validator struct {
	decoder admission.Decoder
}

func NewValidator(scheme *runtime.Scheme) *Validator {
	return &Validator{decoder: admission.NewDecoder(scheme)}
}

func SetupValidationWebhookWithManager(mgr ctrl.Manager) error {
	validator := NewValidator(mgr.GetScheme())
	mgr.GetWebhookServer().Register(cloudflareIdentityWebhookPath, &ctrlwebhook.Admission{Handler: validator})
	return nil
}

func (v *Validator) Handle(_ context.Context, req admission.Request) admission.Response {
	if req.Operation == admissionv1.Delete {
		return admission.Allowed("delete is not validated")
	}
	if req.Kind.Kind != "CloudflareIdentity" {
		return admission.Denied(fmt.Sprintf("unsupported kind %q", req.Kind.Kind))
	}
	var identity cloudflarev1alpha1.CloudflareIdentity
	if err := v.decoder.Decode(req, &identity); err != nil {
		return admission.Denied(err.Error())
	}
	var err error
	if req.SubResource == "status" {
		err = validateCloudflareIdentityStatus(&identity)
	} else {
		err = validateCloudflareIdentity(&identity)
	}
	if err != nil {
		return admission.Denied(err.Error())
	}
	return admission.Allowed("cloudflare validation passed")
}

func validateCloudflareIdentity(identity *cloudflarev1alpha1.CloudflareIdentity) error {
	if strings.TrimSpace(identity.Spec.AccessToken.SecretRef.Name) == "" {
		return errors.New("spec.accessToken.secretRef.name is required")
	}
	if strings.TrimSpace(identity.Spec.AccessToken.SecretRef.Key) == "" {
		return errors.New("spec.accessToken.secretRef.key is required")
	}
	return nil
}

func validateCloudflareIdentityStatus(identity *cloudflarev1alpha1.CloudflareIdentity) error {
	if err := validateObservedGeneration(identity.Status.ObservedGeneration, identity.Generation, "status.observedGeneration"); err != nil {
		return err
	}
	if identity.Status.Account != nil && identity.Status.Account.ID != "" && !cloudflareIDPattern.MatchString(identity.Status.Account.ID) {
		return fmt.Errorf("status.account.id %q is not a valid Cloudflare account ID", identity.Status.Account.ID)
	}
	if err := validateIdentityConditions(identity.Status.Conditions, "status.conditions", cloudflareIdentityReasons()); err != nil {
		return err
	}
	return validateConditionObservedGenerations(identity.Status.Conditions, identity.Generation, "status.conditions")
}

func cloudflareIdentityReasons() map[string]struct{} {
	return map[string]struct{}{
		"Accepted":                   {},
		"AccessTokenInactive":        {},
		"AccessTokenInvalid":         {},
		"AccessTokenUnavailable":     {},
		"CloudflareAccountAmbiguous": {},
		"CloudflareAccountNotFound":  {},
		"InvalidSecretRef":           {},
		"ProviderUnavailable":        {},
		"Ready":                      {},
		"ReconcileError":             {},
		"SecretNotFound":             {},
	}
}

func validateIdentityConditions(conditions []metav1.Condition, path string, reasons map[string]struct{}) error {
	for index, condition := range conditions {
		if condition.Type != "Accepted" && condition.Type != "Ready" {
			return fmt.Errorf("%s[%d].type %q is not supported", path, index, condition.Type)
		}
		if condition.Status != metav1.ConditionTrue && condition.Status != metav1.ConditionFalse && condition.Status != metav1.ConditionUnknown {
			return fmt.Errorf("%s[%d].status %q is not supported", path, index, condition.Status)
		}
		if _, ok := reasons[condition.Reason]; !ok {
			return fmt.Errorf("%s[%d].reason %q is not supported", path, index, condition.Reason)
		}
	}
	return nil
}

func validateObservedGeneration(observedGeneration, generation int64, path string) error {
	if observedGeneration < 0 {
		return fmt.Errorf("%s must be non-negative", path)
	}
	if observedGeneration > generation {
		return fmt.Errorf("%s must not be greater than metadata.generation", path)
	}
	return nil
}

func validateConditionObservedGenerations(conditions []metav1.Condition, generation int64, path string) error {
	for index, condition := range conditions {
		if err := validateObservedGeneration(condition.ObservedGeneration, generation, fmt.Sprintf("%s[%d].observedGeneration", path, index)); err != nil {
			return err
		}
	}
	return nil
}
