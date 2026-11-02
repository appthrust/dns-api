package route53

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"text/template"

	corewebhook "github.com/appthrust/dns-api/internal/go/core/webhook"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	"sigs.k8s.io/yaml"
)

func TestNegativeCacheTTLProviderSchemas(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "..", "..")
	providers := map[string]*dnsv1alpha1.Provider{}
	for name, path := range map[string]string{
		"built-in": "app/operator/config/provider/route53_extensions.yaml",
		"chart":    "deploy/charts/dns-api/templates/provider-route53.yaml",
	} {
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		tmpl, err := template.New(name).Parse(string(data))
		if err != nil {
			t.Fatal(err)
		}
		var rendered bytes.Buffer
		if err := tmpl.Execute(&rendered, map[string]any{
			"Values": map[string]any{"providers": map[string]any{"route53": map[string]any{"enabled": true}}},
		}); err != nil {
			t.Fatal(err)
		}
		provider := &dnsv1alpha1.Provider{}
		if err := yaml.Unmarshal(rendered.Bytes(), provider); err != nil {
			t.Fatal(err)
		}
		providers[name] = provider
	}
	builtIn, chart := providers["built-in"].Spec.Versions[0], providers["chart"].Spec.Versions[0]
	if !reflect.DeepEqual(builtIn.ZoneClass.Schemas.Parameters, chart.ZoneClass.Schemas.Parameters) ||
		!reflect.DeepEqual(builtIn.Zone.Schemas.StatusProviderData, chart.Zone.Schemas.StatusProviderData) {
		t.Fatal("built-in and chart ZoneClass parameters or Zone status schemas differ")
	}
	for name, provider := range providers {
		t.Run(name, func(t *testing.T) {
			t.Run("observed status", func(t *testing.T) {
				scheme := testScheme(t)
				k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(provider.DeepCopy()).Build()
				validator := corewebhook.NewCoreValidator(k8sClient, scheme)
				for _, value := range []string{"0", "60", "2147483647", "-1", "2147483648"} {
					zone := &dnsv1alpha1.Zone{
						TypeMeta: metav1.TypeMeta{APIVersion: "dns.appthrust.io/v1alpha1", Kind: "Zone"},
						Spec:     dnsv1alpha1.ZoneSpec{Provider: dnsv1alpha1.ProviderReference{Name: provider.Name, Version: "v1alpha1"}},
						Status: dnsv1alpha1.ZoneStatus{Provider: &dnsv1alpha1.ProviderStatus{
							Data: runtime.RawExtension{Raw: []byte(`{"negativeCacheTTLSeconds":` + value + `}`)},
						}},
					}
					body, err := json.Marshal(zone)
					if err != nil {
						t.Fatal(err)
					}
					response := validator.Handle(context.Background(), admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
						Kind:      metav1.GroupVersionKind{Group: "dns.appthrust.io", Version: "v1alpha1", Kind: "Zone"},
						Operation: admissionv1.Update, SubResource: "status",
						Object: runtime.RawExtension{Raw: body},
					}})
					valid := value != "-1" && value != "2147483648"
					if response.Allowed != valid {
						t.Fatalf("status %s admission = %v, want %v; result %#v", value, response.Allowed, valid, response.Result)
					}
				}
			})
			for _, test := range []struct {
				name       string
				parameters string
				valid      bool
			}{
				{name: "absent", parameters: `{}`, valid: true},
				{name: "sixty", parameters: `{"negativeCacheTTLSeconds":60}`, valid: true},
				{name: "zero", parameters: `{"negativeCacheTTLSeconds":0}`, valid: true},
				{name: "maximum", parameters: `{"negativeCacheTTLSeconds":2147483647}`, valid: true},
				{name: "private", parameters: `{"zoneType":"Private","negativeCacheTTLSeconds":60}`, valid: true},
				{name: "negative", parameters: `{"negativeCacheTTLSeconds":-1}`},
				{name: "above maximum", parameters: `{"negativeCacheTTLSeconds":2147483648}`},
				{name: "fractional", parameters: `{"negativeCacheTTLSeconds":0.5}`},
				{name: "string", parameters: `{"negativeCacheTTLSeconds":"60"}`},
			} {
				t.Run(test.name, func(t *testing.T) {
					ctx := context.Background()
					class := route53ZoneClass("platform", "route53", nil)
					class.TypeMeta = metav1.TypeMeta{APIVersion: "dns.appthrust.io/v1alpha1", Kind: "ZoneClass"}
					class.Spec.Parameters = runtime.RawExtension{Raw: []byte(test.parameters)}
					scheme := testScheme(t)
					k8sClient := fake.NewClientBuilder().WithScheme(scheme).
						WithObjects(provider.DeepCopy(), class, acceptedRoute53Identity("platform", "route53-dev")).
						WithStatusSubresource(&dnsv1alpha1.ZoneClass{}).Build()
					body, err := json.Marshal(class)
					if err != nil {
						t.Fatal(err)
					}
					response := corewebhook.NewCoreValidator(k8sClient, scheme).Handle(ctx, admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
						Kind:      metav1.GroupVersionKind{Group: "dns.appthrust.io", Version: "v1alpha1", Kind: "ZoneClass"},
						Operation: admissionv1.Create,
						Object:    runtime.RawExtension{Raw: body},
					}})
					if response.Allowed != test.valid {
						t.Fatalf("admission allowed = %v, want %v; result %#v", response.Allowed, test.valid, response.Result)
					}
					if _, err := (&ZoneClassReconciler{Client: k8sClient}).Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(class)}); err != nil {
						t.Fatal(err)
					}
					if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(class), class); err != nil {
						t.Fatal(err)
					}
					if test.valid {
						assertCondition(t, class.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted")
					} else {
						assertCondition(t, class.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionFalse, "InvalidProviderPayload")
					}
				})
			}
		})
	}
}
