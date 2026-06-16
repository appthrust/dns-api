package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	cloudflarev1alpha1 "github.com/appthrust/dns-api/pkg/go/api/cloudflare/v1alpha1"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type IdentityResolution struct {
	Account     cloudflarev1alpha1.CloudflareAccountStatus
	AccessToken cloudflarev1alpha1.CloudflareAccessTokenStatus
}

type IdentityResolver interface {
	ResolveIdentity(ctx context.Context, token string) (IdentityResolution, error)
}

const (
	defaultIdentityRetryAfter       = time.Minute
	defaultIdentityHealthCheckAfter = 10 * time.Minute
)

// IdentityReconciler accepts CloudflareIdentity resources and checks live Cloudflare access.
type IdentityReconciler struct {
	client.Client

	Resolver         IdentityResolver
	Recorder         record.EventRecorder
	Clock            func() time.Time
	RetryAfter       time.Duration
	HealthCheckAfter time.Duration
}

// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=cloudflare.dns.appthrust.io,resources=cloudflareidentities,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=cloudflare.dns.appthrust.io,resources=cloudflareidentities/status,verbs=get;update;patch
func (r *IdentityReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var identity cloudflarev1alpha1.CloudflareIdentity
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
	acceptedStatus, acceptedReason, acceptedMessage := acceptCloudflareIdentity(&identity)
	setCondition(&identity.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), acceptedStatus, acceptedReason, acceptedMessage, identity.Generation)

	if acceptedStatus != metav1.ConditionTrue {
		identity.Status.Account = nil
		identity.Status.AccessToken = nil
		identity.Status.LastCredentialCheckTime = nil
		identity.Status.NextCredentialCheckTime = nil
		setCondition(&identity.Status.Conditions, "Ready", metav1.ConditionFalse, "AccessTokenUnavailable", "CloudflareIdentity is not accepted", identity.Generation)
	} else {
		var token string
		var err error
		token, err = r.readAccessToken(ctx, &identity)
		if err != nil {
			reason, message := cloudflareSecretErrorCondition(err)
			identity.Status.Account = nil
			identity.Status.AccessToken = nil
			identity.Status.LastCredentialCheckTime = nil
			identity.Status.NextCredentialCheckTime = nil
			setCondition(&identity.Status.Conditions, "Ready", metav1.ConditionFalse, reason, message, identity.Generation)
		} else if r.Resolver == nil {
			identity.Status.Account = nil
			identity.Status.AccessToken = nil
			identity.Status.LastCredentialCheckTime = metav1Time(now)
			result = r.scheduleRetry(&identity, now)
			setCondition(&identity.Status.Conditions, "Ready", metav1.ConditionFalse, "ProviderUnavailable", "Cloudflare identity resolver is not configured", identity.Generation)
		} else {
			identity.Status.LastCredentialCheckTime = metav1Time(now)
			resolution, err := r.Resolver.ResolveIdentity(ctx, token)
			if err != nil {
				reason, message := cloudflareResolutionErrorCondition(err)
				identity.Status.Account = nil
				identity.Status.AccessToken = nil
				if cloudflareRetryableReason(reason) {
					result = r.scheduleRetry(&identity, now)
				} else {
					identity.Status.NextCredentialCheckTime = nil
				}
				setCondition(&identity.Status.Conditions, "Ready", metav1.ConditionFalse, reason, message, identity.Generation)
			} else {
				identity.Status.Account = &resolution.Account
				identity.Status.AccessToken = &resolution.AccessToken
				result = r.scheduleHealthCheck(&identity, now)
				setCondition(&identity.Status.Conditions, "Ready", metav1.ConditionTrue, "Ready", fmt.Sprintf("resolved Cloudflare account %q", resolution.Account.ID), identity.Generation)
			}
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
		For(&cloudflarev1alpha1.CloudflareIdentity{}).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.mapSecretToIdentities)).
		Complete(r)
}

func acceptCloudflareIdentity(identity *cloudflarev1alpha1.CloudflareIdentity) (metav1.ConditionStatus, string, string) {
	ref := identity.Spec.AccessToken.SecretRef
	if strings.TrimSpace(ref.Name) == "" {
		return metav1.ConditionFalse, "InvalidSecretRef", "spec.accessToken.secretRef.name is required"
	}
	if strings.TrimSpace(ref.Key) == "" {
		return metav1.ConditionFalse, "InvalidSecretRef", "spec.accessToken.secretRef.key is required"
	}
	return metav1.ConditionTrue, "Accepted", "CloudflareIdentity is accepted"
}

