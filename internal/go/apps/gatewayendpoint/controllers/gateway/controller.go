package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	fieldOwner                     = "dns-api-gateway-endpoint-controller"
	httpRouteFinalizer             = "gateway.endpoint.dns.appthrust.io/httproute-finalizer"
	generatedLabelRouteNamespace   = "gateway.endpoint.dns.appthrust.io/route-namespace"
	generatedLabelRouteName        = "gateway.endpoint.dns.appthrust.io/route-name"
	generatedLabelGatewayNamespace = "gateway.endpoint.dns.appthrust.io/gateway-namespace"
	generatedLabelGatewayName      = "gateway.endpoint.dns.appthrust.io/gateway-name"
	generatedLabelListenerName     = "gateway.endpoint.dns.appthrust.io/listener-name"
	managedByLabel                 = "app.kubernetes.io/managed-by"
	managedByValue                 = "dns-api-gateway-endpoint"
	httpRouteGatewayIndex          = "gateway.endpoint.dns.appthrust.io/parent-gateway"
)

type Reconciler struct {
	client.Client
	Scheme                     *runtime.Scheme
	EndpointRecordSetNamespace string
}

// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch;update
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes/finalizers,verbs=update
// +kubebuilder:rbac:groups=endpoint.dns.appthrust.io,resources=endpointrecordsets,verbs=create;delete;get;list;watch;patch;update
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	namespace := r.EndpointRecordSetNamespace
	if namespace == "" {
		namespace = req.Namespace
	}

	var route gatewayv1.HTTPRoute
	if err := r.Get(ctx, req.NamespacedName, &route); err != nil {
		if apierrors.IsNotFound(err) {
			if err := r.syncGatewaysForMissingRoute(ctx, namespace, req.Namespace, req.Name); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, r.cleanupForRoute(ctx, namespace, req.Namespace, req.Name)
		}
		return ctrl.Result{}, err
	}
	if !route.DeletionTimestamp.IsZero() {
		if err := r.syncGatewaysForRoute(ctx, namespace, &route); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.cleanupForRoute(ctx, namespace, route.Namespace, route.Name); err != nil {
			return ctrl.Result{}, err
		}
		if controllerutil.ContainsFinalizer(&route, httpRouteFinalizer) {
			controllerutil.RemoveFinalizer(&route, httpRouteFinalizer)
			if err := r.Update(ctx, &route); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}
	if !controllerutil.ContainsFinalizer(&route, httpRouteFinalizer) {
		controllerutil.AddFinalizer(&route, httpRouteFinalizer)
		if err := r.Update(ctx, &route); err != nil {
			return ctrl.Result{}, err
		}
	}
	if err := r.syncGatewaysForRoute(ctx, namespace, &route); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.cleanupForRoute(ctx, namespace, route.Namespace, route.Name); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Scheme == nil {
		r.Scheme = mgr.GetScheme()
	}
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &gatewayv1.HTTPRoute{}, httpRouteGatewayIndex, routeGatewayIndexKeys); err != nil {
		return fmt.Errorf("index HTTPRoute parent Gateways: %w", err)
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("gateway-endpoint").
		For(&gatewayv1.HTTPRoute{}).
		Watches(&gatewayv1.Gateway{}, handler.EnqueueRequestsFromMapFunc(r.mapGatewayToHTTPRoutes)).
		// Cross-namespace projections cannot use owner references. The final
		// delete event must wake routes even when their own state has not changed.
		Watches(&endpointv1alpha1.EndpointRecordSet{}, handler.EnqueueRequestsFromMapFunc(r.mapEndpointRecordSetToHTTPRoutes),
			builder.WithPredicates(predicate.Or(predicate.GenerationChangedPredicate{}, predicate.LabelChangedPredicate{}))).
		Complete(r)
}

func (r *Reconciler) mapGatewayToHTTPRoutes(ctx context.Context, obj client.Object) []reconcile.Request {
	gateway, ok := obj.(*gatewayv1.Gateway)
	if !ok {
		return nil
	}
	return r.routesForGateway(ctx, client.ObjectKeyFromObject(gateway))
}

