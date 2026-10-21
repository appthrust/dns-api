package verification

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	pending            = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "dns_external_pending_records", Help: "Active unverified external record claims."}, []string{"reason"})
	pendingMu          sync.Mutex
	pendingUnits       = map[string]map[string]int{}
	withdrawalsPending = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "dns_external_withdrawals_pending", Help: "Retained external DNS withdrawals awaiting verified exclusion."}, []string{"reason"})
	withdrawalUnits    = map[string]map[string]int{}
)

func init() { metrics.Registry.MustRegister(pending, withdrawalsPending) }

// SetPending replaces a unit's contribution; identities are internal keys, never labels.
func SetPending(unit string, counts map[string]int) {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	for reason, count := range pendingUnits[unit] {
		pending.WithLabelValues(reason).Sub(float64(count))
	}
	delete(pendingUnits, unit)
	next := map[string]int{}
	for reason, count := range counts {
		switch reason {
		case "Reconciling", "DNSNameNotFound", "DNSNoData", "ResolutionPartial", "ResolutionMismatch", "ResolutionInconsistent", "NameserverMismatch", "DNSProbeFailed":
		default:
			reason = "Reconciling"
		}
		next[reason] += count
	}
	if len(next) != 0 {
		pendingUnits[unit] = next
	}
	for reason, count := range next {
		pending.WithLabelValues(reason).Add(float64(count))
	}
}

// SetWithdrawalsPending uses only DEP's closed pending reasons as labels.
func SetWithdrawalsPending(unit string, counts map[string]int) {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	for reason, count := range withdrawalUnits[unit] {
		withdrawalsPending.WithLabelValues(reason).Sub(float64(count))
	}
	delete(withdrawalUnits, unit)
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
		withdrawalUnits[unit] = next
	}
	for reason, count := range next {
		withdrawalsPending.WithLabelValues(reason).Add(float64(count))
	}
}
