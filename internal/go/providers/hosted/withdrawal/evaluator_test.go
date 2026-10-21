package withdrawal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/appthrust/dns-api/internal/go/core/dnsprobe"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	hostedv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/hosted/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type resolverFunc func(context.Context, dnsprobe.ProbeRequest) (dnsprobe.ProbeResult, error)

func (f resolverFunc) Probe(ctx context.Context, request dnsprobe.ProbeRequest) (dnsprobe.ProbeResult, error) {
	return f(ctx, request)
}

func fixture() Input {
	return Input{
		Claim:    dnsv1alpha1.ZoneUnitWithdrawal{ClaimNamespace: "app", ClaimName: "retired", Claim: dnsv1alpha1.WithdrawalObjectReference{Namespace: "app", Name: "retired", UID: "claim-1"}, ObservedGeneration: 1, Name: "www", Address: "192.0.2.10", Allowed: true},
		Snapshot: hostedv1alpha1.HostedZoneSpec{DomainName: "example.com", Serial: 1, ContentDigest: "sha256:snapshot", SOA: hostedv1alpha1.SOA{Minimum: 60}},
		Served:   true, Policy: dnsprobe.Policy{Mode: dnsprobe.ModeRecursive, Servers: []string{"192.0.2.53:53"}}, SOAMinimum: 60,
		Now: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC),
	}
}

func answers(request dnsprobe.ProbeRequest, address string, ttl uint32) dnsprobe.ProbeResult {
	result := dnsprobe.ProbeResult{}
	for _, server := range request.Policy.Servers {
		view := dnsprobe.View{ID: "recursive/" + server}
		for _, family := range []string{"A", "AAAA"} {
			answer := dnsprobe.AnswerEvidence{Name: request.Name, QType: family, NegativeTTL: &ttl}
			if family == "A" && address != "" {
				answer.Addresses, answer.PositiveTTL = []string{address}, &ttl
				view.A = []string{address}
			}
			view.Answers = append(view.Answers, answer)
		}
		result.Views = append(result.Views, view)
	}
	return result
}