func routeGatewayIndexKeys(obj client.Object) []string {
	route := obj.(*gatewayv1.HTTPRoute)
	keys := gatewayKeysForRoute(route)
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		values = append(values, key.String())
	}
	return values
}

func (r *Reconciler) mapEndpointRecordSetToHTTPRoutes(ctx context.Context, obj client.Object) []reconcile.Request {
	labels := obj.GetLabels()
	if labels[managedByLabel] != managedByValue {
		return nil
	}
	key := client.ObjectKey{Namespace: labels[generatedLabelGatewayNamespace], Name: labels[generatedLabelGatewayName]}
	if key.Namespace == "" || key.Name == "" {
		return nil
	}
	return r.routesForGateway(ctx, key)
}

func (r *Reconciler) routesForGateway(ctx context.Context, key client.ObjectKey) []reconcile.Request {
	var routes gatewayv1.HTTPRouteList
	if err := r.List(ctx, &routes, client.MatchingFields{httpRouteGatewayIndex: key.String()}); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "list HTTPRoutes for Gateway", "gateway", key)
		return nil
	}
	requests := make([]reconcile.Request, 0, len(routes.Items))
	for _, route := range routes.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&route)})
	}
	return requests
}

func (r *Reconciler) syncGatewaysForRoute(ctx context.Context, endpointRecordSetNamespace string, route *gatewayv1.HTTPRoute) error {
	receiptKeys, err := r.gatewayKeysFromAdmissionReceipts(ctx, endpointRecordSetNamespace, route.Namespace, route.Name)
	if err != nil {
		return err
	}
	return r.syncGatewayKeys(ctx, endpointRecordSetNamespace, append(gatewayKeysForRoute(route), receiptKeys...))
}

func (r *Reconciler) syncGatewaysForMissingRoute(ctx context.Context, endpointRecordSetNamespace, routeNamespace, routeName string) error {
	keys, err := r.gatewayKeysFromAdmissionReceipts(ctx, endpointRecordSetNamespace, routeNamespace, routeName)
	if err != nil {
		return err
	}
	return r.syncGatewayKeys(ctx, endpointRecordSetNamespace, keys)
}

func (r *Reconciler) syncGatewayKeys(ctx context.Context, endpointRecordSetNamespace string, keys []client.ObjectKey) error {
	seen := map[client.ObjectKey]struct{}{}
	for _, key := range keys {
		seen[key] = struct{}{}
	}
	keys = keys[:0]
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Namespace == keys[j].Namespace {
			return keys[i].Name < keys[j].Name
		}
		return keys[i].Namespace < keys[j].Namespace
	})
	for _, key := range keys {
		if err := r.syncGatewayEndpointRecordSet(ctx, endpointRecordSetNamespace, key); err != nil {
			return err
		}
	}
	return nil
}

func gatewayKeysForRoute(route *gatewayv1.HTTPRoute) []client.ObjectKey {
	seen := map[client.ObjectKey]struct{}{}
	keys := make([]client.ObjectKey, 0, len(route.Spec.ParentRefs))
	for _, ref := range route.Spec.ParentRefs {
		namespace := route.Namespace
		if ref.Namespace != nil {
			namespace = string(*ref.Namespace)
		}
		key := client.ObjectKey{Namespace: namespace, Name: string(ref.Name)}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Namespace == keys[j].Namespace {
			return keys[i].Name < keys[j].Name
		}
		return keys[i].Namespace < keys[j].Namespace
	})
	return keys
}

