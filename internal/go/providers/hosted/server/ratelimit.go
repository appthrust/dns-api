package server

import (
	"container/list"
	"net/netip"
	"sync"
	"time"
)

const maxRateLimitSources = 65536
const sourceIdleTimeout = time.Minute

type sourceBucket struct {
	address netip.Addr
	tokens  float64
	updated time.Time
}

type sourceLimiter struct {
	mu      sync.Mutex
	rate    float64
	sources map[netip.Addr]*list.Element
	lru     list.List
}

func newSourceLimiter(rate int) *sourceLimiter {
	return &sourceLimiter{rate: float64(rate), sources: make(map[netip.Addr]*list.Element)}
}

// A source may burst one second's allowance. Idle entries are reclaimed, but
// active entries are never evicted to let rotating source addresses reset them.
func (l *sourceLimiter) allow(address netip.Addr, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if oldest := l.lru.Back(); oldest != nil && now.Sub(oldest.Value.(*sourceBucket).updated) >= sourceIdleTimeout {
		delete(l.sources, oldest.Value.(*sourceBucket).address)
		l.lru.Remove(oldest)
	}
	element := l.sources[address]
	if element == nil {
		if len(l.sources) >= maxRateLimitSources {
			return false
		}
		element = l.lru.PushFront(&sourceBucket{address: address, tokens: l.rate, updated: now})
		l.sources[address] = element
	}
	bucket := element.Value.(*sourceBucket)
	bucket.tokens = min(l.rate, bucket.tokens+max(0, now.Sub(bucket.updated).Seconds())*l.rate)
	bucket.updated = now
	l.lru.MoveToFront(element)
	if bucket.tokens < 1 {
		return false
	}
	bucket.tokens--
	return true
}
