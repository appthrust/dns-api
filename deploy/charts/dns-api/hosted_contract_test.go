package dnsapichart

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	hostedmanifest "github.com/appthrust/dns-api/internal/go/providers/hosted/manifest"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/yaml"
)

func TestHostedDefaultsToDisabled(t *testing.T) {
	rendered := renderChart(t)
	if explicit := renderChart(t, "--set", "providers.hosted.enabled=false"); rendered != explicit {
		t.Fatal("default render differs from explicitly disabled hosted provider")
	}
	for _, forbidden := range []string{"hosted.dns.appthrust.io", "# Source: dns-api/templates/provider-hosted.yaml", "# Source: dns-api/templates/hosted-server-", "--hosted-provider-", "--hosted-endpoint-ttl="} {
		if strings.Contains(rendered, forbidden) {
			t.Errorf("disabled render contains hosted declaration %q", forbidden)
		}
	}
	if !strings.Contains(rendered, "--hosted-controller-enabled=false") {
		t.Fatal("default manager must explicitly disable hosted")
	}
	renderChart(t, "--set", "webhook.enabled=false", "--set", "certManager.enabled=false")
}

func TestHostedOnlyRender(t *testing.T) {
	rendered := renderChart(t, "--set", "providers.hosted.enabled=true", "--set", "providers.route53.enabled=false", "--set", "providers.cloudflare.enabled=false")
	assertManifestEqual(t, hostedmanifest.ProviderYAML, renderedDocument(t, rendered, "dns.appthrust.io/v1alpha1", "Provider"))
	assertManifestEqual(t, hostedmanifest.CapabilityYAML, renderedDocument(t, rendered, "endpoint.dns.appthrust.io/v1alpha1", "EndpointProviderCapability"))
	manager := namedDocument(t, rendered, "Deployment", "dns-api-controller-manager")
	for _, arg := range []string{"--hosted-controller-enabled=true", "--hosted-controller-name=hosted.dns.appthrust.io/controller", "--hosted-provider-name=hosted.dns.appthrust.io", "--hosted-provider-version=v1alpha1", "--hosted-endpoint-ttl=60"} {
		if !strings.Contains(manager, arg) {
			t.Errorf("manager is missing %q", arg)
		}
	}
	for _, kind := range []string{"Provider", "EndpointProviderCapability", "APIService"} {
		if count := strings.Count(rendered, "kind: "+kind+"\n"); count != 1 {
			t.Errorf("got %d %s declarations, want exactly one", count, kind)
		}
	}
	var role rbacv1.ClusterRole
	decodeHostedDocument(t, namedDocument(t, rendered, "ClusterRole", "dns-api-controller-manager-role"), &role)
	var hostedRules []rbacv1.PolicyRule
	for _, rule := range role.Rules {
		for _, group := range rule.APIGroups {
			if group == "hosted.dns.appthrust.io" || group == "endpoint.hosted.dns.appthrust.io" {
				hostedRules = append(hostedRules, rule)
			}
			if strings.Contains(group, "route53") || strings.Contains(group, "cloudflare") {
				t.Errorf("hosted-only role includes cloud group %q", group)
			}
		}
		for _, resource := range rule.Resources {
			if resource == "secrets" || strings.Contains(resource, "identities") || resource == "hostedzones/status" {
				t.Errorf("manager has forbidden permission %q", resource)
			}
		}
	}
	want := []rbacv1.PolicyRule{
		{APIGroups: []string{"hosted.dns.appthrust.io"}, Resources: []string{"hostedzones"}, Verbs: []string{"create", "delete", "get", "list", "patch", "update", "watch"}},
		{APIGroups: []string{"endpoint.hosted.dns.appthrust.io"}, Resources: []string{"endpointrecordsetconversions"}, Verbs: []string{"create"}},
	}
	if !reflect.DeepEqual(hostedRules, want) {
		t.Fatalf("manager hosted permissions differ: %#v", hostedRules)
	}
	assertHostedTLS(t, rendered)
}

