package route53

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/appthrust/dns-api/internal/go/core/providercontract"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	DefaultControllerName = "route53.dns.appthrust.io/controller"

	ZoneFinalizer = "route53.dns.appthrust.io/zoneunit-finalizer"

	defaultRoute53ZoneRequeueAfter   = 10 * time.Minute
	defaultRoute53ChangeCheckAfter   = 10 * time.Second
	route53RecordSetChangeBatchLimit = 1000
)

var reservedHostedZoneTagKeys = map[string]struct{}{
	"appthrust.io/managed-by":           {},
	"appthrust.io/zone-namespace":       {},
	"appthrust.io/zone-name":            {},
	"appthrust.io/zone-class-namespace": {},
	"appthrust.io/zone-class-name":      {},
}

// ZoneReconciler reconciles core Zone objects for Route 53 public hosted zones.
type ZoneReconciler struct {
	client.Client

	Scheme           *runtime.Scheme
	Provider         Provider
	ProviderFactory  ProviderFactory
	ControllerName   string
	ProviderName     string
	ProviderVersion  string
	RequeueAfter     time.Duration
	ChangeCheckAfter time.Duration
	Recorder         record.EventRecorder
}

// +kubebuilder:rbac:groups=dns.appthrust.io,resources=zoneunits,verbs=delete;get;list;watch;update;patch
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=zoneunits/finalizers,verbs=update
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=zoneunits/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=zoneclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=providers,verbs=get;list;watch
// +kubebuilder:rbac:groups=route53.dns.appthrust.io,resources=route53identities,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
func (r *ZoneReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var unit dnsv1alpha1.ZoneUnit
	if err := r.Get(ctx, req.NamespacedName, &unit); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if unit.Spec.Provider.Name != r.providerReference().Name {
		return ctrl.Result{}, nil
	}
	zone := route53ZoneFromZoneUnit(&unit)
	applyRoute53ZoneStatusFromZoneUnit(&zone, &unit)

	if !unit.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &zone, &unit)
	}

	statusData, err := route53ZoneStatusData(&zone)
	if err != nil {
		return ctrl.Result{}, r.setProgrammed(ctx, &zone, metav1.ConditionFalse, "ReconcileError", err.Error())
	}
	if message, mismatch, err := route53ZoneManagedResourceMismatch(&zone, statusData); err != nil {
		return ctrl.Result{}, r.setAccepted(ctx, &zone, metav1.ConditionFalse, "InvalidAdoption", err.Error())
	} else if mismatch {
		return ctrl.Result{}, r.setAccepted(ctx, &zone, metav1.ConditionFalse, "ManagedResourceMismatch", message)
	}

	zoneClass, params, identity, accepted, err := r.acceptZone(ctx, &zone)
	if err != nil || !accepted {
		return ctrl.Result{}, err
	}
	if !isReadyCurrent(identity.Status.Conditions, identity.Generation) {
		if err := r.setProgrammed(ctx, &zone, metav1.ConditionFalse, "ProviderIdentityNotReady", "referenced Route53Identity is not Ready"); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: r.requeueAfter()}, nil
	}

	provider, err := r.providerForIdentity(ctx, identity)
	if err != nil {
		return r.failProgrammedForProviderError(ctx, &zone, err)
	}

	if updated, err := r.reconcileFinalizer(ctx, &unit, params); err != nil {
		return ctrl.Result{}, err
	} else if updated {
		return ctrl.Result{Requeue: true}, nil
	}

	if result, done, err := r.refreshPendingHostedZoneChange(ctx, provider, &zone, statusData); done || err != nil {
		return result, err
	}
	selection, done, result, err := r.selectHostedZone(ctx, provider, &zone, statusData, params)
	if err != nil || done {
		return result, err
	}

	if selection.ID == "" {
		return r.createHostedZone(ctx, provider, &zone, zoneClass, params)
	}

	adoptionID, adopting, err := route53ZoneAdoptionID(&zone)
	if err != nil {
		return ctrl.Result{}, r.setAccepted(ctx, &zone, metav1.ConditionFalse, "InvalidAdoption", err.Error())
	}
	hostedZone, err := provider.GetHostedZone(ctx, selection.ID)
	if err != nil {
		if adopting && isProviderNotFound(err) {
			return ctrl.Result{}, r.setProgrammed(ctx, &zone, metav1.ConditionFalse, "ExternalResourceNotFound", "adopted Route 53 hosted zone was not found")
		}
		return r.failProgrammedForProviderError(ctx, &zone, err)
	}
	if adopting {
		if hostedZone.ID != adoptionID {
			return ctrl.Result{}, r.setProgrammed(ctx, &zone, metav1.ConditionFalse, "ExternalResourceMismatch", "adopted Route 53 hosted zone ID does not match spec.adoption.hostedZoneId")
		}
		if message := hostedZoneSpecMismatch(&zone, params, hostedZone); message != "" {
			return ctrl.Result{}, r.setProgrammed(ctx, &zone, metav1.ConditionFalse, "ExternalResourceMismatch", message)
		}
	} else if message := hostedZoneSpecMismatch(&zone, params, hostedZone); message != "" {
		return ctrl.Result{}, r.setProgrammed(ctx, &zone, metav1.ConditionFalse, "ExternalResourceMismatch", message)
	}

	if err := provider.TagHostedZone(ctx, hostedZone.ID, hostedZoneTags(&zone, zoneClass, params)); err != nil {
		return r.failProgrammedForProviderError(ctx, &zone, err)
	}

	if err := r.patchRoute53ZoneStatus(ctx, &zone, func(data *route53v1alpha1.Route53ZoneStatusData) {
		data.HostedZoneID = hostedZone.ID
		if hostedZone.CallerReference == callerReferenceForZoneState(&zone, statusData) {
			data.CallerReference = hostedZone.CallerReference
		}
	}); err != nil {
		return ctrl.Result{}, err
	}

	result = ctrl.Result{}
	result, err = r.refreshHostedZoneStatus(ctx, provider, &zone, hostedZone)
	if err != nil || result.Requeue || result.RequeueAfter > 0 {
		return result, err
	}

	result, err = r.reconcileZoneRecordSets(ctx, provider, &zone, zoneClass, hostedZone)
	if err != nil || result.Requeue || result.RequeueAfter > 0 {
		return result, err
	}

	return ctrl.Result{RequeueAfter: r.requeueAfter()}, nil
}

