package zoneunit

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"time"

	"github.com/appthrust/dns-api/internal/go/core/declaration"
	"github.com/appthrust/dns-api/internal/go/core/dnsprobe"
	"github.com/appthrust/dns-api/internal/go/core/providercontract"
	"github.com/appthrust/dns-api/internal/go/providers/hosted/controllers/zoneclass"
	"github.com/appthrust/dns-api/internal/go/providers/hosted/render"
	"github.com/appthrust/dns-api/internal/go/providers/hosted/withdrawal"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	hostedv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/hosted/v1alpha1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Options struct {
	ControllerName  string
	ProviderName    string
	ProviderVersion string
	QPS             float64
	Burst           int
	Resolver        dnsprobe.Resolver
	Evaluator       withdrawal.Evaluator
	// DefaultViews selects the hosted Service when class verification.views is empty.
	// Unlike class views, explicit service hostnames are resolved before probing.
	DefaultViews []string
	Clock        func() time.Time
	// Jitter returns a value in [-1,1], scaled to +/-10 percent.
	Jitter func() float64
}

type ZoneReconciler struct {
	client.Client
	Options Options
	// Reader provides live policy fencing after DNS I/O and paginated watch fan-out.
	Reader          client.Reader
	effectivePolicy func(dnsprobe.Policy) (dnsprobe.Policy, error)
}

