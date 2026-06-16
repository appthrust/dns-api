package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/appthrust/dns-api/internal/go/core/providercontract"
	cloudflarev1alpha1 "github.com/appthrust/dns-api/pkg/go/api/cloudflare/v1alpha1"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	ZoneFinalizer = "cloudflare.dns.appthrust.io/zoneunit-finalizer"

	defaultZoneRequeueAfter         = 10 * time.Minute
	defaultCloudflarePendingPoll    = 10 * time.Second
	defaultCloudflareTemporaryRetry = 10 * time.Second
)

type CloudflareZone struct {
	ID          string
	AccountID   string
	Name        string
	Status      string
	Type        string
	NameServers []string
}

type ZoneProvider interface {
	GetZone(ctx context.Context, id string) (CloudflareZone, error)
	ListZonesByName(ctx context.Context, accountID, name string) ([]CloudflareZone, error)
	CreateZone(ctx context.Context, accountID, name string) (CloudflareZone, error)
	DeleteZone(ctx context.Context, id string) error
}

type cloudflareZoneDNSRecordLister interface {
	ListDNSRecords(ctx context.Context, zoneID, name string) ([]CloudflareDNSRecord, error)
}

type ZoneProviderFactory interface {
	ProviderForIdentity(ctx context.Context, k8sClient client.Client, identity *cloudflarev1alpha1.CloudflareIdentity) (ZoneProvider, error)
}

type ZoneReconciler struct {
	client.Client

	Scheme              *runtime.Scheme
	Provider            ZoneProvider
	ProviderFactory     ZoneProviderFactory
	ControllerName      string
	ProviderName        string
	ProviderVersion     string
	RequeueAfter        time.Duration
	PendingPollAfter    time.Duration
	TemporaryRetryAfter time.Duration
	Recorder            record.EventRecorder
}

