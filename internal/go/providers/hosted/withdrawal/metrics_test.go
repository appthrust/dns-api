package withdrawal

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestPendingMetricsReplaceAggregateAndClear(t *testing.T) {
	const first, second = "app/first", "app/second"
	t.Cleanup(func() {
		SetPending(first, nil)
		SetPending(second, nil)
	})
	registry := prometheus.NewRegistry()
	registry.MustRegister(pending)
	values := func() map[string]float64 {
		t.Helper()
		families, err := registry.Gather()
		if err != nil {
			t.Fatal(err)
		}
		result := map[string]float64{}
		for _, family := range families {
			if family.GetName() != "dns_hosted_withdrawals_pending" {
				t.Fatalf("unexpected metric %q", family.GetName())
			}
			for _, metric := range family.Metric {
				if len(metric.Label) != 1 || metric.Label[0].GetName() != "reason" {
					t.Fatalf("unbounded labels: %v", metric.Label)
				}
				result[metric.Label[0].GetValue()] = metric.GetGauge().GetValue()
			}
		}
		return result
	}
	counts := map[string]int{"DNSStillResolves": 2, "private/unbounded": 1}
	SetPending(first, counts)
	counts["DNSStillResolves"] = 99 // Caller-owned input must not mutate contributions.
	SetPending(second, map[string]int{"DNSStillResolves": 3})
	got := values()
	if got["DNSStillResolves"] != 5 || got["DNSProbeFailed"] != 1 || len(got) != 2 {
		t.Fatalf("aggregate=%v", got)
	}
	SetPending(first, map[string]int{"NegativeTTLNotElapsed": 1})
	got = values()
	if got["DNSStillResolves"] != 3 || got["DNSProbeFailed"] != 0 || got["NegativeTTLNotElapsed"] != 1 {
		t.Fatalf("replacement=%v", got)
	}
	SetPending(first, nil)
	SetPending(second, nil)
	for reason, value := range values() {
		if value != 0 {
			t.Fatalf("cleared %s=%v", reason, value)
		}
	}
	pendingMu.Lock()
	defer pendingMu.Unlock()
	if len(pendingUnits) != 0 {
		t.Fatalf("unit contribution leaked: %v", pendingUnits)
	}
}