func TestHostedServerRender(t *testing.T) {
	for _, namespace := range []string{"", "tenant-dns"} {
		t.Run("watchNamespace="+namespace, func(t *testing.T) {
			rendered := renderChart(t, "--set", "providers.hosted.enabled=true", "--set", "providers.hosted.server.watchNamespace="+namespace)
			var deployment appsv1.Deployment
			decodeHostedDocument(t, namedDocument(t, rendered, "Deployment", "dns-api-hosted-server"), &deployment)
			pod := deployment.Spec.Template.Spec
			if deployment.Namespace != "appthrust-dns" || deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 1 || pod.ServiceAccountName != "dns-api-hosted-server" {
				t.Fatalf("unexpected server deployment: %#v", deployment.Spec)
			}
			if pod.SecurityContext == nil || pod.SecurityContext.RunAsNonRoot == nil || !*pod.SecurityContext.RunAsNonRoot {
				t.Fatal("server must run as non-root")
			}
			if len(pod.Containers) != 1 {
				t.Fatalf("got %d server containers", len(pod.Containers))
			}
			container := pod.Containers[0]
			if !reflect.DeepEqual(container.Command, []string{"/hosted-server"}) || container.SecurityContext == nil || container.SecurityContext.ReadOnlyRootFilesystem == nil || !*container.SecurityContext.ReadOnlyRootFilesystem || container.SecurityContext.AllowPrivilegeEscalation == nil || *container.SecurityContext.AllowPrivilegeEscalation {
				t.Fatalf("unexpected server command/security: %#v", container)
			}
			if container.ReadinessProbe.HTTPGet.Path != "/readyz" || container.LivenessProbe.HTTPGet.Path != "/healthz" {
				t.Fatal("server health probes differ")
			}
			if !strings.Contains(strings.Join(container.Args, "\n"), "--watch-namespace="+namespace) {
				t.Fatal("server watch namespace differs")
			}
			for name, field := range map[string]string{"POD_NAMESPACE": "metadata.namespace", "POD_NAME": "metadata.name"} {
				found := false
				for _, env := range container.Env {
					if env.Name == name && env.ValueFrom != nil && env.ValueFrom.FieldRef != nil && env.ValueFrom.FieldRef.FieldPath == field {
						found = true
					}
				}
				if !found {
					t.Errorf("missing downward API %s", name)
				}
			}
			var service corev1.Service
			decodeHostedDocument(t, namedDocument(t, rendered, "Service", "dns-api-hosted-server"), &service)
			if service.Spec.Type != corev1.ServiceTypeClusterIP || !reflect.DeepEqual(service.Spec.Selector, deployment.Spec.Selector.MatchLabels) {
				t.Fatalf("server service does not select server deployment: %#v", service.Spec)
			}
			for _, protocol := range []corev1.Protocol{corev1.ProtocolUDP, corev1.ProtocolTCP} {
				found := false
				for _, port := range service.Spec.Ports {
					if port.Protocol == protocol && port.Port == 53 && port.TargetPort == intstr.FromInt32(5353) {
						for _, containerPort := range container.Ports {
							if containerPort.Protocol == protocol && containerPort.ContainerPort == 5353 {
								found = true
							}
						}
					}
				}
				if !found {
					t.Errorf("missing port 53 -> 5353 for %s", protocol)
				}
			}
			var manager appsv1.Deployment
			decodeHostedDocument(t, namedDocument(t, rendered, "Deployment", "dns-api-controller-manager"), &manager)
			if manager.Spec.Selector.MatchLabels["app.kubernetes.io/component"] == service.Spec.Selector["app.kubernetes.io/component"] {
				t.Fatal("server and manager selectors overlap")
			}
			roleKind, bindingKind := "Role", "RoleBinding"
			if namespace == "" {
				roleKind, bindingKind = "ClusterRole", "ClusterRoleBinding"
			}
			var role rbacv1.Role
			decodeHostedDocument(t, namedDocument(t, rendered, roleKind, "dns-api-hosted-server"), &role)
			want := []rbacv1.PolicyRule{
				{APIGroups: []string{"hosted.dns.appthrust.io"}, Resources: []string{"hostedzones"}, Verbs: []string{"get", "list", "watch"}},
				{APIGroups: []string{"hosted.dns.appthrust.io"}, Resources: []string{"hostedzones/status"}, Verbs: []string{"patch"}},
			}
			if role.Namespace != namespace || !reflect.DeepEqual(role.Rules, want) {
				t.Fatalf("server permissions differ: %#v", role)
			}
			var binding rbacv1.RoleBinding
			decodeHostedDocument(t, namedDocument(t, rendered, bindingKind, "dns-api-hosted-server"), &binding)
			if binding.Namespace != namespace || binding.RoleRef.Kind != roleKind || binding.RoleRef.Name != role.Name || !reflect.DeepEqual(binding.Subjects, []rbacv1.Subject{{Kind: "ServiceAccount", Name: "dns-api-hosted-server", Namespace: "appthrust-dns"}}) {
				t.Fatalf("server binding differs: %#v", binding)
			}
			namedDocument(t, rendered, "ServiceAccount", "dns-api-hosted-server")
		})
	}
}

