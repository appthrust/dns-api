// Package withdrawal verifies served-snapshot and all-view address exclusion.
package withdrawal

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/appthrust/dns-api/internal/go/core/dnsprobe"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	hostedv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/hosted/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type Evaluator interface {
	Evaluate(ctx context.Context, in Input) (Decision, error)
}

type Input struct {
	Claim    dnsv1alpha1.ZoneUnitWithdrawal
	Snapshot hostedv1alpha1.HostedZoneSpec
	Served   bool
	// Policy contains resolved verification views. Delegation observation uses
	// a separate probe policy in the controller, not withdrawal authority gates.
	Policy     dnsprobe.Policy
	SOAMinimum uint32
	Now        time.Time
	Previous   *State
}

type Decision struct {
	Withdrawn         metav1.ConditionStatus
	Ready             metav1.ConditionStatus
	Reason            string
	Message           string
	AbsentSince       *metav1.Time
	NotBefore         *metav1.Time
	VerifiedAt        *metav1.Time
	CheckedNames      []string
	RequiredViews     []string
	ProviderData      map[string]any
	NextProbe         time.Time
	State             *State
	PolicyFingerprint string
	WaiverRef         *dnsv1alpha1.RecordWithdrawalWaiverReference
	OverrideAccepted  *metav1.Condition
}

type evaluator struct {
	resolver dnsprobe.Resolver
	clock    func() time.Time
}

// New does not discover views or fall back to an unconfigured resolver. The
// controller supplies explicit IP:port endpoints, including the default Service.
func New(resolver dnsprobe.Resolver, clock func() time.Time) Evaluator {
	if clock == nil {
		clock = time.Now
	}
	return &evaluator{resolver: resolver, clock: clock}
}

func (e *evaluator) Evaluate(ctx context.Context, in Input) (Decision, error) {
	started := e.clock()
	now := in.Now
	if now.IsZero() {
		now = started
	}
	state := initialState(in)
	views := slices.Clone(in.Policy.Servers)
	slices.Sort(views)
	probePolicy := in.Policy
	probePolicy.Servers = views
	probePolicy.ExpectedNameservers = slices.Clone(probePolicy.ExpectedNameservers)
	slices.Sort(probePolicy.ExpectedNameservers)
	if probePolicy.Mode == "" {
		probePolicy.Mode = dnsprobe.ModeRecursive
	}
	required := make([]string, len(views))
	for i, view := range views {
		required[i] = "recursive/" + view
	}
	policy := dnsprobe.Fingerprint(struct {
		Policy         dnsprobe.Policy
		SOAMinimum     uint32
		SnapshotDigest string
	}{probePolicy, in.SOAMinimum, in.Snapshot.ContentDigest})
	changed := state.Policy != "" && state.Policy != policy
	weakened := state.MissingHistory || in.SOAMinimum < state.SOAMinimum
	for _, floor := range state.Views {
		weakened = weakened || !slices.Contains(required, floor)
	}
	if !state.LastProbe.IsZero() && (now.Before(state.LastProbe) || state.NextProbe.IsZero() || now.After(state.NextProbe.Add(300*time.Second))) {
		state.invalidate()
	}
	if changed || weakened {
		state.invalidate()
	}

	decision := dnsprobe.Decision{Status: metav1.ConditionTrue, Reason: "WithdrawalVerified"}
	valid := validClaim(in)
	switch {
	case !valid:
		decision = failed(in.Claim.Name, "required-views", "invalid-response")
	case e.resolver == nil || dnsprobe.ValidateServers(views) != nil:
		decision = failed(in.Claim.Name, "configured-resolvers", "resolver-config")
	default:
		for _, name := range state.CheckedNames {
			snapshot := snapshotExclusion(&in.Snapshot, name, in.Claim.Address)
			state.retainPositive(name, snapshot.Cache, now)
			decision = combine(decision, snapshot.Decision)
			request := dnsprobe.ProbeRequest{Name: name, Zone: in.Snapshot.DomainName, Policy: probePolicy, DueTime: state.NextProbe, WithdrawalAddress: in.Claim.Address}
			observed, err := e.resolver.Probe(ctx, request)
			one := dnsprobe.EvaluateWithdrawal(request, observed)
			elapsed := e.clock().Sub(started)
			state.retainPositive(name, one.Cache, now.Add(max(elapsed, 0)))
			if err != nil {
				one.Decision = failed(name, "required-views", failureCode(err))
			}
			decision = combine(decision, one.Decision)
		}
		if !in.Served && decision.Status == metav1.ConditionTrue {
			decision = failed(in.Claim.Name, "served-snapshot", "invalid-response")
		}
	}
	elapsed := e.clock().Sub(started)
	if elapsed < 0 || elapsed > 300*time.Second {
		decision = failed(in.Claim.Name, "required-views", "timeout")
	} else {
		now = now.Add(elapsed)
	}
	if !state.LastProbe.IsZero() && (now.Before(state.LastProbe) || state.NextProbe.IsZero() || now.After(state.NextProbe.Add(300*time.Second))) {
		state.invalidate()
	}
	if changed || weakened {
		decision = dnsprobe.Decision{Status: metav1.ConditionUnknown, Reason: "PolicyChanged", Message: fmt.Sprintf("Hosted DNS withdrawal policy changed for %s; the prior required view policy remains a floor.", in.Claim.Name)}
	}
	// Failed probes cannot lower the policy floor or discard retained positives.
	if valid && !weakened && dnsprobe.ValidateServers(views) == nil {
		state.Views = slices.Clone(required)
		state.SOAMinimum = max(state.SOAMinimum, in.SOAMinimum)
	}
	state.Policy = policy
	if decision.Status == metav1.ConditionTrue {
		decision = state.window(in.SOAMinimum, now)
	} else {
		state.invalidate()
	}
	switch decision.Reason {
	case "NegativeTTLNotElapsed":
		decision.Message = fmt.Sprintf("Retired address %s is absent in every required view; waiting until %s for cache safety and a confirming probe.", in.Claim.Address, state.NotBefore.UTC().Format(time.RFC3339))
	case "WithdrawalVerified":
		decision.Message = fmt.Sprintf("Retired address %s was verified absent from %s in every required view at %s.", in.Claim.Address, in.Claim.Name, now.UTC().Format(time.RFC3339))
	}
	state.schedule(decision.Status, changed, now)
	out := Decision{
		Withdrawn: decision.Status, Ready: metav1.ConditionFalse, Reason: decision.Reason,
		Message:     strings.ReplaceAll(decision.Message, "DNS withdrawal cannot", "Hosted DNS withdrawal cannot"),
		AbsentSince: state.AbsentSince.DeepCopy(), NotBefore: state.NotBefore.DeepCopy(), VerifiedAt: state.VerifiedAt.DeepCopy(),
		CheckedNames: slices.Clone(state.CheckedNames), RequiredViews: required,
		ProviderData: map[string]any{"views": dnsprobe.Preview(required)}, NextProbe: state.NextProbe,
		State: state, PolicyFingerprint: policy,
	}
	if len(out.Message) > 2048 {
		out.Message = out.Message[:2048]
	}
	if out.Withdrawn == metav1.ConditionTrue {
		out.Ready = metav1.ConditionTrue
	}
	if valid {
		bindWaiver(&out, in.Claim, now)
	}
	return out, nil
}

