package endpointrecordset

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/appthrust/dns-api/internal/go/apps/endpoint/retirement"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestWithdrawalNoZoneRefreshesNeverPublishedEndpoint(t *testing.T) {
	ctx := context.Background()
	r, ers, zone := withdrawalFixture(t, true, interceptor.Funcs{})
	r.APIReader = r.Client
	if err := r.Delete(ctx, zone); err != nil {
		t.Fatal(err)
	}
	ers = getWithdrawalERS(t, r.Client, client.ObjectKeyFromObject(ers))
	// Identity lane 41305702992 retained generation 12 with observedGeneration 1:
	// no Zone/RecordSet ever existed, but route churn created retirement entries.
	ers.Status = endpointv1alpha1.EndpointRecordSetStatus{
		ObservedGeneration: 1,
		Conditions:         []metav1.Condition{{Type: retirement.ProtectedCondition, Status: metav1.ConditionUnknown, ObservedGeneration: 1}},
		Hostnames: []endpointv1alpha1.EndpointRecordSetHostnameStatus{{
			Hostname:   "h2.example.com",
			Conditions: []metav1.Condition{{Type: "Resolved", Status: metav1.ConditionFalse, Reason: "ZoneNotResolved"}},
		}},
	}
	if err := r.Status().Update(ctx, ers); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrlRequest(ers)); err != nil {
		t.Fatalf("never-published retirement must complete: %v", err)
	}
	current := getWithdrawalERS(t, r.Client, client.ObjectKeyFromObject(ers))
	if hasRetirements(current) || len(listWithdrawalClaims(t, r.Client)) != 0 {
		t.Fatal("never-published retirement retained intent or manufactured a withdrawal claim")
	}
	if _, err := r.Reconcile(ctx, ctrlRequest(ers)); err != nil {
		t.Fatal(err)
	}
	current = getWithdrawalERS(t, r.Client, client.ObjectKeyFromObject(ers))
	if current.Status.ObservedGeneration != current.Generation || len(current.Status.Hostnames) != 1 {
		t.Fatalf("active hostname observation did not converge: %#v", current.Status)
	}
	resolved := meta.FindStatusCondition(current.Status.Hostnames[0].Conditions, "Resolved")
	if resolved == nil || resolved.Status != metav1.ConditionFalse || resolved.Reason != "ZoneNotResolved" || meta.IsStatusConditionTrue(current.Status.Conditions, "Ready") {
		t.Fatalf("missing Zone must remain explicit, not fabricate DNS readiness: %#v", current.Status)
	}
}

func TestWithdrawalNoZoneCompletesUnpublishedDeletion(t *testing.T) {
	for _, mode := range []string{"retire-only", "direct-delete"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			r, ers, zone := withdrawalFixture(t, true, interceptor.Funcs{})
			r.APIReader = r.Client
			if err := r.Delete(ctx, zone); err != nil {
				t.Fatal(err)
			}
			// An unrelated endpoint must not turn the exact source-label absence
			// query into a namespace-wide publication prohibition.
			unrelated := &dnsv1alpha1.RecordSet{ObjectMeta: metav1.ObjectMeta{
				Namespace: ers.Namespace, Name: "other-endpoint",
				Labels: map[string]string{
					generatedLabelEndpointRecordSetNamespace: ers.Namespace,
					generatedLabelEndpointRecordSetName:      "other",
				},
			}}
			if err := r.Create(ctx, unrelated); err != nil {
				t.Fatal(err)
			}
			if mode == "retire-only" {
				ers = getWithdrawalERS(t, r.Client, client.ObjectKeyFromObject(ers))
				ers.Spec.Hostnames, ers.Spec.Targets = nil, nil
				if err := r.Update(ctx, ers); err != nil {
					t.Fatal(err)
				}
				if _, err := r.Reconcile(ctx, ctrlRequest(ers)); err != nil {
					t.Fatal(err)
				}
				ers = getWithdrawalERS(t, r.Client, client.ObjectKeyFromObject(ers))
				if !retirement.Captured(ers) || !hasRetirements(ers) {
					t.Fatal("retire-only capture must acknowledge this generation without pruning its final intent")
				}
				if _, err := retirement.RetireOrDelete(ctx, r.Client, ers); err != nil {
					t.Fatal(err)
				}
			} else if err := r.Delete(ctx, ers); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Reconcile(ctx, ctrlRequest(ers)); err != nil {
				t.Fatal(err)
			}
			if err := r.Get(ctx, client.ObjectKeyFromObject(ers), &endpointv1alpha1.EndpointRecordSet{}); !apierrors.IsNotFound(err) {
				t.Fatalf("never-published ERS finalizer was not released: %v", err)
			}
			if len(listWithdrawalClaims(t, r.Client)) != 0 {
				t.Fatal("never-published deletion manufactured a withdrawal claim")
			}
			if err := r.Get(ctx, client.ObjectKeyFromObject(unrelated), &dnsv1alpha1.RecordSet{}); err != nil {
				t.Fatalf("unrelated endpoint record was affected: %v", err)
			}
		})
	}
}

