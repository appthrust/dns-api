package withdrawal

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	pending      = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "dns_hosted_withdrawals_pending", Help: "Retained hosted DNS withdrawals awaiting verified exclusion."}, []string{"reason"})
	pendingMu    sync.Mutex
	pendingUnits = map[string]map[string]int{}
)

func init() { metrics.Registry.MustRegister(pending) }

// SetPending replaces one unit's contribution. Unit identities are internal
// keys, never labels. Passing nil removes the unit's contribution.
func SetPending(unitKey string, counts map[string]int) {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	for reason, count := range pendingUnits[unitKey] {
		pending.WithLabelValues(reason).Sub(float64(count))
	}
	delete(pendingUnits, unitKey)
	next := map[string]int{}
	for reason, count := range counts {
		switch reason {
		case "DNSStillResolves", "NegativeTTLNotElapsed", "DNSProbeFailed", "PolicyChanged", "InvalidWithdrawal", "NotAllowed", "UnsupportedProvider":
		default:
			reason = "DNSProbeFailed"
		}
		next[reason] += count
	}
	if len(next) != 0 {
		pendingUnits[unitKey] = next
	}
	for reason, count := range next {
		pending.WithLabelValues(reason).Add(float64(count))
	}
}