func (r *ZoneReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Provider == nil && r.ProviderFactory == nil {
		return errors.New("route53 provider is required")
	}
	if r.Scheme == nil {
		r.Scheme = mgr.GetScheme()
	}
	if r.ControllerName == "" {
		r.ControllerName = DefaultControllerName
	}
	if r.RequeueAfter == 0 {
		r.RequeueAfter = defaultRoute53ZoneRequeueAfter
	}
	if r.ChangeCheckAfter == 0 {
		r.ChangeCheckAfter = defaultRoute53ChangeCheckAfter
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named("route53-zoneunit").
		For(&dnsv1alpha1.ZoneUnit{}).
		Watches(&dnsv1alpha1.ZoneClass{}, handler.EnqueueRequestsFromMapFunc(r.mapZoneClassToZoneUnits)).
		Watches(&dnsv1alpha1.Provider{}, handler.EnqueueRequestsFromMapFunc(r.mapProviderToZoneUnits)).
		Watches(&route53v1alpha1.Route53Identity{}, handler.EnqueueRequestsFromMapFunc(r.mapIdentityToZoneUnits)).
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(r.mapNamespaceToZoneUnits)).
		Complete(r)
}

func (r *ZoneReconciler) mapZoneClassToZoneUnits(ctx context.Context, obj client.Object) []reconcile.Request {
	zoneClass, ok := obj.(*dnsv1alpha1.ZoneClass)
	if !ok {
		return nil
	}
	return r.zoneUnitRequestsForZoneClass(ctx, zoneClass.Namespace, zoneClass.Name)
}

func (r *ZoneReconciler) mapNamespaceToZoneUnits(ctx context.Context, obj client.Object) []reconcile.Request {
	namespace, ok := obj.(*corev1.Namespace)
	if !ok {
		return nil
	}
	var units dnsv1alpha1.ZoneUnitList
	if err := r.List(ctx, &units); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0)
	for _, unit := range units.Items {
		if unit.Namespace == namespace.Name {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&unit)})
			continue
		}
		for _, recordSet := range unit.Spec.RecordSets {
			if recordSet.RecordSetNamespace == namespace.Name {
				requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&unit)})
				break
			}
		}
	}
	return requests
}

