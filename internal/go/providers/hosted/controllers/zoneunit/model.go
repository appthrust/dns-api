package zoneunit

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/appthrust/dns-api/internal/go/core/dnsprobe"
	"github.com/appthrust/dns-api/internal/go/providers/hosted/withdrawal"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	hostedv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/hosted/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const MaxNamesPerPass = 32

type withdrawalSchedule struct {
	Identity       string            `json:"identity"`
	Policy         string            `json:"policy"`
	Binding        string            `json:"binding,omitempty"`
	OmissionSerial uint32            `json:"omissionSerial,omitempty"`
	OmissionDigest string            `json:"omissionDigest,omitempty"`
	Next           time.Time         `json:"next"`
	State          *withdrawal.State `json:"state,omitempty"`
}
type schedulerState struct {
	Withdrawals      map[string]withdrawalSchedule `json:"withdrawals,omitempty"`
	Publications     map[string]time.Time          `json:"publications,omitempty"`
	Cursor           string                        `json:"cursor,omitempty"`
	DelegationPolicy string                        `json:"delegationPolicy,omitempty"`
	DelegationNext   time.Time                     `json:"delegationNext,omitempty"`
	Probed           bool                          `json:"-"`
}
type delegationData struct {
	ObservedNameServers []string  `json:"observedNameServers"`
	Matches             bool      `json:"matches"`
	LastProbeTime       time.Time `json:"lastProbeTime"`
	Error               string    `json:"error,omitempty"`
}

func loadState(unit *dnsv1alpha1.ZoneUnit) schedulerState {
	var state schedulerState
	if unit.Status.Provider != nil {
		_ = json.Unmarshal(unit.Status.Provider.State.Raw, &state)
	}
	if state.Withdrawals == nil {
		state.Withdrawals = map[string]withdrawalSchedule{}
	}
	return state
}
func saveState(unit *dnsv1alpha1.ZoneUnit, state schedulerState) {
	state.Probed = false
	if len(state.Withdrawals) == 0 {
		state.Withdrawals = nil
	}
	if len(state.Publications) == 0 {
		state.Publications = nil
	}
	old := loadState(unit)
	old.Probed = false
	if len(old.Withdrawals) == 0 {
		old.Withdrawals = nil
	}
	if reflect.DeepEqual(state, old) {
		return
	}
	raw, _ := json.Marshal(state)
	if unit.Status.Provider == nil {
		unit.Status.Provider = &dnsv1alpha1.ProviderStatus{}
	}
	unit.Status.Provider.State.Raw = raw
}
func minDuration(a, b time.Duration) time.Duration {
	if a == 0 {
		return b
	}
	if b == 0 {
		return a
	}
	return min(a, b)
}
func nextServerExpiry(servers []hostedv1alpha1.HostedZoneServerStatus, now time.Time) time.Duration {
	var next time.Duration
	for _, server := range servers {
		next = minDuration(next, max(time.Second, server.ObservedAt.Add(ServerFreshness).Sub(now)+time.Second))
	}
	return next
}
func (r *ZoneReconciler) observeDelegation(ctx context.Context, unit *dnsv1alpha1.ZoneUnit, env *environment, state *schedulerState, now time.Time) time.Duration {
	if state.DelegationPolicy == env.fingerprint && state.DelegationNext.After(now) {
		return state.DelegationNext.Sub(now)
	}
	expected := make([]string, len(env.params.NameServers))
	for i, server := range env.params.NameServers {
		expected[i] = server.Name
	}
	expected = dnsprobe.Names(expected)
	policy := env.delegationPolicy
	policy.ExpectedNameservers = expected
	request := dnsprobe.ProbeRequest{Name: unit.Spec.Zone.DomainName, Zone: unit.Spec.Zone.DomainName, ZoneOnly: true, DueTime: now, Policy: policy}
	var observed dnsprobe.ProbeResult
	err := env.delegationError
	if err == nil {
		observed, err = r.Options.Resolver.Probe(ctx, request)
	}
	data := delegationData{ObservedNameServers: []string{}, LastProbeTime: r.Options.Clock()}
	if err != nil || observed.Zone == nil || len(observed.Zone.Views) != len(policy.Servers) {
		data.Error = "DNSProbeFailed"
	}
	if observed.Zone != nil {
		for _, view := range observed.Zone.Views {
			data.ObservedNameServers = append(data.ObservedNameServers, view.Nameservers...)
			if view.Failure != "" {
				data.Error = "DNSProbeFailed"
			}
		}
	}
	data.ObservedNameServers = dnsprobe.Names(data.ObservedNameServers)
	data.Matches = data.Error == "" && slices.Equal(data.ObservedNameServers, expected)
	if len(data.ObservedNameServers) > 64 {
		data.ObservedNameServers = data.ObservedNameServers[:64]
	}
	var public zoneData
	_ = json.Unmarshal(unit.Status.Zone.Provider.Data.Raw, &public)
	public.Delegation = &data
	unit.Status.Zone.Provider = providerData(public)
	state.DelegationPolicy = env.fingerprint
	state.DelegationNext = data.LastProbeTime.Add(300 * time.Second)
	state.Probed = true
	return state.DelegationNext.Sub(now)
}

