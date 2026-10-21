// Package manifest contains the hosted Provider and endpoint capability declarations.
package manifest

import (
	_ "embed"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	"sigs.k8s.io/yaml"
)

//go:embed provider.yaml
var ProviderYAML []byte

//go:embed capability.yaml
var CapabilityYAML []byte

func Provider() (*dnsv1alpha1.Provider, error) {
	var provider dnsv1alpha1.Provider
	if err := yaml.UnmarshalStrict(ProviderYAML, &provider); err != nil {
		return nil, err
	}
	return &provider, nil
}