func (r *ZoneReconciler) mapProviderToZoneUnits(ctx context.Context, obj client.Object) []reconcile.Request {
	provider, ok := obj.(*dnsv1alpha1.Provider)
	if !ok {
		return nil
	}

	var units dnsv1alpha1.ZoneUnitList
	if err := r.List(ctx, &units); err != nil {
		return nil
	}
	var requests []reconcile.Request
	for _, unit := range units.Items {
		if unit.Spec.Provider.Name == provider.Name {
			requests = appendUniqueRequests(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&unit)})
		}
	}
	return requests
}

func (r *ZoneReconciler) mapIdentityToZoneUnits(ctx context.Context, obj client.Object) []reconcile.Request {
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
		if err != nil || zoneClass.Spec.ControllerName != r.controllerName() || !route53ProviderVersionMatches(provider, version, r.providerReference()) {
			continue
		}
		if zoneClass.Spec.IdentityRef.Name == identity.Name {
			requests = appendUniqueRequests(requests, r.zoneUnitRequestsForZoneClass(ctx, zoneClass.Namespace, zoneClass.Name)...)
		}
	}
	return requests
}

func (r *ZoneReconciler) zoneUnitRequestsForZoneClass(ctx context.Context, namespace, name string) []reconcile.Request {
	var units dnsv1alpha1.ZoneUnitList
	if err := r.List(ctx, &units); err != nil {
		return nil
	}

	requests := make([]reconcile.Request, 0)
	for _, unit := range units.Items {
		if unit.Spec.Zone.ZoneClassRef.Namespace == namespace && unit.Spec.Zone.ZoneClassRef.Name == name {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&unit)})
		}
	}
	return requests
}

func appendUniqueRequests(requests []reconcile.Request, newRequests ...reconcile.Request) []reconcile.Request {
	seen := make(map[reconcile.Request]struct{}, len(requests)+len(newRequests))
	for _, request := range requests {
		seen[request] = struct{}{}
	}
	for _, request := range newRequests {
		if _, ok := seen[request]; ok {
			continue
		}
		requests = append(requests, request)
		seen[request] = struct{}{}
	}
	return requests
}

func (r *ZoneReconciler) acceptZone(ctx context.Context, zone *dnsv1alpha1.Zone) (*dnsv1alpha1.ZoneClass, *route53v1alpha1.Route53ZoneClassParameters, *route53v1alpha1.Route53Identity, bool, error) {
	zoneClassNamespace := zone.Namespace
	if zone.Spec.ZoneClassRef.Namespace != nil && *zone.Spec.ZoneClassRef.Namespace != "" {
		zoneClassNamespace = *zone.Spec.ZoneClassRef.Namespace
	}

	var zoneClass dnsv1alpha1.ZoneClass
	if err := r.Get(ctx, client.ObjectKey{Namespace: zoneClassNamespace, Name: zone.Spec.ZoneClassRef.Name}, &zoneClass); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "InvalidZoneClassRef", "referenced ZoneClass was not found")
		}
		return nil, nil, nil, false, err
	}

	if zoneClass.Spec.ControllerName != r.controllerName() {
		return &zoneClass, nil, nil, false, nil
	}
	provider, version, err := resolveDNSProviderVersion(ctx, r.Client, zoneClass.Spec.Provider)
	if err != nil || provider == nil || version == nil || provider.Name != r.providerReference().Name {
		return nil, nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "InvalidProvider", "Zone provider is not handled by Route 53 controller")
	}

	if zone.Spec.Provider != zoneClass.Spec.Provider {
		return nil, nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "ProviderMismatch", "Zone provider does not match referenced ZoneClass")
	}

	allowed, err := r.zoneAllowedByClass(ctx, &zoneClass, zone.Namespace)
	if err != nil {
		return nil, nil, nil, false, err
	}
	if !allowed {
		return nil, nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "NotAllowedByZoneClass", "Zone namespace is not allowed by ZoneClass")
	}

	if !isAcceptedCurrent(zoneClass.Status.Conditions, zoneClass.Generation) {
		return nil, nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "ZoneClassNotAccepted", "referenced ZoneClass is not accepted")
	}

	parameters, payloadErr := providercontract.ConvertZoneClassParametersToStorage(provider, version, &zoneClass)
	if payloadErr != nil {
		acceptance := providercontract.PayloadErrorToAcceptance(payloadErr)
		return nil, nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionStatus(acceptance.Status), acceptance.Reason, acceptance.Message)
	}
	converted := zoneClass.DeepCopy()
	converted.Spec.Parameters = parameters
	params, err := route53ZoneClassParameters(converted)
	if err != nil {
		return nil, nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "InvalidProvider", err.Error())
	}

	identity, accepted, err := r.acceptIdentityRef(ctx, zone, &zoneClass)
	if err != nil || !accepted {
		return nil, nil, nil, false, err
	}
	_, adopting, err := route53ZoneAdoptionID(zone)
	if err != nil {
		return nil, nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "InvalidAdoption", err.Error())
	}
	if !adopting && zoneCreationPolicy(params) == route53v1alpha1.ZoneCreationPolicyDeny {
		return nil, nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "DeniedByPolicy", "Route 53 parameters deny hosted zone creation")
	}
	if key, ok := reservedTagConflict(params.Tags); ok {
		return nil, nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "DeniedByPolicy", fmt.Sprintf("tag key %q is reserved", key))
	}

	if err := r.setAccepted(ctx, zone, metav1.ConditionTrue, "Accepted", "Zone is accepted by Route 53 policy"); err != nil {
		return nil, nil, nil, false, err
	}
	return &zoneClass, params, identity, true, nil
}

