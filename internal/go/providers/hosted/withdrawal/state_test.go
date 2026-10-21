package withdrawal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/appthrust/dns-api/internal/go/core/dnsprobe"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPublicationSeedRetainsOnlyPositiveBounds(t *testing.T) {
	in := fixture()
	expires := in.Now.Add(time.Hour)
	in.Previous = &State{
		Positive: map[string]time.Time{"www.example.com/publication/A": expires},
		Policy:   "unrelated-policy", Views: []string{"unrelated-view"},
		AbsentSince: timePointer(in.Now.Add(-time.Hour)), WindowStart: in.Now.Add(-time.Hour),
		VerifiedAt: timePointer(in.Now), NotBefore: timePointer(in.Now),
	}
	got := evaluate(t, in, absentResolver())
	assertDecision(t, got, metav1.ConditionFalse, "NegativeTTLNotElapsed")
	if !got.NotBefore.Time.Equal(expires) || !got.AbsentSince.Time.Equal(in.Now) || got.VerifiedAt != nil {
		t.Fatalf("seed reused window or lost positive: %+v", got)
	}
	got.State.Positive["www.example.com/publication/A"] = in.Now
	if !in.Previous.Positive["www.example.com/publication/A"].Equal(expires) {
		t.Fatal("seed map aliases caller state")
	}
	in.Previous.Identity = "another-claim"
	got = evaluate(t, in, absentResolver())
	if !got.NotBefore.Time.Equal(in.Now.Add(time.Minute)) {
		t.Fatal("mismatched claim inherited positive seed")
	}
}

func TestUnavailableBackoffAndCompletedReset(t *testing.T) {
	in := fixture()
	resolver := resolverFunc(func(context.Context, dnsprobe.ProbeRequest) (dnsprobe.ProbeResult, error) {
		return dnsprobe.ProbeResult{}, errors.New("unavailable")
	})
	for _, seconds := range []int{30, 60, 120, 300, 300} {
		got := evaluate(t, in, resolver)
		assertDecision(t, got, metav1.ConditionUnknown, "DNSProbeFailed")
		if got.NextProbe.Sub(in.Now) != time.Duration(seconds)*time.Second {
			t.Fatalf("next=%s want %ds", got.NextProbe.Sub(in.Now), seconds)
		}
		in.Previous, in.Now = got.State, got.NextProbe
	}
	got := evaluate(t, in, absentResolver())
	if got.State.Failures != 0 || got.NextProbe.Sub(in.Now) != 30*time.Second {
		t.Fatal("completed observation did not reset backoff")
	}
}

func TestLateProbeCannotReuseWindow(t *testing.T) {
	for _, elapsed := range []time.Duration{90 * time.Second, 301 * time.Second, -time.Second} {
		t.Run(elapsed.String(), func(t *testing.T) {
			in := fixture()
			first := evaluate(t, in, absentResolver())
			in.Previous = first.State
			in.Now = first.NextProbe.Add(250 * time.Second)
			clock := in.Now
			resolver := resolverFunc(func(_ context.Context, request dnsprobe.ProbeRequest) (dnsprobe.ProbeResult, error) {
				clock = clock.Add(elapsed)
				return answers(request, "", 1), nil
			})
			got, err := New(resolver, func() time.Time { return clock }).Evaluate(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			if elapsed == 90*time.Second {
				assertDecision(t, got, metav1.ConditionFalse, "NegativeTTLNotElapsed")
				if !got.AbsentSince.Time.Equal(clock) {
					t.Fatal("stale window reused by delayed observation")
				}
			} else {
				assertDecision(t, got, metav1.ConditionUnknown, "DNSProbeFailed")
			}
		})
	}
}

func TestMissingCheckedNamesCannotProveVacuousAbsence(t *testing.T) {
	in := fixture()
	first := evaluate(t, in, absentResolver())
	in.Previous, in.Now = first.State, in.Now.Add(time.Minute)
	in.Previous.CheckedNames = nil
	got := evaluate(t, in, absentResolver())
	assertDecision(t, got, metav1.ConditionUnknown, "PolicyChanged")
}

func TestDeniedClaimDoesNotProbeOrEchoWaiver(t *testing.T) {
	in := fixture()
	in.Claim.Allowed = false
	got := evaluate(t, in, resolverFunc(func(context.Context, dnsprobe.ProbeRequest) (dnsprobe.ProbeResult, error) {
		t.Fatal("denied claim was probed")
		return dnsprobe.ProbeResult{}, nil
	}))
	assertDecision(t, got, metav1.ConditionUnknown, "DNSProbeFailed")
	if got.WaiverRef != nil || got.OverrideAccepted != nil {
		t.Fatal("denied claim received override")
	}
}
