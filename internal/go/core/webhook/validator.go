package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"

	"github.com/appthrust/dns-api/internal/go/core/celruntime"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	"github.com/google/cel-go/cel"
	admissionv1 "k8s.io/api/admission/v1"
	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiservervalidation "k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	providerWebhookPath  = "/validate-dns-appthrust-io-v1alpha1-provider"
	zoneClassWebhookPath = "/validate-dns-appthrust-io-v1alpha1-zoneclass"
	zoneWebhookPath      = "/validate-dns-appthrust-io-v1alpha1-zone"
	recordSetWebhookPath = "/validate-dns-appthrust-io-v1alpha1-recordset"
	zoneUnitWebhookPath  = "/validate-dns-appthrust-io-v1alpha1-zoneunit"
)

var (
	domainNamePattern      = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)(\.([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?))*$`)
	recordNamePattern      = regexp.MustCompile(`^(@|\*|\*\.([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)(\.([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?))*|([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)(\.([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?))*)$`)
	txtRecordNamePattern   = regexp.MustCompile(`^(@|\*|\*\.([a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?)(\.([a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?))*|([a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?)(\.([a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?))*)$`)
	cnameRecordNamePattern = regexp.MustCompile(`^(\*|\*\.([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?|_[a-z0-9]([a-z0-9-]{0,60}[a-z0-9])?)(\.([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?|_[a-z0-9]([a-z0-9-]{0,60}[a-z0-9])?))*|([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?|_[a-z0-9]([a-z0-9-]{0,60}[a-z0-9])?)(\.([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?|_[a-z0-9]([a-z0-9-]{0,60}[a-z0-9])?))*)$`)
	cnameTargetPattern     = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?|_[a-z0-9]([a-z0-9-]{0,60}[a-z0-9])?)(\.([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?|_[a-z0-9]([a-z0-9-]{0,60}[a-z0-9])?))*$`)
	caaTagPattern          = regexp.MustCompile(`^[a-z0-9]+$`)
	conditionReasonPattern = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)
)

// CoreValidator implements the dns-api admission validation contract.
type CoreValidator struct {
	client.Client

	decoder admission.Decoder
}

func NewCoreValidator(k8sClient client.Client, scheme *runtime.Scheme) *CoreValidator {
	return &CoreValidator{
		Client:  k8sClient,
		decoder: admission.NewDecoder(scheme),
	}
}

func SetupCoreValidationWebhookWithManager(mgr ctrl.Manager) error {
	validator := NewCoreValidator(mgr.GetClient(), mgr.GetScheme())
	server := mgr.GetWebhookServer()
	server.Register(providerWebhookPath, &webhook.Admission{Handler: validator})
	server.Register(zoneClassWebhookPath, &webhook.Admission{Handler: validator})
	server.Register(zoneWebhookPath, &webhook.Admission{Handler: validator})
	server.Register(recordSetWebhookPath, &webhook.Admission{Handler: validator})
	server.Register(zoneUnitWebhookPath, &webhook.Admission{Handler: validator})
	return nil
}

func (v *CoreValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	if req.Operation == admissionv1.Delete {
		if req.Kind.Kind == "ZoneUnit" {
			var zoneUnit dnsv1alpha1.ZoneUnit
			if len(req.OldObject.Raw) == 0 {
				return admission.Denied("old ZoneUnit object is required for delete validation")
			}
			if err := json.Unmarshal(req.OldObject.Raw, &zoneUnit); err != nil {
				return admission.Denied(fmt.Sprintf("old ZoneUnit object could not be decoded: %v", err))
			}
			if err := v.validateZoneUnitDelete(ctx, &zoneUnit); err != nil {
				return admission.Denied(err.Error())
			}
		}
		return admission.Allowed("delete is not validated")
	}

	var err error
	switch req.Kind.Kind {
	case "Provider":
		var provider dnsv1alpha1.Provider
		err = v.decoder.Decode(req, &provider)
		if err == nil {
			err = validateProvider(&provider)
		}
	case "ZoneClass":
		var zoneClass dnsv1alpha1.ZoneClass
		err = v.decoder.Decode(req, &zoneClass)
		if err == nil && req.SubResource == "status" {
			err = validateZoneClassStatus(&zoneClass)
		} else if err == nil {
			err = v.validateZoneClass(ctx, &zoneClass)
		}
	case "Zone":
		var zone dnsv1alpha1.Zone
		err = v.decoder.Decode(req, &zone)
		if err == nil && req.SubResource == "status" {
			err = v.validateZoneStatus(ctx, &zone)
		} else if err == nil {
			var oldZone *dnsv1alpha1.Zone
			if req.Operation == admissionv1.Update && len(req.OldObject.Raw) > 0 {
				decoded := &dnsv1alpha1.Zone{}
				if decodeErr := json.Unmarshal(req.OldObject.Raw, decoded); decodeErr != nil {
					err = fmt.Errorf("old Zone object could not be decoded: %w", decodeErr)
				} else {
					oldZone = decoded
				}
			}
			if err == nil {
				err = v.validateZone(ctx, &zone, oldZone)
			}
		}
	case "RecordSet":
		var recordSet dnsv1alpha1.RecordSet
		if req.SubResource == "status" {
			err = validateRecordSetStatusRaw(req.Object.Raw)
			if err != nil {
				break
			}
		}
		err = v.decoder.Decode(req, &recordSet)
		if err == nil && req.SubResource == "status" {
			err = v.validateRecordSetStatus(ctx, &recordSet)
		} else if err == nil {
			err = v.validateRecordSet(ctx, &recordSet)
		}
	case "ZoneUnit":
		var zoneUnit dnsv1alpha1.ZoneUnit
		err = v.decoder.Decode(req, &zoneUnit)
		if err == nil && req.SubResource == "status" {
			err = v.validateZoneUnitStatus(ctx, &zoneUnit)
		} else if err == nil {
			var oldZoneUnit *dnsv1alpha1.ZoneUnit
			if req.Operation == admissionv1.Update && len(req.OldObject.Raw) > 0 {
				decoded := &dnsv1alpha1.ZoneUnit{}
				if decodeErr := json.Unmarshal(req.OldObject.Raw, decoded); decodeErr != nil {
					err = fmt.Errorf("old ZoneUnit object could not be decoded: %w", decodeErr)
				} else {
					oldZoneUnit = decoded
				}
			}
			if err == nil {
				err = v.validateZoneUnit(ctx, &zoneUnit, oldZoneUnit)
			}
		}
	default:
		err = fmt.Errorf("unsupported kind %q", req.Kind.Kind)
	}

	if err != nil {
		return admission.Denied(err.Error())
	}
	return admission.Allowed("dns-api validation passed")
}

func validateProvider(provider *dnsv1alpha1.Provider) error {
	knownRecordTypes := standardRecordTypes()
	if strings.TrimSpace(provider.Spec.Display.Name) == "" {
		return errors.New("spec.display.name is required")
	}
	if len(provider.Spec.Versions) == 0 {
		return errors.New("spec.versions must not be empty")
	}
	seenVersions := map[string]struct{}{}
	storageVersions := 0
	for versionIndex, version := range provider.Spec.Versions {
		if strings.TrimSpace(version.Name) == "" {
			return fmt.Errorf("spec.versions[%d].name is required", versionIndex)
		}
		if version.Storage {
			storageVersions++
		}
		if strings.Contains(version.Name, "/") {
			return fmt.Errorf("spec.versions[%d].name must not contain /", versionIndex)
		}
		if _, ok := seenVersions[version.Name]; ok {
			return fmt.Errorf("spec.versions[%d].name duplicates %q", versionIndex, version.Name)
		}
		seenVersions[version.Name] = struct{}{}
		prefix := fmt.Sprintf("spec.versions[%d]", versionIndex)
		if version.Identity != nil {
			if err := validateProviderIdentityResource(version.Identity.Resource, prefix+".identity.resource"); err != nil {
				return err
			}
		}
		if err := validateExtensionOpenAPISchema(version.ZoneClass.Schemas.Parameters, prefix+".zoneClass.schemas.parameters.openAPIV3Schema"); err != nil {
			return err
		}
		if err := validateObjectCELBoolRules(version.ZoneClass.ValidationRules, prefix+".zoneClass.validationRules"); err != nil {
			return err
		}
		if err := validateProviderConversionTarget(version.ZoneClass.Conversion.ToStorage, version.Served && !version.Storage, prefix+".zoneClass.conversion.toStorage"); err != nil {
			return err
		}
		if err := validateExtensionOpenAPISchema(version.Zone.Schemas.Adoption, prefix+".zone.schemas.adoption.openAPIV3Schema"); err != nil {
			return err
		}
		if err := validateExtensionOpenAPISchema(version.Zone.Schemas.StatusProviderData, prefix+".zone.schemas.statusProviderData.openAPIV3Schema"); err != nil {
			return err
		}
		if err := validateProviderConditionReasons(version.Zone.AdditionalConditionReasons, prefix+".zone.additionalConditionReasons", zoneCoreConditionReasons()); err != nil {
			return err
		}
		if err := validateObjectCELBoolRules(version.Zone.ValidationRules, prefix+".zone.validationRules"); err != nil {
			return err
		}
		if err := validateProviderConversionTarget(version.Zone.Conversion.ToStorage, version.Served && !version.Storage, prefix+".zone.conversion.toStorage"); err != nil {
			return err
		}
		if len(version.RecordSet.SupportedTypes) == 0 {
			return fmt.Errorf("%s.recordSet.supportedTypes must not be empty", prefix)
		}
		seenRecordTypes := map[dnsv1alpha1.RecordType]struct{}{}
		for index, recordType := range version.RecordSet.SupportedTypes {
			if _, ok := knownRecordTypes[recordType]; !ok {
				return fmt.Errorf("%s.recordSet.supportedTypes[%d] is not a known DNS record type", prefix, index)
			}
			if _, ok := seenRecordTypes[recordType]; ok {
				return fmt.Errorf("%s.recordSet.supportedTypes[%d] duplicates %q", prefix, index, recordType)
			}
			seenRecordTypes[recordType] = struct{}{}
		}
		if err := validateExtensionOpenAPISchema(version.RecordSet.Schemas.Options, prefix+".recordSet.schemas.options.openAPIV3Schema"); err != nil {
			return err
		}
		if err := validateExtensionOpenAPISchema(version.RecordSet.Schemas.Adoption, prefix+".recordSet.schemas.adoption.openAPIV3Schema"); err != nil {
			return err
		}
		if err := validateExtensionOpenAPISchema(version.RecordSet.Schemas.StatusProviderData, prefix+".recordSet.schemas.statusProviderData.openAPIV3Schema"); err != nil {
			return err
		}
		if err := validateProviderConditionReasons(version.RecordSet.AdditionalConditionReasons, prefix+".recordSet.additionalConditionReasons", recordSetCoreConditionReasons()); err != nil {
			return err
		}
		for index, toggle := range version.RecordSet.DisableValidations {
			path := fmt.Sprintf("%s.recordSet.disableValidations[%d].when", prefix, index)
			if err := compileObjectCELBoolRule(toggle.When, path); err != nil {
				return err
			}
		}
		if err := validateObjectCELBoolRules(version.RecordSet.ValidationRules, prefix+".recordSet.validationRules"); err != nil {
			return err
		}
		if err := validateProviderConversionTarget(version.RecordSet.Conversion.ToStorage, version.Served && !version.Storage, prefix+".recordSet.conversion.toStorage"); err != nil {
			return err
		}
		for index, toggle := range version.ZoneUnit.DisableValidations {
			path := fmt.Sprintf("%s.zoneUnit.disableValidations[%d].when", prefix, index)
			if err := compileZoneUnitCELBoolRule(toggle.When, path); err != nil {
				return err
			}
		}
		if err := validateZoneUnitCELBoolRules(version.ZoneUnit.ValidationRules, prefix+".zoneUnit.validationRules"); err != nil {
			return err
		}
	}
	if storageVersions != 1 {
		return fmt.Errorf("spec.versions must contain exactly one storage version, got %d", storageVersions)
	}
	return nil
}

func validateProviderConversionTarget(target dnsv1alpha1.ProviderConversionTarget, required bool, path string) error {
	hasCEL := strings.TrimSpace(target.CEL) != ""
	hasWebhook := target.Webhook != nil
	switch {
	case hasCEL && hasWebhook:
		return fmt.Errorf("%s must not combine cel and webhook", path)
	case required && !hasCEL && !hasWebhook:
		return fmt.Errorf("%s must define exactly one conversion strategy for a non-storage served version", path)
	}
	if hasCEL {
		if err := compileCELObjectRule(target.CEL, path+".cel"); err != nil {
			return err
		}
	}
	if hasWebhook {
		if err := validateProviderConversionWebhook(target.Webhook, path+".webhook"); err != nil {
			return err
		}
	}
	return nil
}

func validateProviderConversionWebhook(webhook *dnsv1alpha1.ProviderConversionWebhook, path string) error {
	if webhook == nil {
		return nil
	}
	service := webhook.ClientConfig.Service
	if strings.TrimSpace(service.Namespace) == "" {
		return fmt.Errorf("%s.clientConfig.service.namespace is required", path)
	}
	if strings.TrimSpace(service.Name) == "" {
		return fmt.Errorf("%s.clientConfig.service.name is required", path)
	}
	if service.Path != "" && !strings.HasPrefix(service.Path, "/") {
		return fmt.Errorf("%s.clientConfig.service.path must start with /", path)
	}
	if service.Port != nil && (*service.Port < 1 || *service.Port > 65535) {
		return fmt.Errorf("%s.clientConfig.service.port must be in range 1..65535", path)
	}
	if len(webhook.ClientConfig.CABundle) == 0 {
		return fmt.Errorf("%s.clientConfig.caBundle is required", path)
	}
	if webhook.TimeoutSeconds != nil && (*webhook.TimeoutSeconds < 1 || *webhook.TimeoutSeconds > 30) {
		return fmt.Errorf("%s.timeoutSeconds must be in range 1..30", path)
	}
	return nil
}

func validateProviderIdentityResource(resource dnsv1alpha1.ProviderIdentityResource, path string) error {
	if strings.TrimSpace(resource.Group) == "" {
		return fmt.Errorf("%s.group is required", path)
	}
	if strings.TrimSpace(resource.Kind) == "" {
		return fmt.Errorf("%s.kind is required", path)
	}
	if resource.Scope != "Namespaced" {
		return fmt.Errorf("%s.scope must be Namespaced", path)
	}
	return nil
}

func (v *CoreValidator) validateZoneClass(ctx context.Context, zoneClass *dnsv1alpha1.ZoneClass) error {
	_, providerVersion, err := v.resolveProviderVersion(ctx, zoneClass.Spec.Provider, true)
	if err != nil {
		return err
	}
	if strings.TrimSpace(zoneClass.Spec.IdentityRef.Name) == "" {
		return errors.New("spec.identityRef.name is required")
	}
	if providerVersion.Identity != nil {
		if err := validateProviderIdentityResource(providerVersion.Identity.Resource, "Provider.identity.resource"); err != nil {
			return err
		}
	}
	if err := validateNamespacePolicy(zoneClass.Spec.AllowedZones.Namespaces, "spec.allowedZones.namespaces"); err != nil {
		return err
	}
	if err := validateRawExtensionBySchema(zoneClass.Spec.Parameters, providerVersion.ZoneClass.Schemas.Parameters, "spec.parameters"); err != nil {
		return err
	}
	return validateExtensionCELRules(zoneClass, providerVersion.ZoneClass.ValidationRules, "Provider.zoneClass")
}

func validateZoneClassStatus(zoneClass *dnsv1alpha1.ZoneClass) error {
	if err := validateStatusConditions(zoneClass.Status.Conditions, "status.conditions", zoneClassConditionReasons()); err != nil {
		return err
	}
	return validateConditionObservedGenerations(zoneClass.Status.Conditions, zoneClass.Generation, "status.conditions")
}

func (v *CoreValidator) validateZone(ctx context.Context, zone *dnsv1alpha1.Zone, oldZone *dnsv1alpha1.Zone) error {
	if err := validateDomainName(zone.Spec.DomainName); err != nil {
		return fmt.Errorf("spec.domainName: %w", err)
	}

	_, providerVersion, err := v.resolveProviderVersion(ctx, zone.Spec.Provider, true)
	if err != nil {
		return err
	}

	if len(rawExtensionBytes(zone.Spec.Adoption)) > 0 {
		if err := validateRawExtensionBySchema(zone.Spec.Adoption, providerVersion.Zone.Schemas.Adoption, "spec.adoption"); err != nil {
			return err
		}
	}

	if err := validateAllowedRecordSets(zone.Spec.AllowedRecordSets); err != nil {
		return err
	}
	if err := v.validateAllowedRecordSetTypes(ctx, zone.Spec.AllowedRecordSets); err != nil {
		return err
	}
	return validateExtensionCELRules(zone, providerVersion.Zone.ValidationRules, "Provider.zone")
}

func (v *CoreValidator) validateZoneStatus(ctx context.Context, zone *dnsv1alpha1.Zone) error {
	_, providerVersion, err := v.resolveProviderVersion(ctx, zone.Spec.Provider, false)
	if err != nil {
		return err
	}
	if err := validateObservedGeneration(zone.Status.ObservedGeneration, zone.Generation, "status.observedGeneration"); err != nil {
		return err
	}
	if err := validateZoneStatusNameServers(zone.Status.NameServers); err != nil {
		return err
	}
	if err := validateStatusConditions(zone.Status.Conditions, "status.conditions", allowedConditionReasons(zoneCoreConditionReasons(), providerVersion.Zone.AdditionalConditionReasons)); err != nil {
		return err
	}
	if zone.Status.Provider != nil && len(rawExtensionBytes(zone.Status.Provider.Data)) > 0 {
		if err := validateRawExtensionBySchema(zone.Status.Provider.Data, providerVersion.Zone.Schemas.StatusProviderData, "status.provider.data"); err != nil {
			return err
		}
	}
	return nil
}

func (v *CoreValidator) validateRecordSet(ctx context.Context, recordSet *dnsv1alpha1.RecordSet) error {
	_, providerVersion, err := v.resolveProviderVersion(ctx, recordSet.Spec.Provider, true)
	if err != nil {
		return err
	}
	if !slices.Contains(providerVersion.RecordSet.SupportedTypes, recordSet.Spec.Type) {
		return fmt.Errorf("spec.type %q is not supported by referenced Provider", recordSet.Spec.Type)
	}
	if len(rawExtensionBytes(recordSet.Spec.Options)) > 0 {
		if err := validateRawExtensionBySchema(recordSet.Spec.Options, providerVersion.RecordSet.Schemas.Options, "spec.options"); err != nil {
			return err
		}
	}
	if len(rawExtensionBytes(recordSet.Spec.Adoption)) > 0 {
		if err := validateRawExtensionBySchema(recordSet.Spec.Adoption, providerVersion.RecordSet.Schemas.Adoption, "spec.adoption"); err != nil {
			return err
		}
	}

	disabled, err := disabledRecordSetValidations(recordSet, providerVersion)
	if err != nil {
		return err
	}
	if err := validateRecordNameIntrinsic(recordSet.Spec.Type, recordSet.Spec.Name, disabled); err != nil {
		return err
	}

	zone, zoneClass, err := v.zoneClassForRecordSetIfPresent(ctx, recordSet)
	if err != nil {
		return err
	}
	if zone != nil && zoneClass != nil {
		if err := validateRecordNameLength(recordSet.Spec.Name, zone.Spec.DomainName); err != nil {
			return err
		}
	}

	if !disabled["require-ttl"] && recordSet.Spec.TTL == nil {
		return errors.New("spec.ttl is required")
	}
	if recordSet.Spec.Type == dnsv1alpha1.RecordTypeA && !disabled["require-a-addresses"] {
		if recordSet.Spec.A == nil || len(recordSet.Spec.A.Addresses) == 0 {
			return errors.New("spec.a.addresses is required for A records")
		}
		if err := validateIPAddresses(recordSet.Spec.A.Addresses, "spec.a.addresses", false); err != nil {
			return err
		}
		if recordSet.Spec.AAAA != nil {
			return errors.New("spec.aaaa must not be specified for A records")
		}
		if recordSet.Spec.TXT != nil {
			return errors.New("spec.txt must not be specified for A records")
		}
		if recordSet.Spec.CNAME != nil {
			return errors.New("spec.cname must not be specified for A records")
		}
		if recordSet.Spec.MX != nil {
			return errors.New("spec.mx must not be specified for A records")
		}
		if recordSet.Spec.CAA != nil {
			return errors.New("spec.caa must not be specified for A records")
		}
		if recordSet.Spec.NS != nil {
			return errors.New("spec.ns must not be specified for A records")
		}
	}
	if recordSet.Spec.Type == dnsv1alpha1.RecordTypeAAAA && !disabled["require-aaaa-addresses"] {
		if recordSet.Spec.AAAA == nil || len(recordSet.Spec.AAAA.Addresses) == 0 {
			return errors.New("spec.aaaa.addresses is required for AAAA records")
		}
		if err := validateIPAddresses(recordSet.Spec.AAAA.Addresses, "spec.aaaa.addresses", true); err != nil {
			return err
		}
		if recordSet.Spec.A != nil {
			return errors.New("spec.a must not be specified for AAAA records")
		}
		if recordSet.Spec.TXT != nil {
			return errors.New("spec.txt must not be specified for AAAA records")
		}
		if recordSet.Spec.CNAME != nil {
			return errors.New("spec.cname must not be specified for AAAA records")
		}
		if recordSet.Spec.MX != nil {
			return errors.New("spec.mx must not be specified for AAAA records")
		}
		if recordSet.Spec.CAA != nil {
			return errors.New("spec.caa must not be specified for AAAA records")
		}
		if recordSet.Spec.NS != nil {
			return errors.New("spec.ns must not be specified for AAAA records")
		}
	}
	if recordSet.Spec.Type == dnsv1alpha1.RecordTypeTXT && !disabled["require-txt-values"] {
		if recordSet.Spec.TXT == nil || len(recordSet.Spec.TXT.Values) == 0 {
			return errors.New("spec.txt.values is required for TXT records")
		}
		if err := validateTXTValues(recordSet.Spec.TXT.Values, !disabled["forbid-txt-non-printable-ascii"], "spec.txt.values"); err != nil {
			return err
		}
		if recordSet.Spec.A != nil {
			return errors.New("spec.a must not be specified for TXT records")
		}
		if recordSet.Spec.AAAA != nil {
			return errors.New("spec.aaaa must not be specified for TXT records")
		}
		if recordSet.Spec.CNAME != nil {
			return errors.New("spec.cname must not be specified for TXT records")
		}
		if recordSet.Spec.MX != nil {
			return errors.New("spec.mx must not be specified for TXT records")
		}
		if recordSet.Spec.CAA != nil {
			return errors.New("spec.caa must not be specified for TXT records")
		}
		if recordSet.Spec.NS != nil {
			return errors.New("spec.ns must not be specified for TXT records")
		}
	}
	if recordSet.Spec.Type == dnsv1alpha1.RecordTypeCNAME {
		if recordSet.Spec.CNAME == nil || recordSet.Spec.CNAME.Target == "" {
			return errors.New("spec.cname.target is required for CNAME records")
		}
		if err := validateCNAMETarget(recordSet.Spec.CNAME.Target); err != nil {
			return fmt.Errorf("spec.cname.target: %w", err)
		}
		if recordSet.Spec.A != nil {
			return errors.New("spec.a must not be specified for CNAME records")
		}
		if recordSet.Spec.AAAA != nil {
			return errors.New("spec.aaaa must not be specified for CNAME records")
		}
		if recordSet.Spec.TXT != nil {
			return errors.New("spec.txt must not be specified for CNAME records")
		}
		if recordSet.Spec.MX != nil {
			return errors.New("spec.mx must not be specified for CNAME records")
		}
		if recordSet.Spec.CAA != nil {
			return errors.New("spec.caa must not be specified for CNAME records")
		}
		if recordSet.Spec.NS != nil {
			return errors.New("spec.ns must not be specified for CNAME records")
		}
	}
	if recordSet.Spec.Type == dnsv1alpha1.RecordTypeMX {
		if recordSet.Spec.MX == nil || len(recordSet.Spec.MX.Records) == 0 {
			return errors.New("spec.mx.records is required for MX records")
		}
		if err := validateMXRecords(recordSet.Spec.MX.Records, "spec.mx.records"); err != nil {
			return err
		}
		if recordSet.Spec.A != nil {
			return errors.New("spec.a must not be specified for MX records")
		}
		if recordSet.Spec.AAAA != nil {
			return errors.New("spec.aaaa must not be specified for MX records")
		}
		if recordSet.Spec.TXT != nil {
			return errors.New("spec.txt must not be specified for MX records")
		}
		if recordSet.Spec.CNAME != nil {
			return errors.New("spec.cname must not be specified for MX records")
		}
		if recordSet.Spec.CAA != nil {
			return errors.New("spec.caa must not be specified for MX records")
		}
		if recordSet.Spec.NS != nil {
			return errors.New("spec.ns must not be specified for MX records")
		}
	}
	if recordSet.Spec.Type == dnsv1alpha1.RecordTypeCAA {
		if recordSet.Spec.CAA == nil || len(recordSet.Spec.CAA.Records) == 0 {
			return errors.New("spec.caa.records is required for CAA records")
		}
		if err := validateCAARecords(recordSet.Spec.CAA.Records, "spec.caa.records"); err != nil {
			return err
		}
		if recordSet.Spec.A != nil {
			return errors.New("spec.a must not be specified for CAA records")
		}
		if recordSet.Spec.AAAA != nil {
			return errors.New("spec.aaaa must not be specified for CAA records")
		}
		if recordSet.Spec.TXT != nil {
			return errors.New("spec.txt must not be specified for CAA records")
		}
		if recordSet.Spec.CNAME != nil {
			return errors.New("spec.cname must not be specified for CAA records")
		}
		if recordSet.Spec.MX != nil {
			return errors.New("spec.mx must not be specified for CAA records")
		}
		if recordSet.Spec.NS != nil {
			return errors.New("spec.ns must not be specified for CAA records")
		}
	}
	if recordSet.Spec.Type == dnsv1alpha1.RecordTypeNS {
		if recordSet.Spec.Name == "@" {
			return errors.New("spec.name @ is not allowed for delegated NS records")
		}
		if strings.HasPrefix(recordSet.Spec.Name, "*") {
			return errors.New("spec.name wildcard is not allowed for delegated NS records")
		}
		if recordSet.Spec.NS == nil || len(recordSet.Spec.NS.NameServers) == 0 {
			return errors.New("spec.ns.nameServers is required for NS records")
		}
		if err := validateNSNameServers(recordSet.Spec.NS.NameServers, "spec.ns.nameServers"); err != nil {
			return err
		}
		if recordSet.Spec.A != nil {
			return errors.New("spec.a must not be specified for NS records")
		}
		if recordSet.Spec.AAAA != nil {
			return errors.New("spec.aaaa must not be specified for NS records")
		}
		if recordSet.Spec.TXT != nil {
			return errors.New("spec.txt must not be specified for NS records")
		}
		if recordSet.Spec.CNAME != nil {
			return errors.New("spec.cname must not be specified for NS records")
		}
		if recordSet.Spec.MX != nil {
			return errors.New("spec.mx must not be specified for NS records")
		}
		if recordSet.Spec.CAA != nil {
			return errors.New("spec.caa must not be specified for NS records")
		}
	}

	return validateExtensionCELRules(recordSet, providerVersion.RecordSet.ValidationRules, "Provider.recordSet")
}

func (v *CoreValidator) validateRecordSetStatus(ctx context.Context, recordSet *dnsv1alpha1.RecordSet) error {
	_, providerVersion, err := v.resolveProviderVersion(ctx, recordSet.Spec.Provider, false)
	if err != nil {
		return err
	}
	if err := validateObservedGeneration(recordSet.Status.ObservedGeneration, recordSet.Generation, "status.observedGeneration"); err != nil {
		return err
	}
	if recordSet.Status.Zone != nil {
		expectedNamespace := recordSet.Namespace
		if recordSet.Spec.ZoneRef.Namespace != nil {
			expectedNamespace = *recordSet.Spec.ZoneRef.Namespace
		}
		if recordSet.Status.Zone.Ref.Namespace != expectedNamespace {
			return fmt.Errorf("status.zone.ref.namespace must equal spec.zoneRef.namespace after defaulting")
		}
		if recordSet.Status.Zone.Ref.Name != recordSet.Spec.ZoneRef.Name {
			return fmt.Errorf("status.zone.ref.name must equal spec.zoneRef.name")
		}
	}
	if err := validateStatusConditions(recordSet.Status.Conditions, "status.conditions", allowedConditionReasons(recordSetCoreConditionReasons(), providerVersion.RecordSet.AdditionalConditionReasons)); err != nil {
		return err
	}
	if recordSet.Status.Provider != nil && len(rawExtensionBytes(recordSet.Status.Provider.Data)) > 0 {
		if err := validateRawExtensionBySchema(recordSet.Status.Provider.Data, providerVersion.RecordSet.Schemas.StatusProviderData, "status.provider.data"); err != nil {
			return err
		}
	}
	return nil
}

func validateRecordSetStatusRaw(raw []byte) error {
	if len(raw) == 0 {
		return nil
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return fmt.Errorf("RecordSet status object could not be decoded: %w", err)
	}
	status, ok := object["status"].(map[string]any)
	if !ok {
		return nil
	}
	zone, ok := status["zone"].(map[string]any)
	if !ok {
		return nil
	}
	if _, ok := zone["controllerName"]; ok {
		return errors.New("status.zone.controllerName is not accepted")
	}
	ref, ok := zone["ref"].(map[string]any)
	if !ok {
		return nil
	}
	for key := range ref {
		if key != "namespace" && key != "name" {
			return fmt.Errorf("status.zone.ref.%s is not accepted", key)
		}
	}
	return nil
}

func (v *CoreValidator) validateZoneUnit(ctx context.Context, zoneUnit *dnsv1alpha1.ZoneUnit, oldZoneUnit *dnsv1alpha1.ZoneUnit) error {
	if _, _, err := v.resolveProviderVersion(ctx, zoneUnit.Spec.Provider, false); err != nil {
		return err
	}
	if zoneUnit.Spec.Zone.Ref.Namespace == "" || zoneUnit.Spec.Zone.Ref.Name == "" {
		return errors.New("spec.zone.ref must include namespace and name")
	}
	if zoneUnit.Namespace != zoneUnit.Spec.Zone.Ref.Namespace {
		return errors.New("metadata.namespace must match spec.zone.ref.namespace")
	}
	if zoneUnit.Name != zoneUnit.Spec.Zone.Ref.Name {
		return errors.New("metadata.name must match spec.zone.ref.name")
	}
	if zoneUnit.Spec.Zone.ZoneClassRef.Namespace == "" || zoneUnit.Spec.Zone.ZoneClassRef.Name == "" {
		return errors.New("spec.zone.zoneClassRef must include namespace and name")
	}
	if err := validateDomainName(zoneUnit.Spec.Zone.DomainName); err != nil {
		return fmt.Errorf("spec.zone.domainName: %w", err)
	}
	oldRecordSets := map[string]dnsv1alpha1.ZoneUnitRecordSetSpec{}
	if oldZoneUnit != nil {
		for _, item := range oldZoneUnit.Spec.RecordSets {
			oldRecordSets[recordSetClaimKey(item.RecordSetNamespace, item.RecordSetName)] = item
		}
	}
	seen := map[string]struct{}{}
	for index := range zoneUnit.Spec.RecordSets {
		item := &zoneUnit.Spec.RecordSets[index]
		if item.RecordSetNamespace == "" || item.RecordSetName == "" {
			return fmt.Errorf("spec.recordSets[%d] must include recordSetNamespace and recordSetName", index)
		}
		key := recordSetClaimKey(item.RecordSetNamespace, item.RecordSetName)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("spec.recordSets[%d] duplicates another item by recordSetNamespace and recordSetName", index)
		}
		seen[key] = struct{}{}
		if err := validateRecordNameIntrinsic(item.Type, item.Name, nil); err != nil {
			return fmt.Errorf("spec.recordSets[%d].name: %w", index, err)
		}
		if oldItem, ok := oldRecordSets[key]; ok {
			if item.Name != oldItem.Name {
				return fmt.Errorf("spec.recordSets[%d].name is immutable for RecordSet %s/%s", index, item.RecordSetNamespace, item.RecordSetName)
			}
			if item.Type != oldItem.Type {
				return fmt.Errorf("spec.recordSets[%d].type is immutable for RecordSet %s/%s", index, item.RecordSetNamespace, item.RecordSetName)
			}
		}
	}
	return nil
}

func (v *CoreValidator) validateZoneUnitDelete(ctx context.Context, zoneUnit *dnsv1alpha1.ZoneUnit) error {
	var zone dnsv1alpha1.Zone
	err := v.Client.Get(ctx, client.ObjectKey{Namespace: zoneUnit.Spec.Zone.Ref.Namespace, Name: zoneUnit.Spec.Zone.Ref.Name}, &zone)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !zone.DeletionTimestamp.IsZero() {
		return nil
	}
	return errors.New("ZoneUnit cannot be deleted while the owning Zone exists and is not deleting")
}

func (v *CoreValidator) validateZoneUnitStatus(ctx context.Context, zoneUnit *dnsv1alpha1.ZoneUnit) error {
	_, providerVersion, err := v.resolveProviderVersion(ctx, zoneUnit.Spec.Provider, false)
	if err != nil {
		return err
	}
	if err := validateStatusConditions(zoneUnit.Status.Conditions, "status.conditions", conditionReasonSet{
		dnsv1alpha1.ConditionProgrammed: {
			metav1.ConditionTrue: {
				"Programmed": {},
			},
			metav1.ConditionFalse: {
				"ProviderChangePending":    {},
				"ProviderChangeDeferred":   {},
				"ProviderIdentityNotReady": {},
				"ExternalResourceNotFound": {},
				"ExternalResourceMismatch": {},
				"ProviderConflict":         {},
				"ProviderAccessDenied":     {},
				"ProviderInvalidRequest":   {},
				"ProviderUnavailable":      {},
				"ReconcileError":           {},
			},
			metav1.ConditionUnknown: {
				"Reconciling":            {},
				"ProviderChangePending":  {},
				"ProviderChangeDeferred": {},
				"ProviderUnavailable":    {},
				"ReconcileError":         {},
			},
		},
	}); err != nil {
		return err
	}
	if zoneUnit.Status.Zone != nil {
		if err := validateZoneStatusNameServers(zoneUnit.Status.Zone.NameServers); err != nil {
			return err
		}
		if err := validateStatusConditions(zoneUnit.Status.Zone.Conditions, "status.zone.conditions", allowedConditionReasons(zoneCoreConditionReasons(), providerVersion.Zone.AdditionalConditionReasons)); err != nil {
			return err
		}
		if zoneUnit.Status.Zone.Provider != nil && len(rawExtensionBytes(zoneUnit.Status.Zone.Provider.Data)) > 0 {
			if err := validateRawExtensionBySchema(zoneUnit.Status.Zone.Provider.Data, providerVersion.Zone.Schemas.StatusProviderData, "status.zone.provider.data"); err != nil {
				return err
			}
		}
	}
	for index := range zoneUnit.Status.RecordSets {
		item := &zoneUnit.Status.RecordSets[index]
		if item.RecordSetNamespace == "" || item.RecordSetName == "" {
			return fmt.Errorf("status.recordSets[%d] must include recordSetNamespace and recordSetName", index)
		}
		if err := validateStatusConditions(item.Conditions, fmt.Sprintf("status.recordSets[%d].conditions", index), allowedConditionReasons(recordSetCoreConditionReasons(), providerVersion.RecordSet.AdditionalConditionReasons)); err != nil {
			return err
		}
		if item.Provider != nil && len(rawExtensionBytes(item.Provider.Data)) > 0 {
			if err := validateRawExtensionBySchema(item.Provider.Data, providerVersion.RecordSet.Schemas.StatusProviderData, fmt.Sprintf("status.recordSets[%d].provider.data", index)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (v *CoreValidator) zoneForRecordSet(ctx context.Context, recordSet *dnsv1alpha1.RecordSet) (*dnsv1alpha1.Zone, error) {
	namespace := recordSet.Namespace
	if recordSet.Spec.ZoneRef.Namespace != nil && *recordSet.Spec.ZoneRef.Namespace != "" {
		namespace = *recordSet.Spec.ZoneRef.Namespace
	}
	var zone dnsv1alpha1.Zone
	if err := v.Get(ctx, client.ObjectKey{Namespace: namespace, Name: recordSet.Spec.ZoneRef.Name}, &zone); err != nil {
		return nil, fmt.Errorf("spec.zoneRef is invalid: %w", err)
	}
	return &zone, nil
}

func (v *CoreValidator) zoneClassForZone(ctx context.Context, zone *dnsv1alpha1.Zone) (*dnsv1alpha1.ZoneClass, error) {
	namespace := zone.Namespace
	if zone.Spec.ZoneClassRef.Namespace != nil && *zone.Spec.ZoneClassRef.Namespace != "" {
		namespace = *zone.Spec.ZoneClassRef.Namespace
	}
	var zoneClass dnsv1alpha1.ZoneClass
	if err := v.Get(ctx, client.ObjectKey{Namespace: namespace, Name: zone.Spec.ZoneClassRef.Name}, &zoneClass); err != nil {
		return nil, fmt.Errorf("spec.zoneClassRef is invalid: %w", err)
	}
	return &zoneClass, nil
}

func (v *CoreValidator) zoneClassForZoneIfPresent(ctx context.Context, zone *dnsv1alpha1.Zone) (*dnsv1alpha1.ZoneClass, error) {
	zoneClass, err := v.zoneClassForZone(ctx, zone)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return zoneClass, nil
}

func (v *CoreValidator) zoneClassForRecordSetIfPresent(ctx context.Context, recordSet *dnsv1alpha1.RecordSet) (*dnsv1alpha1.Zone, *dnsv1alpha1.ZoneClass, error) {
	namespace := recordSet.Namespace
	if recordSet.Spec.ZoneRef.Namespace != nil && *recordSet.Spec.ZoneRef.Namespace != "" {
		namespace = *recordSet.Spec.ZoneRef.Namespace
	}
	var zone dnsv1alpha1.Zone
	if err := v.Get(ctx, client.ObjectKey{Namespace: namespace, Name: recordSet.Spec.ZoneRef.Name}, &zone); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("spec.zoneRef is invalid: %w", err)
	}
	zoneClass, err := v.zoneClassForZone(ctx, &zone)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return &zone, nil, nil
		}
		return nil, nil, err
	}
	return &zone, zoneClass, nil
}

func (v *CoreValidator) resolveProviderVersion(ctx context.Context, ref dnsv1alpha1.ProviderReference, requireServed bool) (*dnsv1alpha1.Provider, *dnsv1alpha1.ProviderVersion, error) {
	if ref.Name == "" || ref.Version == "" {
		return nil, nil, errors.New("spec.provider must include name and version")
	}
	var provider dnsv1alpha1.Provider
	if err := v.Get(ctx, client.ObjectKey{Name: ref.Name}, &provider); err != nil {
		return nil, nil, fmt.Errorf("spec.provider is invalid: %w", err)
	}
	for index := range provider.Spec.Versions {
		version := &provider.Spec.Versions[index]
		if version.Name != ref.Version {
			continue
		}
		if requireServed && !version.Served {
			return nil, nil, fmt.Errorf("spec.provider references unserved Provider version %q", ref.Version)
		}
		return &provider, version, nil
	}
	return nil, nil, fmt.Errorf("spec.provider references unknown Provider version %q", ref.Version)
}

func validateDomainName(domainName string) error {
	if domainName == "" {
		return errors.New("must not be empty")
	}
	if len(domainName) > 253 {
		return errors.New("must be 253 octets or fewer")
	}
	if !domainNamePattern.MatchString(domainName) {
		return errors.New("must be normalized lowercase ASCII without a trailing root dot")
	}
	return nil
}

func validateRecordNameIntrinsic(recordType dnsv1alpha1.RecordType, name string, disabled map[string]bool) error {
	if name == "" {
		return errors.New("spec.name must not be empty")
	}
	pattern := recordNamePattern
	if recordType == dnsv1alpha1.RecordTypeTXT {
		pattern = txtRecordNamePattern
	}
	if recordType == dnsv1alpha1.RecordTypeCNAME {
		if name == "@" {
			if disabled["forbid-cname-apex"] {
				return nil
			}
			return errors.New("spec.name @ is not allowed for CNAME records")
		}
		pattern = cnameRecordNamePattern
	}
	if recordType == dnsv1alpha1.RecordTypeNS {
		if name == "@" {
			return errors.New("spec.name @ is not allowed for delegated NS records")
		}
		if strings.HasPrefix(name, "*") {
			return errors.New("spec.name wildcard is not allowed for delegated NS records")
		}
	}
	if !pattern.MatchString(name) {
		return fmt.Errorf("spec.name %q is invalid for %s records", name, recordType)
	}
	return nil
}

func validateRecordNameLength(name, domainName string) error {
	if len(canonicalRecordName(name, domainName)) > 253 {
		return errors.New("spec.name and Zone spec.domainName produce a FQDN longer than 253 octets")
	}
	return nil
}

func validateCNAMETarget(target string) error {
	if target == "" {
		return errors.New("must not be empty")
	}
	if len(target) > 253 {
		return errors.New("must be 253 octets or fewer")
	}
	if _, err := netip.ParseAddr(target); err == nil {
		return errors.New("must be a DNS name, not an IP address")
	}
	if !cnameTargetPattern.MatchString(target) {
		return errors.New("must be normalized lowercase ASCII without a trailing root dot")
	}
	return nil
}

func validateMXRecords(records []dnsv1alpha1.MXRecord, path string) error {
	if len(records) == 0 {
		return fmt.Errorf("%s is required", path)
	}
	seen := make(map[string]struct{}, len(records))
	nullMXIndex := -1
	for index, record := range records {
		itemPath := fmt.Sprintf("%s[%d]", path, index)
		if record.Preference < 0 || record.Preference > 65535 {
			return fmt.Errorf("%s.preference must be in range 0..65535", itemPath)
		}
		if record.Exchange == "" {
			return fmt.Errorf("%s.exchange is required", itemPath)
		}
		if record.Exchange == "." {
			nullMXIndex = index
		} else if err := validateMXExchange(record.Exchange); err != nil {
			return fmt.Errorf("%s.exchange: %w", itemPath, err)
		}
		key := fmt.Sprintf("%d\x00%s", record.Preference, record.Exchange)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("%s must not contain duplicate preference and exchange pairs", path)
		}
		seen[key] = struct{}{}
	}
	if nullMXIndex >= 0 {
		if len(records) != 1 {
			return fmt.Errorf("%s must contain only one record when exchange is \".\"", path)
		}
		if records[nullMXIndex].Preference != 0 {
			return fmt.Errorf("%s[%d].preference must be 0 when exchange is \".\"", path, nullMXIndex)
		}
	}
	return nil
}

func validateMXExchange(exchange string) error {
	if exchange == "" {
		return errors.New("must not be empty")
	}
	if len(exchange) > 253 {
		return errors.New("must be 253 octets or fewer")
	}
	if _, err := netip.ParseAddr(exchange); err == nil {
		return errors.New("must be a DNS name, not an IP address")
	}
	if !domainNamePattern.MatchString(exchange) {
		return errors.New("must be normalized lowercase ASCII without a trailing root dot")
	}
	return nil
}

func validateCAARecords(records []dnsv1alpha1.CAARecord, path string) error {
	if len(records) == 0 {
		return fmt.Errorf("%s is required", path)
	}
	seen := make(map[string]struct{}, len(records))
	for index, record := range records {
		itemPath := fmt.Sprintf("%s[%d]", path, index)
		if record.Flags < 0 || record.Flags > 255 {
			return fmt.Errorf("%s.flags must be in range 0..255", itemPath)
		}
		if record.Tag == "" {
			return fmt.Errorf("%s.tag is required", itemPath)
		}
		if !caaTagPattern.MatchString(record.Tag) {
			return fmt.Errorf("%s.tag must be lowercase ASCII alphanumeric", itemPath)
		}
		if record.Value == "" {
			return fmt.Errorf("%s.value is required", itemPath)
		}
		key := fmt.Sprintf("%d\x00%s\x00%s", record.Flags, record.Tag, record.Value)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("%s must not contain duplicate flags, tag, and value tuples", path)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateNSNameServers(nameServers []string, path string) error {
	if len(nameServers) == 0 {
		return fmt.Errorf("%s is required", path)
	}
	seen := make(map[string]struct{}, len(nameServers))
	for index, nameServer := range nameServers {
		itemPath := fmt.Sprintf("%s[%d]", path, index)
		if err := validateNSNameServer(nameServer); err != nil {
			return fmt.Errorf("%s: %w", itemPath, err)
		}
		if _, ok := seen[nameServer]; ok {
			return fmt.Errorf("%s must not contain duplicate name servers", path)
		}
		seen[nameServer] = struct{}{}
	}
	return nil
}

func validateNSNameServer(nameServer string) error {
	if nameServer == "" {
		return errors.New("must not be empty")
	}
	if len(nameServer) > 253 {
		return errors.New("must be 253 octets or fewer")
	}
	if _, err := netip.ParseAddr(nameServer); err == nil {
		return errors.New("must be a DNS name, not an IP address")
	}
	if !domainNamePattern.MatchString(nameServer) {
		return errors.New("must be normalized lowercase ASCII without a trailing root dot")
	}
	return nil
}

func validateZoneStatusNameServers(nameServers []string) error {
	seen := make(map[string]struct{}, len(nameServers))
	for index, nameServer := range nameServers {
		itemPath := fmt.Sprintf("status.nameServers[%d]", index)
		if err := validateNSNameServer(nameServer); err != nil {
			return fmt.Errorf("%s: %w", itemPath, err)
		}
		if _, ok := seen[nameServer]; ok {
			return errors.New("status.nameServers must not contain duplicate name servers")
		}
		seen[nameServer] = struct{}{}
	}
	return nil
}

type conditionReasonSet map[dnsv1alpha1.ConditionType]map[metav1.ConditionStatus]map[string]struct{}

func validateProviderConditionReasons(reasons []dnsv1alpha1.ProviderConditionReason, path string, core conditionReasonSet) error {
	seen := conditionReasonSet{}
	for index, reason := range reasons {
		itemPath := fmt.Sprintf("%s[%d]", path, index)
		if !isKnownConditionType(reason.ConditionType) {
			return fmt.Errorf("%s.conditionType must be Accepted or Programmed", itemPath)
		}
		if !isKnownConditionStatus(reason.Status) {
			return fmt.Errorf("%s.status must be True, False, or Unknown", itemPath)
		}
		if strings.TrimSpace(reason.Reason) == "" {
			return fmt.Errorf("%s.reason is required", itemPath)
		}
		if !conditionReasonPattern.MatchString(reason.Reason) {
			return fmt.Errorf("%s.reason must be CamelCase", itemPath)
		}
		if strings.TrimSpace(reason.Description) == "" {
			return fmt.Errorf("%s.description is required", itemPath)
		}
		if hasConditionReason(core, reason.ConditionType, reason.Status, reason.Reason) {
			return fmt.Errorf("%s duplicates core reason %q for %s=%s", itemPath, reason.Reason, reason.ConditionType, reason.Status)
		}
		if hasConditionReason(seen, reason.ConditionType, reason.Status, reason.Reason) {
			return fmt.Errorf("%s duplicates provider reason %q for %s=%s", itemPath, reason.Reason, reason.ConditionType, reason.Status)
		}
		addConditionReason(seen, reason.ConditionType, reason.Status, reason.Reason)
	}
	return nil
}

func validateStatusConditions(conditions []metav1.Condition, path string, allowed conditionReasonSet) error {
	for index, condition := range conditions {
		itemPath := fmt.Sprintf("%s[%d]", path, index)
		conditionType := dnsv1alpha1.ConditionType(condition.Type)
		if _, ok := allowed[conditionType]; !ok {
			return fmt.Errorf("%s.type %q is not allowed", itemPath, condition.Type)
		}
		if !isKnownConditionStatus(condition.Status) {
			return fmt.Errorf("%s.status must be True, False, or Unknown", itemPath)
		}
		if !hasConditionReason(allowed, conditionType, condition.Status, condition.Reason) {
			return fmt.Errorf("%s.reason %q is not allowed for %s=%s", itemPath, condition.Reason, conditionType, condition.Status)
		}
	}
	return nil
}

func validateObservedGeneration(observedGeneration, generation int64, path string) error {
	if observedGeneration < 0 {
		return fmt.Errorf("%s must not be negative", path)
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

func allowedConditionReasons(core conditionReasonSet, additional []dnsv1alpha1.ProviderConditionReason) conditionReasonSet {
	allowed := copyConditionReasonSet(core)
	for _, reason := range additional {
		addConditionReason(allowed, reason.ConditionType, reason.Status, reason.Reason)
	}
	return allowed
}

func zoneClassConditionReasons() conditionReasonSet {
	return conditionReasonSet{
		dnsv1alpha1.ConditionAccepted: {
			metav1.ConditionTrue: {
				"Accepted": {},
			},
			metav1.ConditionFalse: {
				"InvalidProvider":    {},
				"InvalidIdentityRef": {},
				"InvalidParameters":  {},
				"DeniedByPolicy":     {},
			},
			metav1.ConditionUnknown: {
				"IdentityNotResolved":               {},
				"ProviderStorageVersionNotResolved": {},
				"ReconcileError":                    {},
			},
		},
	}
}

func route53IdentityConditionReasons() conditionReasonSet {
	return conditionReasonSet{
		dnsv1alpha1.ConditionAccepted: {
			metav1.ConditionTrue: {
				"Accepted": {},
			},
			metav1.ConditionFalse: {
				"InvalidRegion":           {},
				"InvalidCredentialSource": {},
				"InvalidAssumeRoleChain":  {},
				"DeniedByPolicy":          {},
			},
		},
		dnsv1alpha1.ConditionType("Ready"): {
			metav1.ConditionTrue: {
				"Ready": {},
			},
			metav1.ConditionFalse: {
				"CredentialUnavailable":   {},
				"AssumeRoleFailed":        {},
				"GetCallerIdentityFailed": {},
				"AccountMismatch":         {},
			},
		},
	}
}

func cloudflareIdentityConditionReasons() conditionReasonSet {
	return conditionReasonSet{
		dnsv1alpha1.ConditionAccepted: {
			metav1.ConditionTrue: {
				"Accepted": {},
			},
			metav1.ConditionFalse: {
				"InvalidSecretRef": {},
				"DeniedByPolicy":   {},
			},
		},
		dnsv1alpha1.ConditionType("Ready"): {
			metav1.ConditionTrue: {
				"Ready": {},
			},
			metav1.ConditionFalse: {
				"SecretNotFound":             {},
				"AccessTokenUnavailable":     {},
				"AccessTokenInvalid":         {},
				"AccessTokenInactive":        {},
				"CloudflareAccountNotFound":  {},
				"CloudflareAccountAmbiguous": {},
				"ProviderAccessDenied":       {},
				"ProviderInvalidRequest":     {},
				"ProviderUnavailable":        {},
				"ReconcileError":             {},
			},
		},
	}
}

func zoneCoreConditionReasons() conditionReasonSet {
	return conditionReasonSet{
		dnsv1alpha1.ConditionAccepted: {
			metav1.ConditionTrue: {
				"Accepted": {},
			},
			metav1.ConditionFalse: {
				"InvalidZoneClassRef":      {},
				"InvalidProviderPayload":   {},
				"NotAllowedByZoneClass":    {},
				"ZoneClassNotAccepted":     {},
				"InvalidIdentityRef":       {},
				"InvalidProvider":          {},
				"ProviderMismatch":         {},
				"ProviderConversionFailed": {},
				"InvalidAdoption":          {},
				"ManagedResourceMismatch":  {},
				"DeniedByPolicy":           {},
			},
			metav1.ConditionUnknown: {
				"Reconciling":                       {},
				"ZoneClassNotResolved":              {},
				"IdentityNotResolved":               {},
				"ProviderStorageVersionNotResolved": {},
				"ProviderConversionUnavailable":     {},
				"OwnerStateNotResolved":             {},
				"ReconcileError":                    {},
			},
		},
		dnsv1alpha1.ConditionProgrammed: {
			metav1.ConditionTrue: {
				"Programmed": {},
			},
			metav1.ConditionFalse: {
				"ProviderChangePending":    {},
				"ProviderIdentityNotReady": {},
				"ExternalResourceNotFound": {},
				"ExternalResourceMismatch": {},
				"ProviderConflict":         {},
				"ProviderAccessDenied":     {},
				"ProviderInvalidRequest":   {},
				"ProviderUnavailable":      {},
				"ReconcileError":           {},
			},
			metav1.ConditionUnknown: {
				"Reconciling":           {},
				"ProviderChangePending": {},
				"ProviderUnavailable":   {},
				"ReconcileError":        {},
			},
		},
	}
}

func recordSetCoreConditionReasons() conditionReasonSet {
	return conditionReasonSet{
		dnsv1alpha1.ConditionAccepted: {
			metav1.ConditionTrue: {
				"Accepted": {},
			},
			metav1.ConditionFalse: {
				"InvalidZoneRef":           {},
				"InvalidProvider":          {},
				"InvalidProviderPayload":   {},
				"ProviderMismatch":         {},
				"ProviderConversionFailed": {},
				"NotAllowedByZone":         {},
				"ZoneNotAccepted":          {},
				"RecordSetConflict":        {},
				"InvalidAdoption":          {},
				"ManagedResourceMismatch":  {},
				"DeniedByPolicy":           {},
			},
			metav1.ConditionUnknown: {
				"Reconciling":                       {},
				"ZoneNotResolved":                   {},
				"ZoneClassNotResolved":              {},
				"IdentityNotResolved":               {},
				"ProviderStorageVersionNotResolved": {},
				"ProviderConversionUnavailable":     {},
				"OwnerStateNotResolved":             {},
				"ReconcileError":                    {},
			},
		},
		dnsv1alpha1.ConditionProgrammed: {
			metav1.ConditionTrue: {
				"Programmed": {},
			},
			metav1.ConditionFalse: {
				"ProviderChangePending":    {},
				"ProviderChangeDeferred":   {},
				"ProviderIdentityNotReady": {},
				"ExternalResourceNotFound": {},
				"ExternalResourceMismatch": {},
				"ProviderConflict":         {},
				"ProviderAccessDenied":     {},
				"ProviderInvalidRequest":   {},
				"ProviderUnavailable":      {},
				"ReconcileError":           {},
			},
			metav1.ConditionUnknown: {
				"Reconciling":            {},
				"ProviderChangePending":  {},
				"ProviderChangeDeferred": {},
				"ProviderUnavailable":    {},
				"ReconcileError":         {},
			},
		},
	}
}

func isKnownConditionType(conditionType dnsv1alpha1.ConditionType) bool {
	return conditionType == dnsv1alpha1.ConditionAccepted || conditionType == dnsv1alpha1.ConditionProgrammed
}

func isKnownConditionStatus(status metav1.ConditionStatus) bool {
	return status == metav1.ConditionTrue || status == metav1.ConditionFalse || status == metav1.ConditionUnknown
}

func copyConditionReasonSet(source conditionReasonSet) conditionReasonSet {
	copied := conditionReasonSet{}
	for conditionType, statuses := range source {
		for status, reasons := range statuses {
			for reason := range reasons {
				addConditionReason(copied, conditionType, status, reason)
			}
		}
	}
	return copied
}

func hasConditionReason(set conditionReasonSet, conditionType dnsv1alpha1.ConditionType, status metav1.ConditionStatus, reason string) bool {
	statuses, ok := set[conditionType]
	if !ok {
		return false
	}
	reasons, ok := statuses[status]
	if !ok {
		return false
	}
	_, ok = reasons[reason]
	return ok
}

func addConditionReason(set conditionReasonSet, conditionType dnsv1alpha1.ConditionType, status metav1.ConditionStatus, reason string) {
	if set[conditionType] == nil {
		set[conditionType] = map[metav1.ConditionStatus]map[string]struct{}{}
	}
	if set[conditionType][status] == nil {
		set[conditionType][status] = map[string]struct{}{}
	}
	set[conditionType][status][reason] = struct{}{}
}

func validateNamespacePolicy(policy dnsv1alpha1.NamespacePolicy, path string) error {
	switch namespacePolicyFrom(policy) {
	case dnsv1alpha1.NamespacesFromSame, dnsv1alpha1.NamespacesFromAll:
		return nil
	case dnsv1alpha1.NamespacesFromSelector:
		if policy.Selector == nil {
			return fmt.Errorf("%s.selector is required when from is Selector", path)
		}
		if _, err := metav1.LabelSelectorAsSelector(policy.Selector); err != nil {
			return fmt.Errorf("%s.selector is invalid: %w", path, err)
		}
		return nil
	default:
		return fmt.Errorf("%s.from is invalid", path)
	}
}

func validateAllowedRecordSets(grants []dnsv1alpha1.AllowedRecordSet) error {
	for grantIndex, grant := range grants {
		selector := &grant.Namespaces.Selector
		if isEmptyLabelSelector(selector) {
			return fmt.Errorf("spec.allowedRecordSets[%d].namespaces.selector must not be empty", grantIndex)
		}
		if _, err := metav1.LabelSelectorAsSelector(selector); err != nil {
			return fmt.Errorf("spec.allowedRecordSets[%d].namespaces.selector is invalid: %w", grantIndex, err)
		}
		if len(grant.Records) == 0 {
			return fmt.Errorf("spec.allowedRecordSets[%d].records must not be empty", grantIndex)
		}
		for recordIndex, record := range grant.Records {
			if record.Name.Pattern == "" {
				return fmt.Errorf("spec.allowedRecordSets[%d].records[%d].name.pattern is required", grantIndex, recordIndex)
			}
			if _, err := regexp.Compile(record.Name.Pattern); err != nil {
				return fmt.Errorf("spec.allowedRecordSets[%d].records[%d].name.pattern is invalid: %w", grantIndex, recordIndex, err)
			}
			if len(record.Types) == 0 {
				return fmt.Errorf("spec.allowedRecordSets[%d].records[%d].types must not be empty", grantIndex, recordIndex)
			}
		}
	}
	return nil
}

func (v *CoreValidator) validateAllowedRecordSetTypes(ctx context.Context, grants []dnsv1alpha1.AllowedRecordSet) error {
	knownTypes := standardRecordTypes()
	var providers dnsv1alpha1.ProviderList
	if err := v.List(ctx, &providers); err != nil {
		return fmt.Errorf("could not list Providers for allowedRecordSets validation: %w", err)
	}
	for _, provider := range providers.Items {
		for _, version := range provider.Spec.Versions {
			for _, recordType := range version.RecordSet.SupportedTypes {
				knownTypes[recordType] = struct{}{}
			}
		}
	}
	for grantIndex, grant := range grants {
		for recordIndex, record := range grant.Records {
			for typeIndex, recordType := range record.Types {
				if _, ok := knownTypes[recordType]; !ok {
					return fmt.Errorf("spec.allowedRecordSets[%d].records[%d].types[%d] is not supported by core API or any Provider", grantIndex, recordIndex, typeIndex)
				}
			}
		}
	}
	return nil
}

func validateExtensionOpenAPISchema(schema *dnsv1alpha1.ProviderOpenAPISchema, path string) error {
	if schema == nil || len(rawExtensionBytes(schema.OpenAPIV3Schema)) == 0 {
		return nil
	}
	var schemaV1 apiextensionsv1.JSONSchemaProps
	if err := json.Unmarshal(rawExtensionBytes(schema.OpenAPIV3Schema), &schemaV1); err != nil {
		return fmt.Errorf("%s schema is invalid: %w", path, err)
	}
	if err := validateOpenAPISchemaDescriptions(schemaV1, path); err != nil {
		return err
	}
	_, err := schemaValidator(schema, path)
	return err
}

func validateOpenAPISchemaDescriptions(schema apiextensionsv1.JSONSchemaProps, path string) error {
	for name, property := range schema.Properties {
		propertyPath := fmt.Sprintf("%s.properties.%s", path, name)
		if strings.TrimSpace(property.Description) == "" {
			return fmt.Errorf("%s.description is required", propertyPath)
		}
		if err := validateOpenAPISchemaDescriptions(property, propertyPath); err != nil {
			return err
		}
		if property.Items != nil && property.Items.Schema != nil {
			itemPath := propertyPath + ".items"
			if strings.TrimSpace(property.Items.Schema.Description) == "" {
				return fmt.Errorf("%s.description is required", itemPath)
			}
			if err := validateOpenAPISchemaDescriptions(*property.Items.Schema, itemPath); err != nil {
				return err
			}
		}
		if property.AdditionalProperties != nil && property.AdditionalProperties.Schema != nil {
			additionalPath := propertyPath + ".additionalProperties"
			if strings.TrimSpace(property.AdditionalProperties.Schema.Description) == "" {
				return fmt.Errorf("%s.description is required", additionalPath)
			}
			if err := validateOpenAPISchemaDescriptions(*property.AdditionalProperties.Schema, additionalPath); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateRawExtensionBySchema(value runtime.RawExtension, schema *dnsv1alpha1.ProviderOpenAPISchema, path string) error {
	if schema == nil || len(rawExtensionBytes(schema.OpenAPIV3Schema)) == 0 {
		return nil
	}

	validator, err := schemaValidator(schema, path)
	if err != nil {
		return err
	}

	raw := rawExtensionBytes(value)
	if len(raw) == 0 {
		raw = []byte(`{}`)
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
	var schemaV1 apiextensionsv1.JSONSchemaProps
	if err := json.Unmarshal(rawExtensionBytes(schema.OpenAPIV3Schema), &schemaV1); err != nil {
		return nil, fmt.Errorf("%s schema is invalid: %w", path, err)
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

func disabledRecordSetValidations(recordSet *dnsv1alpha1.RecordSet, providerVersion *dnsv1alpha1.ProviderVersion) (map[string]bool, error) {
	disabled := map[string]bool{}
	for _, toggle := range providerVersion.RecordSet.DisableValidations {
		ok, err := evaluateCEL(recordSet, toggle.When)
		if err != nil {
			return nil, fmt.Errorf("Provider recordSet.disableValidations[%s] CEL failed: %w", toggle.Name, err)
		}
		if ok {
			disabled[toggle.Name] = true
		}
	}
	return disabled, nil
}

func validateExtensionCELRules(obj runtime.Object, rules []dnsv1alpha1.ProviderValidationRule, source string) error {
	for index, rule := range rules {
		ok, err := evaluateCEL(obj, rule.Rule)
		if err != nil {
			return fmt.Errorf("%s validationRules[%d] CEL failed: %w", source, index, err)
		}
		if !ok {
			if strings.TrimSpace(rule.Message) != "" {
				return errors.New(rule.Message)
			}
			return fmt.Errorf("%s validationRules[%d] failed", source, index)
		}
	}
	return nil
}

func validateObjectCELBoolRules(rules []dnsv1alpha1.ProviderValidationRule, path string) error {
	for index, rule := range rules {
		if err := compileObjectCELBoolRule(rule.Rule, fmt.Sprintf("%s[%d].rule", path, index)); err != nil {
			return err
		}
	}
	return nil
}

func validateZoneUnitCELBoolRules(rules []dnsv1alpha1.ProviderValidationRule, path string) error {
	for index, rule := range rules {
		if err := compileZoneUnitCELBoolRule(rule.Rule, fmt.Sprintf("%s[%d].rule", path, index)); err != nil {
			return err
		}
	}
	return nil
}

func compileObjectCELBoolRule(rule string, path string) error {
	err := celruntime.CompileBool(rule,
		cel.Variable("self", cel.DynType),
	)
	if err != nil {
		return fmt.Errorf("%s CEL failed: %w", path, err)
	}
	return nil
}

func compileZoneUnitCELBoolRule(rule string, path string) error {
	err := celruntime.CompileBool(rule,
		cel.Variable("self", cel.DynType),
		cel.Variable("other", cel.DynType),
		cel.Variable("provider", cel.DynType),
		cel.Variable("toVersion", cel.DynType),
		cel.Variable("zone", cel.DynType),
	)
	if err != nil {
		return fmt.Errorf("%s CEL failed: %w", path, err)
	}
	return nil
}

func compileCELObjectRule(rule string, path string) error {
	if strings.TrimSpace(rule) == "" {
		return nil
	}
	err := celruntime.CompileObject(rule,
		cel.Variable("self", cel.DynType),
		cel.Variable("target", cel.StringType),
		cel.Variable("provider", cel.DynType),
		cel.Variable("fromVersion", cel.DynType),
		cel.Variable("toVersion", cel.DynType),
		cel.Variable("zone", cel.DynType),
		cel.Variable("recordSet", cel.DynType),
		cel.Variable("zoneClass", cel.DynType),
	)
	if err != nil {
		return fmt.Errorf("%s CEL failed: %w", path, err)
	}
	return nil
}

func evaluateCEL(obj runtime.Object, rule string) (bool, error) {
	self, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return false, err
	}
	return celruntime.EvalBool(rule, map[string]interface{}{"self": self}, cel.Variable("self", cel.DynType))
}

func rawExtensionBytes(value runtime.RawExtension) []byte {
	if len(value.Raw) > 0 {
		return value.Raw
	}
	if value.Object == nil {
		return nil
	}
	raw, err := json.Marshal(value.Object)
	if err != nil {
		return nil
	}
	return raw
}

func namespacePolicyFrom(policy dnsv1alpha1.NamespacePolicy) dnsv1alpha1.NamespacesFrom {
	if policy.From == "" {
		return dnsv1alpha1.NamespacesFromSame
	}
	return policy.From
}

func recordSetClaimKey(namespace, name string) string {
	return namespace + "\x00" + name
}

func isEmptyLabelSelector(selector *metav1.LabelSelector) bool {
	if selector == nil {
		return true
	}
	return len(selector.MatchLabels) == 0 && len(selector.MatchExpressions) == 0
}

func standardRecordTypes() map[dnsv1alpha1.RecordType]struct{} {
	return map[dnsv1alpha1.RecordType]struct{}{
		dnsv1alpha1.RecordTypeA:     {},
		dnsv1alpha1.RecordTypeAAAA:  {},
		dnsv1alpha1.RecordTypeTXT:   {},
		dnsv1alpha1.RecordTypeCNAME: {},
		dnsv1alpha1.RecordTypeMX:    {},
		dnsv1alpha1.RecordTypeCAA:   {},
		dnsv1alpha1.RecordTypeNS:    {},
	}
}

func recordTypes() map[dnsv1alpha1.RecordType]struct{} {
	return map[dnsv1alpha1.RecordType]struct{}{
		dnsv1alpha1.RecordTypeA:     {},
		dnsv1alpha1.RecordTypeAAAA:  {},
		dnsv1alpha1.RecordTypeCNAME: {},
		dnsv1alpha1.RecordTypeNS:    {},
		dnsv1alpha1.RecordTypeTXT:   {},
		dnsv1alpha1.RecordTypeMX:    {},
		dnsv1alpha1.RecordTypeCAA:   {},
	}
}

func canonicalRecordName(ownerName, domainName string) string {
	switch ownerName {
	case "@":
		return domainName + "."
	case "*":
		return "*." + domainName + "."
	default:
		return ownerName + "." + domainName + "."
	}
}

func validateIPAddresses(values []string, path string, wantIPv6 bool) error {
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		address, err := netip.ParseAddr(value)
		if err != nil {
			return fmt.Errorf("%s[%d] must be a valid IP address", path, index)
		}
		if address.Is6() != wantIPv6 {
			if wantIPv6 {
				return fmt.Errorf("%s[%d] must be an IPv6 address", path, index)
			}
			return fmt.Errorf("%s[%d] must be an IPv4 address", path, index)
		}
		key := address.String()
		if _, ok := seen[key]; ok {
			return fmt.Errorf("%s must not contain duplicate IP addresses", path)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateTXTValues(values []string, forbidNonPrintableASCII bool, path string) error {
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		valueLen := len([]byte(value))
		if valueLen == 0 {
			return fmt.Errorf("%s[%d] must not be empty", path, index)
		}
		if valueLen > 4000 {
			return fmt.Errorf("%s[%d] must be 4000 UTF-8 octets or fewer", path, index)
		}
		if forbidNonPrintableASCII && !isPrintableASCII(value) {
			return fmt.Errorf("%s[%d] must contain only printable ASCII characters", path, index)
		}
		if _, ok := seen[value]; ok {
			return fmt.Errorf("%s must not contain duplicate TXT values", path)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func isPrintableASCII(value string) bool {
	for index := 0; index < len(value); index++ {
		if value[index] < 0x20 || value[index] > 0x7e {
			return false
		}
	}
	return true
}
