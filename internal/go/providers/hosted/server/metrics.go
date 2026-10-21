package server

import "github.com/prometheus/client_golang/prometheus"

// The standalone server has its own registry, so independent Run instances and
// tests do not register duplicate collectors in controller-runtime's registry.
type serverMetrics struct {
	registry    *prometheus.Registry
	queries     *prometheus.CounterVec
	duration    prometheus.Histogram
	serial      *prometheus.GaugeVec
	loaded      prometheus.Gauge
	rateLimited prometheus.Counter
	rejected    *prometheus.CounterVec
}

func newMetrics() *serverMetrics {
	m := &serverMetrics{
		registry:    prometheus.NewRegistry(),
		queries:     prometheus.NewCounterVec(prometheus.CounterOpts{Name: "dns_hosted_queries_total", Help: "Completed authoritative DNS queries."}, []string{"zone", "qtype", "rcode"}),
		duration:    prometheus.NewHistogram(prometheus.HistogramOpts{Name: "dns_hosted_query_duration_seconds", Help: "Authoritative DNS query duration."}),
		serial:      prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "dns_hosted_zone_serial", Help: "Currently served zone serial."}, []string{"zone"}),
		loaded:      prometheus.NewGauge(prometheus.GaugeOpts{Name: "dns_hosted_zones_loaded", Help: "Number of authoritative snapshots loaded."}),
		rateLimited: prometheus.NewCounter(prometheus.CounterOpts{Name: "dns_hosted_udp_rate_limited_total", Help: "UDP requests dropped by the per-source rate limiter."}),
		rejected:    prometheus.NewCounterVec(prometheus.CounterOpts{Name: "dns_hosted_snapshots_rejected_total", Help: "Snapshot events rejected by ownership or apex arbitration."}, []string{"reason"}),
	}
	m.registry.MustRegister(m.queries, m.duration, m.serial, m.loaded, m.rateLimited, m.rejected)
	m.rejected.WithLabelValues("unowned").Add(0)
	m.rejected.WithLabelValues("apex_conflict").Add(0)
	return m
}
