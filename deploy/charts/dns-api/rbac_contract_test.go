package dnsapichart

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