// +kubebuilder:rbac:groups=dns.appthrust.io,resources=zoneunits,verbs=delete;get;list;watch;update;patch
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=zoneunits/finalizers,verbs=update
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=zoneunits/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=zoneclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=providers,verbs=get;list;watch
// +kubebuilder:rbac:groups=cloudflare.dns.appthrust.io,resources=cloudflareidentities,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
func (r *ZoneReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var unit dnsv1alpha1.ZoneUnit
	if err := r.Get(ctx, req.NamespacedName, &unit); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if unit.Spec.Provider.Name != r.providerReference().Name {
		return ctrl.Result{}, nil
	}
	zone := cloudflareZoneFromZoneUnit(&unit)
	applyCloudflareZoneStatusFromZoneUnit(&zone, &unit)
	if !unit.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &zone, &unit)
	}

	statusData, err := cloudflareZoneStatusData(&zone)
	if err != nil {
		return ctrl.Result{}, r.setProgrammed(ctx, &zone, metav1.ConditionFalse, "ReconcileError", err.Error())
	}
	statusState, err := cloudflareZoneState(&zone)
	if err != nil {
		return ctrl.Result{}, r.setProgrammed(ctx, &zone, metav1.ConditionFalse, "ReconcileError", err.Error())
	}
	if message, mismatch, err := cloudflareZoneManagedResourceMismatch(&zone, statusData); err != nil {
		return ctrl.Result{}, r.setAccepted(ctx, &zone, metav1.ConditionFalse, "InvalidAdoption", err.Error())
	} else if mismatch {
		return ctrl.Result{}, r.setAccepted(ctx, &zone, metav1.ConditionFalse, "ManagedResourceMismatch", message)
	}

	zoneClass, params, identity, accepted, err := r.acceptZone(ctx, &zone)
	if err != nil || !accepted {
		return ctrl.Result{}, err
	}
	if !cloudflareConditionCurrent(identity.Status.Conditions, string(dnsv1alpha1.ConditionReady), identity.Generation) {
		if err := r.setProgrammed(ctx, &zone, metav1.ConditionFalse, "ProviderIdentityNotReady", "referenced CloudflareIdentity is not Ready"); err != nil {
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

	cloudflareZone, done, result, err := r.selectZone(ctx, provider, &zone, statusData, statusState, identity, params)
	if err != nil || done {
		return result, err
	}
	if cloudflareZone.ID == "" {
		if err := r.markCloudflareZoneCreationIntent(ctx, &zone, identity); err != nil {
			return ctrl.Result{}, err
		}
		cloudflareZone, err = provider.CreateZone(ctx, identity.Status.Account.ID, zone.Spec.DomainName)
		if err != nil {
			return r.failProgrammedForProviderError(ctx, &zone, err)
		}
		r.recordEvent(&zone, corev1.EventTypeNormal, "CloudflareZoneCreated", fmt.Sprintf("Cloudflare zone %s was created", cloudflareZone.ID))
	}

	if done, err := r.applyObservedZone(ctx, &zone, identity, cloudflareZone); err != nil || done {
		return ctrl.Result{}, err
	}
	if cloudflareZone.Status == "initializing" {
		return ctrl.Result{RequeueAfter: r.pendingPollAfter()}, r.setProgrammed(ctx, &zone, metav1.ConditionFalse, "ProviderChangePending", "Cloudflare zone is initializing")
	}
	if cloudflareZone.Status == "moved" {
		reason := "ProviderConflict"
		if _, adopting, _ := cloudflareZoneAdoptionID(&zone); adopting {
			reason = "ExternalResourceMismatch"
		}
		return ctrl.Result{}, r.setProgrammed(ctx, &zone, metav1.ConditionFalse, reason, "Cloudflare zone has moved")
	}
	if cloudflareZone.Status != "pending" && cloudflareZone.Status != "active" {
		return ctrl.Result{RequeueAfter: r.pendingPollAfter()}, r.setProgrammed(ctx, &zone, metav1.ConditionFalse, "ProviderChangePending", fmt.Sprintf("Cloudflare zone status %q is pending provider-side completion", cloudflareZone.Status))
	}
	if err := r.setProgrammed(ctx, &zone, metav1.ConditionTrue, "Programmed", "Cloudflare zone matches desired state"); err != nil {
		return ctrl.Result{}, err
	}
	_ = zoneClass
	if result, err := r.reconcileRecordSets(ctx, &unit); err != nil || result.Requeue || result.RequeueAfter > 0 {
		return result, err
	}
	return ctrl.Result{RequeueAfter: r.requeueAfter()}, nil
}

func (r *ZoneReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Provider == nil && r.ProviderFactory == nil {
		return errors.New("cloudflare provider is required")
	}
	if r.Scheme == nil {
		r.Scheme = mgr.GetScheme()
	}
	if r.ControllerName == "" {
		r.ControllerName = DefaultControllerName
	}
	if r.RequeueAfter == 0 {
		r.RequeueAfter = defaultZoneRequeueAfter
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("cloudflare-zoneunit").
		For(&dnsv1alpha1.ZoneUnit{}).
		Watches(&dnsv1alpha1.ZoneClass{}, handler.EnqueueRequestsFromMapFunc(r.mapZoneClassToZoneUnits)).
		Watches(&dnsv1alpha1.Provider{}, handler.EnqueueRequestsFromMapFunc(r.mapProviderToZoneUnits)).
		Watches(&cloudflarev1alpha1.CloudflareIdentity{}, handler.EnqueueRequestsFromMapFunc(r.mapIdentityToZoneUnits)).
		Complete(r)
}

func (r *ZoneReconciler) acceptZone(ctx context.Context, zone *dnsv1alpha1.Zone) (*dnsv1alpha1.ZoneClass, *cloudflarev1alpha1.CloudflareZoneClassParameters, *cloudflarev1alpha1.CloudflareIdentity, bool, error) {
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
		return nil, nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "InvalidProvider", "Zone provider is not handled by Cloudflare controller")
	}
	if zone.Spec.Provider != zoneClass.Spec.Provider {
		return nil, nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "ProviderMismatch", "Zone provider does not match referenced ZoneClass")
	}
	if allowed, err := r.zoneAllowedByClass(ctx, &zoneClass, zone.Namespace); err != nil {
		return nil, nil, nil, false, err
	} else if !allowed {
		return nil, nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "NotAllowedByZoneClass", "Zone namespace is not allowed by ZoneClass")
	}
	if !cloudflareConditionCurrent(zoneClass.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), zoneClass.Generation) {
		return nil, nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "ZoneClassNotAccepted", "referenced ZoneClass is not accepted")
	}
	parameters, payloadErr := providercontract.ConvertZoneClassParametersToStorage(provider, version, &zoneClass)
	if payloadErr != nil {
		acceptance := providercontract.PayloadErrorToAcceptance(payloadErr)
		return nil, nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionStatus(acceptance.Status), acceptance.Reason, acceptance.Message)
	}
	converted := zoneClass.DeepCopy()
	converted.Spec.Parameters = parameters
	params, err := cloudflareZoneClassParameters(converted)
	if err != nil {
		return nil, nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "InvalidProvider", err.Error())
	}
	identity, accepted, err := r.acceptIdentityRef(ctx, zone, &zoneClass)
	if err != nil || !accepted {
		return nil, nil, nil, false, err
	}
	if _, adopting, err := cloudflareZoneAdoptionID(zone); err != nil {
		return nil, nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "InvalidAdoption", err.Error())
	} else if !adopting && zoneCreationPolicy(params) == cloudflarev1alpha1.ZoneCreationPolicyDeny {
		return nil, nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "DeniedByPolicy", "Cloudflare parameters deny zone creation")
	}
	if err := r.setAccepted(ctx, zone, metav1.ConditionTrue, "Accepted", "Zone is accepted by Cloudflare policy"); err != nil {
		return nil, nil, nil, false, err
	}
	return &zoneClass, params, identity, true, nil
}