func validClaim(in Input) bool {
	item := in.Claim
	ip, err := netip.ParseAddr(item.Address)
	if err != nil || ip.Zone() != "" || ip.Unmap().String() != item.Address || !item.Allowed || item.Claim.UID == "" || item.ClaimNamespace == "" || item.ClaimName == "" || item.Claim.Namespace != item.ClaimNamespace || item.Claim.Name != item.ClaimName || item.ObservedGeneration < 1 || in.Snapshot.ContentDigest == "" || !dnsprobe.ValidName(in.Snapshot.DomainName) {
		return false
	}
	name := in.Snapshot.DomainName
	if item.Name != "@" && item.Name != "*" {
		name = strings.TrimPrefix(item.Name, "*.") + "." + name
	}
	return dnsprobe.ValidName(name)
}

func failed(name, view, code string) dnsprobe.Decision {
	return dnsprobe.WithdrawalFailed(name, view, code).Decision
}

func failureCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	switch err.Error() {
	case "resolver-config", "invalid-response", "probe-limit", "chain-limit":
		return err.Error()
	}
	return "transport"
}

func combine(current, next dnsprobe.Decision) dnsprobe.Decision {
	if next.Status == metav1.ConditionUnknown || (next.Status == metav1.ConditionFalse && current.Status == metav1.ConditionTrue) {
		return next
	}
	return current
}

func bindWaiver(out *Decision, claim dnsv1alpha1.ZoneUnitWithdrawal, now time.Time) {
	waiver := claim.Waiver
	if waiver == nil || waiver.Name == "" || waiver.UID == "" || waiver.Digest == "" || waiver.ReservationRef.UID == "" {
		return
	}
	out.WaiverRef = &dnsv1alpha1.RecordWithdrawalWaiverReference{Name: waiver.Name, UID: waiver.UID, Digest: waiver.Digest, ReservationRef: waiver.ReservationRef}
	out.OverrideAccepted = &metav1.Condition{Type: "OverrideAccepted", Status: metav1.ConditionTrue, Reason: "OperatorAuthorizedRelease", Message: "Core-validated withdrawal waiver is bound to its exact UID and immutable scope.", ObservedGeneration: claim.ObservedGeneration, LastTransitionTime: metav1.NewTime(now)}
}

