package route53

import (
	"fmt"
	"testing"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := dnsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add dns scheme: %v", err)
	}
	if err := route53v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add route53 scheme: %v", err)
	}
	return scheme
}

func assertCondition(t *testing.T, conditions []metav1.Condition, conditionType string, status metav1.ConditionStatus, reason string) metav1.Condition {
	t.Helper()
	condition := meta.FindStatusCondition(conditions, conditionType)
	if condition == nil {
		t.Fatalf("condition %s was not found in %#v", conditionType, conditions)
	}
	if condition.Status != status || condition.Reason != reason {
		t.Fatalf("condition %s = (%s, %s), want (%s, %s)", conditionType, condition.Status, condition.Reason, status, reason)
	}
	return *condition
}

type recordedEvent struct {
	Object string
	Reason string
}

type capturingRecorder struct {
	events []recordedEvent
}

var _ record.EventRecorder = (*capturingRecorder)(nil)

func (r *capturingRecorder) Event(object runtime.Object, _ string, reason, _ string) {
	r.events = append(r.events, recordedEvent{Object: eventObjectName(object), Reason: reason})
}

func (r *capturingRecorder) Eventf(object runtime.Object, eventType, reason, messageFmt string, args ...interface{}) {
	r.Event(object, eventType, reason, fmt.Sprintf(messageFmt, args...))
}

func (r *capturingRecorder) AnnotatedEventf(object runtime.Object, _ map[string]string, eventType, reason, messageFmt string, args ...interface{}) {
	r.Event(object, eventType, reason, fmt.Sprintf(messageFmt, args...))
}

func (r *capturingRecorder) has(object, reason string) bool {
	for _, event := range r.events {
		if event.Object == object && event.Reason == reason {
			return true
		}
	}
	return false
}

func eventObjectName(object runtime.Object) string {
	switch object.(type) {
	case *route53v1alpha1.Route53Identity:
		return "Route53Identity"
	default:
		return fmt.Sprintf("%T", object)
	}
}