func (r *ZoneReconciler) acceptIdentityRef(ctx context.Context, zone *dnsv1alpha1.Zone, zoneClass *dnsv1alpha1.ZoneClass) (*route53v1alpha1.Route53Identity, bool, error) {
	if zoneClass.Spec.IdentityRef.Name == "" {
		return nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "InvalidIdentityRef", "spec.identityRef.name is required")
	}

	var identity route53v1alpha1.Route53Identity
	if err := r.Get(ctx, client.ObjectKey{Namespace: zoneClass.Namespace, Name: zoneClass.Spec.IdentityRef.Name}, &identity); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, false, r.setAccepted(ctx, zone, metav1.ConditionUnknown, "IdentityNotResolved", "referenced Route53Identity was not found")
		}
		return nil, false, err
	}

	accepted := meta.FindStatusCondition(identity.Status.Conditions, string(dnsv1alpha1.ConditionAccepted))
	if accepted != nil && accepted.Status == metav1.ConditionFalse && accepted.ObservedGeneration == identity.Generation {
		return nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "InvalidIdentityRef", "referenced Route53Identity is not accepted")
	}
	if accepted == nil || accepted.Status != metav1.ConditionTrue || accepted.ObservedGeneration != identity.Generation {
		return nil, false, r.setAccepted(ctx, zone, metav1.ConditionUnknown, "IdentityNotResolved", "referenced Route53Identity acceptance is not resolved")
	}

	return &identity, true, nil
}

func (r *ZoneReconciler) providerForIdentity(ctx context.Context, identity *route53v1alpha1.Route53Identity) (Provider, error) {
	if r.ProviderFactory != nil {
		return r.ProviderFactory.ProviderForIdentity(ctx, identity)
	}
	if r.Provider != nil {
		return r.Provider, nil
	}
	return nil, errors.New("route53 provider is required")
}

func (r *ZoneReconciler) reconcileFinalizer(ctx context.Context, unit *dnsv1alpha1.ZoneUnit, params *route53v1alpha1.Route53ZoneClassParameters) (bool, error) {
	hasFinalizer := slices.Contains(unit.Finalizers, ZoneFinalizer)
	shouldHaveFinalizer := zoneDeletionPolicy(params) == route53v1alpha1.ZoneDeletionPolicyDelete

	switch {
	case shouldHaveFinalizer && !hasFinalizer:
		base := unit.DeepCopy()
		unit.Finalizers = append(unit.Finalizers, ZoneFinalizer)
		return true, client.IgnoreNotFound(r.Patch(ctx, unit, client.MergeFrom(base)))
	case !shouldHaveFinalizer && hasFinalizer:
		base := unit.DeepCopy()
		unit.Finalizers = slices.DeleteFunc(unit.Finalizers, func(finalizer string) bool {
			return finalizer == ZoneFinalizer
		})
		return true, client.IgnoreNotFound(r.Patch(ctx, unit, client.MergeFrom(base)))
	default:
		return false, nil
	}
}