func (r *ZoneReconciler) acceptIdentityRef(ctx context.Context, zone *dnsv1alpha1.Zone, zoneClass *dnsv1alpha1.ZoneClass) (*cloudflarev1alpha1.CloudflareIdentity, bool, error) {
	if zoneClass.Spec.IdentityRef.Name == "" {
		return nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "InvalidIdentityRef", "spec.identityRef.name is required")
	}
	var identity cloudflarev1alpha1.CloudflareIdentity
	if err := r.Get(ctx, client.ObjectKey{Namespace: zoneClass.Namespace, Name: zoneClass.Spec.IdentityRef.Name}, &identity); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, false, r.setAccepted(ctx, zone, metav1.ConditionUnknown, "IdentityNotResolved", "referenced CloudflareIdentity was not found")
		}
		return nil, false, err
	}
	accepted := meta.FindStatusCondition(identity.Status.Conditions, string(dnsv1alpha1.ConditionAccepted))
	if accepted != nil && accepted.Status == metav1.ConditionFalse && accepted.ObservedGeneration == identity.Generation {
		return nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "InvalidIdentityRef", "referenced CloudflareIdentity is not accepted")
	}
	if accepted == nil || accepted.Status != metav1.ConditionTrue || accepted.ObservedGeneration != identity.Generation {
		return nil, false, r.setAccepted(ctx, zone, metav1.ConditionUnknown, "IdentityNotResolved", "referenced CloudflareIdentity acceptance is not resolved")
	}
	return &identity, true, nil
}

func (r *ZoneReconciler) selectZone(ctx context.Context, provider ZoneProvider, zone *dnsv1alpha1.Zone, statusData cloudflarev1alpha1.CloudflareZoneStatusData, statusState cloudflarev1alpha1.CloudflareZoneState, identity *cloudflarev1alpha1.CloudflareIdentity, params *cloudflarev1alpha1.CloudflareZoneClassParameters) (CloudflareZone, bool, ctrl.Result, error) {
	if adoptionID, adopting, err := cloudflareZoneAdoptionID(zone); err != nil {
		return CloudflareZone{}, true, ctrl.Result{}, r.setAccepted(ctx, zone, metav1.ConditionFalse, "InvalidAdoption", err.Error())
	} else if adopting {
		observed, err := provider.GetZone(ctx, adoptionID)
		if err != nil {
			if isCloudflareNotFound(err) {
				return CloudflareZone{}, true, ctrl.Result{}, r.setProgrammed(ctx, zone, metav1.ConditionFalse, "ExternalResourceNotFound", "adopted Cloudflare zone was not found")
			}
			result, err := r.failProgrammedForProviderError(ctx, zone, err)
			return CloudflareZone{}, true, result, err
		}
		if message := cloudflareZoneMismatch(zone, identity, observed); message != "" {
			return CloudflareZone{}, true, ctrl.Result{}, r.setProgrammed(ctx, zone, metav1.ConditionFalse, "ExternalResourceMismatch", message)
		}
		return observed, false, ctrl.Result{}, nil
	}
	if statusData.Zone.ID != "" {
		observed, err := provider.GetZone(ctx, statusData.Zone.ID)
		if err == nil {
			if message := cloudflareZoneMismatch(zone, identity, observed); message != "" {
				return CloudflareZone{}, true, ctrl.Result{}, r.setProgrammed(ctx, zone, metav1.ConditionFalse, "ProviderConflict", message)
			}
			return observed, false, ctrl.Result{}, nil
		}
		if !isCloudflareNotFound(err) {
			result, err := r.failProgrammedForProviderError(ctx, zone, err)
			return CloudflareZone{}, true, result, err
		}
	}
	sameNameZones, err := provider.ListZonesByName(ctx, identity.Status.Account.ID, zone.Spec.DomainName)
	if err != nil {
		result, err := r.failProgrammedForProviderError(ctx, zone, err)
		return CloudflareZone{}, true, result, err
	}
	if len(sameNameZones) > 0 {
		if observed, recovered, err := r.recoverCreatedCloudflareZone(ctx, provider, zone, statusState, identity, sameNameZones); err != nil {
			result, err := r.failProgrammedForProviderError(ctx, zone, err)
			return CloudflareZone{}, true, result, err
		} else if recovered {
			return observed, false, ctrl.Result{}, nil
		}
		return CloudflareZone{}, true, ctrl.Result{}, r.setProgrammed(ctx, zone, metav1.ConditionFalse, "ProviderConflict", "same-name Cloudflare zone already exists")
	}
	if zoneCreationPolicy(params) == cloudflarev1alpha1.ZoneCreationPolicyDeny {
		return CloudflareZone{}, true, ctrl.Result{}, r.setAccepted(ctx, zone, metav1.ConditionFalse, "DeniedByPolicy", "Cloudflare parameters deny zone creation")
	}
	return CloudflareZone{}, false, ctrl.Result{}, nil
}

