package webhook

import (
	"testing"

	externalmanifest "github.com/appthrust/dns-api/internal/go/providers/external/manifest"
	hostedmanifest "github.com/appthrust/dns-api/internal/go/providers/hosted/manifest"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	"sigs.k8s.io/yaml"
)

// Every packaged Provider manifest must pass the same admission the core
// webhook applies when the chart installs it. The hosted 0.4.0 manifest was
// rejected live (`nameServers.items.description is required`) because only
// Route 53 and Cloudflare were admitted in tests.
func TestBuiltInProviderManifestsPassAdmission(t *testing.T) {
	manifests := map[string][]byte{
		"external": externalmanifest.ProviderYAML,
		"hosted":   hostedmanifest.ProviderYAML,
	}
	for name, file := range map[string]string{"route53": "route53_extensions.yaml", "cloudflare": "cloudflare_extensions.yaml"} {
		provider := builtInProviderManifest(t, file)
		data, err := yaml.Marshal(provider)
		if err != nil {
			t.Fatal(err)
		}
		manifests[name] = data
	}
	for name, data := range manifests {
		t.Run(name, func(t *testing.T) {
			var provider dnsv1alpha1.Provider
			if err := yaml.Unmarshal(data, &provider); err != nil {
				t.Fatalf("unmarshal %s provider: %v", name, err)
			}
			if err := validateProvider(&provider); err != nil {
				t.Fatalf("validateProvider(%s): %v", name, err)
			}
		})
	}
}
