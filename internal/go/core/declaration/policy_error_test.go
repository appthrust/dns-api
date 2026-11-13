package declaration

import (
	"context"
	"errors"
	"testing"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestPolicyNamespaceReadErrorIsNotADenial(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	zone := &dnsv1alpha1.Zone{ObjectMeta: metav1.ObjectMeta{Namespace: "dns"}, Spec: dnsv1alpha1.ZoneSpec{AllowedRecordSets: []dnsv1alpha1.AllowedRecordSet{{
		Namespaces: dnsv1alpha1.AllowedRecordSetNamespaces{Selector: metav1.LabelSelector{MatchLabels: map[string]string{"dns": "allowed"}}},
		Records:    []dnsv1alpha1.AllowedRecord{{Name: dnsv1alpha1.RecordNamePolicy{Pattern: ".*"}, Types: []dnsv1alpha1.RecordType{dnsv1alpha1.RecordTypeA}}},
	}}}}
	for _, failure := range []error{errors.New("namespace read unavailable"), apierrors.NewNotFound(schema.GroupResource{Resource: "namespaces"}, "writer")} {
		t.Run(failure.Error(), func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
				return failure
			}}).Build()
			record := &dnsv1alpha1.RecordSet{ObjectMeta: metav1.ObjectMeta{Namespace: "writer"}, Spec: dnsv1alpha1.RecordSetSpec{Name: "www", Type: dnsv1alpha1.RecordTypeA}}
			allowed, message, err := RecordSetAllowedByZone(t.Context(), c, record, zone)
			if allowed || message != "" || !errors.Is(err, failure) {
				t.Fatalf("record policy=(%v,%q,%v)", allowed, message, err)
			}
			claim := &dnsv1alpha1.RecordWithdrawal{ObjectMeta: record.ObjectMeta, Spec: dnsv1alpha1.RecordWithdrawalSpec{Name: "www", Address: "192.0.2.1"}}
			allowed, message, err = WithdrawalAllowedByZone(t.Context(), c, claim, zone)
			if allowed || message != "" || !errors.Is(err, failure) {
				t.Fatalf("withdrawal policy=(%v,%q,%v)", allowed, message, err)
			}
		})
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "writer"}}).Build()
	allowed, message, err := RecordSetAllowedByZone(t.Context(), c, &dnsv1alpha1.RecordSet{ObjectMeta: metav1.ObjectMeta{Namespace: "writer"}}, zone)
	if allowed || err != nil || message != "RecordSet namespace is not allowed by the referenced Zone." {
		t.Fatalf("observed denial changed: (%v,%q,%v)", allowed, message, err)
	}
}
