package dnsapichart

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

// The dns-api chart owns only its controller identity. Cloud's central
// allocation writer is authorized by Cloud deployment RBAC; tenant identities
// must never gain EndpointRecordSet create through this upstream chart.
func TestEndpointRecordSetRBACBindsOnlyControllerServiceAccount(t *testing.T) {
	rendered := renderChart(t,
		"--set", "serviceAccount.name=dns-api-central-controller",
		"--set", "gatewayEndpoint.enabled=false",
		"--set", "serviceEndpoint.enabled=false",
	)
	role := renderedDocument(t, rendered, "rbac.authorization.k8s.io/v1", "ClusterRole")
	endpointRule := ruleForAPIGroup(t, role, "endpoint.dns.appthrust.io")
	for _, required := range []string{
		"- endpoint.dns.appthrust.io",
		"- endpointrecordsets",
		"- create",
	} {
		if !strings.Contains(endpointRule, required) {
			t.Fatalf("controller EndpointRecordSet rule is missing %q:\n%s", required, endpointRule)
		}
	}
	binding := renderedDocument(t, rendered, "rbac.authorization.k8s.io/v1", "ClusterRoleBinding")
	for _, required := range []string{
		"kind: ServiceAccount",
		"name: dns-api-central-controller",
		"namespace: appthrust-dns",
	} {
		if !strings.Contains(binding, required) {
			t.Fatalf("controller binding is missing %q:\n%s", required, binding)
		}
	}
	for _, forbidden := range []string{
		"system:authenticated",
		"system:serviceaccounts",
		"tenant",
		"project",
	} {
		if strings.Contains(binding, forbidden) {
			t.Fatalf("controller binding grants an ambient tenant subject %q:\n%s", forbidden, binding)
		}
	}
}

func TestProviderEnablementGatesRuntimePermissionsAndAdmission(t *testing.T) {
	for _, route53 := range []bool{false, true} {
		for _, cloudflare := range []bool{false, true} {
			for _, conversion := range []bool{false, true} {
				t.Run(fmt.Sprintf("route53=%t/cloudflare=%t/conversion=%t", route53, cloudflare, conversion), func(t *testing.T) {
					rendered := renderChart(t,
						"--set", "providers.route53.enabled="+strconv.FormatBool(route53),
						"--set", "providers.cloudflare.enabled="+strconv.FormatBool(cloudflare),
						"--set", "providers.route53.endpoint.conversionAPI.enabled="+strconv.FormatBool(conversion),
						"--set", "gatewayEndpoint.enabled=false",
						"--set", "serviceEndpoint.enabled=false",
					)
					role := renderedDocument(t, rendered, "rbac.authorization.k8s.io/v1", "ClusterRole")
					deployment := renderedDocument(t, rendered, "apps/v1", "Deployment")
					webhooks := renderedDocument(t, rendered, "admissionregistration.k8s.io/v1", "ValidatingWebhookConfiguration")
					for flag, enabled := range map[string]bool{
						"route53-controller-enabled":          route53,
						"cloudflare-controller-enabled":       cloudflare,
						"route53-endpoint-conversion-enabled": route53 && conversion,
					} {
						if !strings.Contains(deployment, "--"+flag+"="+strconv.FormatBool(enabled)) {
							t.Fatalf("Deployment does not pass %s=%t:\n%s", flag, enabled, deployment)
						}
					}
					for permission, enabled := range map[string]bool{
						"- route53.dns.appthrust.io\n":          route53,
						"- cloudflare.dns.appthrust.io\n":       cloudflare,
						"- secrets\n":                           cloudflare,
						"- endpoint.route53.dns.appthrust.io\n": route53 && conversion,
						"- gateway.networking.k8s.io\n":         false,
						"- services\n":                          false,
						"- namespaces\n":                        true,
						"- dns.appthrust.io\n":                  true,
						"- endpoint.dns.appthrust.io\n":         true,
					} {
						if strings.Contains(role, permission) != enabled {
							t.Fatalf("permission %q presence does not equal %t:\n%s", permission, enabled, role)
						}
					}
					for resource, enabled := range map[string]bool{
						"route53identities":    route53,
						"cloudflareidentities": cloudflare,
						"zoneclasses":          true,
					} {
						for _, suffix := range []string{"", "/status"} {
							if strings.Contains(webhooks, "- "+resource+suffix+"\n") != enabled {
								t.Fatalf("admission for %s%s does not equal %t:\n%s", resource, suffix, enabled, webhooks)
							}
						}
					}
					for declaration, enabled := range map[string]bool{
						"name: route53.dns.appthrust.io\n":    route53,
						"name: cloudflare.dns.appthrust.io\n": cloudflare,
						"kind: EndpointProviderCapability\n":  route53 && conversion,
						"kind: APIService\n":                  route53 && conversion,
					} {
						if strings.Contains(rendered, declaration) != enabled {
							t.Fatalf("declaration %q presence does not equal %t", declaration, enabled)
						}
					}
					if strings.Count(rendered, "kind: ClusterRoleBinding\n") != 1 {
						t.Fatal("expected exactly one controller ClusterRoleBinding")
					}
				})
			}
		}
	}
}

