package main

import (
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"strings"
	"testing"

	externalv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/external/v1alpha1"
)

func TestProviderEnablementEnvironmentDefaults(t *testing.T) {
	for name, fallback := range map[string]bool{
		"ROUTE53_CONTROLLER_ENABLED":          true,
		"CLOUDFLARE_CONTROLLER_ENABLED":       true,
		"ROUTE53_ENDPOINT_CONVERSION_ENABLED": true,
		"EXTERNAL_CONTROLLER_ENABLED":         false,
	} {
		t.Run(name, func(t *testing.T) {
			for _, test := range []struct {
				value string
				want  bool
			}{
				{value: "", want: fallback},
				{value: "false", want: false},
				{value: "true", want: true},
			} {
				t.Setenv(name, test.value)
				if got := envBoolOrDefault(name, fallback); got != test.want {
					t.Fatalf("envBoolOrDefault(%q) with %q = %t, want %t", name, test.value, got, test.want)
				}
			}
		})
	}
}

func TestExternalSchemeRegistered(t *testing.T) {
	if !scheme.IsVersionRegistered(externalv1alpha1.SchemeGroupVersion) {
		t.Fatal("external API scheme is not registered")
	}
}

// Run the real manager's flag binding in a subprocess. Help exits before any
// Kubernetes client, resolver, controller, or webhook is constructed.
func TestManagerFlagsProcess(t *testing.T) {
	if os.Getenv("DNS_API_TEST_MANAGER_FLAGS") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{os.Args[0]}, os.Args[i+1:]...)
			break
		}
	}
	flag.CommandLine = flag.NewFlagSet("manager", flag.ExitOnError)
	flag.CommandLine.Usage = func() {
		values := map[string]string{}
		flag.CommandLine.VisitAll(func(f *flag.Flag) { values[f.Name] = f.Value.String() })
		if err := json.NewEncoder(os.Stdout).Encode(values); err != nil {
			os.Exit(1)
		}
	}
	main()
}

func TestManagerProviderFlags(t *testing.T) {
	defaults := map[string]string{
		"route53-controller-enabled":          "true",
		"cloudflare-controller-enabled":       "true",
		"route53-endpoint-conversion-enabled": "true",
		"external-controller-enabled":         "false",
		"route53-controller-name":             "route53.dns.appthrust.io/controller",
		"route53-provider-name":               "route53.dns.appthrust.io",
		"route53-provider-version":            "v1alpha1",
		"cloudflare-controller-name":          "cloudflare.dns.appthrust.io/controller",
		"cloudflare-provider-name":            "cloudflare.dns.appthrust.io",
		"cloudflare-provider-version":         "v1alpha1",
		"external-controller-name":            "external.dns.appthrust.io/controller",
		"external-provider-name":              "external.dns.appthrust.io",
		"external-provider-version":           "v1alpha1",
		"external-dns-qps":                    "40",
		"external-dns-burst":                  "80",
	}
	for _, mode := range []string{"defaults", "environment", "flags-override-environment"} {
		t.Run(mode, func(t *testing.T) {
			env := append(os.Environ(), "DNS_API_TEST_MANAGER_FLAGS=1")
			args := []string{"-test.run=^TestManagerFlagsProcess$", "--"}
			expected := map[string]string{}
			for name, fallback := range defaults {
				value := ""
				want := fallback
				hasEnvironment := name != "external-dns-qps" && name != "external-dns-burst"
				if mode != "defaults" && hasEnvironment {
					if strings.HasSuffix(name, "-enabled") {
						value = "false"
						if fallback == "false" {
							value = "true"
						}
					} else {
						value = "environment-" + name
					}
					want = value
				}
				if hasEnvironment {
					env = append(env, strings.ToUpper(strings.ReplaceAll(name, "-", "_"))+"="+value)
				}
				if mode == "flags-override-environment" {
					want = fallback
					if name == "external-dns-qps" {
						want = "12.5"
					}
					if name == "external-dns-burst" {
						want = "25"
					}
					args = append(args, "--"+name+"="+want)
				}
				expected[name] = want
			}
			args = append(args, "--help")
			cmd := exec.Command(os.Args[0], args...)
			cmd.Env = env
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("manager flag parsing failed: %v\n%s", err, output)
			}
			var actual map[string]string
			if err := json.Unmarshal(output, &actual); err != nil {
				t.Fatalf("decode manager flags: %v\n%s", err, output)
			}
			for name, want := range expected {
				if actual[name] != want {
					t.Errorf("--%s = %q, want %q", name, actual[name], want)
				}
			}
		})
	}
}
