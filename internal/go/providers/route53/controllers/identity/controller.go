package route53

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type IdentityResolution struct {
	AccountID string
}

type IdentityResolver interface {
	ResolveIdentity(ctx context.Context, identity *route53v1alpha1.Route53Identity) (IdentityResolution, error)
}

const (
	defaultRoute53IdentityRetryAfter       = time.Minute
	defaultRoute53IdentityHealthCheckAfter = 10 * time.Minute
)

// IdentityReconciler accepts Route53Identity resources and checks live AWS access.
type IdentityReconciler struct {
	client.Client

	Resolver         IdentityResolver
	Recorder         record.EventRecorder
	Clock            func() time.Time
	RetryAfter       time.Duration
	HealthCheckAfter time.Duration
}

// +kubebuilder:rbac:groups=route53.dns.appthrust.io,resources=route53identities,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=route53.dns.appthrust.io,resources=route53identities/status,verbs=get;update;patch
func (r *IdentityReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var identity route53v1alpha1.Route53Identity
	if err := r.Get(ctx, req.NamespacedName, &identity); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !identity.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	base := identity.DeepCopy()
	now := r.now()
	result := ctrl.Result{}
	identity.Status.ObservedGeneration = identity.Generation
	identity.Status.AccountID = ""
	acceptedStatus, acceptedReason, acceptedMessage := acceptRoute53Identity(&identity)
	setCondition(&identity.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), acceptedStatus, acceptedReason, acceptedMessage, identity.Generation)

	if acceptedStatus != metav1.ConditionTrue {
		identity.Status.NextCredentialCheckTime = nil
		setCondition(&identity.Status.Conditions, "Ready", metav1.ConditionFalse, "CredentialUnavailable", "Route53Identity is not accepted", identity.Generation)
	} else if r.Resolver == nil {
		identity.Status.LastCredentialCheckTime = metav1Time(now)
		result = r.scheduleCredentialRetry(&identity, now)
		setCondition(&identity.Status.Conditions, "Ready", metav1.ConditionFalse, "CredentialUnavailable", "Route 53 identity resolver is not configured", identity.Generation)
	} else {
		identity.Status.LastCredentialCheckTime = metav1Time(now)
		resolution, err := r.Resolver.ResolveIdentity(ctx, &identity)
		if err != nil {
			reason, message := identityResolutionErrorCondition(err)
			if route53IdentityRetryableReason(reason) {
				result = r.scheduleCredentialRetry(&identity, now)
			} else {
				identity.Status.NextCredentialCheckTime = nil
			}
			setCondition(&identity.Status.Conditions, "Ready", metav1.ConditionFalse, reason, message, identity.Generation)
		} else {
			identity.Status.AccountID = resolution.AccountID
			result = r.scheduleCredentialHealthCheck(&identity, now)
			setCondition(&identity.Status.Conditions, "Ready", metav1.ConditionTrue, "Ready", fmt.Sprintf("resolved AWS account %q", resolution.AccountID), identity.Generation)
		}
	}

	if err := r.Status().Patch(ctx, &identity, client.MergeFrom(base)); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	r.recordConditionEvents(base.Status.Conditions, &identity)
	return result, nil
}

func (r *IdentityReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&route53v1alpha1.Route53Identity{}).
		Complete(r)
}

func acceptRoute53Identity(identity *route53v1alpha1.Route53Identity) (metav1.ConditionStatus, string, string) {
	if err := validateAWSRegionEndpoint(context.Background(), identity.Spec.Region); err != nil {
		return metav1.ConditionFalse, "InvalidRegion", err.Error()
	}
	if identity.Spec.Credentials.Runtime == nil {
		return metav1.ConditionFalse, "InvalidCredentialSource", "credentials.runtime must be specified"
	}
	for index, role := range identity.Spec.AssumeRoleChain {
		if strings.TrimSpace(role.RoleARN) == "" {
			return metav1.ConditionFalse, "InvalidAssumeRoleChain", fmt.Sprintf("assumeRoleChain[%d].roleARN is required", index)
		}
		if len(role.SessionName) > 64 {
			return metav1.ConditionFalse, "InvalidAssumeRoleChain", fmt.Sprintf("assumeRoleChain[%d].sessionName must be 64 characters or fewer", index)
		}
	}
	return metav1.ConditionTrue, "Accepted", "Route53Identity is accepted"
}

func identityResolutionErrorCondition(err error) (string, string) {
	var reasonErr *identityReasonError
	if errors.As(err, &reasonErr) {
		return reasonErr.reason, reasonErr.message
	}
	return "GetCallerIdentityFailed", err.Error()
}

func route53IdentityRetryableReason(reason string) bool {
	switch reason {
	case "CredentialUnavailable", "AssumeRoleFailed", "GetCallerIdentityFailed":
		return true
	default:
		return false
	}
}

