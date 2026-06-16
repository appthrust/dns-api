package route53

import (
	"context"
	"fmt"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

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