func (r *ZoneReconciler) reconcileDelete(ctx context.Context, zone *dnsv1alpha1.Zone, unit *dnsv1alpha1.ZoneUnit) (ctrl.Result, error) {
	if !slices.Contains(unit.Finalizers, ZoneFinalizer) {
		return ctrl.Result{}, nil
	}

	params, identity, accepted, err := r.deleteContextForZone(ctx, zone)
	if err != nil || !accepted {
		return ctrl.Result{}, err
	}
	if zoneDeletionPolicy(params) == route53v1alpha1.ZoneDeletionPolicyRetain {
		r.recordEvent(zone, corev1.EventTypeNormal, "ExternalResourceRetained", "Zone was deleted and the external DNS zone was retained by policy")
		return ctrl.Result{}, r.removeFinalizer(ctx, unit)
	}
	if !isReadyCurrent(identity.Status.Conditions, identity.Generation) {
		if err := r.setProgrammed(ctx, zone, metav1.ConditionFalse, "ProviderIdentityNotReady", "referenced Route53Identity is not Ready"); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: r.requeueAfter()}, nil
	}
	provider, err := r.providerForIdentity(ctx, identity)
	if err != nil {
		return r.failProgrammedForProviderError(ctx, zone, err)
	}

	statusData, err := route53ZoneStatusData(zone)
	if err != nil {
		return ctrl.Result{}, r.setProgrammed(ctx, zone, metav1.ConditionFalse, "ReconcileError", err.Error())
	}
	if result, done, err := r.refreshDeletingHostedZoneChange(ctx, provider, zone, statusData); done || err != nil {
		return result, err
	}

	hostedZoneID, err := hostedZoneIDForDelete(zone, statusData)
	if err != nil {
		return ctrl.Result{}, r.setAccepted(ctx, zone, metav1.ConditionFalse, "InvalidAdoption", err.Error())
	}
	if hostedZoneID != "" {
		if _, err := provider.GetHostedZone(ctx, hostedZoneID); err != nil {
			if isProviderNotFound(err) {
				return ctrl.Result{}, r.removeFinalizer(ctx, unit)
			}
			return r.failProgrammedForProviderError(ctx, zone, err)
		}

		change, err := provider.DeleteHostedZone(ctx, hostedZoneID)
		if err != nil {
			return r.failProgrammedForProviderError(ctx, zone, err)
		}
		r.recordEvent(zone, corev1.EventTypeNormal, "Route53HostedZoneChangeSubmitted", fmt.Sprintf("Route 53 hosted zone delete change was submitted for %s", hostedZoneID))
		if change != nil {
			if err := r.patchRoute53ZoneStatus(ctx, zone, func(data *route53v1alpha1.Route53ZoneStatusData) {
				data.PendingHostedZoneChange = pendingChangeFromChange(change, "DELETE")
			}); err != nil {
				return ctrl.Result{}, err
			}
		}
		if change != nil && change.Status == route53v1alpha1.Route53ChangeStatusPending {
			if err := r.setProgrammed(ctx, zone, metav1.ConditionFalse, "ProviderChangePending", "Route 53 hosted zone deletion is pending"); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: r.changeCheckAfter()}, nil
		}
	}

	r.recordEvent(zone, corev1.EventTypeNormal, "ExternalResourceDeleted", "Zone was deleted and the external DNS zone was deleted")
	return ctrl.Result{}, r.removeFinalizer(ctx, unit)
}

