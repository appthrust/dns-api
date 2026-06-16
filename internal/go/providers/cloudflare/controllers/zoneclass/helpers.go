package cloudflare

import (
	"context"
	"fmt"

	cloudflarev1alpha1 "github.com/appthrust/dns-api/pkg/go/api/cloudflare/v1alpha1"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const DefaultControllerName = "cloudflare.dns.appthrust.io/controller"

func cloudflareProviderReference(providerName, providerVersion string) dnsv1alpha1.ProviderReference {
	if providerName == "" {
		providerName = cloudflarev1alpha1.ProviderName
	}
	if providerVersion == "" {
		providerVersion = cloudflarev1alpha1.ProviderVersion
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

func setCondition(conditions *[]metav1.Condition, conditionType string, status metav1.ConditionStatus, reason, message string, observedGeneration int64) {
	meta.SetStatusCondition(conditions, metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: observedGeneration,
	})
}
