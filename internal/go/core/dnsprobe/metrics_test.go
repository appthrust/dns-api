package dnsprobe

import (
	"context"
	"testing"

	"github.com/miekg/dns"
	"github.com/prometheus/client_golang/prometheus"
)

func TestWireMetricsAttributeStaticProvider(t *testing.T) {
	server := localDNS(t, func(w dns.ResponseWriter, request *dns.Msg) {
		reply := new(dns.Msg)
		reply.SetReply(request)
		_ = w.WriteMsg(reply)
	})
	for _, provider := range []string{"metric-test-external", "metric-test-hosted"} {
		t.Cleanup(func() {
			probes.DeleteLabelValues(provider, "observed", "A")
			probes.DeleteLabelValues(provider, "observed", "AAAA")
			duration.DeleteLabelValues(provider, "observed")
			queueDelay.DeleteLabelValues(provider)
		})
		resolver, err := NewWireResolver(WireOptions{Provider: provider})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := resolver.Probe(context.Background(), ProbeRequest{Name: "www.example.test", Policy: Policy{Mode: ModeRecursive, Servers: []string{server}}}); err != nil {
			t.Fatal(err)
		}
	}
	registry := prometheus.NewRegistry()
	registry.MustRegister(probes, duration, queueDelay)
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]float64{}
	for _, family := range families {
		switch family.GetName() {
		case "dns_probe_total", "dns_probe_duration_seconds", "dns_probe_queue_delay_seconds":
		default:
			t.Fatalf("provider-specific metric leaked into transport: %s", family.GetName())
		}
		for _, metric := range family.Metric {
			provider := ""
			for _, label := range metric.Label {
				switch label.GetName() {
				case "provider":
					provider = label.GetValue()
				case "outcome", "qtype":
				default:
					t.Fatalf("unexpected metric label: %s", label.GetName())
				}
			}
			if provider == "" {
				t.Fatal("transport metric lacks provider attribution")
			}
			if family.GetName() == "dns_probe_total" {
				counts[provider] += metric.GetCounter().GetValue()
			}
		}
	}
	if counts["metric-test-external"] != 2 || counts["metric-test-hosted"] != 2 {
		t.Fatalf("queries attributed incorrectly: %v", counts)
	}
}