func (r *ZoneReconciler) reconcileDelete(ctx context.Context, zone *dnsv1alpha1.Zone, unit *dnsv1alpha1.ZoneUnit) (ctrl.Result, error) {
	if !slices.Contains(unit.Finalizers, ZoneFinalizer) {
		return ctrl.Result{}, nil
	}
	params, identity, accepted, err := r.deleteContextForZone(ctx, zone)
	if err != nil || !accepted {
		return ctrl.Result{}, err
	}
	if zoneDeletionPolicy(params) == cloudflarev1alpha1.ZoneDeletionPolicyRetain {
		r.recordEvent(zone, corev1.EventTypeNormal, "ExternalResourceRetained", "Zone was deleted and the external DNS zone was retained by policy")
		return ctrl.Result{}, r.removeFinalizer(ctx, unit)
	}
	if count := len(unit.Spec.RecordSets); count > 0 {
		return ctrl.Result{RequeueAfter: r.requeueAfter()}, r.setProgrammed(ctx, zone, metav1.ConditionFalse, "ProviderConflict", fmt.Sprintf("%d RecordSet resources still reference this Zone", count))
	}
	if !cloudflareConditionCurrent(identity.Status.Conditions, string(dnsv1alpha1.ConditionReady), identity.Generation) {
		return ctrl.Result{RequeueAfter: r.requeueAfter()}, r.setProgrammed(ctx, zone, metav1.ConditionFalse, "ProviderIdentityNotReady", "referenced CloudflareIdentity is not Ready")
	}
	provider, err := r.providerForIdentity(ctx, identity)
	if err != nil {
		return r.failProgrammedForProviderError(ctx, zone, err)
	}
	statusData, err := cloudflareZoneStatusData(zone)
	if err != nil {
		return ctrl.Result{}, r.setProgrammed(ctx, zone, metav1.ConditionFalse, "ReconcileError", err.Error())
	}
	zoneID, err := cloudflareZoneIDForDelete(zone, statusData)
	if err != nil {
		return ctrl.Result{}, r.setAccepted(ctx, zone, metav1.ConditionFalse, "InvalidAdoption", err.Error())
	}
	if zoneID != "" {
		if err := provider.DeleteZone(ctx, zoneID); err != nil && !isCloudflareNotFound(err) {
			return r.failProgrammedForProviderError(ctx, zone, err)
		}
		r.recordEvent(zone, corev1.EventTypeNormal, "CloudflareZoneDeleted", fmt.Sprintf("Cloudflare zone %s was deleted", zoneID))
	}
	return ctrl.Result{}, r.removeFinalizer(ctx, unit)
}

func (r *ZoneReconciler) deleteContextForZone(ctx context.Context, zone *dnsv1alpha1.Zone) (*cloudflarev1alpha1.CloudflareZoneClassParameters, *cloudflarev1alpha1.CloudflareIdentity, bool, error) {
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
		return nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "InvalidProvider", "Zone provider is not handled by Cloudflare controller")
	}
	parameters, payloadErr := providercontract.ConvertZoneClassParametersToStorage(provider, version, &zoneClass)
	if payloadErr != nil {
		acceptance := providercontract.PayloadErrorToAcceptance(payloadErr)
		return nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionStatus(acceptance.Status), acceptance.Reason, acceptance.Message)
	}
	converted := zoneClass.DeepCopy()
	converted.Spec.Parameters = parameters
	params, err := cloudflareZoneClassParameters(converted)
	if err != nil {
		return nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "InvalidProvider", err.Error())
	}
	identity, accepted, err := r.acceptIdentityRef(ctx, zone, &zoneClass)
	return params, identity, accepted, err
}

func (r *ZoneReconciler) reconcileFinalizer(ctx context.Context, unit *dnsv1alpha1.ZoneUnit, params *cloudflarev1alpha1.CloudflareZoneClassParameters) (bool, error) {
	hasFinalizer := slices.Contains(unit.Finalizers, ZoneFinalizer)
	shouldHaveFinalizer := zoneDeletionPolicy(params) == cloudflarev1alpha1.ZoneDeletionPolicyDelete
	switch {
	case shouldHaveFinalizer && !hasFinalizer:
		base := unit.DeepCopy()
		unit.Finalizers = append(unit.Finalizers, ZoneFinalizer)
		return true, r.Patch(ctx, unit, client.MergeFrom(base))
	case !shouldHaveFinalizer && hasFinalizer:
		base := unit.DeepCopy()
		unit.Finalizers = slices.DeleteFunc(unit.Finalizers, func(finalizer string) bool { return finalizer == ZoneFinalizer })
		return true, r.Patch(ctx, unit, client.MergeFrom(base))
	default:
		return false, nil
	}
}

