package dnsapichart

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	externalmanifest "github.com/appthrust/dns-api/internal/go/providers/external/manifest"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

func TestExternalOnlyRender(t *testing.T) {
	for _, gateway := range []bool{false, true} {
		for _, service := range []bool{false, true} {
			t.Run(fmt.Sprintf("gateway=%t/service=%t", gateway, service), func(t *testing.T) {
				rendered := renderChart(t,
					"--set", "providers.route53.enabled=false",
					"--set", "providers.cloudflare.enabled=false",
					"--set", "providers.external.enabled=true",
					"--set", "gatewayEndpoint.enabled="+strconv.FormatBool(gateway),
					"--set", "serviceEndpoint.enabled="+strconv.FormatBool(service),
				)
				role := renderedDocument(t, rendered, "rbac.authorization.k8s.io/v1", "ClusterRole")
				for _, forbidden := range []string{"secrets", "identities", "route53.dns.appthrust.io", "cloudflare.dns.appthrust.io"} {
					if strings.Contains(role, forbidden) {
						t.Errorf("external-only role contains %q:\n%s", forbidden, role)
					}
				}
				for permission, enabled := range map[string]bool{
					"- gateway.networking.k8s.io\n": gateway,
					"- services\n":                 service,
				} {
					if strings.Contains(role, permission) != enabled {
						t.Errorf("permission %q presence does not equal %t", permission, enabled)
					}
				}
				assertExternalConversionRule(t, role)
				webhooks := renderedDocument(t, rendered, "admissionregistration.k8s.io/v1", "ValidatingWebhookConfiguration")
				if strings.Contains(webhooks, "identities") {
					t.Fatal("external-only installation includes identity admission")
				}
				deployment := renderedDocument(t, rendered, "apps/v1", "Deployment")
				for _, arg := range []string{
					"--route53-controller-enabled=false",
					"--cloudflare-controller-enabled=false",
					"--route53-endpoint-conversion-enabled=false",
					"--external-controller-enabled=true",
					"--external-controller-name=external.dns.appthrust.io/controller",
					"--external-provider-name=external.dns.appthrust.io",
					"--external-provider-version=v1alpha1",
					"--external-dns-qps=40",
					"--external-dns-burst=80",
				} {
					if !strings.Contains(deployment, arg) {
						t.Errorf("external-only Deployment is missing %q", arg)
					}
				}
				for _, kind := range []string{"Provider", "EndpointProviderCapability", "APIService", "ClusterRoleBinding"} {
					if count := strings.Count(rendered, "kind: "+kind+"\n"); count != 1 {
						t.Errorf("got %d %s declarations, want exactly one", count, kind)
					}
				}
				assertManifestEqual(t, externalmanifest.ProviderYAML, renderedDocument(t, rendered, "dns.appthrust.io/v1alpha1", "Provider"))
				assertManifestEqual(t, externalmanifest.CapabilityYAML, renderedDocument(t, rendered, "endpoint.dns.appthrust.io/v1alpha1", "EndpointProviderCapability"))
				assertExternalTLS(t, rendered)
			})
		}
	}
}

func TestExternalCanCoexistWithCloudProviders(t *testing.T) {
	for _, route53 := range []bool{false, true} {
		for _, cloudflare := range []bool{false, true} {
			t.Run(fmt.Sprintf("route53=%t/cloudflare=%t", route53, cloudflare), func(t *testing.T) {
				rendered := renderChart(t,
					"--set", "providers.external.enabled=true",
					"--set", "providers.route53.enabled="+strconv.FormatBool(route53),
					"--set", "providers.cloudflare.enabled="+strconv.FormatBool(cloudflare),
				)
				role := renderedDocument(t, rendered, "rbac.authorization.k8s.io/v1", "ClusterRole")
				assertExternalConversionRule(t, role)
				for permission, enabled := range map[string]bool{
					"- route53.dns.appthrust.io\n":          route53,
					"- endpoint.route53.dns.appthrust.io\n": route53,
					"- cloudflare.dns.appthrust.io\n":       cloudflare,
					"- secrets\n":                           cloudflare,
				} {
					if strings.Contains(role, permission) != enabled {
						t.Errorf("permission %q presence does not equal %t", permission, enabled)
					}
				}
			})
		}
	}
}

func TestExternalRequiresWebhookAndCertManager(t *testing.T) {
	for _, webhook := range []bool{false, true} {
		for _, certManager := range []bool{false, true} {
			if webhook && certManager {
				continue
			}
			t.Run(fmt.Sprintf("webhook=%t/certManager=%t", webhook, certManager), func(t *testing.T) {
				_, file, _, _ := runtime.Caller(0)
				output, err := exec.Command("helm", "template", "dns-api", filepath.Dir(file),
					"--set", "providers.external.enabled=true",
					"--set", "webhook.enabled="+strconv.FormatBool(webhook),
					"--set", "certManager.enabled="+strconv.FormatBool(certManager),
				).CombinedOutput()
				if err == nil {
					t.Fatal("external enablement without serving TLS succeeded")
				}
				if !strings.Contains(string(output), "providers.external.enabled requires webhook.enabled=true and certManager.enabled=true") {
					t.Fatalf("unexpected render failure: %v\n%s", err, output)
				}
			})
		}
	}
}

