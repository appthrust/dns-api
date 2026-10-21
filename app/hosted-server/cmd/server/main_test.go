package main

import (
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"testing"
)

// Exercise the real entrypoint's flags; help exits before the Kubernetes client
// and DNS listeners are constructed.
func TestServerFlagsProcess(t *testing.T) {
	if os.Getenv("DNS_API_TEST_SERVER_FLAGS") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{os.Args[0]}, os.Args[i+1:]...)
			break
		}
	}
	flag.CommandLine = flag.NewFlagSet("hosted-server", flag.ExitOnError)
	flag.CommandLine.Usage = func() {
		values := map[string]string{}
		flag.CommandLine.VisitAll(func(f *flag.Flag) { values[f.Name] = f.Value.String() })
		if err := json.NewEncoder(os.Stdout).Encode(values); err != nil {
			os.Exit(1)
		}
	}
	main()
}

func TestServerFlags(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want map[string]string
	}{
		{
			name: "defaults",
			want: map[string]string{
				"listen-address":            ":5353",
				"watch-namespace":           "",
				"max-zones":                 "4096",
				"udp-rate-limit":            "1000",
				"metrics-bind-address":      ":8080",
				"health-probe-bind-address": ":8081",
			},
		},
		{
			name: "overrides",
			args: []string{"--listen-address=:1053", "--watch-namespace=tenant-dns", "--max-zones=16", "--udp-rate-limit=25", "--metrics-bind-address=0", "--health-probe-bind-address=0"},
			want: map[string]string{
				"listen-address":            ":1053",
				"watch-namespace":           "tenant-dns",
				"max-zones":                 "16",
				"udp-rate-limit":            "25",
				"metrics-bind-address":      "0",
				"health-probe-bind-address": "0",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string{"-test.run=^TestServerFlagsProcess$", "--"}, test.args...)
			args = append(args, "--help")
			cmd := exec.Command(os.Args[0], args...)
			cmd.Env = append(os.Environ(), "DNS_API_TEST_SERVER_FLAGS=1")
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("server flag parsing failed: %v\n%s", err, output)
			}
			var actual map[string]string
			if err := json.Unmarshal(output, &actual); err != nil {
				t.Fatalf("decode server flags: %v\n%s", err, output)
			}
			for name, want := range test.want {
				if actual[name] != want {
					t.Errorf("--%s = %q, want %q", name, actual[name], want)
				}
			}
		})
	}
}