func (r *ZoneReconciler) applyObservedZone(ctx context.Context, zone *dnsv1alpha1.Zone, identity *cloudflarev1alpha1.CloudflareIdentity, observed CloudflareZone) (bool, error) {
	if message := cloudflareZoneMismatch(zone, identity, observed); message != "" {
		reason := "ProviderConflict"
		if _, adopting, _ := cloudflareZoneAdoptionID(zone); adopting {
			reason = "ExternalResourceMismatch"
		}
		return true, r.setProgrammed(ctx, zone, metav1.ConditionFalse, reason, message)
	}
	if err := validateCloudflareID(observed.ID, "Cloudflare zone.id"); err != nil {
		return true, r.setProgrammed(ctx, zone, metav1.ConditionFalse, "ProviderInvalidRequest", err.Error())
	}
	data := cloudflarev1alpha1.CloudflareZoneStatusData{
		Zone: cloudflarev1alpha1.CloudflareZoneStatus{ID: observed.ID, Status: observed.Status, Type: observed.Type},
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return false, err
	}
	zone.Status.NameServers = slices.Clone(observed.NameServers)
	zone.Status.Provider = &dnsv1alpha1.ProviderStatus{Data: runtime.RawExtension{Raw: raw}}
	return false, r.patchZoneUnitZoneStatus(ctx, zone)
}

func (r *ZoneReconciler) markCloudflareZoneCreationIntent(ctx context.Context, zone *dnsv1alpha1.Zone, identity *cloudflarev1alpha1.CloudflareIdentity) error {
	state := cloudflarev1alpha1.CloudflareZoneState{
		CreationIntent: &cloudflarev1alpha1.CloudflareZoneCreationIntent{
			AccountID: identity.Status.Account.ID,
			Name:      zone.Spec.DomainName,
		},
	}
	rawState, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if zone.Status.Provider == nil {
		zone.Status.Provider = &dnsv1alpha1.ProviderStatus{}
	}
	zone.Status.Provider.State = runtime.RawExtension{Raw: rawState}
	return r.patchZoneUnitZoneStatus(ctx, zone)
}

func (r *ZoneReconciler) recoverCreatedCloudflareZone(ctx context.Context, provider ZoneProvider, zone *dnsv1alpha1.Zone, statusState cloudflarev1alpha1.CloudflareZoneState, identity *cloudflarev1alpha1.CloudflareIdentity, sameNameZones []CloudflareZone) (CloudflareZone, bool, error) {
	if len(sameNameZones) != 1 || statusState.CreationIntent == nil {
		return CloudflareZone{}, false, nil
	}
	if statusState.CreationIntent.AccountID != identity.Status.Account.ID || statusState.CreationIntent.Name != zone.Spec.DomainName {
		return CloudflareZone{}, false, nil
	}
	observed := sameNameZones[0]
	if message := cloudflareZoneMismatch(zone, identity, observed); message != "" {
		return CloudflareZone{}, false, nil
	}
	lister, ok := provider.(cloudflareZoneDNSRecordLister)
	if !ok {
		return CloudflareZone{}, false, nil
	}
	records, err := lister.ListDNSRecords(ctx, observed.ID, "")
	if err != nil {
		return CloudflareZone{}, false, err
	}
	if cloudflareZoneHasUserDNSRecords(zone, observed, records) {
		return CloudflareZone{}, false, nil
	}
	return observed, true, nil
}

func cloudflareZoneHasUserDNSRecords(zone *dnsv1alpha1.Zone, observed CloudflareZone, records []CloudflareDNSRecord) bool {
	for _, record := range records {
		if !isDefaultCloudflareZoneRecord(zone, observed, record) {
			return true
		}
	}
	return false
}

func isDefaultCloudflareZoneRecord(zone *dnsv1alpha1.Zone, observed CloudflareZone, record CloudflareDNSRecord) bool {
	if record.Name != zone.Spec.DomainName {
		return false
	}
	switch record.Type {
	case "NS":
		return slices.Contains(observed.NameServers, record.Content)
	case "SOA":
		return true
	default:
		return false
	}
}

func cloudflareZoneMismatch(zone *dnsv1alpha1.Zone, identity *cloudflarev1alpha1.CloudflareIdentity, observed CloudflareZone) string {
	if identity.Status.Account == nil || identity.Status.Account.ID == "" {
		return "Cloudflare account is not observed on identity"
	}
	if observed.AccountID != "" && observed.AccountID != identity.Status.Account.ID {
		return "Cloudflare zone account does not match identity account"
	}
	if observed.Name != zone.Spec.DomainName {
		return "Cloudflare zone name does not match Zone spec.domainName"
	}
	if observed.Type != "full" {
		return "Cloudflare zone type is not full"
	}
	return ""
}