func (r *ZoneReconciler) refreshDeletingHostedZoneChange(ctx context.Context, provider Provider, zone *dnsv1alpha1.Zone, statusData route53v1alpha1.Route53ZoneStatusData) (ctrl.Result, bool, error) {
	pendingChange := statusData.PendingHostedZoneChange
	if pendingChange == nil || pendingChange.ID == "" || pendingChange.Status != route53v1alpha1.Route53ChangeStatusPending || pendingChange.Operation != "DELETE" {
		return ctrl.Result{}, false, nil
	}

	change, err := provider.GetChange(ctx, pendingChange.ID)
	if err != nil {
		result, err := r.failProgrammedForProviderError(ctx, zone, err)
		return result, true, err
	}
	if change != nil {
		pendingChange = pendingChangeFromChange(change, "DELETE")
		if err := r.patchRoute53ZoneStatus(ctx, zone, func(data *route53v1alpha1.Route53ZoneStatusData) {
			data.PendingHostedZoneChange = pendingChange
		}); err != nil {
			return ctrl.Result{}, true, err
		}
	}
	if pendingChange != nil && pendingChange.Status == route53v1alpha1.Route53ChangeStatusPending {
		if err := r.setProgrammed(ctx, zone, metav1.ConditionFalse, "ProviderChangePending", "Route 53 hosted zone deletion is pending"); err != nil {
			return ctrl.Result{}, true, err
		}
		return ctrl.Result{RequeueAfter: r.changeCheckAfter()}, true, nil
	}
	if pendingChange != nil {
		r.recordEvent(zone, corev1.EventTypeNormal, "Route53HostedZoneChangeInSync", fmt.Sprintf("Route 53 hosted zone delete change %s is INSYNC", pendingChange.ID))
	}
	if err := r.patchRoute53ZoneStatus(ctx, zone, func(data *route53v1alpha1.Route53ZoneStatusData) {
		data.PendingHostedZoneChange = nil
	}); err != nil {
		return ctrl.Result{}, true, err
	}
	return ctrl.Result{}, false, nil
}

func (r *ZoneReconciler) deleteContextForZone(ctx context.Context, zone *dnsv1alpha1.Zone) (*route53v1alpha1.Route53ZoneClassParameters, *route53v1alpha1.Route53Identity, bool, error) {
	zoneClassNamespace := zone.Namespace
	if zone.Spec.ZoneClassRef.Namespace != nil && *zone.Spec.ZoneClassRef.Namespace != "" {
		zoneClassNamespace = *zone.Spec.ZoneClassRef.Namespace
	}

	var zoneClass dnsv1alpha1.ZoneClass
	if err := r.Get(ctx, client.ObjectKey{Namespace: zoneClassNamespace, Name: zone.Spec.ZoneClassRef.Name}, &zoneClass); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "InvalidZoneClassRef", "referenced ZoneClass was not found")
		}
		return nil, nil, false, err
	}

	if zoneClass.Spec.ControllerName != r.controllerName() {
		return nil, nil, false, nil
	}
	provider, version, err := resolveDNSProviderVersion(ctx, r.Client, zoneClass.Spec.Provider)
	if err != nil || provider == nil || version == nil || provider.Name != r.providerReference().Name {
		return nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "InvalidProvider", "Zone provider is not handled by Route 53 controller")
	}
	if zone.Spec.Provider != zoneClass.Spec.Provider {
		return nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "ProviderMismatch", "Zone provider does not match referenced ZoneClass")
	}
	parameters, payloadErr := providercontract.ConvertZoneClassParametersToStorage(provider, version, &zoneClass)
	if payloadErr != nil {
		acceptance := providercontract.PayloadErrorToAcceptance(payloadErr)
		return nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionStatus(acceptance.Status), acceptance.Reason, acceptance.Message)
	}
	converted := zoneClass.DeepCopy()
	converted.Spec.Parameters = parameters
	params, err := route53ZoneClassParameters(converted)
	if err != nil {
		return nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "InvalidProvider", err.Error())
	}

	identity, accepted, err := r.acceptIdentityRef(ctx, zone, &zoneClass)
	return params, identity, accepted, err
}

