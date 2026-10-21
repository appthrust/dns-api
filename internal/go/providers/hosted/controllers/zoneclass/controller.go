package zoneclass

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/appthrust/dns-api/internal/go/core/providercontract"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	hostedv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/hosted/v1alpha1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type ZoneClassReconciler struct {
	client.Client
	// Reader provides real pagination for Provider dependency fan-out.
	Reader          client.Reader
	ControllerName  string
	ProviderName    string
	ProviderVersion string
}

func (r *ZoneClassReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var class dnsv1alpha1.ZoneClass
	if err := r.Get(ctx, req.NamespacedName, &class); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if class.Spec.ControllerName != controllerName(r.ControllerName) || !class.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	var provider dnsv1alpha1.Provider
	if err := r.Get(ctx, client.ObjectKey{Name: class.Spec.Provider.Name}, &provider); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	status, reason, message := r.accept(&class, &provider)
	base := class.DeepCopy()
	meta.SetStatusCondition(&class.Status.Conditions, metav1.Condition{Type: "Accepted", Status: status, Reason: reason, Message: message, ObservedGeneration: class.Generation})
	if equality.Semantic.DeepEqual(base.Status, class.Status) {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, r.Status().Patch(ctx, &class, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
}

func (r *ZoneClassReconciler) accept(class *dnsv1alpha1.ZoneClass, provider *dnsv1alpha1.Provider) (metav1.ConditionStatus, string, string) {
	binding := binding(r.ProviderName, r.ProviderVersion)
	version, served := providercontract.ServedVersion(provider, class.Spec.Provider.Version)
	if provider.Name != binding.Name || !served {
		return metav1.ConditionFalse, "InvalidProvider", "ZoneClass provider is not handled by hosted controller"
	}
	storage, resolved := providercontract.StorageVersion(provider)
	if !resolved {
		return metav1.ConditionUnknown, "ProviderStorageVersionNotResolved", "provider storage version is not resolved"
	}
	if storage.Name != binding.Version || version.Identity != nil || storage.Identity != nil {
		return metav1.ConditionFalse, "InvalidProvider", "hosted provider requires its configured credentialless storage version"
	}
	if class.Spec.IdentityRef != nil {
		return metav1.ConditionFalse, "InvalidIdentityRef", "spec.identityRef must be omitted for hosted DNS"
	}
	if _, err := Parameters(provider, version, class); err != nil {
		var payloadErr *providercontract.PayloadError
		if errors.As(err, &payloadErr) && payloadErr.Kind == providercontract.ProviderContractNotReady {
			return metav1.ConditionUnknown, "ProviderStorageVersionNotResolved", "provider storage version is not resolved"
		}
		return metav1.ConditionFalse, "InvalidParameters", err.Error()
	}
	return metav1.ConditionTrue, "Accepted", "ZoneClass is accepted by hosted controller"
}

func (r *ZoneClassReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Client == nil {
		r.Client = mgr.GetClient()
	}
	r.Reader = mgr.GetAPIReader()
	return ctrl.NewControllerManagedBy(mgr).Named("hosted-zoneclass").
		For(&dnsv1alpha1.ZoneClass{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&dnsv1alpha1.Provider{}, handler.EnqueueRequestsFromMapFunc(r.mapProvider), builder.WithPredicates(predicate.GenerationChangedPredicate{})).Complete(r)
}

func (r *ZoneClassReconciler) mapProvider(ctx context.Context, obj client.Object) []reconcile.Request {
	reader := r.Reader
	if reader == nil {
		reader = r.Client
	}
	var requests []reconcile.Request
	continuation := ""
	for {
		var classes dnsv1alpha1.ZoneClassList
		if err := reader.List(ctx, &classes, client.Limit(256), client.Continue(continuation)); err != nil {
			return nil
		}
		for i := range classes.Items {
			class := &classes.Items[i]
			if class.Spec.Provider.Name == obj.GetName() && class.Spec.ControllerName == controllerName(r.ControllerName) {
				requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(class)})
			}
		}
		continuation = classes.Continue
		if continuation == "" {
			return requests
		}
	}
}

// Parameters converts and revalidates the payload against the Provider storage schema.
func Parameters(provider *dnsv1alpha1.Provider, version *dnsv1alpha1.ProviderVersion, class *dnsv1alpha1.ZoneClass) (hostedv1alpha1.HostedZoneClassParameters, error) {
	var params hostedv1alpha1.HostedZoneClassParameters
	raw, err := providercontract.ConvertZoneClassParametersToStorage(provider, version, class)
	if err != nil {
		return params, err
	}
	if err := json.Unmarshal(raw.Raw, &params); err != nil {
		return params, fmt.Errorf("invalid hosted parameters: %w", err)
	}
	return params, nil
}
func controllerName(name string) string {
	if name == "" {
		return hostedv1alpha1.ControllerName
	}
	return name
}
func binding(name, version string) dnsv1alpha1.ProviderReference {
	if name == "" {
		name = hostedv1alpha1.ProviderName
	}
	if version == "" {
		version = hostedv1alpha1.ProviderVersion
	}
	return dnsv1alpha1.ProviderReference{Name: name, Version: version}
}
