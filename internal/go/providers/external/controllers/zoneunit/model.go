package zoneunit

import (
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/appthrust/dns-api/internal/go/providers/external/probe"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const MaxNamesPerPass = 32

type schedule struct {
	Fingerprint string    `json:"fingerprint"`
	Next        time.Time `json:"next"`
	Failures    int       `json:"failures,omitempty"`
}

type schedulerState struct {
	Policy string              `json:"policy"`
	Cursor string              `json:"cursor,omitempty"`
	Zone   schedule            `json:"zone"`
	Names  map[string]schedule `json:"names"`
}

type nameGroup struct {
	Name            string
	CheckedName     string
	SampledWildcard bool
	Items           []int
	Expected        probe.RRsets
	Fingerprint     string
}

func groupNames(unit *dnsv1alpha1.ZoneUnit, policy string) []nameGroup {
	byName := map[string][]int{}
	for i, item := range unit.Spec.RecordSets {
		if item.IsAllowed() && !item.DeletionRequested && item.RecordSetUID != "" {
			byName[item.Name] = append(byName[item.Name], i)
		}
	}
	groups := make([]nameGroup, 0, len(byName))
	for name, indices := range byName {
		slices.SortFunc(indices, func(a, b int) int {
			return strings.Compare(itemKey(unit.Spec.RecordSets[a]), itemKey(unit.Spec.RecordSets[b]))
		})
		items := make([]dnsv1alpha1.ZoneUnitRecordSetSpec, 0, len(indices))
		group := nameGroup{Name: name, Items: indices}
		for _, index := range indices {
			item := unit.Spec.RecordSets[index]
			items = append(items, item)
			if item.A != nil {
				group.Expected.A = append(group.Expected.A, item.A.Addresses...)
			}
			if item.AAAA != nil {
				group.Expected.AAAA = append(group.Expected.AAAA, item.AAAA.Addresses...)
			}
			if item.CNAME != nil {
				group.Expected.CNAME = probe.CanonicalName(item.CNAME.Target)
			}
		}
		group.Expected.A, group.Expected.AAAA = probe.Addresses(group.Expected.A), probe.Addresses(group.Expected.AAAA)
		group.Fingerprint = probe.Fingerprint(struct {
			Policy string
			Items  []dnsv1alpha1.ZoneUnitRecordSetSpec
		}{policy, items})
		group.CheckedName = unit.Spec.Zone.DomainName
		if name != "@" {
			group.CheckedName = name + "." + group.CheckedName
		}
		if name == "*" || strings.HasPrefix(name, "*.") {
			group.SampledWildcard = true
			// Resolver policy does not alter the witness: only the Zone and sorted claim tuple do.
			tuple := make([]string, 0, len(items))
			for _, item := range items {
				tuple = append(tuple, probe.Fingerprint(struct {
					UID        string
					Generation int64
				}{string(item.RecordSetUID), item.ObservedGeneration}))
			}
			slices.Sort(tuple)
			witness := probe.Fingerprint(struct {
				UnitUID string
				Zone    dnsv1alpha1.ObjectReference
				Claims  []string
			}{string(unit.UID), unit.Spec.Zone.Ref, tuple})
			group.CheckedName = "dns-verify-" + witness[:24] + strings.TrimPrefix(group.CheckedName, "*")
		}
		groups = append(groups, group)
	}
	slices.SortFunc(groups, func(a, b nameGroup) int { return strings.Compare(a.Name, b.Name) })
	return groups
}

func itemKey(item dnsv1alpha1.ZoneUnitRecordSetSpec) string {
	return item.RecordSetNamespace + "/" + item.RecordSetName
}

func loadState(unit *dnsv1alpha1.ZoneUnit, policy string) schedulerState {
	state := schedulerState{}
	if unit.Status.Provider != nil {
		_ = json.Unmarshal(unit.Status.Provider.State.Raw, &state)
	}
	if state.Policy != policy {
		state = schedulerState{Policy: policy}
	}
	if state.Names == nil {
		state.Names = map[string]schedule{}
	}
	return state
}

func nextInterval(decision probe.Decision, failures int, jitter float64) time.Duration {
	seconds := 30.0
	switch {
	case decision.Status == metav1.ConditionUnknown:
		steps := [...]float64{30, 60, 120, 300}
		seconds = steps[min(max(failures-1, 0), len(steps)-1)]
	case decision.Status == metav1.ConditionTrue && decision.TTL != nil:
		seconds = float64(*decision.TTL)
	case (decision.Reason == "DNSNameNotFound" || decision.Reason == "DNSNoData") && decision.NegativeTTL != nil:
		seconds = float64(*decision.NegativeTTL)
	}
	seconds = max(30, min(300, seconds))
	seconds *= 1 + max(-1, min(1, jitter))*0.1
	return time.Duration(max(30, min(300, seconds)) * float64(time.Second))
}

func advance(previous schedule, fingerprint string, decision probe.Decision, now time.Time, jitter float64) schedule {
	failures := 0
	if decision.Status == metav1.ConditionUnknown {
		if previous.Fingerprint == fingerprint {
			failures = previous.Failures
		}
		failures++
	}
	return schedule{Fingerprint: fingerprint, Failures: failures, Next: now.Add(nextInterval(decision, failures, jitter))}
}

func selectDue(groups []nameGroup, state schedulerState, now time.Time) []int {
	indices := make([]int, 0, MaxNamesPerPass)
	start := 0
	for start < len(groups) && groups[start].Name <= state.Cursor {
		start++
	}
	for offset := 0; offset < len(groups) && len(indices) < MaxNamesPerPass; offset++ {
		index := (start + offset) % len(groups)
		group := groups[index]
		scheduled := state.Names[group.Name]
		if scheduled.Fingerprint != group.Fingerprint || !scheduled.Next.After(now) {
			indices = append(indices, index)
		}
	}
	return indices
}

func nextRequeue(groups []nameGroup, state schedulerState, now time.Time) time.Duration {
	next := 300 * time.Second
	if !state.Zone.Next.IsZero() {
		next = min(next, state.Zone.Next.Sub(now))
	}
	for _, group := range groups {
		scheduled := state.Names[group.Name]
		if scheduled.Fingerprint != group.Fingerprint || !scheduled.Next.After(now) {
			return time.Second
		}
		next = min(next, scheduled.Next.Sub(now))
	}
	return max(time.Second, next)
}