func (r *ZoneReconciler) selectHostedZone(ctx context.Context, provider Provider, zone *dnsv1alpha1.Zone, statusData route53v1alpha1.Route53ZoneStatusData, params *route53v1alpha1.Route53ZoneClassParameters) (HostedZone, bool, ctrl.Result, error) {
	if adoptionID, adopting, err := route53ZoneAdoptionID(zone); err != nil {
		return HostedZone{}, true, ctrl.Result{}, r.setAccepted(ctx, zone, metav1.ConditionFalse, "InvalidAdoption", err.Error())
	} else if adopting {
		return HostedZone{ID: adoptionID}, false, ctrl.Result{}, nil
	}

	statusID := normalizeHostedZoneID(statusData.HostedZoneID)
	if statusID != "" {
		if hostedZone, err := provider.GetHostedZone(ctx, statusID); err != nil {
			if isProviderNotFound(err) {
				if err := r.clearHostedZoneProviderStatus(ctx, zone); err != nil {
					return HostedZone{}, true, ctrl.Result{}, err
				}
			} else {
				result, err := r.failProgrammedForProviderError(ctx, zone, err)
				return HostedZone{}, true, result, err
			}
		} else {
			return hostedZone, false, ctrl.Result{}, nil
		}
	}

	sameNameZones, err := provider.ListHostedZonesByName(ctx, zone.Spec.DomainName)
	if err != nil {
		result, err := r.failProgrammedForProviderError(ctx, zone, err)
		return HostedZone{}, true, result, err
	}

	callerReference := callerReferenceForZoneState(zone, statusData)
	var selected HostedZone
	for _, hostedZone := range sameNameZones {
		if hostedZone.Private {
			continue
		}
		if hostedZone.CallerReference == callerReference {
			selected = hostedZone
			break
		}
	}

	if params != nil && sameNameZonePolicy(params) == route53v1alpha1.SameNameZonePolicyDeny {
		for _, hostedZone := range sameNameZones {
			if hostedZone.Private {
				continue
			}
			if selected.ID != "" && sameHostedZoneID(hostedZone.ID, selected.ID) {
				continue
			}
			err := r.setProgrammed(ctx, zone, metav1.ConditionFalse, "ProviderConflict", "same-name Route 53 hosted zone already exists")
			return HostedZone{}, true, ctrl.Result{}, err
		}
	}

	return selected, false, ctrl.Result{}, nil
}

func (r *ZoneReconciler) clearHostedZoneProviderStatus(ctx context.Context, zone *dnsv1alpha1.Zone) error {
	statusData, err := route53ZoneStatusData(zone)
	if err != nil {
		return err
	}
	statusData.HostedZoneID = ""
	statusData.CallerReference = ""
	statusData.PendingHostedZoneChange = nil
	statusData.PendingRecordSetChange = nil
	raw, err := json.Marshal(statusData)
	if err != nil {
		return err
	}
	return r.patchZoneStatus(ctx, zone, func(status *dnsv1alpha1.ZoneStatus) {
		status.NameServers = nil
		status.Provider = &dnsv1alpha1.ProviderStatus{State: runtime.RawExtension{Raw: raw}}
	})
}

func (r *ZoneReconciler) createHostedZone(ctx context.Context, provider Provider, zone *dnsv1alpha1.Zone, zoneClass *dnsv1alpha1.ZoneClass, params *route53v1alpha1.Route53ZoneClassParameters) (ctrl.Result, error) {
	statusData, err := route53ZoneStatusData(zone)
	if err != nil {
		return ctrl.Result{}, err
	}
	callerReference := callerReferenceForZoneState(zone, statusData)
	if statusData.CallerReference == "" {
		if err := r.patchRoute53ZoneStatus(ctx, zone, func(data *route53v1alpha1.Route53ZoneStatusData) {
			data.CallerReference = callerReference
		}); err != nil {
			return ctrl.Result{}, err
		}
	}

	created, err := provider.CreateHostedZone(ctx, zone.Spec.DomainName, callerReference)
	if err != nil {
		return r.failProgrammedForProviderError(ctx, zone, err)
	}
	if created.Change != nil {
		r.recordEvent(zone, corev1.EventTypeNormal, "Route53HostedZoneChangeSubmitted", fmt.Sprintf("Route 53 hosted zone create change was submitted for %s", created.HostedZone.ID))
	}

	if err := provider.TagHostedZone(ctx, created.HostedZone.ID, hostedZoneTags(zone, zoneClass, params)); err != nil {
		return r.failProgrammedForProviderError(ctx, zone, err)
	}

	if err := r.patchRoute53ZoneStatus(ctx, zone, func(data *route53v1alpha1.Route53ZoneStatusData) {
		data.HostedZoneID = created.HostedZone.ID
		data.CallerReference = callerReference
		if created.Change != nil && created.Change.Status == route53v1alpha1.Route53ChangeStatusPending {
			data.PendingHostedZoneChange = pendingChangeFromChange(created.Change, "CREATE")
		}
	}); err != nil {
		return ctrl.Result{}, err
	}

	return r.refreshHostedZoneStatus(ctx, provider, zone, created.HostedZone)
}