func NewReconciler(k8sClient client.Client, options Options) (*ZoneReconciler, error) {
	if options.ControllerName == "" {
		options.ControllerName = hostedv1alpha1.ControllerName
	}
	if options.ProviderName == "" {
		options.ProviderName = hostedv1alpha1.ProviderName
	}
	if options.ProviderVersion == "" {
		options.ProviderVersion = hostedv1alpha1.ProviderVersion
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	if options.Jitter == nil {
		options.Jitter = func() float64 { return rand.Float64()*2 - 1 }
	}
	if options.Resolver == nil {
		resolver, err := dnsprobe.NewWireResolver(dnsprobe.WireOptions{QPS: options.QPS, Burst: options.Burst, Provider: "hosted"})
		if err != nil {
			return nil, err
		}
		options.Resolver = resolver
	}
	if options.Evaluator == nil {
		options.Evaluator = withdrawal.New(options.Resolver, options.Clock)
	}
	if len(options.DefaultViews) == 0 {
		namespace := os.Getenv("POD_NAMESPACE")
		if namespace == "" {
			namespace = "dns-api-system"
		}
		options.DefaultViews = []string{"dns-api-hosted-server." + namespace + ".svc:53"}
	}
	var effective func(dnsprobe.Policy) (dnsprobe.Policy, error)
	if resolver, ok := options.Resolver.(interface {
		EffectivePolicy(dnsprobe.Policy) (dnsprobe.Policy, error)
	}); ok {
		effective = resolver.EffectivePolicy
	} else {
		wire, err := dnsprobe.NewWireResolver(dnsprobe.WireOptions{QPS: options.QPS, Burst: options.Burst, Provider: "hosted"})
		if err != nil {
			return nil, err
		}
		effective = wire.EffectivePolicy
	}
	return &ZoneReconciler{Client: k8sClient, Reader: k8sClient, Options: options, effectivePolicy: effective}, nil
}

type environment struct {
	class            dnsv1alpha1.ZoneClass
	provider         dnsv1alpha1.Provider
	params           hostedv1alpha1.HostedZoneClassParameters
	fingerprint      string
	delegationPolicy dnsprobe.Policy
	delegationError  error
}
type acceptance struct {
	Status          metav1.ConditionStatus
	Reason, Message string
}

func (r *ZoneReconciler) observe(ctx context.Context, reader client.Reader, unit *dnsv1alpha1.ZoneUnit) (*environment, *acceptance, error) {
	env := &environment{}
	ref := unit.Spec.Zone.ZoneClassRef
	if err := reader.Get(ctx, client.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}, &env.class); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, &acceptance{metav1.ConditionFalse, "InvalidZoneClassRef", "referenced ZoneClass was not found"}, nil
		}
		return nil, nil, err
	}
	if env.class.Spec.ControllerName != r.Options.ControllerName {
		return nil, nil, nil
	}
	if unit.Spec.Provider.Version != r.Options.ProviderVersion || env.class.Spec.Provider.Name != r.Options.ProviderName {
		return nil, &acceptance{metav1.ConditionFalse, "InvalidProvider", "ZoneUnit provider is not handled by hosted controller"}, nil
	}
	if err := reader.Get(ctx, client.ObjectKey{Name: r.Options.ProviderName}, &env.provider); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, &acceptance{metav1.ConditionFalse, "InvalidProvider", "referenced Provider was not found"}, nil
		}
		return nil, nil, err
	}
	version, served := providercontract.ServedVersion(&env.provider, env.class.Spec.Provider.Version)
	storage, resolved := providercontract.StorageVersion(&env.provider)
	if !resolved {
		return nil, &acceptance{metav1.ConditionUnknown, "ProviderStorageVersionNotResolved", "provider storage version is not resolved"}, nil
	}
	if !served || storage.Name != r.Options.ProviderVersion || version.Identity != nil || storage.Identity != nil {
		return nil, &acceptance{metav1.ConditionFalse, "InvalidProvider", "hosted provider requires its configured credentialless storage version"}, nil
	}
	if env.class.Spec.IdentityRef != nil {
		return nil, &acceptance{metav1.ConditionFalse, "InvalidIdentityRef", "spec.identityRef must be omitted for hosted DNS"}, nil
	}
	params, err := zoneclass.Parameters(&env.provider, version, &env.class)
	if err != nil {
		return nil, &acceptance{metav1.ConditionFalse, "InvalidProviderPayload", boundedMessage(err.Error())}, nil
	}
	condition := meta.FindStatusCondition(env.class.Status.Conditions, "Accepted")
	if !env.class.DeletionTimestamp.IsZero() || condition == nil || condition.Status != metav1.ConditionTrue || condition.ObservedGeneration != env.class.Generation {
		return nil, &acceptance{metav1.ConditionFalse, "ZoneClassNotAccepted", "referenced ZoneClass is not accepted"}, nil
	}
	if allowed, err := declaration.ZoneAllowedByClass(ctx, readerClient{Client: r.Client, reader: reader}, &env.class, unit.Spec.Zone.Ref.Namespace); err != nil {
		return nil, nil, err
	} else if !allowed {
		return nil, &acceptance{metav1.ConditionFalse, "NotAllowedByZoneClass", "Zone namespace is not allowed by ZoneClass"}, nil
	}
	env.params = params
	env.delegationPolicy, env.delegationError = r.effectivePolicy(dnsprobe.Policy{Mode: params.Verification.Delegation.Resolver.Mode, Servers: params.Verification.Delegation.Resolver.Servers})
	env.fingerprint = dnsprobe.Fingerprint(struct {
		ClassUID         string
		Class            dnsv1alpha1.ZoneClassSpec
		ProviderUID      string
		Provider         dnsv1alpha1.ProviderSpec
		Zone             dnsv1alpha1.ZoneUnitZoneSpec
		Delegation       dnsprobe.Policy
		DelegationFailed bool
	}{string(env.class.UID), env.class.Spec, string(env.provider.UID), env.provider.Spec, unit.Spec.Zone, env.delegationPolicy, env.delegationError != nil})
	return env, nil, nil
}