func (r *Reconciler) syncGatewayEndpointRecordSet(ctx context.Context, endpointRecordSetNamespace string, gatewayKey client.ObjectKey) error {
	var gateway gatewayv1.Gateway
	if err := r.Get(ctx, gatewayKey, &gateway); err != nil {
		if apierrors.IsNotFound(err) {
			return r.applyGatewayEndpointRecordSets(ctx, endpointRecordSetNamespace, gatewayKey, nil)
		}
		return err
	}
	if !gateway.DeletionTimestamp.IsZero() {
		return r.applyGatewayEndpointRecordSets(ctx, endpointRecordSetNamespace, gatewayKey, nil)
	}
	existing, err := r.existingGatewayEndpointRecordSet(ctx, endpointRecordSetNamespace, &gateway)
	if err != nil {
		return err
	}
	desired, err := r.endpointRecordSetsForGateway(ctx, &gateway, endpointRecordSetNamespace, existing)
	if err != nil {
		return err
	}
	return r.applyGatewayEndpointRecordSets(ctx, endpointRecordSetNamespace, gatewayKey, desired)
}

func (r *Reconciler) endpointRecordSetsForGateway(ctx context.Context, gateway *gatewayv1.Gateway, namespace string, existing *endpointv1alpha1.EndpointRecordSet) ([]endpointv1alpha1.EndpointRecordSet, error) {
	if gatewayAcceptance(gateway) == parentAcceptanceRejected {
		return nil, nil
	}
	targets := endpointTargets(gateway.Status.Addresses)
	var routes gatewayv1.HTTPRouteList
	if err := r.List(ctx, &routes); err != nil {
		return nil, err
	}

	receipt := existingAdmissionReceipt(existing)
	publishedHostnames := map[string]struct{}{}
	if existing != nil {
		for _, hostname := range existing.Spec.Hostnames {
			publishedHostnames[hostname] = struct{}{}
		}
	}
	hostnames := map[string]struct{}{}
	retainedHostnames := map[string]struct{}{}
	receiptBindings := map[string]admissionReceiptBinding{}
	for i := range routes.Items {
		route := &routes.Items[i]
		if !route.DeletionTimestamp.IsZero() {
			continue
		}
		fullyAccepted := allRouteParentRefsAccepted(route)
		for _, binding := range routeGatewayBindings(route, gateway) {
			acceptance := routeParentAcceptance(route, binding.ref)
			for _, hostname := range effectiveHostnamesForListener(route, binding.listener) {
				match, hasReceipt := admissionReceiptBinding{}, false
				conflictingReceipt := false
				if _, published := publishedHostnames[hostname]; published {
					match, hasReceipt = receipt.matchingBinding(route, gateway, binding, hostname)
					conflictingReceipt = receipt.conflictsCurrentBinding(route, gateway, binding, hostname)
				}
				if fullyAccepted && acceptance == parentAcceptanceAccepted {
					if conflictingReceipt {
						continue
					}
					hostnames[hostname] = struct{}{}
					if len(targets) != 0 {
						if current, ok := newAdmissionReceiptBinding(route, gateway, binding); ok {
							addReceiptHostname(receiptBindings, current, hostname)
						}
					} else if hasReceipt {
						addReceiptHostname(receiptBindings, match, hostname)
					}
					if hasReceipt {
						retainedHostnames[hostname] = struct{}{}
					}
					continue
				}
				if acceptance == parentAcceptanceRejected ||
					(acceptance == parentAcceptanceUnknown && !listenerAllowsUnknownRetention(route, gateway, binding.listener)) ||
					!hasReceipt {
					continue
				}
				hostnames[hostname] = struct{}{}
				retainedHostnames[hostname] = struct{}{}
				addReceiptHostname(receiptBindings, match, hostname)
			}
		}
	}
	if len(targets) == 0 {
		hostnames = retainedHostnames
	}
	if len(hostnames) == 0 || len(targets) == 0 && (existing == nil || len(existing.Spec.Targets) == 0) {
		return nil, nil
	}
	if len(targets) == 0 {
		targets = existing.Spec.Targets
	}

	hostnameList := make([]string, 0, len(hostnames))
	for hostname := range hostnames {
		hostnameList = append(hostnameList, hostname)
	}
	sort.Strings(hostnameList)
	retainReceiptHostnames(receiptBindings, hostnames)
	annotations := map[string]string(nil)
	if encoded := encodeAdmissionReceipt(receiptBindings); encoded != "" {
		annotations = map[string]string{admissionReceiptAnnotation: encoded}
	}
	return []endpointv1alpha1.EndpointRecordSet{{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   namespace,
			Name:        generatedGatewayEndpointRecordSetName(gateway.Namespace, gateway.Name),
			Annotations: annotations,
			Labels: map[string]string{
				managedByLabel:                 managedByValue,
				generatedLabelGatewayNamespace: gateway.Namespace,
				generatedLabelGatewayName:      gateway.Name,
			},
		},
		Spec: endpointv1alpha1.EndpointRecordSetSpec{
			Hostnames: hostnameList,
			Targets:   targets,
		},
	}}, nil
}