// SnapshotExcludes evaluates only the local snapshot term. It is not serving
// evidence and never replaces the all-view probes and continuous absence window.
func SnapshotExcludes(spec hostedv1alpha1.HostedZoneSpec, claim dnsv1alpha1.ZoneUnitWithdrawal) bool {
	in := Input{Snapshot: spec, Claim: claim}
	if !validClaim(in) {
		return false
	}
	name := checkedName(&in, claimIdentity(&in))
	return snapshotExclusion(&spec, name, claim.Address).Status == metav1.ConditionTrue
}

// snapshotExclusion follows only links served by this snapshot. Foreign links
// require complete terminal evidence from every probe view, never a local guess.
func snapshotExclusion(spec *hostedv1alpha1.HostedZoneSpec, checked, address string) dnsprobe.WithdrawalDecision {
	decision := dnsprobe.WithdrawalDecision{Decision: dnsprobe.Decision{Status: metav1.ConditionTrue, Reason: "WithdrawalVerified"}}
	retired, _ := netip.ParseAddr(address) // validClaim has already checked it.
	name, ttl := checked, uint32(0)
	var visited [dnsprobe.MaxCNAMEs + 1]string
	for depth := 0; depth <= dnsprobe.MaxCNAMEs; depth++ {
		if name != spec.DomainName && !strings.HasSuffix(name, "."+spec.DomainName) {
			return decision
		}
		if slices.Contains(visited[:depth], name) {
			return dnsprobe.WithdrawalFailed(checked, "snapshot", "chain-limit")
		}
		visited[depth] = name
		owner := snapshotOwner(spec, name)
		var cname *hostedv1alpha1.HostedZoneRecord
		for i := range spec.Records {
			record := &spec.Records[i]
			if record.Name != owner {
				continue
			}
			if record.Type == "CNAME" {
				cname = record
				continue
			}
			if record.Type != "A" && record.Type != "AAAA" {
				continue
			}
			for _, value := range record.Values {
				ip, err := netip.ParseAddr(value)
				if err != nil || ip.Zone() != "" || (record.Type == "A" && !ip.Is4()) || (record.Type == "AAAA" && !ip.Is6()) {
					return dnsprobe.WithdrawalFailed(checked, "snapshot", "invalid-response")
				}
				if ip.Unmap() == retired {
					bound := max(ttl, record.TTL)
					decision.Cache = append(decision.Cache, dnsprobe.WithdrawalCacheEvidence{Name: name, View: "snapshot", QType: string(record.Type), PositiveTTL: &bound})
					decision.Status, decision.Reason = metav1.ConditionFalse, "DNSStillResolves"
					decision.Message = fmt.Sprintf("DNS %s still resolves to retired address %s in snapshot (%s).", checked, address, record.Type)
				}
			}
		}
		if cname == nil || decision.Status == metav1.ConditionFalse {
			return decision
		}
		if len(cname.Values) != 1 || !dnsprobe.ValidName(dnsprobe.CanonicalName(cname.Values[0])) {
			return dnsprobe.WithdrawalFailed(checked, "snapshot", "invalid-response")
		}
		ttl = max(ttl, cname.TTL)
		name = dnsprobe.CanonicalName(cname.Values[0])
	}
	return dnsprobe.WithdrawalFailed(checked, "snapshot", "chain-limit")
}

// Existing owners and empty nonterminals block wildcard synthesis. Otherwise
// only the wildcard below the closest existing encloser can answer (RFC 4592).
func snapshotOwner(spec *hostedv1alpha1.HostedZoneSpec, name string) string {
	relative := "@"
	if name != spec.DomainName {
		relative = strings.TrimSuffix(name, "."+spec.DomainName)
	}
	if snapshotNodeExists(spec, relative) {
		return relative
	}
	for relative != "@" {
		_, parent, found := strings.Cut(relative, ".")
		if !found {
			parent = "@"
		}
		if snapshotNodeExists(spec, parent) {
			if parent == "@" {
				return "*"
			}
			return "*." + parent
		}
		relative = parent
	}
	return "@"
}

func snapshotNodeExists(spec *hostedv1alpha1.HostedZoneSpec, relative string) bool {
	if relative == "@" {
		return true
	}
	suffix := "." + relative
	for i := range spec.Records {
		name := spec.Records[i].Name
		if name == relative || strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}
