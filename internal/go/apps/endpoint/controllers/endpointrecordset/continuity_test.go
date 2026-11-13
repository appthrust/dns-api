package endpointrecordset

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	conversionv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/conversion/v1alpha1"
	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

type conversionTransport func(*http.Request) (*http.Response, error)

func (f conversionTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// Exercise the actual dynamic conversion client and response classification,
// without opening sockets or replacing convertEndpointRecordSet with a stub.
func continuityRESTConfig(t *testing.T, mode *string) *rest.Config {
	t.Helper()
	return &rest.Config{Host: "https://conversion.invalid", Transport: conversionTransport(func(req *http.Request) (*http.Response, error) {
		var conversion conversionv1alpha1.EndpointRecordSetConversion
		if err := json.NewDecoder(req.Body).Decode(&conversion); err != nil {
			return nil, err
		}
		conversion.Status.UID = conversion.Spec.UID
		conversion.Status.Result = conversionv1alpha1.EndpointRecordSetConversionResult{Status: "Success", Reason: "Converted"}
		conversion.Status.Output = &endpointv1alpha1.EndpointRecordSetConversionOutput{Fragments: continuityFragments(conversion.Spec.Input.Name)}
		code := http.StatusCreated
		var body any = conversion
		if conversion.Spec.Input.Name == "z" {
			switch *mode {
			case "transport":
				return nil, errors.New("conversion connection unavailable")
			case "unavailable", "forbidden", "unauthorized":
				code = map[string]int{"unavailable": 503, "forbidden": 403, "unauthorized": 401}[*mode]
				body = metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: "Failure", Code: int32(code), Message: "conversion unavailable"}
			case "malformed":
				return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"apiVersion":`))}, nil
			case "malformed-status":
				body = map[string]any{"apiVersion": conversion.APIVersion, "kind": conversion.Kind, "status": map[string]any{"uid": conversion.Spec.UID, "result": true}}
			case "uid":
				conversion.Status.UID = "wrong"
			case "retryable", "unknown-failure", "internal", "rejected", "invalid-input":
				reason := map[string]string{"retryable": "UnsupportedTarget", "unknown-failure": "UnrecognizedFailure", "internal": "InternalError", "rejected": "UnsupportedTarget", "invalid-input": "InvalidInput"}[*mode]
				conversion.Status.Result = conversionv1alpha1.EndpointRecordSetConversionResult{Status: "Failure", Reason: reason, Retryable: *mode == "retryable"}
				conversion.Status.Output = nil
			case "empty-failure":
				conversion.Status.Result = conversionv1alpha1.EndpointRecordSetConversionResult{Status: "Failure"}
				conversion.Status.Output = nil
			case "unknown-status":
				conversion.Status.Result.Status = "Pending"
			case "empty":
				conversion.Status.Output = nil
			case "name":
				conversion.Status.Output.Fragments[0].Name = "different"
			case "type":
				conversion.Status.Output.Fragments[0].Type = endpointv1alpha1.EndpointRecordSetType("TXT")
			}
		}
		if _, ok := body.(conversionv1alpha1.EndpointRecordSetConversion); ok {
			body = conversion
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(encoded)))}, nil
	})}
}

func continuityFragments(name string) []endpointv1alpha1.RecordSetSpecFragment {
	ttl := int32(60)
	return []endpointv1alpha1.RecordSetSpecFragment{
		{Name: name, Type: endpointv1alpha1.EndpointRecordSetTypeA, TTL: &ttl, A: &dnsv1alpha1.ARecordSet{Addresses: []string{"192.0.2.1"}}},
		{Name: name, Type: endpointv1alpha1.EndpointRecordSetTypeAAAA, TTL: &ttl, AAAA: &dnsv1alpha1.AAAARecordSet{Addresses: []string{"2001:db8::1"}}},
	}
}

func continuityFixture(t *testing.T, mode *string) (*Reconciler, *endpointv1alpha1.EndpointRecordSet, client.WithWatch, *int) {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, dnsv1alpha1.AddToScheme, endpointv1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	ers := &endpointv1alpha1.EndpointRecordSet{ObjectMeta: metav1.ObjectMeta{Namespace: "writer", Name: "public", UID: "ers-uid", Generation: 1, Finalizers: []string{generatedRecordSetsFinalizer}}, Spec: endpointv1alpha1.EndpointRecordSetSpec{
		Hostnames: []string{"a.example.com", "z.example.com"}, Targets: []endpointv1alpha1.EndpointTarget{{Type: endpointv1alpha1.EndpointTargetTypeIPAddress, Value: "192.0.2.1"}},
	}}
	zone := &dnsv1alpha1.Zone{ObjectMeta: metav1.ObjectMeta{Namespace: "dns", Name: "zone"}, Spec: dnsv1alpha1.ZoneSpec{
		DomainName: "example.com", Provider: route53v1alpha1.ProviderRef,
		AllowedRecordSets: []dnsv1alpha1.AllowedRecordSet{{Namespaces: dnsv1alpha1.AllowedRecordSetNamespaces{Selector: metav1.LabelSelector{MatchLabels: map[string]string{"writer": "true"}}}, Records: []dnsv1alpha1.AllowedRecord{{Name: dnsv1alpha1.RecordNamePolicy{Pattern: ".*"}, Types: []dnsv1alpha1.RecordType{dnsv1alpha1.RecordTypeA, dnsv1alpha1.RecordTypeAAAA}}}}},
	}}
	objects := []client.Object{ers, zone,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ers.Namespace, Labels: map[string]string{"writer": "true"}}},
		&dnsv1alpha1.Provider{ObjectMeta: metav1.ObjectMeta{Name: route53v1alpha1.ProviderRef.Name}, Spec: dnsv1alpha1.ProviderSpec{Versions: []dnsv1alpha1.ProviderVersion{{Name: route53v1alpha1.ProviderRef.Version}}}},
		&endpointv1alpha1.EndpointProviderCapability{ObjectMeta: metav1.ObjectMeta{Name: "conversion"}, Spec: endpointv1alpha1.EndpointProviderCapabilitySpec{Provider: zone.Spec.Provider, Conversion: endpointv1alpha1.EndpointRecordSetConversionAPI{Group: "endpoint.route53.dns.appthrust.io", Resource: "endpointrecordsetconversions"}}},
	}
	r := &Reconciler{Scheme: scheme, RESTConfig: continuityRESTConfig(t, mode)}
	for _, name := range []string{"a", "z"} {
		for _, fragment := range continuityFragments(name) {
			child := r.recordSetFromFragment(t.Context(), ers, ers.Namespace, zone, fragment)
			child.UID, child.Generation = types.UID(child.Name), 1
			child.Status.ObservedGeneration = 1
			child.Status.Conditions = readyGeneratedRecordSetStatus(child.Name, 1).Conditions
			objects = append(objects, &child)
		}
	}
	base := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(ers, &dnsv1alpha1.RecordSet{}).WithObjects(objects...).Build()
	writes := new(int)
	r.Client = interceptor.NewClient(base, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*dnsv1alpha1.RecordSet); ok {
				*writes++
			}
			return c.Create(ctx, obj, opts...)
		},
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if _, ok := obj.(*dnsv1alpha1.RecordSet); ok {
				*writes++
			}
			return c.Update(ctx, obj, opts...)
		},
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if _, ok := obj.(*dnsv1alpha1.RecordSet); ok {
				*writes++
			}
			return c.Delete(ctx, obj, opts...)
		},
	})
	if _, err := r.Reconcile(t.Context(), ctrlRequest(ers)); err != nil {
		t.Fatal(err)
	}
	if err := base.Get(t.Context(), client.ObjectKeyFromObject(ers), ers); err != nil {
		t.Fatal(err)
	}
	assertEndpointCondition(t, ers.Status.Conditions, "Ready", metav1.ConditionTrue, 1)
	*writes = 0
	return r, ers, base, writes
}

func continuityChildren(t *testing.T, c client.Client) []dnsv1alpha1.RecordSet {
	t.Helper()
	var list dnsv1alpha1.RecordSetList
	if err := c.List(t.Context(), &list); err != nil {
		t.Fatal(err)
	}
	return list.Items
}

func assertRetainedOnError(t *testing.T, r *Reconciler, ers *endpointv1alpha1.EndpointRecordSet, base client.Client, writes *int) {
	t.Helper()
	before := continuityChildren(t, base)
	for range 2 {
		if _, err := r.Reconcile(t.Context(), ctrlRequest(ers)); err == nil {
			t.Fatal("incomplete observation did not return an error")
		}
	}
	if after := continuityChildren(t, base); !reflect.DeepEqual(before, after) || *writes != 0 {
		t.Fatalf("error changed established children (including UIDs/status): writes=%d before=%+v after=%+v", *writes, before, after)
	}
	var current endpointv1alpha1.EndpointRecordSet
	if err := base.Get(t.Context(), client.ObjectKeyFromObject(ers), &current); err != nil {
		t.Fatal(err)
	}
	assertEndpointCondition(t, current.Status.Conditions, "Resolved", metav1.ConditionUnknown, current.Generation)
	assertEndpointCondition(t, current.Status.Conditions, "Ready", metav1.ConditionUnknown, current.Generation)
	assertEndpointCondition(t, current.Status.Conditions, "Accepted", metav1.ConditionTrue, 1)
	assertEndpointCondition(t, current.Status.Conditions, "Programmed", metav1.ConditionTrue, 1)
	if !reflect.DeepEqual(current.Status.Hostnames, ers.Status.Hostnames) {
		t.Fatal("error erased last accepted hostname/child snapshot")
	}
}

func TestConversionAPIErrorsRetainEveryChildAndRecover(t *testing.T) {
	for _, failure := range []string{"transport", "unavailable", "forbidden", "unauthorized", "malformed", "malformed-status", "uid", "retryable", "internal", "unknown-failure", "empty-failure", "unknown-status", "empty", "name", "type", "no-config"} {
		t.Run(failure, func(t *testing.T) {
			mode := "success"
			r, ers, base, writes := continuityFixture(t, &mode)
			before := continuityChildren(t, base)
			mode = failure
			if failure == "no-config" {
				r.RESTConfig = nil
			}
			assertRetainedOnError(t, r, ers, base, writes)
			mode = "success"
			r.RESTConfig = continuityRESTConfig(t, &mode)
			if _, err := r.Reconcile(t.Context(), ctrlRequest(ers)); err != nil {
				t.Fatal(err)
			}
			if after := continuityChildren(t, base); !reflect.DeepEqual(before, after) || *writes != 0 {
				t.Fatal("recovery replaced or rewrote children")
			}
			if err := base.Get(t.Context(), client.ObjectKeyFromObject(ers), ers); err != nil {
				t.Fatal(err)
			}
			assertEndpointCondition(t, ers.Status.Conditions, "Ready", metav1.ConditionTrue, 1)
		})
	}
}

func TestDesiredChildReadErrorsRetain(t *testing.T) {
	for _, failure := range []string{"zone-list", "capability-list", "namespace-get", "namespace-not-found", "child-get"} {
		t.Run(failure, func(t *testing.T) {
			mode := "success"
			r, ers, base, writes := continuityFixture(t, &mode)
			observed := r.Client.(client.WithWatch)
			r.Client = interceptor.NewClient(observed, interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					switch obj.(type) {
					case *corev1.Namespace:
						if failure == "namespace-get" {
							return errors.New("namespace observation unavailable")
						}
						if failure == "namespace-not-found" {
							return apierrors.NewNotFound(schema.GroupResource{Resource: "namespaces"}, key.Name)
						}
					case *dnsv1alpha1.RecordSet:
						if failure == "child-get" {
							return errors.New("child observation unavailable")
						}
					}
					return c.Get(ctx, key, obj, opts...)
				},
				List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					if err := c.List(ctx, list, opts...); err != nil {
						return err
					}
					switch list.(type) {
					case *dnsv1alpha1.ZoneList:
						if failure == "zone-list" {
							return errors.New("zone observation unavailable")
						}
					case *endpointv1alpha1.EndpointProviderCapabilityList:
						if failure == "capability-list" {
							return errors.New("capability observation unavailable")
						}
					}
					return nil
				},
			})
			// A partially observed update must not add a new name either.
			ers.Spec.Hostnames = append(ers.Spec.Hostnames, "new.example.com")
			ers.Generation++
			if err := base.Update(t.Context(), ers); err != nil {
				t.Fatal(err)
			}
			assertRetainedOnError(t, r, ers, base, writes)
		})
	}
}

func TestExplicitConversionAndZoneRejectionsStillPrune(t *testing.T) {
	for _, rejection := range []string{"rejected", "invalid-input", "zone-policy"} {
		t.Run(rejection, func(t *testing.T) {
			mode := "success"
			r, ers, base, writes := continuityFixture(t, &mode)
			mode = rejection
			if rejection == "zone-policy" {
				var zone dnsv1alpha1.Zone
				if err := base.Get(t.Context(), client.ObjectKey{Namespace: "dns", Name: "zone"}, &zone); err != nil {
					t.Fatal(err)
				}
				zone.Spec.AllowedRecordSets[0].Records[0].Name.Pattern = "a"
				if err := base.Update(t.Context(), &zone); err != nil {
					t.Fatal(err)
				}
			}
			// Protection moves to Unknown first when a previously resolved binding
			// is rejected; the existing withdrawal barrier must remain intact.
			for range 2 {
				if _, err := r.Reconcile(t.Context(), ctrlRequest(ers)); err != nil {
					t.Fatal(err)
				}
			}
			children := continuityChildren(t, base)
			if len(children) != 2 || *writes != 2 {
				t.Fatalf("rejection children=%d writes=%d, want 2 retained and 2 deletes", len(children), *writes)
			}
			for _, child := range children {
				if child.Spec.Name != "a" {
					t.Fatalf("rejected child retained: %s", child.Name)
				}
			}
		})
	}
}

func TestRecordSetPrunedEventOnlyOnDeleteTransition(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		t.Run(fmt.Sprint(cleanup), func(t *testing.T) {
			mode := "success"
			r, ers, base, writes := continuityFixture(t, &mode)
			recorder := record.NewFakeRecorder(10)
			r.Recorder = recorder
			children := continuityChildren(t, base)
			for i := range children {
				children[i].Finalizers = []string{"test.example/hold"}
				if err := base.Update(t.Context(), &children[i]); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				if cleanup {
					if _, err := r.deleteGeneratedRecordSets(t.Context(), ers.Namespace, ers.Namespace, ers.Name); err != nil {
						t.Fatal(err)
					}
				} else if err := r.applyRecordSets(t.Context(), ers, ers.Namespace, recordSetPlan{}); err != nil {
					t.Fatal(err)
				}
			}
			if *writes != len(children) {
				t.Fatalf("delete calls=%d, want %d", *writes, len(children))
			}
			for range children {
				select {
				case event := <-recorder.Events:
					if !strings.HasPrefix(event, "Normal "+string(reasonRecordSetPruned)+" ") {
						t.Fatalf("unexpected event: %s", event)
					}
				default:
					t.Fatal("missing prune event")
				}
			}
			select {
			case event := <-recorder.Events:
				t.Fatalf("repeated prune event: %s", event)
			default:
			}
			for _, child := range continuityChildren(t, base) {
				if child.DeletionTimestamp.IsZero() {
					t.Fatal("delete did not reach terminating state")
				}
			}
		})
	}
}

func TestPruneFailureOrAlreadyAbsentDoesNotEmitEvent(t *testing.T) {
	for _, failure := range []error{errors.New("delete unavailable"), apierrors.NewNotFound(schema.GroupResource{Resource: "recordsets"}, "child")} {
		t.Run(failure.Error(), func(t *testing.T) {
			mode := "success"
			r, _, base, _ := continuityFixture(t, &mode)
			recorder := record.NewFakeRecorder(1)
			r.Recorder = recorder
			r.Client = interceptor.NewClient(base, interceptor.Funcs{Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error { return failure }})
			child := continuityChildren(t, base)[0]
			err := r.pruneRecordSet(t.Context(), &child)
			if !apierrors.IsNotFound(failure) && !errors.Is(err, failure) {
				t.Fatalf("delete error lost: %v", err)
			}
			select {
			case event := <-recorder.Events:
				t.Fatalf("failed delete emitted: %s", event)
			default:
			}
		})
	}
}