func (r *ZoneReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	var unit dnsv1alpha1.ZoneUnit
	if err := r.Get(ctx, request.NamespacedName, &unit); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		withdrawal.SetPending(request.NamespacedName.String(), nil)
		return ctrl.Result{}, r.deleteSnapshot(ctx, request.NamespacedName)
	}
	if unit.Spec.Provider.Name != r.Options.ProviderName {
		return ctrl.Result{}, r.deleteSnapshot(ctx, request.NamespacedName)
	}
	var snapshot hostedv1alpha1.HostedZone
	err := r.Get(ctx, request.NamespacedName, &snapshot)
	exists := err == nil
	if err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	base := unit.DeepCopy()
	now := r.Options.Clock()
	if unit.Status.Zone == nil {
		unit.Status.Zone = &dnsv1alpha1.ZoneUnitZoneStatus{}
	}
	if exists && !ownedSnapshot(&snapshot, &unit) {
		if err := r.Delete(ctx, &snapshot, client.Preconditions{UID: &snapshot.UID, ResourceVersion: &snapshot.ResourceVersion}); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		r.snapshotRejected(&unit, "HostedZone owner reference does not match ZoneUnit; the shadowing snapshot was deleted.", now)
		return ctrl.Result{RequeueAfter: time.Second}, r.patch(ctx, base, &unit)
	}
	if !unit.DeletionTimestamp.IsZero() && exists {
		if err := r.Delete(ctx, &snapshot, client.Preconditions{UID: &snapshot.UID, ResourceVersion: &snapshot.ResourceVersion}); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		exists = false
	}
	env, rejected, err := r.observe(ctx, r.Client, &unit)
	if err != nil {
		return ctrl.Result{}, err
	}
	if rejected != nil {
		r.reject(&unit, *rejected, now)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, r.patch(ctx, base, &unit)
	}
	if env == nil {
		return ctrl.Result{}, nil
	}
	state := loadState(&unit)
	if exists {
		snapshot.Status.Servers = freshServers(snapshot.Status.Servers, now)
		retainPublication(&state, snapshot.Spec, snapshot.Status.Servers, now)
	}
	renderUnit := unit
	actualDigest := ""
	if exists {
		actualDigest = render.ContentDigest(snapshot.Spec)
		renderUnit.Status.Zone = &dnsv1alpha1.ZoneUnitZoneStatus{Provider: providerData(map[string]any{"contentDigest": actualDigest})}
	}
	previous := uint32(0)
	if exists {
		previous = snapshot.Spec.Serial
	} else if unit.Status.Zone.Provider != nil {
		var data zoneData
		if json.Unmarshal(unit.Status.Zone.Provider.Data.Raw, &data) == nil {
			previous = data.Serial
		}
	}
	desired, err := render.Render(&renderUnit, env.params, previous, now)
	if err != nil {
		r.snapshotRejected(&unit, err.Error(), now)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, r.patch(ctx, base, &unit)
	}
	if unit.DeletionTimestamp.IsZero() {
		if !exists || snapshot.Spec.ContentDigest != desired.ContentDigest || actualDigest != desired.ContentDigest {
			desiredSnapshot := &hostedv1alpha1.HostedZone{TypeMeta: metav1.TypeMeta{APIVersion: hostedv1alpha1.SchemeGroupVersion.String(), Kind: "HostedZone"}, ObjectMeta: metav1.ObjectMeta{Namespace: unit.Namespace, Name: unit.Name, OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(&unit, dnsv1alpha1.SchemeGroupVersion.WithKind("ZoneUnit"))}}, Spec: desired}
			if exists {
				desiredSnapshot.ResourceVersion = snapshot.ResourceVersion
			}
			if err := r.Patch(ctx, desiredSnapshot, client.Apply, client.FieldOwner("hosted-controller"), client.ForceOwnership); err != nil {
				r.snapshotRejected(&unit, err.Error(), now)
				if patchErr := r.patch(ctx, base, &unit); patchErr != nil {
					return ctrl.Result{}, patchErr
				}
				return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}
			// Keep observed acknowledgements, not a cache read after the successful apply.
			snapshot.Spec = desired
			snapshot.ObjectMeta = desiredSnapshot.ObjectMeta
			exists = true
		}
		snapshot.Status.Servers = freshServers(snapshot.Status.Servers, now)
	} else {
		snapshot.Spec = desired
		snapshot.Spec.Records = nil
		snapshot.Status.Servers = nil
	}
	served := unit.DeletionTimestamp.IsZero() && snapshotServed(snapshot.Spec, snapshot.Status.Servers)
	setResults(&unit, snapshot.Spec, snapshot.Status.Servers, served, now)
	next := nextServerExpiry(snapshot.Status.Servers, now)
	next = minDuration(next, r.observeDelegation(ctx, &unit, env, &state, now))
	withdrawalNext, err := r.reconcileWithdrawals(ctx, &unit, env, snapshot.Spec, snapshot.Status.Servers, &state, now)
	if err != nil {
		return ctrl.Result{}, err
	}
	next = minDuration(next, withdrawalNext)
	if state.Probed {
		fresh := &dnsv1alpha1.ZoneUnit{}
		if err := r.Reader.Get(ctx, request.NamespacedName, fresh); err != nil {
			return ctrl.Result{}, err
		}
		current, rejection, err := r.observe(ctx, r.Reader, fresh)
		if err != nil {
			return ctrl.Result{}, err
		}
		if fresh.UID != unit.UID || fresh.Generation != unit.Generation || !equality.Semantic.DeepEqual(fresh.Spec, unit.Spec) || !fresh.DeletionTimestamp.Equal(unit.DeletionTimestamp) || rejection != nil || current == nil || current.fingerprint != env.fingerprint {
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
	}
	saveState(&unit, state)
	return ctrl.Result{RequeueAfter: next}, r.patch(ctx, base, &unit)
}

