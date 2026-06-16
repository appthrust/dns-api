package route53

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const DefaultControllerName = "route53.dns.appthrust.io/controller"

var reservedHostedZoneTagKeys = map[string]struct{}{
	"appthrust.io/managed-by":           {},
	"appthrust.io/zone-namespace":       {},
	"appthrust.io/zone-name":            {},
	"appthrust.io/zone-class-namespace": {},
	"appthrust.io/zone-class-name":      {},
}

func route53ProviderReference(providerName, providerVersion string) dnsv1alpha1.ProviderReference {
	if providerName == "" {
		providerName = route53v1alpha1.ProviderName
	}
	if providerVersion == "" {
		providerVersion = route53v1alpha1.ProviderVersion
	}
	return dnsv1alpha1.ProviderReference{Name: providerName, Version: providerVersion}
}

func resolveDNSProviderVersion(ctx context.Context, c client.Client, ref dnsv1alpha1.ProviderReference) (*dnsv1alpha1.Provider, *dnsv1alpha1.ProviderVersion, error) {
	if ref.Name == "" || ref.Version == "" {
		err := fmt.Errorf("provider reference must include name and version")
		return nil, nil, err
	}
	var provider dnsv1alpha1.Provider
	if err := c.Get(ctx, client.ObjectKey{Name: ref.Name}, &provider); err != nil {
		return nil, nil, err
	}
	for index := range provider.Spec.Versions {
		version := &provider.Spec.Versions[index]
		if version.Name == ref.Version {
			return &provider, version, nil
		}
	}
	return &provider, nil, apierrors.NewNotFound(dnsv1alpha1.Resource("providers"), ref.Name+"/"+ref.Version)
}

func route53ProviderVersionMatches(provider *dnsv1alpha1.Provider, version *dnsv1alpha1.ProviderVersion, binding dnsv1alpha1.ProviderReference) bool {
	return provider != nil &&
		version != nil &&
		provider.Name == binding.Name &&
		version.Name == binding.Version
}

func setCondition(conditions *[]metav1.Condition, conditionType string, status metav1.ConditionStatus, reason, message string, observedGeneration int64) {
	meta.SetStatusCondition(conditions, metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: observedGeneration,
	})
}

func route53ZoneClassParameters(zoneClass *dnsv1alpha1.ZoneClass) (*route53v1alpha1.Route53ZoneClassParameters, error) {
	if len(zoneClass.Spec.Parameters.Raw) == 0 {
		return nil, errors.New("parameters must be an object")
	}
	var params route53v1alpha1.Route53ZoneClassParameters
	if err := json.Unmarshal(zoneClass.Spec.Parameters.Raw, &params); err != nil {
		return nil, fmt.Errorf("parameters must match Route 53 ZoneClass schema: %w", err)
	}
	return &params, nil
}

func reservedTagConflict(tags map[string]string) (string, bool) {
	for key := range tags {
		if _, ok := reservedHostedZoneTagKeys[key]; ok {
			return key, true
		}
	}
	return "", false
}
