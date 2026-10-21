// Package config implements static external-provider configuration, without DNS I/O.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/appthrust/dns-api/internal/go/core/dnsprobe"
	"github.com/appthrust/dns-api/internal/go/core/providercontract"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	externalv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/external/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
)

func Binding(name, version string) dnsv1alpha1.ProviderReference {
	if name == "" {
		name = externalv1alpha1.ProviderName
	}
	if version == "" {
		version = externalv1alpha1.ProviderVersion
	}
	return dnsv1alpha1.ProviderReference{Name: name, Version: version}
}

func ControllerName(name string) string {
	if name == "" {
		return externalv1alpha1.ControllerName
	}
	return name
}

// HasIdentity never dereferences IdentityRef. Only the optional reference's nil
// value is absent; a pre-cutover value reference or an empty object is present.
func HasIdentity(class *dnsv1alpha1.ZoneClass) bool {
	return any(class.Spec.IdentityRef) != (*dnsv1alpha1.LocalObjectReference)(nil)
}

func Parameters(provider *dnsv1alpha1.Provider, version *dnsv1alpha1.ProviderVersion, class *dnsv1alpha1.ZoneClass) (externalv1alpha1.ExternalZoneClassParameters, error) {
	raw, err := providercontract.ConvertZoneClassParametersToStorage(provider, version, class)
	if err != nil {
		return externalv1alpha1.ExternalZoneClassParameters{}, err
	}
	return Decode(raw)
}

func Decode(raw runtime.RawExtension) (externalv1alpha1.ExternalZoneClassParameters, error) {
	var params externalv1alpha1.ExternalZoneClassParameters
	if len(raw.Raw) != 0 {
		decoder := json.NewDecoder(bytes.NewReader(raw.Raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&params); err != nil {
			return params, fmt.Errorf("invalid external parameters: %w", err)
		}
	}
	if params.Resolver.Mode == "" {
		params.Resolver.Mode = externalv1alpha1.ResolverModeCluster
	}
	switch params.Resolver.Mode {
	case externalv1alpha1.ResolverModeCluster:
		if len(params.Resolver.Servers) != 0 {
			return params, fmt.Errorf("resolver.servers must be empty for Cluster")
		}
	case externalv1alpha1.ResolverModeRecursive:
		if err := dnsprobe.ValidateServers(params.Resolver.Servers); err != nil {
			return params, err
		}
	default:
		return params, fmt.Errorf("resolver.mode must be Cluster or Recursive")
	}
	if len(params.ExpectedNameservers) > 16 {
		return params, fmt.Errorf("expectedNameservers must contain at most 16 names")
	}
	for i, name := range params.ExpectedNameservers {
		if !dnsprobe.ValidName(name) || slices.Contains(params.ExpectedNameservers[:i], name) {
			return params, fmt.Errorf("expectedNameservers must contain distinct canonical DNS names")
		}
	}
	slices.Sort(params.Resolver.Servers)
	slices.Sort(params.ExpectedNameservers)
	return params, nil
}

// ProbePolicy converts provider API parameters at the provider boundary.
func ProbePolicy(params externalv1alpha1.ExternalZoneClassParameters) dnsprobe.Policy {
	return dnsprobe.Policy{Mode: string(params.Resolver.Mode), Servers: params.Resolver.Servers, ExpectedNameservers: params.ExpectedNameservers}
}
