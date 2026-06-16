package webhook

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlwebhook "sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const route53IdentityWebhookPath = "/validate-route53-dns-appthrust-io-v1alpha1-route53identity"

var (
	route53RegionPattern  = regexp.MustCompile(`^[a-z]{2}(-[a-z0-9]+)+-[0-9]+$`)
	route53RoleARNPattern = regexp.MustCompile(`^arn:aws[a-zA-Z-]*:iam::[0-9]{12}:role/[A-Za-z0-9+=,.@_/-]+$`)
)

type Validator struct {
	decoder admission.Decoder
}

func NewValidator(scheme *runtime.Scheme) *Validator {
	return &Validator{decoder: admission.NewDecoder(scheme)}
}

func SetupValidationWebhookWithManager(mgr ctrl.Manager) error {
	validator := NewValidator(mgr.GetScheme())
	mgr.GetWebhookServer().Register(route53IdentityWebhookPath, &ctrlwebhook.Admission{Handler: validator})
	return nil
}

func (v *Validator) Handle(_ context.Context, req admission.Request) admission.Response {
	if req.Operation == admissionv1.Delete {
		return admission.Allowed("delete is not validated")
	}
	if req.Kind.Kind != "Route53Identity" {
		return admission.Denied(fmt.Sprintf("unsupported kind %q", req.Kind.Kind))
	}
	var identity route53v1alpha1.Route53Identity
	if err := v.decoder.Decode(req, &identity); err != nil {
		return admission.Denied(err.Error())
	}
	var err error
	if req.SubResource == "status" {
		err = validateRoute53IdentityStatus(&identity)
	} else {
		err = validateRoute53Identity(&identity)
	}
	if err != nil {
		return admission.Denied(err.Error())
	}
	return admission.Allowed("route53 validation passed")
}

func validateRoute53Identity(identity *route53v1alpha1.Route53Identity) error {
	if !route53RegionPattern.MatchString(identity.Spec.Region) {
		return fmt.Errorf("spec.region %q is not a valid AWS region name", identity.Spec.Region)
	}
	if identity.Spec.Credentials.Runtime == nil {
		return errors.New("spec.credentials.runtime is required")
	}
	for index, role := range identity.Spec.AssumeRoleChain {
		path := fmt.Sprintf("spec.assumeRoleChain[%d]", index)
		if strings.TrimSpace(role.RoleARN) == "" {
			return fmt.Errorf("%s.roleARN is required", path)
		}
		if !route53RoleARNPattern.MatchString(role.RoleARN) {
			return fmt.Errorf("%s.roleARN must be an IAM role ARN", path)
		}
	}
	return nil
}

func validateRoute53IdentityStatus(identity *route53v1alpha1.Route53Identity) error {
	if err := validateObservedGeneration(identity.Status.ObservedGeneration, identity.Generation, "status.observedGeneration"); err != nil {
		return err
	}
	if err := validateIdentityConditions(identity.Status.Conditions, "status.conditions", route53IdentityReasons()); err != nil {
		return err
	}
	return validateConditionObservedGenerations(identity.Status.Conditions, identity.Generation, "status.conditions")
}

func route53IdentityReasons() map[string]struct{} {
	return map[string]struct{}{
		"Accepted":                {},
		"AccessKeyInvalid":        {},
		"AccountMismatch":         {},
		"AssumeRoleFailed":        {},
		"CredentialUnavailable":   {},
		"GetCallerIdentityFailed": {},
		"InvalidRegion":           {},
		"Ready":                   {},
		"ReconcileError":          {},
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
