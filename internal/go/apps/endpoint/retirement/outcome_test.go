package retirement

import (
	"context"
	"errors"
	"testing"

	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestRetireOrDeleteReportsOnlyPersistedTransitions(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := endpointv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"retire", "delete", "absent", "patch-failure", "delete-failure"} {
		t.Run(operation, func(t *testing.T) {
			ers := retirementFixture()
			ers.Finalizers = []string{endpointv1alpha1.GeneratedRecordSetsFinalizer}
			if operation == "delete" || operation == "absent" || operation == "delete-failure" {
				ers.Status.Conditions = []metav1.Condition{{Type: ProtectedCondition, Status: metav1.ConditionFalse, ObservedGeneration: ers.Generation}}
			}
			builder := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(ers)
			if operation != "absent" {
				builder = builder.WithObjects(ers)
			}
			failure := errors.New("write unavailable")
			c := builder.WithInterceptorFuncs(interceptor.Funcs{
				Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					if operation == "patch-failure" {
						return failure
					}
					return c.Patch(ctx, obj, patch, opts...)
				},
				Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					if operation == "delete-failure" {
						return failure
					}
					return c.Delete(ctx, obj, opts...)
				},
			}).Build()
			if operation != "absent" {
				if err := c.Get(t.Context(), client.ObjectKeyFromObject(ers), ers); err != nil {
					t.Fatal(err)
				}
			}
			outcome, err := RetireOrDelete(t.Context(), c, ers)
			switch operation {
			case "retire":
				if outcome != Patched || !errors.Is(err, ErrCapturePending) {
					t.Fatalf("retire=(%q,%v)", outcome, err)
				}
			case "delete":
				if outcome != Deleted || err != nil {
					t.Fatalf("delete=(%q,%v)", outcome, err)
				}
			case "absent":
				if outcome != NoChange || err != nil {
					t.Fatalf("absent=(%q,%v)", outcome, err)
				}
			default:
				if outcome != NoChange || !errors.Is(err, failure) {
					t.Fatalf("failed write=(%q,%v)", outcome, err)
				}
			}
			if operation == "retire" || operation == "delete" {
				if err := c.Get(t.Context(), client.ObjectKeyFromObject(ers), ers); err != nil {
					t.Fatal(err)
				}
				outcome, err = RetireOrDelete(t.Context(), c, ers)
				if outcome != NoChange {
					t.Fatalf("repeated transition=%q", outcome)
				}
				if operation == "retire" && !errors.Is(err, ErrCapturePending) || operation == "delete" && err != nil {
					t.Fatalf("repeat error=%v", err)
				}
			}
		})
	}
}
