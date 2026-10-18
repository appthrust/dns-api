package webhook

import (
	"context"
	"encoding/json"
	"net/netip"
	"strings"
	"testing"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func validWithdrawal() *dnsv1alpha1.RecordWithdrawal {
	return &dnsv1alpha1.RecordWithdrawal{TypeMeta: metav1.TypeMeta{APIVersion: dnsv1alpha1.SchemeGroupVersion.String(), Kind: "RecordWithdrawal"}, ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "retired", UID: "claim-uid", Generation: 1, Labels: map[string]string{dnsv1alpha1.AddressLabelKey: "192.0.2.1"}}, Spec: dnsv1alpha1.RecordWithdrawalSpec{ZoneRef: dnsv1alpha1.ZoneReference{Name: "example"}, Name: "*.apps", Address: "192.0.2.1"}}
}

func withdrawalRequest(t *testing.T, obj runtime.Object, old runtime.Object, kind, subresource string) admission.Request {
	t.Helper()
	data, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	req := admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{Operation: admissionv1.Create, Kind: metav1.GroupVersionKind{Group: dnsv1alpha1.GroupName, Version: "v1alpha1", Kind: kind}, Object: runtime.RawExtension{Raw: data}, SubResource: subresource}}
	if old != nil {
		data, err = json.Marshal(old)
		if err != nil {
			t.Fatal(err)
		}
		req.Operation = admissionv1.Update
		req.OldObject.Raw = data
	}
	return req
}

func TestWithdrawalAdmissionShapeAndImmutability(t *testing.T) {
	validator := newTestValidator(t)
	cases := []struct {
		name   string
		mutate func(*dnsv1alpha1.RecordWithdrawal)
		want   string
	}{
		{"valid", func(*dnsv1alpha1.RecordWithdrawal) {}, ""},
		{"label mismatch", func(c *dnsv1alpha1.RecordWithdrawal) { c.Labels[dnsv1alpha1.AddressLabelKey] = "192.0.2.2" }, "must match spec.address"},
		{"hostname address", func(c *dnsv1alpha1.RecordWithdrawal) { c.Spec.Address = "vip.example.com" }, "canonical"},
		{"noncanonical IPv6", func(c *dnsv1alpha1.RecordWithdrawal) { c.Spec.Address = "2001:0db8::1" }, "canonical"},
		{"mapped IPv6", func(c *dnsv1alpha1.RecordWithdrawal) { c.Spec.Address = "::ffff:192.0.2.1" }, "canonical"},
		{"scoped IPv6", func(c *dnsv1alpha1.RecordWithdrawal) { c.Spec.Address = "fe80::1%eth0" }, "canonical"},
		{"IPv6", func(c *dnsv1alpha1.RecordWithdrawal) {
			c.Spec.Address = "2001:db8::1"
			c.Labels[dnsv1alpha1.AddressLabelKey] = dnsv1alpha1.AddressLabelValue(netip.MustParseAddr(c.Spec.Address))
		}, ""},
		{"wildcard position", func(c *dnsv1alpha1.RecordWithdrawal) { c.Spec.Name = "app.*" }, "canonical relative"},
		{"noncanonical owner", func(c *dnsv1alpha1.RecordWithdrawal) { c.Spec.Name = "APP." }, "canonical relative"},
		{"finalizer", func(c *dnsv1alpha1.RecordWithdrawal) { c.Finalizers = []string{"test"} }, "ownerReferences or finalizers"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validWithdrawal()
			tc.mutate(c)
			response := validator.Handle(context.Background(), withdrawalRequest(t, c, nil, "RecordWithdrawal", ""))
			if tc.want == "" {
				if !response.Allowed {
					t.Fatal(response.Result.Message)
				}
			} else if response.Allowed || !strings.Contains(response.Result.Message, tc.want) {
				t.Fatalf("response=%#v want %s", response, tc.want)
			}
		})
	}
	old := validWithdrawal()
	changed := old.DeepCopy()
	changed.Spec.Name = "other"
	response := validator.Handle(context.Background(), withdrawalRequest(t, changed, old, "RecordWithdrawal", ""))
	if response.Allowed || response.Result.Message != "spec is immutable" {
		t.Fatalf("response=%#v", response)
	}
}

func validWaiver() *dnsv1alpha1.RecordWithdrawalWaiver {
	claim := validWithdrawal()
	return &dnsv1alpha1.RecordWithdrawalWaiver{TypeMeta: metav1.TypeMeta{APIVersion: dnsv1alpha1.SchemeGroupVersion.String(), Kind: "RecordWithdrawalWaiver"}, ObjectMeta: metav1.ObjectMeta{Namespace: claim.Namespace, Name: "override", UID: "waiver-uid", Generation: 1}, Spec: dnsv1alpha1.RecordWithdrawalWaiverSpec{WithdrawalRef: dnsv1alpha1.WithdrawalObjectReference{Namespace: claim.Namespace, Name: claim.Name, UID: claim.UID}, ClaimSpecDigest: dnsv1alpha1.WithdrawalSpecDigest(claim.Spec), Address: claim.Spec.Address, ReservationRef: dnsv1alpha1.WithdrawalObjectReference{Namespace: "mgmt", Name: "reservation", UID: "reservation-uid"}, Reason: "operator accepted risk", TicketRef: "change-123", AcceptStaleDNSRisk: true}}
}