func TestHostedCustomBindingsAndService(t *testing.T) {
	rendered := renderChart(t,
		"--set", "providers.hosted.enabled=true", "--set", "fullnameOverride=custom-dns",
		"--set", "providers.hosted.controllerName=custom.example/controller", "--set", "providers.hosted.providerName=custom.example", "--set", "providers.hosted.providerVersion=v1beta1", "--set", "providers.hosted.endpointTTL=120",
		"--set", "providers.hosted.server.replicas=2", "--set", "providers.hosted.server.service.type=LoadBalancer", "--set", "providers.hosted.server.service.loadBalancerIP=192.0.2.53", "--set", "providers.hosted.server.service.annotations.test=value", "--set", "providers.hosted.server.service.ipFamilies={IPv4}",
		"--set", "providers.hosted.server.resources.requests.cpu=75m", "--set", "webhook.service.port=8443",
	)
	manager := namedDocument(t, rendered, "Deployment", "custom-dns-controller-manager")
	for _, arg := range []string{"--hosted-controller-name=custom.example/controller", "--hosted-provider-name=custom.example", "--hosted-provider-version=v1beta1", "--hosted-endpoint-ttl=120"} {
		if !strings.Contains(manager, arg) {
			t.Errorf("manager is missing %q", arg)
		}
	}
	if !strings.Contains(manager, "name: HOSTED_SERVER_ADDRESS") || !strings.Contains(manager, "custom-dns-hosted-server.appthrust-dns.svc:53") {
		t.Fatal("manager default verification view does not match renamed server Service")
	}
	provider := manifestMap(t, namedDocument(t, rendered, "Provider", "custom.example"))
	if provider["spec"].(map[string]any)["versions"].([]any)[0].(map[string]any)["name"] != "v1beta1" {
		t.Fatal("Provider version differs from binding")
	}
	capability := manifestMap(t, namedDocument(t, rendered, "EndpointProviderCapability", "hosted-v1beta1"))["spec"].(map[string]any)
	if !reflect.DeepEqual(capability["provider"], map[string]any{"name": "custom.example", "version": "v1beta1"}) || !reflect.DeepEqual(capability["conversion"], map[string]any{"group": "endpoint.hosted.dns.appthrust.io", "version": "v1alpha1", "resource": "endpointrecordsetconversions"}) {
		t.Fatalf("capability binding/GVR differs: %#v", capability)
	}
	var service corev1.Service
	decodeHostedDocument(t, namedDocument(t, rendered, "Service", "custom-dns-hosted-server"), &service)
	if service.Spec.Type != corev1.ServiceTypeLoadBalancer || service.Spec.LoadBalancerIP != "192.0.2.53" || service.Annotations["test"] != "value" || !reflect.DeepEqual(service.Spec.IPFamilies, []corev1.IPFamily{corev1.IPv4Protocol}) {
		t.Fatalf("custom server service differs: %#v", service)
	}
	var deployment appsv1.Deployment
	decodeHostedDocument(t, namedDocument(t, rendered, "Deployment", "custom-dns-hosted-server"), &deployment)
	if *deployment.Spec.Replicas != 2 || deployment.Spec.Template.Spec.Containers[0].Resources.Requests.Cpu().String() != "75m" {
		t.Fatal("custom server replicas/resources differ")
	}
	assertHostedTLS(t, rendered)
}

