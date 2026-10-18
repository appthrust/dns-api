package zoneclass

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/appthrust/dns-api/internal/go/providers/external/manifest"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	externalv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/external/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func classFixture() *dnsv1alpha1.ZoneClass {
	return &dnsv1alpha1.ZoneClass{ObjectMeta: metav1.ObjectMeta{Namespace: "platform", Name: "external", Generation: 1}, Spec: dnsv1alpha1.ZoneClassSpec{Provider: dnsv1alpha1.ProviderReference{Name: externalv1alpha1.ProviderName, Version: externalv1alpha1.ProviderVersion}, ControllerName: externalv1alpha1.ControllerName, Parameters: runtime.RawExtension{Raw: []byte(`{}`)}}}
}

func TestClassAcceptanceAndIdentityRejection(t *testing.T) {
	provider, err := manifest.Provider()
	if err != nil {
		t.Fatal(err)
	}
	r := &ZoneClassReconciler{}
	class := classFixture()
	status, reason, message := r.accept(class, provider)
	if status != metav1.ConditionTrue || reason != "Accepted" {
		t.Fatalf("accept=%s/%s %s (requires optional identityRef core cutover)", status, reason, message)
	}
	for _, identity := range []string{`{}`, `{"name":"forbidden"}`} {
		// JSON keeps this test source compatible with both sides of the optional-pointer cutover.
		if err := json.Unmarshal([]byte(`{"spec":{"identityRef":`+identity+`}}`), class); err != nil {
			t.Fatal(err)
		}
		status, reason, _ = r.accept(class, provider)
		if status != metav1.ConditionFalse || reason != "InvalidIdentityRef" {
			t.Fatalf("identity=%s accept=%s/%s", identity, status, reason)
		}
	}
	class = classFixture()
	class.Spec.Parameters.Raw = []byte(`{"resolver":{"mode":"Recursive"}}`)
	if status, reason, _ := r.accept(class, provider); status != metav1.ConditionFalse || reason != "InvalidParameters" {
		t.Fatalf("accept=%s/%s", status, reason)
	}
	provider.Spec.Versions[0].Storage = false
	if status, reason, _ := r.accept(class, provider); status != metav1.ConditionUnknown || reason != "ProviderStorageVersionNotResolved" {
		t.Fatalf("accept=%s/%s", status, reason)
	}
}

func TestClassScopeAndUnchangedStatus(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := dnsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	provider, err := manifest.Provider()
	if err != nil {
		t.Fatal(err)
	}
	class := classFixture()
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(provider, class).WithStatusSubresource(&dnsv1alpha1.ZoneClass{}).Build()
	r := &ZoneClassReconciler{Client: c}
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(class)}
	if _, err := r.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), request.NamespacedName, class); err != nil {
		t.Fatal(err)
	}
	condition := meta.FindStatusCondition(class.Status.Conditions, "Accepted")
	if condition == nil || condition.Status != metav1.ConditionTrue || condition.ObservedGeneration != 1 {
		t.Fatalf("condition=%#v", condition)
	}
	version := class.ResourceVersion
	if _, err := r.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), request.NamespacedName, class); err != nil {
		t.Fatal(err)
	}
	if class.ResourceVersion != version {
		t.Fatal("unchanged class status was patched")
	}
	r.ControllerName = "other.example/controller"
	if _, err := r.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), request.NamespacedName, class); err != nil {
		t.Fatal(err)
	}
	if class.ResourceVersion != version {
		t.Fatal("foreign controller changed class")
	}
}
