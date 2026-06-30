package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
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
	return ctrl.NewControllerManagedBy(mgr).
		Named("gateway-endpoint").
		For(&gatewayv1.HTTPRoute{}).
		Watches(&gatewayv1.Gateway{}, handler.EnqueueRequestsFromMapFunc(r.mapGatewayToHTTPRoutes)).
		Complete(r)
}

func (r *Reconciler) mapGatewayToHTTPRoutes(ctx context.Context, obj client.Object) []reconcile.Request {
	gateway, ok := obj.(*gatewayv1.Gateway)
	if !ok {
		return nil
	}
	var routes gatewayv1.HTTPRouteList
	if err := r.List(ctx, &routes); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0)
	for _, route := range routes.Items {
		for _, parentRef := range route.Spec.ParentRefs {
			namespace := route.Namespace
			if parentRef.Namespace != nil {
				namespace = string(*parentRef.Namespace)
			}
			if namespace == gateway.Namespace && string(parentRef.Name) == gateway.Name {
				requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&route)})
				break
			}
		}
	}
	return requests
}

func (r *Reconciler) syncGatewaysForRoute(ctx context.Context, endpointRecordSetNamespace string, route *gatewayv1.HTTPRoute) error {
	keys := gatewayKeysForRoute(route)
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
	desired, err := r.endpointRecordSetsForGateway(ctx, &gateway, endpointRecordSetNamespace)
	if err != nil {
		return err
	}
	return r.applyGatewayEndpointRecordSets(ctx, endpointRecordSetNamespace, gatewayKey, desired)
}

func (r *Reconciler) endpointRecordSetsForGateway(ctx context.Context, gateway *gatewayv1.Gateway, namespace string) ([]endpointv1alpha1.EndpointRecordSet, error) {
	targets := endpointTargets(gateway.Status.Addresses)
	if len(targets) == 0 {
		return nil, nil
	}
	var routes gatewayv1.HTTPRouteList
	if err := r.List(ctx, &routes); err != nil {
		return nil, err
	}
	hostnames := map[string]struct{}{}
	for _, route := range routes.Items {
		if !route.DeletionTimestamp.IsZero() || !allRouteParentRefsAccepted(&route) {
			continue
		}
		parents, err := r.acceptedParents(ctx, &route)
		if err != nil {
			return nil, err
		}
		for _, parent := range parents {
			if parent.gateway.Namespace != gateway.Namespace || parent.gateway.Name != gateway.Name {
				continue
			}
			for _, hostname := range effectiveHostnamesForParent(&route, parent) {
				hostnames[hostname] = struct{}{}
			}
		}
	}
	if len(hostnames) == 0 {
		return nil, nil
	}
	hostnameList := make([]string, 0, len(hostnames))
	for hostname := range hostnames {
		hostnameList = append(hostnameList, hostname)
	}
	sort.Strings(hostnameList)
	return []endpointv1alpha1.EndpointRecordSet{{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      generatedGatewayEndpointRecordSetName(gateway.Namespace, gateway.Name),
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

func (r *Reconciler) endpointRecordSetsForRoute(ctx context.Context, route *gatewayv1.HTTPRoute, namespace string) ([]endpointv1alpha1.EndpointRecordSet, error) {
	parents, err := r.acceptedParents(ctx, route)
	if err != nil {
		return nil, err
	}
	if len(parents) == 0 || !allRouteParentRefsAccepted(route) {
		return nil, nil
	}
	desired := make([]endpointv1alpha1.EndpointRecordSet, 0, len(parents))
	for _, parent := range parents {
		hostnames := effectiveHostnamesForParent(route, parent)
		targets := endpointTargets(parent.addresses)
		if len(hostnames) == 0 || len(targets) == 0 {
			continue
		}
		listenerName := string(parent.listener.Name)
		gatewayNamespace := parent.gateway.Namespace
		gatewayName := parent.gateway.Name
		item := endpointv1alpha1.EndpointRecordSet{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      generatedEndpointRecordSetName(route.Namespace, route.Name, gatewayNamespace, gatewayName, listenerName),
				Labels: map[string]string{
					managedByLabel:                 managedByValue,
					generatedLabelRouteNamespace:   route.Namespace,
					generatedLabelRouteName:        route.Name,
					generatedLabelGatewayNamespace: gatewayNamespace,
					generatedLabelGatewayName:      gatewayName,
					generatedLabelListenerName:     listenerName,
				},
			},
			Spec: endpointv1alpha1.EndpointRecordSetSpec{
				Hostnames: hostnames,
				Targets:   targets,
			},
		}
		desired = append(desired, item)
	}
	sort.Slice(desired, func(i, j int) bool {
		return desired[i].Name < desired[j].Name
	})
	return desired, nil
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
		existing.Labels = desired.Labels
		existing.Spec = desired.Spec
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

func (r *Reconciler) applyEndpointRecordSets(ctx context.Context, route *gatewayv1.HTTPRoute, namespace string, desired []endpointv1alpha1.EndpointRecordSet) error {
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
		existing.Labels = desired.Labels
		existing.Spec = desired.Spec
		if err := r.Update(ctx, &existing, client.FieldOwner(fieldOwner)); err != nil {
			return err
		}
	}
	var existing endpointv1alpha1.EndpointRecordSetList
	if err := r.List(ctx, &existing, client.InNamespace(namespace), client.MatchingLabels{
		managedByLabel:               managedByValue,
		generatedLabelRouteNamespace: route.Namespace,
		generatedLabelRouteName:      route.Name,
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

func generatedEndpointRecordSetName(routeNamespace, routeName, gatewayNamespace, gatewayName, listenerName string) string {
	hashInput := routeNamespace + "/" + routeName + "/" + gatewayNamespace + "/" + gatewayName + "/" + listenerName
	sum := sha256.Sum256([]byte(hashInput))
	hash := hex.EncodeToString(sum[:])[:10]
	candidate := fmt.Sprintf("%s-%s-%s", routeName, gatewayName, listenerName)
	candidate = strings.NewReplacer(".", "-", "_", "-", "*", "wildcard").Replace(candidate)
	candidate = strings.ToLower(candidate)
	if len(candidate) <= 52 {
		return candidate + "-" + hash
	}
	return strings.Trim(candidate[:52], "-") + "-" + hash
}