func TestWithdrawalWaiverAdmission(t *testing.T) {
	validator := newTestValidator(t)
	for _, tc := range []struct {
		name   string
		mutate func(*dnsv1alpha1.RecordWithdrawalWaiver)
		want   string
	}{
		{"valid", func(*dnsv1alpha1.RecordWithdrawalWaiver) {}, ""},
		{"missing UID", func(w *dnsv1alpha1.RecordWithdrawalWaiver) { w.Spec.ReservationRef.UID = "" }, "exact namespace, name and UID"},
		{"cross namespace", func(w *dnsv1alpha1.RecordWithdrawalWaiver) { w.Spec.WithdrawalRef.Namespace = "tenant" }, "must match waiver namespace"},
		{"missing acceptance", func(w *dnsv1alpha1.RecordWithdrawalWaiver) { w.Spec.AcceptStaleDNSRisk = false }, "must be true"},
		{"invalid digest", func(w *dnsv1alpha1.RecordWithdrawalWaiver) { w.Spec.ClaimSpecDigest = "bad" }, "sha256 digest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := validWaiver()
			tc.mutate(w)
			response := validator.Handle(context.Background(), withdrawalRequest(t, w, nil, "RecordWithdrawalWaiver", ""))
			if tc.want == "" {
				if !response.Allowed {
					t.Fatal(response.Result.Message)
				}
			} else if response.Allowed || !strings.Contains(response.Result.Message, tc.want) {
				t.Fatalf("response=%#v", response)
			}
		})
	}
	old := validWaiver()
	changed := old.DeepCopy()
	changed.Spec.ReservationRef.UID = "recreated"
	response := validator.Handle(context.Background(), withdrawalRequest(t, changed, old, "RecordWithdrawalWaiver", ""))
	if response.Allowed || response.Result.Message != "spec is immutable" {
		t.Fatalf("response=%#v", response)
	}
}

func TestWithdrawalStatusReasonsAndGeneration(t *testing.T) {
	validator := newTestValidator(t)
	for condition, statuses := range withdrawalConditionReasons() {
		for status, reasons := range statuses {
			for reason := range reasons {
				t.Run(string(condition)+"/"+reason, func(t *testing.T) {
					c := validWithdrawal()
					c.Status.ObservedGeneration = 1
					c.Status.Conditions = []metav1.Condition{{Type: string(condition), Status: status, Reason: reason, Message: "provider message", ObservedGeneration: 1}}
					response := validator.Handle(context.Background(), withdrawalRequest(t, c, nil, "RecordWithdrawal", "status"))
					if !response.Allowed {
						t.Fatal(response.Result.Message)
					}
				})
			}
		}
	}
	for _, tc := range []struct {
		name            string
		generation      int64
		reason, message string
	}{{"future", 2, "WithdrawalVerified", "ok"}, {"invalid reason", 1, "TimerElapsed", "ok"}, {"oversized message", 1, "WithdrawalVerified", strings.Repeat("x", 2049)}} {
		t.Run(tc.name, func(t *testing.T) {
			c := validWithdrawal()
			c.Status.Conditions = []metav1.Condition{{Type: "Withdrawn", Status: metav1.ConditionTrue, Reason: tc.reason, Message: tc.message, ObservedGeneration: tc.generation}}
			if response := validator.Handle(context.Background(), withdrawalRequest(t, c, nil, "RecordWithdrawal", "status")); response.Allowed {
				t.Fatal("invalid status admitted")
			}
		})
	}
}

func TestWithdrawalBlocksUnitDeletionEvenWhenZoneDeleting(t *testing.T) {
	claim := validWithdrawal()
	claim.Spec.ZoneRef.Name = "example-com"
	unit := validZoneUnit()
	claim.Namespace = unit.Spec.Zone.Ref.Namespace
	claim.Spec.ZoneRef.Name = unit.Spec.Zone.Ref.Name
	validator := newTestValidator(t, claim)
	if err := validator.validateZoneUnitDelete(context.Background(), unit); err == nil || !strings.Contains(err.Error(), "RecordWithdrawal") {
		t.Fatalf("delete error=%v", err)
	}
}

func TestWithdrawalProviderReasonsRemainResourceScoped(t *testing.T) {
	provider := route53Provider(t)
	provider.Spec.Versions[0].Zone.AdditionalConditionReasons = []dnsv1alpha1.ProviderConditionReason{{ConditionType: dnsv1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: "WithdrawalVerified", Description: "Wrong resource."}}
	if err := validateProvider(provider); err == nil || !strings.Contains(err.Error(), "conditionType") {
		t.Fatalf("withdrawal condition leaked into Zone declarations: %v", err)
	}
}