func (r *IdentityReconciler) scheduleCredentialRetry(identity *route53v1alpha1.Route53Identity, now time.Time) ctrl.Result {
	retryAfter := r.retryAfter()
	identity.Status.NextCredentialCheckTime = metav1Time(now.Add(retryAfter))
	return ctrl.Result{RequeueAfter: retryAfter}
}

func (r *IdentityReconciler) scheduleCredentialHealthCheck(identity *route53v1alpha1.Route53Identity, now time.Time) ctrl.Result {
	healthCheckAfter := r.healthCheckAfter()
	identity.Status.NextCredentialCheckTime = metav1Time(now.Add(healthCheckAfter))
	return ctrl.Result{RequeueAfter: healthCheckAfter}
}

func (r *IdentityReconciler) now() time.Time {
	if r.Clock != nil {
		return r.Clock()
	}
	return time.Now()
}

func (r *IdentityReconciler) retryAfter() time.Duration {
	if r.RetryAfter > 0 {
		return r.RetryAfter
	}
	return defaultRoute53IdentityRetryAfter
}

func (r *IdentityReconciler) healthCheckAfter() time.Duration {
	if r.HealthCheckAfter > 0 {
		return r.HealthCheckAfter
	}
	return defaultRoute53IdentityHealthCheckAfter
}

func metav1Time(t time.Time) *metav1.Time {
	mt := metav1.NewTime(t)
	return &mt
}

func (r *IdentityReconciler) recordConditionEvents(previous []metav1.Condition, identity *route53v1alpha1.Route53Identity) {
	if r.Recorder == nil {
		return
	}
	for _, condition := range identity.Status.Conditions {
		if !route53IdentityEventReason(condition.Reason) {
			continue
		}
		old := meta.FindStatusCondition(previous, condition.Type)
		if old != nil && old.Status == condition.Status && old.Reason == condition.Reason {
			continue
		}
		eventType := corev1.EventTypeWarning
		if condition.Status == metav1.ConditionTrue {
			eventType = corev1.EventTypeNormal
		}
		r.Recorder.Event(identity, eventType, condition.Reason, route53IdentityEventMessage(identity, condition))
	}
}

func route53IdentityEventReason(reason string) bool {
	switch reason {
	case "Accepted",
		"InvalidRegion",
		"InvalidCredentialSource",
		"InvalidAssumeRoleChain",
		"DeniedByPolicy",
		"Ready",
		"CredentialUnavailable",
		"AssumeRoleFailed",
		"GetCallerIdentityFailed",
		"AccountMismatch":
		return true
	default:
		return false
	}
}

func route53IdentityEventMessage(identity *route53v1alpha1.Route53Identity, condition metav1.Condition) string {
	resource := fmt.Sprintf("%s/%s", identity.Namespace, identity.Name)
	switch condition.Reason {
	case "Accepted":
		return fmt.Sprintf("Route53Identity %s is accepted for AWS account %s in %s.", resource, identity.Spec.AccountID, identity.Spec.Region)
	case "Ready":
		return fmt.Sprintf("Route53Identity %s resolved AWS account %s in %s.", resource, identity.Spec.AccountID, identity.Spec.Region)
	case "InvalidRegion":
		return fmt.Sprintf("Route53Identity %s has invalid AWS region %q: %s", resource, identity.Spec.Region, condition.Message)
	case "InvalidCredentialSource":
		return fmt.Sprintf("Route53Identity %s has invalid credential source: %s", resource, condition.Message)
	case "InvalidAssumeRoleChain":
		return fmt.Sprintf("Route53Identity %s has invalid assume role chain: %s", resource, condition.Message)
	case "DeniedByPolicy":
		return fmt.Sprintf("Route53Identity %s was denied by policy: %s", resource, condition.Message)
	case "CredentialUnavailable":
		return fmt.Sprintf("Route53Identity %s could not resolve runtime credentials in %s: %s", resource, identity.Spec.Region, condition.Message)
	case "AssumeRoleFailed":
		return fmt.Sprintf("Route53Identity %s failed an assume role step in %s: %s", resource, identity.Spec.Region, condition.Message)
	case "GetCallerIdentityFailed":
		return fmt.Sprintf("Route53Identity %s could not call STS GetCallerIdentity in %s: %s", resource, identity.Spec.Region, condition.Message)
	case "AccountMismatch":
		return fmt.Sprintf("Route53Identity %s resolved an unexpected AWS account in %s: %s", resource, identity.Spec.Region, condition.Message)
	default:
		return fmt.Sprintf("Route53Identity %s changed %s to %s: %s", resource, condition.Type, condition.Reason, condition.Message)
	}
}