func TestWithdrawalNoZoneRetainsPublicationEvidenceAndReadFailures(t *testing.T) {
	for _, scenario := range []string{
		"sticky-protection", "lost-bound-zone", "other-host-binding", "retained-record-reference",
		"generated-record", "cached-empty-generated-record", "record-list-error", "zone-list-error",
		"unresolved-capability", "missing-reader", "partial-record-list",
	} {
		for _, mode := range []string{"active", "deleting"} {
			t.Run(scenario+"/"+mode, func(t *testing.T) {
				deleting := mode == "deleting"
				ctx := context.Background()
				r, ers, zone := withdrawalFixture(t, true, interceptor.Funcs{})
				base := r.Client.(client.WithWatch)
				r.APIReader = base
				if scenario != "unresolved-capability" {
					if err := r.Delete(ctx, zone); err != nil {
						t.Fatal(err)
					}
				} else {
					r.conversionFunc = func(context.Context, *endpointv1alpha1.EndpointProviderCapability, endpointv1alpha1.EndpointRecordSetConversionInput) ([]endpointv1alpha1.RecordSetSpecFragment, string, error) {
						return nil, "conversion refused", nil
					}
				}
				ers = getWithdrawalERS(t, base, client.ObjectKeyFromObject(ers))
				switch scenario {
				case "sticky-protection":
					ers.Status.Conditions = []metav1.Condition{{Type: retirement.ProtectedCondition, Status: metav1.ConditionTrue, ObservedGeneration: 1}}
				case "lost-bound-zone", "other-host-binding":
					host := "h1.example.com"
					if scenario == "other-host-binding" {
						host = "unrelated.example.com"
					}
					ers.Status.Hostnames = []endpointv1alpha1.EndpointRecordSetHostnameStatus{{
						Hostname: host, Zone: &endpointv1alpha1.EndpointRecordSetZoneStatus{
							Ref:        dnsv1alpha1.ObjectReference{Namespace: zone.Namespace, Name: zone.Name},
							DomainName: zone.Spec.DomainName, Provider: zone.Spec.Provider,
						},
					}}
				case "retained-record-reference":
					ers.Status.Hostnames = []endpointv1alpha1.EndpointRecordSetHostnameStatus{{
						Hostname: "h1.example.com", RecordSets: []endpointv1alpha1.EndpointRecordSetGeneratedRecordSetStatus{{
							Ref: dnsv1alpha1.ObjectReference{Namespace: ers.Namespace, Name: "published"},
						}},
					}}
				case "generated-record", "cached-empty-generated-record":
					record := &dnsv1alpha1.RecordSet{ObjectMeta: metav1.ObjectMeta{
						Namespace: ers.Namespace, Name: "published",
						Labels: map[string]string{
							generatedLabelEndpointRecordSetNamespace: ers.Namespace,
							generatedLabelEndpointRecordSetName:      ers.Name,
						},
					}}
					if err := base.Create(ctx, record); err != nil {
						t.Fatal(err)
					}
					if scenario == "cached-empty-generated-record" {
						r.Client = interceptor.NewClient(base, interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
							if records, ok := list.(*dnsv1alpha1.RecordSetList); ok {
								records.Items = nil
								return nil
							}
							return c.List(ctx, list, opts...)
						}})
					}
				case "record-list-error", "zone-list-error", "partial-record-list":
					r.APIReader = interceptor.NewClient(base, interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
						switch records := list.(type) {
						case *dnsv1alpha1.RecordSetList:
							if scenario == "record-list-error" {
								return errors.New("generated record observation unavailable")
							}
							if scenario == "partial-record-list" {
								records.Continue = "unread-page"
								return nil
							}
						case *dnsv1alpha1.ZoneList:
							if scenario == "zone-list-error" {
								return errors.New("authoritative Zone observation unavailable")
							}
						}
						return c.List(ctx, list, opts...)
					}})
				case "missing-reader":
					r.APIReader = nil
				}
				if err := base.Status().Update(ctx, ers); err != nil {
					t.Fatal(err)
				}
				if deleting {
					if err := base.Delete(ctx, ers); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := r.Reconcile(ctx, ctrlRequest(ers)); err == nil {
					t.Fatal("publication evidence or an unavailable observation authorized capture")
				}
				current := getWithdrawalERS(t, base, client.ObjectKeyFromObject(ers))
				ready := meta.FindStatusCondition(current.Status.Conditions, "Ready")
				if !reflect.DeepEqual(current.Spec, ers.Spec) || !slices.Contains(current.Finalizers, generatedRecordSetsFinalizer) ||
					ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != retirement.PendingCondition ||
					len(listWithdrawalClaims(t, base)) != 0 {
					t.Fatalf("capture failure discarded intent/finalizer or manufactured success: %#v", current)
				}
			})
		}
	}
}