// Explicit service discovery is only for the configured hosted Service, never a
// fallback for DNS observations. Evaluation receives concrete, bounded endpoints.
func resolveViews(ctx context.Context, configured []string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var views []string
	for _, endpoint := range configured {
		if address, err := netip.ParseAddrPort(endpoint); err == nil && address.Port() != 0 {
			views = append(views, address.String())
			continue
		}
		host, port, err := net.SplitHostPort(endpoint)
		if err != nil {
			return nil, fmt.Errorf("invalid hosted service endpoint")
		}
		addresses, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("hosted service discovery failed")
		}
		for _, address := range addresses {
			views = append(views, net.JoinHostPort(address.String(), port))
		}
	}
	slices.Sort(views)
	views = slices.Compact(views)
	if len(views) == 0 || len(views) > 3 {
		return nil, fmt.Errorf("hosted verification requires 1 through 3 resolved endpoints")
	}
	return views, nil
}

type readerClient struct {
	client.Client
	reader client.Reader
}

func (c readerClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if c.reader == nil {
		return c.Client.Get(ctx, key, obj, opts...)
	}
	return c.reader.Get(ctx, key, obj, opts...)
}

// Retain the positive-cache horizon of each served owner/address, including
// CNAME-chain TTLs. A later claim receives only its exact name/address horizon.
func retainPublication(state *schedulerState, spec hostedv1alpha1.HostedZoneSpec, servers []hostedv1alpha1.HostedZoneServerStatus, now time.Time) {
	for key, expires := range state.Publications {
		if !expires.After(now) {
			delete(state.Publications, key)
		}
	}
	// A single replica can populate caches before the all-replica serving gate
	// converges, so historical positive bounds deliberately use any exact ack.
	if !slices.ContainsFunc(servers, func(server hostedv1alpha1.HostedZoneServerStatus) bool {
		return server.Serial == spec.Serial && server.ContentDigest == spec.ContentDigest
	}) {
		return
	}
	byName := make(map[string][]hostedv1alpha1.HostedZoneRecord, len(spec.Records))
	for _, record := range spec.Records {
		byName[record.Name] = append(byName[record.Name], record)
	}
	if state.Publications == nil {
		state.Publications = map[string]time.Time{}
	}
	for name := range byName {
		current := name
		ttl := uint32(0)
		for range 9 {
			next := ""
			for _, record := range byName[current] {
				ttl = max(ttl, record.TTL)
				if record.Type == dnsv1alpha1.RecordTypeCNAME && len(record.Values) == 1 {
					target := dnsprobe.CanonicalName(record.Values[0])
					if target == spec.DomainName {
						next = "@"
					} else if strings.HasSuffix(target, "."+spec.DomainName) {
						next = strings.TrimSuffix(target, "."+spec.DomainName)
					}
				}
			}
			for _, record := range byName[current] {
				if record.Type != dnsv1alpha1.RecordTypeA && record.Type != dnsv1alpha1.RecordTypeAAAA {
					continue
				}
				for _, address := range record.Values {
					key := name + "/" + address
					expires := now.Add(time.Duration(ttl) * time.Second)
					if expires.After(state.Publications[key]) {
						state.Publications[key] = expires
					}
				}
			}
			if next == "" {
				break
			}
			current = next
		}
	}
}