func TestHostedRequiresWebhookAndCertManager(t *testing.T) {
	for _, webhook := range []bool{false, true} {
		for _, certManager := range []bool{false, true} {
			if webhook && certManager {
				continue
			}
			t.Run(fmt.Sprintf("webhook=%t/certManager=%t", webhook, certManager), func(t *testing.T) {
				_, file, _, _ := runtime.Caller(0)
				output, err := exec.Command("helm", "template", "dns-api", filepath.Dir(file), "--set", "providers.hosted.enabled=true", "--set", "webhook.enabled="+strconv.FormatBool(webhook), "--set", "certManager.enabled="+strconv.FormatBool(certManager)).CombinedOutput()
				if err == nil || !strings.Contains(string(output), "providers.hosted.enabled requires webhook.enabled=true and certManager.enabled=true") {
					t.Fatalf("unexpected TLS enablement result: %v\n%s", err, output)
				}
			})
		}
	}
}

func TestHostedRBACCanBeExternallyManaged(t *testing.T) {
	rendered := renderChart(t, "--set", "providers.hosted.enabled=true", "--set", "rbac.create=false")
	for _, kind := range []string{"Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding"} {
		if strings.Contains(rendered, "kind: "+kind+"\n") {
			t.Errorf("rbac.create=false rendered %s", kind)
		}
	}
	namedDocument(t, rendered, "ServiceAccount", "dns-api-hosted-server")
}

func TestHostedCoexistsWithOtherProviders(t *testing.T) {
	rendered := renderChart(t, "--set", "providers.hosted.enabled=true", "--set", "providers.external.enabled=true")
	for _, name := range []string{"hosted.dns.appthrust.io", "external.dns.appthrust.io", "route53.dns.appthrust.io", "cloudflare.dns.appthrust.io"} {
		namedDocument(t, rendered, "Provider", name)
	}
	for _, name := range []string{"hosted", "external", "route53"} {
		namedDocument(t, rendered, "APIService", "v1alpha1.endpoint."+name+".dns.appthrust.io")
	}
	assertHostedTLS(t, rendered)
}

func namedDocument(t *testing.T, rendered, kind, name string) string {
	t.Helper()
	for _, document := range strings.Split(rendered, "\n---") {
		value := manifestMap(t, document)
		metadata, ok := value["metadata"].(map[string]any)
		if ok && value["kind"] == kind && metadata["name"] == name {
			return document
		}
	}
	t.Fatalf("rendered %s %s was not found", kind, name)
	return ""
}

func decodeHostedDocument(t *testing.T, document string, target any) {
	t.Helper()
	if err := yaml.Unmarshal([]byte(document), target); err != nil {
		t.Fatal(err)
	}
}

func assertHostedTLS(t *testing.T, rendered string) {
	t.Helper()
	apiService := manifestMap(t, namedDocument(t, rendered, "APIService", "v1alpha1.endpoint.hosted.dns.appthrust.io"))
	spec := apiService["spec"].(map[string]any)
	if _, exists := spec["insecureSkipTLSVerify"]; exists {
		t.Fatal("hosted APIService bypasses TLS verification")
	}
	if spec["group"] != "endpoint.hosted.dns.appthrust.io" || spec["version"] != "v1alpha1" {
		t.Fatalf("hosted APIService GVR differs: %#v", spec)
	}
	certificate := manifestMap(t, renderedDocument(t, rendered, "cert-manager.io/v1", "Certificate"))
	certMetadata := certificate["metadata"].(map[string]any)
	annotation := apiService["metadata"].(map[string]any)["annotations"].(map[string]any)["cert-manager.io/inject-ca-from"]
	if annotation != certMetadata["namespace"].(string)+"/"+certMetadata["name"].(string) {
		t.Fatal("CA injection does not reference serving Certificate")
	}
	serviceRef := spec["service"].(map[string]any)
	service := manifestMap(t, namedDocument(t, rendered, "Service", serviceRef["name"].(string)))
	if serviceRef["namespace"] != service["metadata"].(map[string]any)["namespace"] || serviceRef["port"] != service["spec"].(map[string]any)["ports"].([]any)[0].(map[string]any)["port"] {
		t.Fatal("APIService does not reference webhook Service")
	}
	wantDNSName := serviceRef["name"].(string) + "." + serviceRef["namespace"].(string) + ".svc"
	for _, name := range certificate["spec"].(map[string]any)["dnsNames"].([]any) {
		if name == wantDNSName {
			return
		}
	}
	t.Fatalf("Certificate does not cover webhook Service DNS name %s", wantDNSName)
}
