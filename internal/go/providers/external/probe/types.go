// Package probe observes DNS using QUERY only. It has no DNS mutation interface.
package probe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/netip"
	"slices"
	"strings"
	"time"

	externalv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/external/v1alpha1"
)

type Resolver interface {
	Probe(context.Context, ProbeRequest) (ProbeResult, error)
}

type RRsets struct {
	A     []string `json:"a,omitempty"`
	AAAA  []string `json:"aaaa,omitempty"`
	CNAME string   `json:"cname,omitempty"`
}

type ProbeRequest struct {
	Name     string
	Zone     string
	Expected RRsets
	Policy   externalv1alpha1.ExternalZoneClassParameters
	// DueTime lets the wire resolver measure scheduling plus limiter queue delay once.
	DueTime time.Time
	// ZoneOnly collects apex/NS-host work once for this Zone's probe cycle.
	ZoneOnly bool
	// Evidence is cycle-local, never a process-wide DNS answer cache.
	Evidence *ZoneEvidence
	// WithdrawalAddress selects exclusion evaluation, never exact-set publication.
	WithdrawalAddress string
}

type ProbeResult struct {
	Views []View
	Zone  *ZoneEvidence
}

type View struct {
	ID            string
	RCode         int
	A             []string
	AAAA          []string
	Chain         []string
	Authoritative bool
	TTL           *uint32
	NegativeTTL   *uint32
	// Failure is a closed diagnostic code; raw transport errors never enter status.
	Failure  string
	Mismatch bool
	// Answers retains each chain query's complete cache evidence. Publication's
	// minimum-TTL summaries are not sufficient to authorize address reuse.
	Answers []AnswerEvidence
}

type AuthorityObservation struct {
	ID            string
	Nameservers   []string
	SOAOwner      string
	Authoritative bool
	TTL           *uint32
	NegativeTTL   *uint32
	Failure       string
}

type ZoneEvidence struct {
	PolicyFingerprint string
	Zone              string
	Views             []AuthorityObservation
	Authorities       []string
}

func CanonicalName(name string) string { return strings.TrimSuffix(strings.ToLower(name), ".") }

func Names(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, CanonicalName(value))
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func Addresses(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if ip, err := netip.ParseAddr(value); err == nil {
			value = ip.Unmap().String()
		}
		out = append(out, value)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func Fingerprint(value any) string {
	data, _ := json.Marshal(value)
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func minTTL(current *uint32, next *uint32) *uint32 {
	if next == nil {
		return current
	}
	if current == nil || *next < *current {
		value := *next
		return &value
	}
	return current
}