func cloudflareZoneStatusData(zone *dnsv1alpha1.Zone) (cloudflarev1alpha1.CloudflareZoneStatusData, error) {
	if zone.Status.Provider == nil || len(zone.Status.Provider.Data.Raw) == 0 {
		return cloudflarev1alpha1.CloudflareZoneStatusData{}, nil
	}
	var data cloudflarev1alpha1.CloudflareZoneStatusData
	if err := json.Unmarshal(zone.Status.Provider.Data.Raw, &data); err != nil {
		return cloudflarev1alpha1.CloudflareZoneStatusData{}, fmt.Errorf("Zone status.provider.data must match Cloudflare schema: %w", err)
	}
	if data.Zone.ID != "" {
		if err := validateCloudflareID(data.Zone.ID, "Zone status.provider.data.zone.id"); err != nil {
			return cloudflarev1alpha1.CloudflareZoneStatusData{}, err
		}
	}
	return data, nil
}

func cloudflareZoneState(zone *dnsv1alpha1.Zone) (cloudflarev1alpha1.CloudflareZoneState, error) {
	if zone.Status.Provider == nil || len(zone.Status.Provider.State.Raw) == 0 {
		return cloudflarev1alpha1.CloudflareZoneState{}, nil
	}
	var state cloudflarev1alpha1.CloudflareZoneState
	if err := json.Unmarshal(zone.Status.Provider.State.Raw, &state); err != nil {
		return cloudflarev1alpha1.CloudflareZoneState{}, fmt.Errorf("Zone status.provider.state must match Cloudflare schema: %w", err)
	}
	return state, nil
}

func cloudflareZoneAdoptionID(zone *dnsv1alpha1.Zone) (string, bool, error) {
	if len(zone.Spec.Adoption.Raw) == 0 {
		return "", false, nil
	}
	var ref cloudflarev1alpha1.CloudflareZoneAdoption
	if err := json.Unmarshal(zone.Spec.Adoption.Raw, &ref); err != nil {
		return "", true, err
	}
	if ref.ZoneID == "" {
		return "", true, errors.New("spec.adoption.zoneID is required")
	}
	if err := validateCloudflareID(ref.ZoneID, "spec.adoption.zoneID"); err != nil {
		return "", true, err
	}
	return ref.ZoneID, true, nil
}

func cloudflareZoneManagedResourceMismatch(zone *dnsv1alpha1.Zone, statusData cloudflarev1alpha1.CloudflareZoneStatusData) (string, bool, error) {
	adoptionID, adopting, err := cloudflareZoneAdoptionID(zone)
	if err != nil || !adopting || statusData.Zone.ID == "" {
		return "", false, err
	}
	if adoptionID != statusData.Zone.ID {
		return "spec.adoption.zoneID differs from the managed Cloudflare zone ID in status", true, nil
	}
	return "", false, nil
}

func cloudflareZoneIDForDelete(zone *dnsv1alpha1.Zone, statusData cloudflarev1alpha1.CloudflareZoneStatusData) (string, error) {
	if adoptionID, adopting, err := cloudflareZoneAdoptionID(zone); err != nil {
		return "", err
	} else if adopting {
		return adoptionID, nil
	}
	return statusData.Zone.ID, nil
}

func (r *ZoneReconciler) setAccepted(ctx context.Context, zone *dnsv1alpha1.Zone, status metav1.ConditionStatus, reason, message string) error {
	return r.setZoneCondition(ctx, zone, string(dnsv1alpha1.ConditionAccepted), status, reason, message)
}

func (r *ZoneReconciler) setProgrammed(ctx context.Context, zone *dnsv1alpha1.Zone, status metav1.ConditionStatus, reason, message string) error {
	return r.setZoneCondition(ctx, zone, string(dnsv1alpha1.ConditionProgrammed), status, reason, message)
}

func (r *ZoneReconciler) setZoneCondition(ctx context.Context, zone *dnsv1alpha1.Zone, conditionType string, status metav1.ConditionStatus, reason, message string) error {
	base := zone.DeepCopy()
	setCondition(&zone.Status.Conditions, conditionType, status, reason, message, zone.Generation)
	if equality.Semantic.DeepEqual(base.Status, zone.Status) {
		return nil
	}
	return r.patchZoneUnitZoneStatus(ctx, zone)
}

