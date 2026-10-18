package zoneclass

import (
	"context"
	"errors"

	"github.com/appthrust/dns-api/internal/go/core/providercontract"
	"github.com/appthrust/dns-api/internal/go/providers/external/config"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
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
	if class.Spec.ControllerName != config.ControllerName(r.ControllerName) || !class.DeletionTimestamp.IsZero() {
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
	binding := config.Binding(r.ProviderName, r.ProviderVersion)
	version, served := providercontract.ServedVersion(provider, class.Spec.Provider.Version)
	if provider.Name != binding.Name || !served {
		return metav1.ConditionFalse, "InvalidProvider", "ZoneClass provider is not handled by external controller"
	}
	storage, resolved := providercontract.StorageVersion(provider)
	if !resolved {
		return metav1.ConditionUnknown, "ProviderStorageVersionNotResolved", "provider storage version is not resolved"
	}
	if storage.Name != binding.Version || version.Identity != nil || storage.Identity != nil {
		return metav1.ConditionFalse, "InvalidProvider", "external provider requires its configured credentialless storage version"
	}
	if config.HasIdentity(class) {
		return metav1.ConditionFalse, "InvalidIdentityRef", "spec.identityRef must be omitted for external DNS"
	}
	if _, err := config.Parameters(provider, version, class); err != nil {
		var payloadErr *providercontract.PayloadError
		if errors.As(err, &payloadErr) && payloadErr.Kind == providercontract.ProviderContractNotReady {
			return metav1.ConditionUnknown, "ProviderStorageVersionNotResolved", "provider storage version is not resolved"
		}
		return metav1.ConditionFalse, "InvalidParameters", err.Error()
	}
	return metav1.ConditionTrue, "Accepted", "ZoneClass is accepted by external controller"
}

func (r *ZoneClassReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Client == nil {
		r.Client = mgr.GetClient()
	}
	r.Reader = mgr.GetAPIReader()
	return ctrl.NewControllerManagedBy(mgr).Named("external-zoneclass").
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
			if class.Spec.Provider.Name == obj.GetName() && class.Spec.ControllerName == config.ControllerName(r.ControllerName) {
				requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(class)})
			}
		}
		continuation = classes.Continue
		if continuation == "" {
			return requests
		}
	}
}
