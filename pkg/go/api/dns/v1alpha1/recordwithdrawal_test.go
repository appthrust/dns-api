package v1alpha1_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/netip"
	"testing"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestWithdrawalAddressLabelsAndDigests(t *testing.T) {
	for _, tc := range []struct{ address, want string }{
		{"192.0.2.1", "192.0.2.1"},
		{"::ffff:192.0.2.1", "192.0.2.1"},
		{"::1", "aaaaaaaaaaaaaaaaaaaaaaaaae"},
		{"2001:db8::1", "eaaq3oaaaaaaaaaaaaaaaaaaae"},
		{"fe80::1%eth0", ""},
	} {
		t.Run(tc.address, func(t *testing.T) {
			if got := dnsv1alpha1.AddressLabelValue(netip.MustParseAddr(tc.address)); got != tc.want {
				t.Fatalf("label=%s want=%s", got, tc.want)
			}
		})
	}
	if got := dnsv1alpha1.AddressLabelValue(netip.Addr{}); got != "" {
		t.Fatalf("invalid label=%s", got)
	}
	spec := dnsv1alpha1.RecordWithdrawalSpec{ZoneRef: dnsv1alpha1.ZoneReference{Name: "example"}, Name: "*.apps", Address: "192.0.2.1"}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	if got := dnsv1alpha1.WithdrawalSpecDigest(spec); got != "sha256:"+hex.EncodeToString(digest[:]) {
		t.Fatal("digest does not bind exact serialized spec")
	}
	before := dnsv1alpha1.WithdrawalSpecDigest(spec)
	namespace := "app"
	spec.ZoneRef.Namespace = &namespace
	if before == dnsv1alpha1.WithdrawalSpecDigest(spec) {
		t.Fatal("digest ignored explicit reference namespace")
	}
}

func TestWithdrawalKindsRegisteredAndDeepCopy(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := dnsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"RecordWithdrawal", "RecordWithdrawalList", "RecordWithdrawalWaiver", "RecordWithdrawalWaiverList"} {
		if _, err := scheme.New(dnsv1alpha1.SchemeGroupVersion.WithKind(kind)); err != nil {
			t.Fatal(err)
		}
	}
	claim := &dnsv1alpha1.RecordWithdrawal{Status: dnsv1alpha1.RecordWithdrawalStatus{RequiredViews: []string{"first"}, WaiverRef: &dnsv1alpha1.RecordWithdrawalWaiverReference{Name: "waiver"}}}
	copied := claim.DeepCopy()
	copied.Status.RequiredViews[0] = "changed"
	copied.Status.WaiverRef.Name = "changed"
	if claim.Status.RequiredViews[0] != "first" || claim.Status.WaiverRef.Name != "waiver" {
		t.Fatal("deepcopy aliased public evidence")
	}
}