func (r *ZoneReconciler) patchZoneUnitZoneStatus(ctx context.Context, zone *dnsv1alpha1.Zone) error {
	var unit dnsv1alpha1.ZoneUnit
	if err := r.Get(ctx, client.ObjectKey{Namespace: zone.Namespace, Name: zone.Name}, &unit); err != nil {
		return client.IgnoreNotFound(err)
	}
	base := unit.DeepCopy()
	if unit.Status.Zone == nil {
		unit.Status.Zone = &dnsv1alpha1.ZoneUnitZoneStatus{}
	}
	unit.Status.Zone.NameServers = slices.Clone(zone.Status.NameServers)
	unit.Status.Zone.Provider = nil
	if zone.Status.Provider != nil && (len(zone.Status.Provider.Data.Raw) > 0 || len(zone.Status.Provider.State.Raw) > 0) {
		unit.Status.Zone.Provider = &dnsv1alpha1.ProviderStatus{Data: zone.Status.Provider.Data, State: zone.Status.Provider.State}
	}
	unit.Status.Zone.Conditions = slices.Clone(zone.Status.Conditions)
	unit.Status.ObservedGeneration = unit.Generation
	setZoneUnitProgrammedCondition(&unit)
	if equality.Semantic.DeepEqual(base.Status, unit.Status) {
		return nil
	}
	return r.Status().Patch(ctx, &unit, client.MergeFrom(base))
}

func (r *ZoneReconciler) hydrateCloudflareZoneStatusFromZoneUnit(ctx context.Context, zone *dnsv1alpha1.Zone) error {
	var unit dnsv1alpha1.ZoneUnit
	if err := r.Get(ctx, client.ObjectKey{Namespace: zone.Namespace, Name: zone.Name}, &unit); err != nil {
		return client.IgnoreNotFound(err)
	}
	applyCloudflareZoneStatusFromZoneUnit(zone, &unit)
	return nil
}

func cloudflareZoneFromZoneUnit(unit *dnsv1alpha1.ZoneUnit) dnsv1alpha1.Zone {
	zoneClassNamespace := unit.Spec.Zone.ZoneClassRef.Namespace
	return dnsv1alpha1.Zone{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         unit.Spec.Zone.Ref.Namespace,
			Name:              unit.Spec.Zone.Ref.Name,
			Generation:        unit.Spec.Zone.ObservedGeneration,
			DeletionTimestamp: unit.DeletionTimestamp,
		},
		Spec: dnsv1alpha1.ZoneSpec{
			DomainName: unit.Spec.Zone.DomainName,
			Provider:   unit.Spec.Provider,
			ZoneClassRef: dnsv1alpha1.ZoneClassReference{
				Namespace: &zoneClassNamespace,
				Name:      unit.Spec.Zone.ZoneClassRef.Name,
			},
			Adoption: unit.Spec.Zone.Adoption,
		},
	}
}

func applyCloudflareZoneStatusFromZoneUnit(zone *dnsv1alpha1.Zone, unit *dnsv1alpha1.ZoneUnit) {
	if unit.Status.Zone == nil {
		return
	}
	zone.Status.NameServers = slices.Clone(unit.Status.Zone.NameServers)
	zone.Status.Provider = unit.Status.Zone.Provider
	zone.Status.Conditions = slices.Clone(unit.Status.Zone.Conditions)
}

func (r *ZoneReconciler) providerForIdentity(ctx context.Context, identity *cloudflarev1alpha1.CloudflareIdentity) (ZoneProvider, error) {
	if r.ProviderFactory != nil {
		return r.ProviderFactory.ProviderForIdentity(ctx, r.Client, identity)
	}
	if r.Provider != nil {
		return r.Provider, nil
	}
	return nil, errors.New("cloudflare provider is required")
}

func (r *ZoneReconciler) failProgrammedForProviderError(ctx context.Context, zone *dnsv1alpha1.Zone, err error) (ctrl.Result, error) {
	reason := "ReconcileError"
	message := err.Error()
	var reasonErr *identityReasonError
	if errors.As(err, &reasonErr) {
		reason = reasonErr.reason
		message = reasonErr.message
	}
	if reason == "AccessTokenInvalid" || reason == "AccessTokenInactive" {
		reason = "ProviderAccessDenied"
	}
	if err := r.setProgrammed(ctx, zone, metav1.ConditionFalse, reason, message); err != nil {
		return ctrl.Result{}, err
	}
	if reason == "ProviderUnavailable" {
		return ctrl.Result{RequeueAfter: r.temporaryRetryAfter()}, nil
	}
	return ctrl.Result{}, nil
}

func isCloudflareNotFound(err error) bool {
	var reasonErr *identityReasonError
	return errors.As(err, &reasonErr) && reasonErr.reason == "ExternalResourceNotFound"
}

func (r *ZoneReconciler) removeFinalizer(ctx context.Context, unit *dnsv1alpha1.ZoneUnit) error {
	base := unit.DeepCopy()
	unit.Finalizers = slices.DeleteFunc(unit.Finalizers, func(finalizer string) bool { return finalizer == ZoneFinalizer })
	return r.Patch(ctx, unit, client.MergeFrom(base))
}

