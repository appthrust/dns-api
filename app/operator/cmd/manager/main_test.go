package main

import "testing"

func TestProviderEnablementEnvironmentDefaults(t *testing.T) {
	for _, name := range []string{"ROUTE53_CONTROLLER_ENABLED", "CLOUDFLARE_CONTROLLER_ENABLED", "ROUTE53_ENDPOINT_CONVERSION_ENABLED"} {
		t.Run(name, func(t *testing.T) {
			for _, test := range []struct {
				value string
				want  bool
			}{
				{value: "", want: true},
				{value: "false", want: false},
				{value: "true", want: true},
			} {
				t.Setenv(name, test.value)
				if got := envBoolOrDefault(name, true); got != test.want {
					t.Fatalf("envBoolOrDefault(%q) with %q = %t, want %t", name, test.value, got, test.want)
				}
			}
		})
	}
}