func ownedSnapshot(snapshot *hostedv1alpha1.HostedZone, unit *dnsv1alpha1.ZoneUnit) bool {
	owner := metav1.GetControllerOf(snapshot)
	return owner != nil && owner.APIVersion == dnsv1alpha1.SchemeGroupVersion.String() && owner.Kind == "ZoneUnit" && owner.Name == unit.Name && owner.UID == unit.UID
}
func snapshotServed(spec hostedv1alpha1.HostedZoneSpec, servers []hostedv1alpha1.HostedZoneServerStatus) bool {
	if len(servers) == 0 {
		return false
	}
	for _, server := range servers {
		if server.Serial != spec.Serial || server.ContentDigest != spec.ContentDigest {
			return false
		}
	}
	return true
}

// ServerFreshness bounds authoritative serving acknowledgements independently
// of SOA refresh; servers renew observedAt every thirty seconds.
const ServerFreshness = 3 * time.Minute

// Stale acknowledgements are ignored, never patched: each server is the sole
// writer of its own status entry.
func freshServers(servers []hostedv1alpha1.HostedZoneServerStatus, now time.Time) []hostedv1alpha1.HostedZoneServerStatus {
	cutoff := now.Add(-ServerFreshness)
	fresh := servers[:0]
	for _, server := range servers {
		if !server.ObservedAt.Time.Before(cutoff) && !server.ObservedAt.Time.After(now) {
			fresh = append(fresh, server)
		}
	}
	return fresh
}
func (r *ZoneReconciler) patch(ctx context.Context, base, unit *dnsv1alpha1.ZoneUnit) error {
	unit.Status.ObservedGeneration = unit.Generation
	if equality.Semantic.DeepEqual(base.Status, unit.Status) {
		updatePending(unit)
		return nil
	}
	if err := r.Status().Patch(ctx, unit, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return err
	}
	updatePending(unit)
	return nil
}
func pendingMessage(serial uint32) string {
	return fmt.Sprintf("Snapshot serial %d is not yet served by every fresh hosted server.", serial)
}

func (r *ZoneReconciler) deleteSnapshot(ctx context.Context, key client.ObjectKey) error {
	var snapshot hostedv1alpha1.HostedZone
	if err := r.Get(ctx, key, &snapshot); err != nil {
		return client.IgnoreNotFound(err)
	}
	return client.IgnoreNotFound(r.Delete(ctx, &snapshot, client.Preconditions{UID: &snapshot.UID, ResourceVersion: &snapshot.ResourceVersion}))
}