func TestRoute53ConversionRequiresWebhookService(t *testing.T) {
	rendered := renderChart(t, "--set", "webhook.enabled=false")
	for _, forbidden := range []string{"kind: APIService\n", "kind: EndpointProviderCapability\n", "- endpoint.route53.dns.appthrust.io\n"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("webhook-disabled render contains %q", forbidden)
		}
	}
	deployment := renderedDocument(t, rendered, "apps/v1", "Deployment")
	if !strings.Contains(deployment, "--route53-endpoint-conversion-enabled=false") {
		t.Fatal("webhook-disabled deployment must disable Route 53 conversion")
	}
}

func TestHelmAndKustomizeAdmissionCoverageAgree(t *testing.T) {
	rendered := renderedDocument(t, renderChart(t), "admissionregistration.k8s.io/v1", "ValidatingWebhookConfiguration")
	var helmConfig admissionv1.ValidatingWebhookConfiguration
	if err := yaml.Unmarshal([]byte(rendered), &helmConfig); err != nil {
		t.Fatalf("decode Helm admission: %v", err)
	}
	_, file, _, _ := runtime.Caller(0)
	source, err := os.ReadFile(filepath.Join(filepath.Dir(file), "../../../app/operator/config/webhook/validating_webhook_configuration.yaml"))
	if err != nil {
		t.Fatalf("read Kustomize admission: %v", err)
	}
	var kustomizeConfig admissionv1.ValidatingWebhookConfiguration
	if err := yaml.Unmarshal(source, &kustomizeConfig); err != nil {
		t.Fatalf("decode Kustomize admission: %v", err)
	}
	if len(helmConfig.Webhooks) != len(kustomizeConfig.Webhooks) {
		t.Fatalf("Helm has %d webhooks, Kustomize has %d", len(helmConfig.Webhooks), len(kustomizeConfig.Webhooks))
	}
	for index, expected := range kustomizeConfig.Webhooks {
		actual := helmConfig.Webhooks[index]
		// Release namespace and service names differ; admission coverage must not.
		actual.ClientConfig.Service.Namespace = expected.ClientConfig.Service.Namespace
		actual.ClientConfig.Service.Name = expected.ClientConfig.Service.Name
		if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("admission differs for %s:\nHelm: %#v\nKustomize: %#v", expected.Name, actual, expected)
		}
	}
}

func TestZoneClassCRDDistributionsAgreeOnOptionalIdentity(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	chartDir := filepath.Dir(file)
	chartCRD, err := os.ReadFile(filepath.Join(chartDir, "crds/dns.appthrust.io_zoneclasses.yaml"))
	if err != nil {
		t.Fatalf("read chart CRD: %v", err)
	}
	operatorCRD, err := os.ReadFile(filepath.Join(chartDir, "../../../app/operator/config/crd/bases/dns.appthrust.io_zoneclasses.yaml"))
	if err != nil {
		t.Fatalf("read operator CRD: %v", err)
	}
	if !bytes.Equal(chartCRD, operatorCRD) {
		t.Fatal("ZoneClass CRD distributions differ; run task controller-chart:sync-crds")
	}
	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(chartCRD, &crd); err != nil {
		t.Fatalf("decode ZoneClass CRD: %v", err)
	}
	for _, version := range crd.Spec.Versions {
		spec := version.Schema.OpenAPIV3Schema.Properties["spec"]
		if slices.Contains(spec.Required, "identityRef") {
			t.Fatalf("%s structurally requires identityRef", version.Name)
		}
		identity := spec.Properties["identityRef"]
		if identity.Type != "object" || !slices.Contains(identity.Required, "name") {
			t.Fatalf("%s does not require name when identityRef is present: %#v", version.Name, identity)
		}
	}
}

func ruleForAPIGroup(t *testing.T, role, apiGroup string) string {
	t.Helper()
	start := strings.Index(role, "  - apiGroups:\n      - "+apiGroup+"\n")
	if start < 0 {
		t.Fatalf("ClusterRole rule for %s was not found:\n%s", apiGroup, role)
	}
	remainder := role[start+1:]
	if next := strings.Index(remainder, "\n  - apiGroups:\n"); next >= 0 {
		return role[start : start+1+next]
	}
	return role[start:]
}

func renderChart(t *testing.T, extra ...string) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	args := []string{"template", "dns-api", filepath.Dir(file), "--namespace", "appthrust-dns"}
	args = append(args, extra...)
	output, err := exec.Command("helm", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, output)
	}
	return string(output)
}

func renderedDocument(t *testing.T, rendered, apiVersion, kind string) string {
	t.Helper()
	for _, document := range strings.Split(rendered, "\n---") {
		if strings.Contains(document, "apiVersion: "+apiVersion+"\n") &&
			strings.Contains(document, "kind: "+kind+"\n") {
			return document
		}
	}
	t.Fatalf("rendered %s %s was not found:\n%s", apiVersion, kind, rendered)
	return ""
}