func (r *IdentityReconciler) readAccessToken(ctx context.Context, identity *cloudflarev1alpha1.CloudflareIdentity) (string, error) {
	ref := identity.Spec.AccessToken.SecretRef
	var secret corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: identity.Namespace, Name: ref.Name}, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return "", &identityReasonError{reason: "SecretNotFound", message: "referenced Secret does not exist"}
		}
		return "", &identityReasonError{reason: "ReconcileError", message: err.Error()}
	}
	raw, ok := secret.Data[ref.Key]
	if !ok || strings.TrimSpace(string(raw)) == "" {
		return "", &identityReasonError{reason: "AccessTokenUnavailable", message: "referenced Secret key does not exist or is empty"}
	}
	return strings.TrimSpace(string(raw)), nil
}

func cloudflareSecretErrorCondition(err error) (string, string) {
	var reasonErr *identityReasonError
	if errors.As(err, &reasonErr) {
		return reasonErr.reason, reasonErr.message
	}
	return "ReconcileError", err.Error()
}

func cloudflareResolutionErrorCondition(err error) (string, string) {
	var reasonErr *identityReasonError
	if errors.As(err, &reasonErr) {
		return reasonErr.reason, reasonErr.message
	}
	return "ReconcileError", err.Error()
}

func cloudflareRetryableReason(reason string) bool {
	switch reason {
	case "ProviderUnavailable", "ReconcileError":
		return true
	default:
		return false
	}
}

func (r *IdentityReconciler) scheduleRetry(identity *cloudflarev1alpha1.CloudflareIdentity, now time.Time) ctrl.Result {
	retryAfter := r.retryAfter()
	identity.Status.NextCredentialCheckTime = metav1Time(now.Add(retryAfter))
	return ctrl.Result{RequeueAfter: retryAfter}
}

func (r *IdentityReconciler) scheduleHealthCheck(identity *cloudflarev1alpha1.CloudflareIdentity, now time.Time) ctrl.Result {
	healthCheckAfter := r.healthCheckAfter()
	identity.Status.NextCredentialCheckTime = metav1Time(now.Add(healthCheckAfter))
	return ctrl.Result{RequeueAfter: healthCheckAfter}
}

func (r *IdentityReconciler) mapSecretToIdentities(ctx context.Context, obj client.Object) []reconcile.Request {
	secret, ok := obj.(*corev1.Secret)
	if !ok {
		return nil
	}
	var identities cloudflarev1alpha1.CloudflareIdentityList
	if err := r.List(ctx, &identities, client.InNamespace(secret.Namespace)); err != nil {
		return nil
	}
	var requests []reconcile.Request
	for _, identity := range identities.Items {
		if identity.Spec.AccessToken.SecretRef.Name == secret.Name {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&identity)})
		}
	}
	return requests
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
	return defaultIdentityRetryAfter
}

func (r *IdentityReconciler) healthCheckAfter() time.Duration {
	if r.HealthCheckAfter > 0 {
		return r.HealthCheckAfter
	}
	return defaultIdentityHealthCheckAfter
}

func metav1Time(t time.Time) *metav1.Time {
	mt := metav1.NewTime(t)
	return &mt
}

func setCondition(conditions *[]metav1.Condition, conditionType string, status metav1.ConditionStatus, reason, message string, observedGeneration int64) {
	meta.SetStatusCondition(conditions, metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: observedGeneration,
	})
}

func (r *IdentityReconciler) recordConditionEvents(previous []metav1.Condition, identity *cloudflarev1alpha1.CloudflareIdentity) {
	if r.Recorder == nil {
		return
	}
	for _, condition := range identity.Status.Conditions {
		if !cloudflareIdentityEventReason(condition.Reason) {
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
		r.Recorder.Event(identity, eventType, condition.Reason, cloudflareIdentityEventMessage(identity, condition))
	}
}

func cloudflareIdentityEventReason(reason string) bool {
	switch reason {
	case "Accepted",
		"InvalidSecretRef",
		"DeniedByPolicy",
		"Ready",
		"SecretNotFound",
		"AccessTokenUnavailable",
		"AccessTokenInvalid",
		"AccessTokenInactive",
		"CloudflareAccountNotFound",
		"CloudflareAccountAmbiguous",
		"ProviderAccessDenied",
		"ProviderUnavailable",
		"ReconcileError":
		return true
	default:
		return false
	}
}

func cloudflareIdentityEventMessage(identity *cloudflarev1alpha1.CloudflareIdentity, condition metav1.Condition) string {
	resource := fmt.Sprintf("%s/%s", identity.Namespace, identity.Name)
	switch condition.Reason {
	case "Accepted":
		return fmt.Sprintf("CloudflareIdentity %s is accepted.", resource)
	case "Ready":
		return fmt.Sprintf("CloudflareIdentity %s resolved Cloudflare account.", resource)
	default:
		return fmt.Sprintf("CloudflareIdentity %s changed %s to %s: %s", resource, condition.Type, condition.Reason, condition.Message)
	}
}

type identityReasonError struct {
	reason  string
	message string
}

func (e *identityReasonError) Error() string {
	return e.message
}