func TestExternalDefaultsToDisabled(t *testing.T) {
	rendered := renderChart(t)
	if explicit := renderChart(t, "--set", "providers.external.enabled=false"); rendered != explicit {
		t.Fatal("default render differs from explicitly disabled external provider")
	}
	for _, forbidden := range []string{"external.dns.appthrust.io", "--external-dns-", "--external-provider-"} {
		if strings.Contains(rendered, forbidden) {
			t.Errorf("default render contains external declaration %q", forbidden)
		}
	}
	deployment := renderedDocument(t, rendered, "apps/v1", "Deployment")
	for _, arg := range []string{"--route53-controller-enabled=true", "--cloudflare-controller-enabled=true", "--route53-endpoint-conversion-enabled=true", "--external-controller-enabled=false"} {
		if !strings.Contains(deployment, arg) {
			t.Errorf("default Deployment is missing %q", arg)
		}
	}
	// External's strict TLS requirement must not change existing disabled-provider renders.
	renderChart(t, "--set", "webhook.enabled=false", "--set", "certManager.enabled=false")
}

func TestExternalCustomBindings(t *testing.T) {
	rendered := renderChart(t,
		"--set", "providers.external.enabled=true",
		"--set", "providers.route53.enabled=false",
		"--set", "providers.cloudflare.enabled=false",
		"--set", "providers.external.controllerName=custom.example/controller",
		"--set", "providers.external.providerName=custom.example",
		"--set", "providers.external.providerVersion=v1beta1",
		"--set", "providers.external.dnsQPS=12.5",
		"--set", "providers.external.dnsBurst=25",
		"--set", "fullnameOverride=custom-dns",
		"--set", "webhook.service.port=8443",
	)
	deployment := renderedDocument(t, rendered, "apps/v1", "Deployment")
	for _, arg := range []string{"--external-controller-name=custom.example/controller", "--external-provider-name=custom.example", "--external-provider-version=v1beta1", "--external-dns-qps=12.5", "--external-dns-burst=25"} {
		if !strings.Contains(deployment, arg) {
			t.Errorf("custom Deployment is missing %q", arg)
		}
	}
	provider := manifestMap(t, renderedDocument(t, rendered, "dns.appthrust.io/v1alpha1", "Provider"))
	if provider["metadata"].(map[string]any)["name"] != "custom.example" || provider["spec"].(map[string]any)["versions"].([]any)[0].(map[string]any)["name"] != "v1beta1" {
		t.Fatal("Provider does not match custom runtime binding")
	}
	capability := manifestMap(t, renderedDocument(t, rendered, "endpoint.dns.appthrust.io/v1alpha1", "EndpointProviderCapability"))
	spec := capability["spec"].(map[string]any)
	if !reflect.DeepEqual(spec["provider"], map[string]any{"name": "custom.example", "version": "v1beta1"}) {
		t.Fatalf("capability does not match custom binding: %#v", spec)
	}
	// Provider versions are configurable; the stateless conversion protocol is not.
	if !reflect.DeepEqual(spec["conversion"], map[string]any{"group": "endpoint.external.dns.appthrust.io", "version": "v1alpha1", "resource": "endpointrecordsetconversions"}) {
		t.Fatalf("custom binding changed the conversion GVR: %#v", spec)
	}
	assertExternalTLS(t, rendered)
}

