package dnsprobe

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	probes     = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "dns_probe_total", Help: "Completed DNS observations by provider."}, []string{"provider", "outcome", "qtype"})
	duration   = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "dns_probe_duration_seconds", Help: "Admitted DNS probe duration by provider."}, []string{"provider", "outcome"})
	queueDelay = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "dns_probe_queue_delay_seconds", Help: "Scheduling and limiter admission delay for DNS probes."}, []string{"provider"})
)

func init() { metrics.Registry.MustRegister(probes, duration, queueDelay) }

func observeQueueDelay(provider string, delay time.Duration) {
	queueDelay.WithLabelValues(provider).Observe(max(0, delay.Seconds()))
}

// observationOutcome describes transport evidence only. Provider publication
// evaluators decide exact-set success after this shared read-only boundary.
func observationOutcome(result ProbeResult) string {
	if result.Zone != nil {
		for _, view := range result.Zone.Views {
			if view.Failure != "" {
				return "failed"
			}
		}
	}
	for _, view := range result.Views {
		if view.Failure != "" || view.Mismatch {
			return "failed"
		}
	}
	return "observed"
}