func (r *ZoneReconciler) reconcileRecordSets(ctx context.Context, unit *dnsv1alpha1.ZoneUnit) (ctrl.Result, error) {
	var provider RecordSetProvider
	if r.Provider != nil {
		if recordSetProvider, ok := r.Provider.(RecordSetProvider); ok {
			provider = recordSetProvider
		}
	}
	reconciler := &recordSetReconciler{
		Client:              r.Client,
		Scheme:              r.Scheme,
		Provider:            provider,
		ProviderFactory:     r.ProviderFactory,
		ControllerName:      r.ControllerName,
		ProviderName:        r.ProviderName,
		ProviderVersion:     r.ProviderVersion,
		RequeueAfter:        r.RequeueAfter,
		TemporaryRetryAfter: r.TemporaryRetryAfter,
		Recorder:            r.Recorder,
	}
	return reconciler.reconcileZoneUnitRecordSets(ctx, unit)
}

func (r *ZoneReconciler) recordEvent(zone *dnsv1alpha1.Zone, eventType, reason, message string) {
	if r.Recorder != nil {
		r.Recorder.Event(zone, eventType, reason, message)
	}
}

func (r *ZoneReconciler) requeueAfter() time.Duration {
	if r.RequeueAfter > 0 {
		return r.RequeueAfter
	}
	return defaultZoneRequeueAfter
}

func (r *ZoneReconciler) pendingPollAfter() time.Duration {
	if r.PendingPollAfter > 0 {
		return r.PendingPollAfter
	}
	return defaultCloudflarePendingPoll
}

func (r *ZoneReconciler) temporaryRetryAfter() time.Duration {
	if r.TemporaryRetryAfter > 0 {
		return r.TemporaryRetryAfter
	}
	return defaultCloudflareTemporaryRetry
}

func (r *ZoneReconciler) controllerName() string {
	if r.ControllerName != "" {
		return r.ControllerName
	}
	return DefaultControllerName
}

func (r *ZoneReconciler) providerReference() dnsv1alpha1.ProviderReference {
	return cloudflareProviderReference(r.ProviderName, r.ProviderVersion)
}

func (r *ZoneReconciler) zoneAllowedByClass(ctx context.Context, zoneClass *dnsv1alpha1.ZoneClass, zoneNamespace string) (bool, error) {
	policy := zoneClass.Spec.AllowedZones.Namespaces
	switch namespacePolicyFrom(policy) {
	case dnsv1alpha1.NamespacesFromSame:
		return zoneNamespace == zoneClass.Namespace, nil
	case dnsv1alpha1.NamespacesFromAll:
		return true, nil
	case dnsv1alpha1.NamespacesFromSelector:
		if policy.Selector == nil {
			return false, nil
		}
		selector, err := metav1.LabelSelectorAsSelector(policy.Selector)
		if err != nil {
			return false, err
		}
		var namespace corev1.Namespace
		if err := r.Get(ctx, client.ObjectKey{Name: zoneNamespace}, &namespace); err != nil {
			return false, err
		}
		return selector.Matches(labels.Set(namespace.Labels)), nil
	default:
		return false, nil
	}
}

func namespacePolicyFrom(policy dnsv1alpha1.NamespacePolicy) dnsv1alpha1.NamespacesFrom {
	if policy.From == "" {
		return dnsv1alpha1.NamespacesFromSame
	}
	return policy.From
}

func (r *ZoneReconciler) referencingRecordSetCount(ctx context.Context, zone *dnsv1alpha1.Zone) (int, error) {
	var recordSets dnsv1alpha1.RecordSetList
	if err := r.List(ctx, &recordSets); err != nil {
		return 0, err
	}
	count := 0
	for _, recordSet := range recordSets.Items {
		namespace := recordSet.Namespace
		if recordSet.Spec.ZoneRef.Namespace != nil && *recordSet.Spec.ZoneRef.Namespace != "" {
			namespace = *recordSet.Spec.ZoneRef.Namespace
		}
		if namespace == zone.Namespace && recordSet.Spec.ZoneRef.Name == zone.Name {
			count++
		}
	}
	return count, nil
}

func (r *ZoneReconciler) mapZoneClassToZoneUnits(ctx context.Context, obj client.Object) []reconcile.Request {
	zoneClass, ok := obj.(*dnsv1alpha1.ZoneClass)
	if !ok {
		return nil
	}
	return r.zoneUnitRequestsForZoneClass(ctx, zoneClass.Namespace, zoneClass.Name)
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
	identity, ok := obj.(*cloudflarev1alpha1.CloudflareIdentity)
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
		if err != nil || zoneClass.Spec.ControllerName != r.controllerName() || !cloudflareProviderVersionMatches(provider, version, r.providerReference()) {
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
	var requests []reconcile.Request
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