func (r *ZoneReconciler) refreshHostedZoneStatus(ctx context.Context, provider Provider, zone *dnsv1alpha1.Zone, hostedZone HostedZone) (ctrl.Result, error) {
	statusData, err := route53ZoneStatusData(zone)
	if err != nil {
		return ctrl.Result{}, r.setProgrammed(ctx, zone, metav1.ConditionFalse, "ReconcileError", err.Error())
	}
	pendingChange := statusData.PendingHostedZoneChange
	if pendingChange != nil && pendingChange.ID != "" && pendingChange.Status == route53v1alpha1.Route53ChangeStatusPending {
		change, err := provider.GetChange(ctx, pendingChange.ID)
		if err != nil {
			return r.failProgrammedForProviderError(ctx, zone, err)
		}
		if change != nil {
			pendingChange = pendingChangeFromChange(change, pendingChange.Operation)
			if err := r.patchRoute53ZoneStatus(ctx, zone, func(data *route53v1alpha1.Route53ZoneStatusData) {
				data.PendingHostedZoneChange = pendingChange
			}); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	if err := r.patchZoneStatus(ctx, zone, func(status *dnsv1alpha1.ZoneStatus) {
		status.NameServers = slices.Clone(hostedZone.NameServers)
		if pendingChange != nil && pendingChange.Status == route53v1alpha1.Route53ChangeStatusPending {
			setCondition(&status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ProviderChangePending", "Route 53 change is pending", zone.Generation)
			return
		}
		setCondition(&status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionTrue, "Programmed", "Route 53 hosted zone is programmed", zone.Generation)
	}); err != nil {
		return ctrl.Result{}, err
	}

	if pendingChange != nil && pendingChange.Status == route53v1alpha1.Route53ChangeStatusPending {
		return ctrl.Result{RequeueAfter: r.changeCheckAfter()}, nil
	}
	if pendingChange != nil {
		r.recordEvent(zone, corev1.EventTypeNormal, "Route53HostedZoneChangeInSync", fmt.Sprintf("Route 53 hosted zone change %s is INSYNC", pendingChange.ID))
		return ctrl.Result{Requeue: true}, r.patchRoute53ZoneStatus(ctx, zone, func(data *route53v1alpha1.Route53ZoneStatusData) {
			data.PendingHostedZoneChange = nil
		})
	}
	return ctrl.Result{}, nil
}

func (r *ZoneReconciler) refreshPendingHostedZoneChange(ctx context.Context, provider Provider, zone *dnsv1alpha1.Zone, statusData route53v1alpha1.Route53ZoneStatusData) (ctrl.Result, bool, error) {
	pendingChange := statusData.PendingHostedZoneChange
	if pendingChange == nil || pendingChange.ID == "" || pendingChange.Status != route53v1alpha1.Route53ChangeStatusPending {
		return ctrl.Result{}, false, nil
	}

	change, err := provider.GetChange(ctx, pendingChange.ID)
	if err != nil {
		result, err := r.failProgrammedForProviderError(ctx, zone, err)
		return result, true, err
	}
	if change != nil {
		pendingChange = pendingChangeFromChange(change, pendingChange.Operation)
		if err := r.patchRoute53ZoneStatus(ctx, zone, func(data *route53v1alpha1.Route53ZoneStatusData) {
			data.PendingHostedZoneChange = pendingChange
		}); err != nil {
			return ctrl.Result{}, true, err
		}
	}
	if pendingChange != nil && pendingChange.Status == route53v1alpha1.Route53ChangeStatusPending {
		if err := r.setProgrammed(ctx, zone, metav1.ConditionFalse, "ProviderChangePending", "Route 53 hosted zone change is pending"); err != nil {
			return ctrl.Result{}, true, err
		}
		return ctrl.Result{RequeueAfter: r.changeCheckAfter()}, true, nil
	}
	if pendingChange != nil {
		r.recordEvent(zone, corev1.EventTypeNormal, "Route53HostedZoneChangeInSync", fmt.Sprintf("Route 53 hosted zone change %s is INSYNC", pendingChange.ID))
	}
	if err := r.patchRoute53ZoneStatus(ctx, zone, func(data *route53v1alpha1.Route53ZoneStatusData) {
		data.PendingHostedZoneChange = nil
	}); err != nil {
		return ctrl.Result{}, true, err
	}
	return ctrl.Result{Requeue: true}, true, nil
}
