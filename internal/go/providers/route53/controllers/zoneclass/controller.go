package route53

import (
	"context"

	"github.com/appthrust/dns-api/internal/go/core/providercontract"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// ZoneClassReconciler accepts Route 53 ZoneClass resources from core identityRef and inline parameters.
type ZoneClassReconciler struct {
	client.Client

	ControllerName  string
	ProviderName    string
	ProviderVersion string
}

// +kubebuilder:rbac:groups=dns.appthrust.io,resources=zoneclasses,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=zoneclasses/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=providers,verbs=get;list;watch
// +kubebuilder:rbac:groups=route53.dns.appthrust.io,resources=route53identities,verbs=get;list;watch
func (r *ZoneClassReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var zoneClass dnsv1alpha1.ZoneClass
	if err := r.Get(ctx, req.NamespacedName, &zoneClass); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !zoneClass.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	if zoneClass.Spec.ControllerName != r.controllerName() {
		return ctrl.Result{}, nil
	}
	provider, version, err := resolveDNSProviderVersion(ctx, r.Client, zoneClass.Spec.Provider)
	if err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	status, reason, message, err := r.acceptZoneClass(ctx, &zoneClass, provider, version)
	if err != nil {
		return ctrl.Result{}, err
	}

	base := zoneClass.DeepCopy()
	setCondition(&zoneClass.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), status, reason, message, zoneClass.Generation)
	if err := r.Status().Patch(ctx, &zoneClass, client.MergeFrom(base)); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *ZoneClassReconciler) acceptZoneClass(ctx context.Context, zoneClass *dnsv1alpha1.ZoneClass, provider *dnsv1alpha1.Provider, version *dnsv1alpha1.ProviderVersion) (metav1.ConditionStatus, string, string, error) {
	if provider == nil || version == nil || provider.Name != r.providerReference().Name {
		return metav1.ConditionFalse, "InvalidProvider", "ZoneClass provider is not handled by Route 53 controller", nil
	}

	parameters, payloadErr := providercontract.ConvertZoneClassParametersToStorage(provider, version, zoneClass)
	if payloadErr != nil {
		acceptance := providercontract.PayloadErrorToAcceptance(payloadErr)
		return metav1.ConditionStatus(acceptance.Status), acceptance.Reason, acceptance.Message, nil
	}
	converted := zoneClass.DeepCopy()
	converted.Spec.Parameters = parameters
	params, err := route53ZoneClassParameters(converted)
	if err != nil {
		return metav1.ConditionFalse, "InvalidParameters", err.Error(), nil
	}
	if zoneClass.Spec.IdentityRef.Name == "" {
		return metav1.ConditionFalse, "InvalidIdentityRef", "spec.identityRef.name is required", nil
	}

	var identity route53v1alpha1.Route53Identity
	if err := r.Get(ctx, client.ObjectKey{Namespace: zoneClass.Namespace, Name: zoneClass.Spec.IdentityRef.Name}, &identity); err != nil {
		if apierrors.IsNotFound(err) {
			return metav1.ConditionUnknown, "IdentityNotResolved", "referenced Route53Identity was not found", nil
		}
		return metav1.ConditionFalse, "", "", err
	}
	accepted := meta.FindStatusCondition(identity.Status.Conditions, string(dnsv1alpha1.ConditionAccepted))
	if accepted != nil && accepted.Status == metav1.ConditionFalse && accepted.ObservedGeneration == identity.Generation {
		return metav1.ConditionFalse, "InvalidIdentityRef", "referenced Route53Identity is not accepted", nil
	}
	if accepted == nil || accepted.Status != metav1.ConditionTrue || accepted.ObservedGeneration != identity.Generation {
		return metav1.ConditionUnknown, "IdentityNotResolved", "referenced Route53Identity acceptance is not resolved", nil
	}
	if key, ok := reservedTagConflict(params.Tags); ok {
		return metav1.ConditionFalse, "DeniedByPolicy", "tag key " + key + " is reserved", nil
	}

	return metav1.ConditionTrue, "Accepted", "ZoneClass is accepted by Route 53 controller", nil
}

func (r *ZoneClassReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.ControllerName == "" {
		r.ControllerName = DefaultControllerName
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&dnsv1alpha1.ZoneClass{}).
		Watches(&dnsv1alpha1.Provider{}, handler.EnqueueRequestsFromMapFunc(r.mapProviderToZoneClasses)).
		Watches(&route53v1alpha1.Route53Identity{}, handler.EnqueueRequestsFromMapFunc(r.mapIdentityToZoneClasses)).
		Complete(r)
}

func (r *ZoneClassReconciler) mapProviderToZoneClasses(ctx context.Context, obj client.Object) []reconcile.Request {
	provider, ok := obj.(*dnsv1alpha1.Provider)
	if !ok {
		return nil
	}

	var zoneClasses dnsv1alpha1.ZoneClassList
	if err := r.List(ctx, &zoneClasses); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for _, zoneClass := range zoneClasses.Items {
		if zoneClass.Spec.Provider.Name == provider.Name {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&zoneClass)})
		}
	}
	return requests
}

func (r *ZoneClassReconciler) mapIdentityToZoneClasses(ctx context.Context, obj client.Object) []reconcile.Request {
	identity, ok := obj.(*route53v1alpha1.Route53Identity)
	if !ok {
		return nil
	}

	var zoneClasses dnsv1alpha1.ZoneClassList
	if err := r.List(ctx, &zoneClasses, client.InNamespace(identity.Namespace)); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for _, zoneClass := range zoneClasses.Items {
		provider, version, err := resolveDNSProviderVersion(ctx, r.Client, zoneClass.Spec.Provider)
		if err != nil || zoneClass.Spec.ControllerName != r.controllerName() || provider == nil || version == nil || provider.Name != r.providerReference().Name {
			continue
		}
		if zoneClass.Spec.IdentityRef.Name == identity.Name {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&zoneClass)})
		}
	}
	return requests
}

func (r *ZoneClassReconciler) controllerName() string {
	if r.ControllerName != "" {
		return r.ControllerName
	}
	return DefaultControllerName
}

func (r *ZoneClassReconciler) providerReference() dnsv1alpha1.ProviderReference {
	return route53ProviderReference(r.ProviderName, r.ProviderVersion)
}

func isAcceptedCurrent(conditions []metav1.Condition, generation int64) bool {
	condition := meta.FindStatusCondition(conditions, string(dnsv1alpha1.ConditionAccepted))
	return condition != nil &&
		condition.Status == metav1.ConditionTrue &&
		condition.ObservedGeneration == generation
}

func isReadyCurrent(conditions []metav1.Condition, generation int64) bool {
	condition := meta.FindStatusCondition(conditions, "Ready")
	return condition != nil &&
		condition.Status == metav1.ConditionTrue &&
		condition.ObservedGeneration == generation
}

func isProgrammedCurrent(conditions []metav1.Condition, generation int64) bool {
	condition := meta.FindStatusCondition(conditions, string(dnsv1alpha1.ConditionProgrammed))
	return condition != nil &&
		condition.Status == metav1.ConditionTrue &&
		condition.ObservedGeneration == generation
}
