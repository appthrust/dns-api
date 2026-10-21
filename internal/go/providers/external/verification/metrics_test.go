package verification

import "testing"

func TestWithdrawalPendingMetricClosedReasons(t *testing.T) {
	const unit = "test-withdrawal-metric"
	t.Cleanup(func() { SetWithdrawalsPending(unit, nil) })
	SetWithdrawalsPending(unit, map[string]int{"private/unbounded": 1, "DNSStillResolves": 2})
	pendingMu.Lock()
	counts := withdrawalUnits[unit]
	if len(counts) != 2 || counts["DNSProbeFailed"] != 1 || counts["DNSStillResolves"] != 2 {
		t.Errorf("unbounded reason labels: %#v", counts)
	}
	pendingMu.Unlock()
	SetWithdrawalsPending(unit, nil)
	pendingMu.Lock()
	_, exists := withdrawalUnits[unit]
	pendingMu.Unlock()
	if exists {
		t.Fatal("deleted unit retained pending metrics")
	}
}