func evaluate(t *testing.T, in Input, resolver dnsprobe.Resolver) Decision {
	t.Helper()
	got, err := New(resolver, func() time.Time { return in.Now }).Evaluate(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func absentResolver() dnsprobe.Resolver {
	return resolverFunc(func(_ context.Context, request dnsprobe.ProbeRequest) (dnsprobe.ProbeResult, error) {
		return answers(request, "", 1), nil
	})
}

func assertDecision(t *testing.T, got Decision, status metav1.ConditionStatus, reason string) {
	t.Helper()
	ready := metav1.ConditionFalse
	if status == metav1.ConditionTrue {
		ready = metav1.ConditionTrue
	}
	if got.Withdrawn != status || got.Ready != ready || got.Reason != reason || got.Message == "" || len(got.Message) > 2048 {
		t.Fatalf("decision=%+v, want %s/%s Ready=%s", got, status, reason, ready)
	}
}

func TestSnapshotAndEveryViewMustExclude(t *testing.T) {
	for _, mode := range []string{"snapshot", "view", "not served", "absent"} {
		t.Run(mode, func(t *testing.T) {
			in := fixture()
			in.Policy.Servers = append(in.Policy.Servers, "192.0.2.54:53")
			if mode == "snapshot" {
				in.Snapshot.Records = []hostedv1alpha1.HostedZoneRecord{{Name: "www", Type: "A", TTL: 600, Values: []string{in.Claim.Address}}}
			}
			in.Served = mode != "not served"
			got := evaluate(t, in, resolverFunc(func(_ context.Context, request dnsprobe.ProbeRequest) (dnsprobe.ProbeResult, error) {
				result := answers(request, "", 1)
				if mode == "view" {
					result.Views[1] = answers(request, in.Claim.Address, 600).Views[1]
				}
				return result, nil
			}))
			switch mode {
			case "snapshot", "view":
				assertDecision(t, got, metav1.ConditionFalse, "DNSStillResolves")
				if got.AbsentSince != nil || got.VerifiedAt != nil || len(got.State.Positive) == 0 {
					t.Fatal("positive evidence did not clear absence and retain its cache bound")
				}
			case "not served":
				assertDecision(t, got, metav1.ConditionUnknown, "DNSProbeFailed")
			default:
				assertDecision(t, got, metav1.ConditionFalse, "NegativeTTLNotElapsed")
			}
		})
	}
}

func TestAbsenceRequiresFreshConfirmationAfterSOAMinimum(t *testing.T) {
	in := fixture()
	in.Now = in.Now.Add(250 * time.Millisecond)
	first := evaluate(t, in, absentResolver())
	assertDecision(t, first, metav1.ConditionFalse, "NegativeTTLNotElapsed")
	horizon := in.Now.Truncate(time.Second).Add(61 * time.Second)
	if first.NotBefore == nil || !first.NotBefore.Time.Equal(horizon) {
		t.Fatalf("horizon=%v want %v", first.NotBefore, horizon)
	}
	// Public metav1.Time loses subsecond precision; private WindowStart must not.
	data, err := json.Marshal(first.State)
	if err != nil {
		t.Fatal(err)
	}
	in.Previous = &State{}
	if err := json.Unmarshal(data, in.Previous); err != nil {
		t.Fatal(err)
	}
	in.Now = horizon.Add(-time.Second)
	pending := evaluate(t, in, absentResolver())
	assertDecision(t, pending, metav1.ConditionFalse, "NegativeTTLNotElapsed")
	if !pending.NotBefore.Equal(first.NotBefore) {
		t.Fatal("repeated absence slid the horizon")
	}
	in.Previous, in.Now = pending.State, horizon
	good := evaluate(t, in, absentResolver())
	assertDecision(t, good, metav1.ConditionTrue, "WithdrawalVerified")
	if good.VerifiedAt == nil || good.VerifiedAt.Before(good.NotBefore) {
		t.Fatal("confirmation precedes cache safety horizon")
	}
	if good.Message != fmt.Sprintf("Retired address %s was verified absent from www in every required view at %s.", in.Claim.Address, horizon.Format(time.RFC3339)) {
		t.Fatalf("message=%s", good.Message)
	}
}

func TestZeroMinimumStillRequiresAnotherProbe(t *testing.T) {
	in := fixture()
	in.SOAMinimum = 0
	first := evaluate(t, in, absentResolver())
	in.Previous = first.State
	assertDecision(t, evaluate(t, in, absentResolver()), metav1.ConditionFalse, "NegativeTTLNotElapsed")
	in.Now = in.Now.Add(time.Second)
	assertDecision(t, evaluate(t, in, absentResolver()), metav1.ConditionTrue, "WithdrawalVerified")
}

func TestRetainedPositiveSurvivesFailureAndRestart(t *testing.T) {
	in := fixture()
	positive := evaluate(t, in, resolverFunc(func(_ context.Context, request dnsprobe.ProbeRequest) (dnsprobe.ProbeResult, error) {
		return answers(request, in.Claim.Address, 3600), nil
	}))
	in.Previous, in.Now = positive.State, in.Now.Add(30*time.Second)
	failed := evaluate(t, in, resolverFunc(func(_ context.Context, request dnsprobe.ProbeRequest) (dnsprobe.ProbeResult, error) {
		return answers(request, "", 1), errors.New("private transport detail")
	}))
	assertDecision(t, failed, metav1.ConditionUnknown, "DNSProbeFailed")
	if failed.Message != "Hosted DNS withdrawal cannot be verified for www.example.com in required-views: transport." {
		t.Fatalf("message=%s", failed.Message)
	}
	in.Previous, in.Now = failed.State, in.Now.Add(30*time.Second)
	pending := evaluate(t, in, absentResolver())
	if !pending.NotBefore.Time.Equal(fixture().Now.Add(time.Hour)) {
		t.Fatalf("positive horizon capped or lost: %v", pending.NotBefore)
	}
}

func TestPolicyChangesInvalidateWindowAndPreserveFloor(t *testing.T) {
	for _, change := range []string{"views", "minimum", "digest", "serial", "order"} {
		t.Run(change, func(t *testing.T) {
			in := fixture()
			in.Policy.Servers = append(in.Policy.Servers, "192.0.2.54:53")
			first := evaluate(t, in, absentResolver())
			in.Previous, in.Now = first.State, in.Now.Add(time.Minute)
			switch change {
			case "views":
				in.Policy.Servers = in.Policy.Servers[:1]
			case "minimum":
				in.SOAMinimum--
			case "digest":
				in.Snapshot.ContentDigest = "sha256:changed"
			case "serial":
				in.Snapshot.Serial++
			case "order":
				in.Policy.Servers[0], in.Policy.Servers[1] = in.Policy.Servers[1], in.Policy.Servers[0]
			}
			got := evaluate(t, in, absentResolver())
			if change == "serial" || change == "order" {
				assertDecision(t, got, metav1.ConditionTrue, "WithdrawalVerified")
				return
			}
			assertDecision(t, got, metav1.ConditionUnknown, "PolicyChanged")
			if got.Message != "Hosted DNS withdrawal policy changed for www; the prior required view policy remains a floor." || got.AbsentSince != nil {
				t.Fatalf("policy decision=%+v", got)
			}
			in.Previous, in.Now = got.State, in.Now.Add(30*time.Second)
			again := evaluate(t, in, absentResolver())
			if change == "digest" {
				assertDecision(t, again, metav1.ConditionFalse, "NegativeTTLNotElapsed")
			} else {
				assertDecision(t, again, metav1.ConditionUnknown, "PolicyChanged")
			}
		})
	}
}

func TestSnapshotCNAMEAndWildcardSemantics(t *testing.T) {
	for _, mode := range []string{"chain", "loop", "foreign", "wildcard", "closer encloser", "empty nonterminal", "ipv6", "mapped ipv6"} {
		t.Run(mode, func(t *testing.T) {
			in := fixture()
			in.Snapshot.Records = []hostedv1alpha1.HostedZoneRecord{{Name: "www", Type: "CNAME", TTL: 900, Values: []string{"target.example.com."}}, {Name: "target", Type: "A", TTL: 60, Values: []string{in.Claim.Address}}}
			want := "DNSStillResolves"
			switch mode {
			case "loop":
				in.Snapshot.Records[0].Values[0] = "www.example.com."
				want = "DNSProbeFailed"
			case "foreign":
				in.Snapshot.Records[0].Values[0] = "other.example.net."
				want = "NegativeTTLNotElapsed"
			case "wildcard", "closer encloser", "empty nonterminal":
				in.Claim.Name = "x.nested"
				in.Snapshot.Records = []hostedv1alpha1.HostedZoneRecord{{Name: "*", Type: "A", TTL: 600, Values: []string{in.Claim.Address}}}
				if mode == "closer encloser" {
					in.Snapshot.Records = append(in.Snapshot.Records, hostedv1alpha1.HostedZoneRecord{Name: "nested", Type: "TXT", TTL: 60, Values: []string{"\"exists\""}})
					want = "NegativeTTLNotElapsed"
				}
				if mode == "empty nonterminal" {
					in.Snapshot.Records = append(in.Snapshot.Records, hostedv1alpha1.HostedZoneRecord{Name: "child.x.nested", Type: "TXT", TTL: 60, Values: []string{"\"exists\""}})
					want = "NegativeTTLNotElapsed"
				}
			case "ipv6", "mapped ipv6":
				address := "2001:db8::1"
				in.Claim.Address = address
				if mode == "mapped ipv6" {
					in.Claim.Address = "192.0.2.10"
					address = "::ffff:192.0.2.10"
				}
				in.Snapshot.Records[1].Type, in.Snapshot.Records[1].Values = "AAAA", []string{address}
			}
			got := evaluate(t, in, absentResolver())
			if excluded := SnapshotExcludes(in.Snapshot, in.Claim); excluded != (want == "NegativeTTLNotElapsed") {
				t.Fatalf("snapshot helper excluded=%v, evaluator reason=%s", excluded, want)
			}
			status := metav1.ConditionFalse
			if want == "DNSProbeFailed" {
				status = metav1.ConditionUnknown
			}
			assertDecision(t, got, status, want)
			if mode == "chain" && !got.State.Positive["www.example.com/snapshot/A"].Equal(in.Now.Add(900*time.Second)) {
				t.Fatal("CNAME cache bound not retained")
			}
		})
	}
}

func TestWaiverEchoDoesNotAuthorizeDNS(t *testing.T) {
	in := fixture()
	in.Claim.Waiver = &dnsv1alpha1.ZoneUnitWithdrawalWaiver{Name: "emergency", UID: "waiver-1", Digest: "sha256:scope", ReservationRef: dnsv1alpha1.WithdrawalObjectReference{Namespace: "infra", Name: "reservation", UID: "reservation-1"}}
	first := evaluate(t, in, absentResolver())
	assertDecision(t, first, metav1.ConditionFalse, "NegativeTTLNotElapsed")
	want := &dnsv1alpha1.RecordWithdrawalWaiverReference{Name: "emergency", UID: "waiver-1", Digest: "sha256:scope", ReservationRef: in.Claim.Waiver.ReservationRef}
	if !reflect.DeepEqual(first.WaiverRef, want) || first.OverrideAccepted == nil || first.OverrideAccepted.Status != metav1.ConditionTrue || first.OverrideAccepted.Reason != "OperatorAuthorizedRelease" || first.OverrideAccepted.Message != "Core-validated withdrawal waiver is bound to its exact UID and immutable scope." {
		t.Fatalf("waiver=%+v condition=%+v", first.WaiverRef, first.OverrideAccepted)
	}
	in.Previous, in.Claim.Waiver = first.State, nil
	got := evaluate(t, in, absentResolver())
	if got.WaiverRef != nil || got.OverrideAccepted != nil {
		t.Fatal("revoked waiver echoed")
	}
}

func TestFreshnessIdentityAndFailures(t *testing.T) {
	for _, mode := range []string{"clock reversal", "stale", "claim recreated", "missing window", "missing history", "missing view", "failed probe"} {
		t.Run(mode, func(t *testing.T) {
			in := fixture()
			first := evaluate(t, in, absentResolver())
			in.Previous, in.Now = first.State, in.Now.Add(time.Minute)
			good := evaluate(t, in, absentResolver())
			in.Previous, in.Now = good.State, in.Now.Add(30*time.Second)
			resolver := absentResolver()
			want, status := "NegativeTTLNotElapsed", metav1.ConditionFalse
			switch mode {
			case "clock reversal":
				in.Now = good.State.LastProbe.Add(-time.Second)
			case "stale":
				in.Now = good.NextProbe.Add(301 * time.Second)
			case "claim recreated":
				in.Claim.Claim.UID = "claim-2"
			case "missing window":
				in.Previous.WindowStart = time.Time{}
			case "missing history":
				in.Previous.MissingHistory = true
				want, status = "PolicyChanged", metav1.ConditionUnknown
			case "missing view":
				resolver = resolverFunc(func(context.Context, dnsprobe.ProbeRequest) (dnsprobe.ProbeResult, error) {
					return dnsprobe.ProbeResult{}, nil
				})
				want, status = "DNSProbeFailed", metav1.ConditionUnknown
			case "failed probe":
				resolver = resolverFunc(func(context.Context, dnsprobe.ProbeRequest) (dnsprobe.ProbeResult, error) {
					return dnsprobe.ProbeResult{}, context.DeadlineExceeded
				})
				want, status = "DNSProbeFailed", metav1.ConditionUnknown
			}
			got := evaluate(t, in, resolver)
			assertDecision(t, got, status, want)
			if got.VerifiedAt != nil {
				t.Fatal("stale success survived")
			}
		})
	}
}

func TestWildcardWitnessAndPreviousStateRemainStable(t *testing.T) {
	in := fixture()
	in.Claim.Name = "*.project"
	first := evaluate(t, in, absentResolver())
	if len(first.CheckedNames) != 1 || !strings.HasPrefix(first.CheckedNames[0], "dns-retire-") || !strings.HasSuffix(first.CheckedNames[0], ".project.example.com") {
		t.Fatalf("names=%v", first.CheckedNames)
	}
	before, _ := json.Marshal(first.State)
	in.Previous, in.Now = first.State, in.Now.Add(time.Minute)
	evaluate(t, in, absentResolver())
	after, _ := json.Marshal(first.State)
	if string(before) != string(after) {
		t.Fatal("evaluation mutated previous state")
	}
	in.Claim.Claim.UID = "new-claim"
	recreated := evaluate(t, in, absentResolver())
	if reflect.DeepEqual(recreated.CheckedNames, first.CheckedNames) {
		t.Fatal("new claim reused old wildcard witness")
	}
}