func TestExternalKustomizeParity(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	configDir := filepath.Join(filepath.Dir(file), "../../../app/operator/config")
	for name, expected := range map[string][]byte{"external_extensions.yaml": externalmanifest.ProviderYAML, "endpoint_capabilities.yaml": externalmanifest.CapabilityYAML} {
		actual, err := os.ReadFile(filepath.Join(configDir, "external", name))
		if err != nil {
			t.Fatal(err)
		}
		assertManifestEqual(t, expected, string(actual))
	}
	output, err := exec.Command("kustomize", "build", filepath.Join(configDir, "external")).CombinedOutput()
	if err != nil {
		t.Fatalf("external kustomize build failed: %v\n%s", err, output)
	}
	rendered := string(output)
	assertExternalTLS(t, rendered)
	role := renderedDocument(t, rendered, "rbac.authorization.k8s.io/v1", "ClusterRole")
	assertExternalConversionRule(t, role)
	deployment := renderedDocument(t, rendered, "apps/v1", "Deployment")
	for _, arg := range []string{"--external-controller-enabled=true", "--external-controller-name=external.dns.appthrust.io/controller", "--external-provider-name=external.dns.appthrust.io", "--external-provider-version=v1alpha1", "--external-dns-qps=40", "--external-dns-burst=80"} {
		if !strings.Contains(deployment, arg) {
			t.Errorf("Kustomize Deployment is missing %q", arg)
		}
	}
	base, err := exec.Command("kustomize", "build", filepath.Join(configDir, "default")).CombinedOutput()
	if err != nil {
		t.Fatalf("default kustomize build failed: %v\n%s", err, base)
	}
	if strings.Contains(string(base), "external.dns.appthrust.io") || strings.Contains(string(base), "--external-controller-enabled=true") {
		t.Fatal("Kustomize default installation enables external DNS")
	}
	var baseRole, externalRole rbacv1.ClusterRole
	if err := yaml.Unmarshal([]byte(renderedDocument(t, string(base), "rbac.authorization.k8s.io/v1", "ClusterRole")), &baseRole); err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal([]byte(role), &externalRole); err != nil {
		t.Fatal(err)
	}
	var unchanged []rbacv1.PolicyRule
	for _, rule := range externalRole.Rules {
		if !reflect.DeepEqual(rule.APIGroups, []string{"endpoint.external.dns.appthrust.io"}) {
			unchanged = append(unchanged, rule)
		}
	}
	if !reflect.DeepEqual(baseRole.Rules, unchanged) {
		t.Fatal("external overlay changes permissions beyond its conversion-create rule")
	}
}

func assertExternalConversionRule(t *testing.T, source string) {
	t.Helper()
	var role rbacv1.ClusterRole
	if err := yaml.Unmarshal([]byte(source), &role); err != nil {
		t.Fatal(err)
	}
	expected := rbacv1.PolicyRule{APIGroups: []string{"endpoint.external.dns.appthrust.io"}, Resources: []string{"endpointrecordsetconversions"}, Verbs: []string{"create"}}
	count := 0
	for _, rule := range role.Rules {
		for _, group := range rule.APIGroups {
			if group == "endpoint.external.dns.appthrust.io" {
				count++
				if !reflect.DeepEqual(rule, expected) {
					t.Errorf("external conversion permission differs: %#v", rule)
				}
			}
		}
	}
	if count != 1 {
		t.Errorf("got %d external conversion rules, want exactly one", count)
	}
}

func assertExternalTLS(t *testing.T, rendered string) {
	t.Helper()
	var apiService map[string]any
	for _, document := range strings.Split(rendered, "\n---") {
		if strings.Contains(document, "kind: APIService\n") && strings.Contains(document, "name: v1alpha1.endpoint.external.dns.appthrust.io\n") {
			apiService = manifestMap(t, document)
		}
	}
	if apiService == nil {
		t.Fatal("external conversion APIService is missing")
	}
	spec := apiService["spec"].(map[string]any)
	if _, exists := spec["insecureSkipTLSVerify"]; exists {
		t.Fatal("external APIService bypasses TLS verification")
	}
	if spec["group"] != "endpoint.external.dns.appthrust.io" || spec["version"] != "v1alpha1" {
		t.Fatalf("external APIService GVR differs: %#v", spec)
	}
	certificate := manifestMap(t, renderedDocument(t, rendered, "cert-manager.io/v1", "Certificate"))
	certMetadata := certificate["metadata"].(map[string]any)
	annotation := apiService["metadata"].(map[string]any)["annotations"].(map[string]any)["cert-manager.io/inject-ca-from"]
	if annotation != certMetadata["namespace"].(string)+"/"+certMetadata["name"].(string) {
		t.Fatalf("CA injection does not reference serving Certificate: %v", annotation)
	}
	service := manifestMap(t, renderedDocument(t, rendered, "v1", "Service"))
	serviceMetadata := service["metadata"].(map[string]any)
	serviceRef := spec["service"].(map[string]any)
	servicePort := service["spec"].(map[string]any)["ports"].([]any)[0].(map[string]any)["port"]
	if serviceRef["namespace"] != serviceMetadata["namespace"] || serviceRef["name"] != serviceMetadata["name"] || serviceRef["port"] != servicePort {
		t.Fatalf("APIService does not reference webhook Service: %#v", serviceRef)
	}
	wantDNSName := serviceRef["name"].(string) + "." + serviceRef["namespace"].(string) + ".svc"
	for _, name := range certificate["spec"].(map[string]any)["dnsNames"].([]any) {
		if name == wantDNSName {
			return
		}
	}
	t.Fatalf("Certificate does not cover webhook Service DNS name %s", wantDNSName)
}

func assertManifestEqual(t *testing.T, expected []byte, actual string) {
	t.Helper()
	if !reflect.DeepEqual(manifestMap(t, string(expected)), manifestMap(t, actual)) {
		t.Fatalf("packaged manifest differs from embedded source:\n%s", actual)
	}
}

func manifestMap(t *testing.T, source string) map[string]any {
	t.Helper()
	var result map[string]any
	if err := yaml.Unmarshal([]byte(source), &result); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	return result
}