func (r *Reconciler) applyGatewayEndpointRecordSets(ctx context.Context, namespace string, gatewayKey client.ObjectKey, desired []endpointv1alpha1.EndpointRecordSet) error {
	desiredNames := map[string]struct{}{}
	for _, item := range desired {
		desired := item
		desiredNames[desired.Name] = struct{}{}
		var existing endpointv1alpha1.EndpointRecordSet
		err := r.Get(ctx, client.ObjectKey{Namespace: desired.Namespace, Name: desired.Name}, &existing)
		if apierrors.IsNotFound(err) {
			if err := r.Create(ctx, &desired, client.FieldOwner(fieldOwner)); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if !existing.DeletionTimestamp.IsZero() {
			// Deletion is irreversible. Let the endpoint controller finish its
			// provider cleanup; the EndpointRecordSet delete watch retries creation.
			continue
		}
		desiredReceipt := desired.Annotations[admissionReceiptAnnotation]
		if apiequality.Semantic.DeepEqual(existing.Labels, desired.Labels) &&
			apiequality.Semantic.DeepEqual(existing.Spec, desired.Spec) &&
			existing.Annotations[admissionReceiptAnnotation] == desiredReceipt {
			continue
		}
		existing.Labels = desired.Labels
		existing.Spec = desired.Spec
		if desiredReceipt == "" {
			delete(existing.Annotations, admissionReceiptAnnotation)
		} else {
			if existing.Annotations == nil {
				existing.Annotations = map[string]string{}
			}
			existing.Annotations[admissionReceiptAnnotation] = desiredReceipt
		}
		if err := r.Update(ctx, &existing, client.FieldOwner(fieldOwner)); err != nil {
			return err
		}
	}
	var existing endpointv1alpha1.EndpointRecordSetList
	if err := r.List(ctx, &existing, client.InNamespace(namespace), client.MatchingLabels{
		managedByLabel:                 managedByValue,
		generatedLabelGatewayNamespace: gatewayKey.Namespace,
		generatedLabelGatewayName:      gatewayKey.Name,
	}); err != nil {
		return err
	}
	for _, item := range existing.Items {
		if _, ok := desiredNames[item.Name]; ok {
			continue
		}
		item := item
		if err := r.Delete(ctx, &item); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (r *Reconciler) cleanupForRoute(ctx context.Context, endpointRecordSetNamespace, routeNamespace, routeName string) error {
	var existing endpointv1alpha1.EndpointRecordSetList
	if err := r.List(ctx, &existing, client.InNamespace(endpointRecordSetNamespace), client.MatchingLabels{
		managedByLabel:               managedByValue,
		generatedLabelRouteNamespace: routeNamespace,
		generatedLabelRouteName:      routeName,
	}); err != nil {
		return err
	}
	for _, item := range existing.Items {
		item := item
		if err := r.Delete(ctx, &item); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func generatedGatewayEndpointRecordSetName(gatewayNamespace, gatewayName string) string {
	hashInput := gatewayNamespace + "/" + gatewayName
	sum := sha256.Sum256([]byte(hashInput))
	hash := hex.EncodeToString(sum[:])[:10]
	candidate := gatewayNamespace + "-" + gatewayName
	candidate = strings.NewReplacer(".", "-", "_", "-").Replace(candidate)
	candidate = strings.ToLower(candidate)
	if len(candidate) <= 52 {
		return candidate + "-" + hash
	}
	return strings.Trim(candidate[:52], "-") + "-" + hash
}
