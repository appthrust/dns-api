package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"sort"
	"strings"

	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	fieldOwner                     = "dns-api-service-endpoint-controller"
	serviceFinalizer               = "service.endpoint.dns.appthrust.io/service-finalizer"
	annotationHostnames            = "endpoint.dns.appthrust.io/hostnames"
	legacyExternalDNSHostname      = "external-dns.alpha.kubernetes.io/hostname"
	generatedLabelServiceNamespace = "service.endpoint.dns.appthrust.io/service-namespace"
	generatedLabelServiceName      = "service.endpoint.dns.appthrust.io/service-name"
	managedByLabel                 = "app.kubernetes.io/managed-by"
	managedByValue                 = "dns-api-service-endpoint"
)

type Reconciler struct {
	client.Client
	Scheme                     *runtime.Scheme
	EndpointRecordSetNamespace string
}

// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;update
// +kubebuilder:rbac:groups="",resources=services/finalizers,verbs=update
// +kubebuilder:rbac:groups=endpoint.dns.appthrust.io,resources=endpointrecordsets,verbs=create;delete;get;list;watch;patch;update
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	namespace := r.EndpointRecordSetNamespace
	if namespace == "" {
		namespace = req.Namespace
	}

	var service corev1.Service
	if err := r.Get(ctx, req.NamespacedName, &service); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.cleanupForService(ctx, namespace, req.Namespace, req.Name)
		}
		return ctrl.Result{}, err
	}

	if !service.DeletionTimestamp.IsZero() {
		if err := r.cleanupForService(ctx, namespace, service.Namespace, service.Name); err != nil {
			return ctrl.Result{}, err
		}
		if controllerutil.ContainsFinalizer(&service, serviceFinalizer) {
			controllerutil.RemoveFinalizer(&service, serviceFinalizer)
			if err := r.Update(ctx, &service); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	desired := r.endpointRecordSetForService(&service, namespace)
	if desired == nil {
		if err := r.cleanupForService(ctx, namespace, service.Namespace, service.Name); err != nil {
			return ctrl.Result{}, err
		}
		if controllerutil.ContainsFinalizer(&service, serviceFinalizer) {
			controllerutil.RemoveFinalizer(&service, serviceFinalizer)
			if err := r.Update(ctx, &service); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(&service, serviceFinalizer) {
		controllerutil.AddFinalizer(&service, serviceFinalizer)
		if err := r.Update(ctx, &service); err != nil {
			return ctrl.Result{}, err
		}
	}

	if err := r.applyEndpointRecordSet(ctx, desired); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Scheme == nil {
		r.Scheme = mgr.GetScheme()
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("service-endpoint").
		For(&corev1.Service{}).
		Complete(r)
}

func (r *Reconciler) endpointRecordSetForService(service *corev1.Service, namespace string) *endpointv1alpha1.EndpointRecordSet {
	hostnames := hostnamesForService(service)
	targets := endpointTargets(service.Status.LoadBalancer.Ingress)
	if service.Spec.Type != corev1.ServiceTypeLoadBalancer || len(hostnames) == 0 || len(targets) == 0 {
		return nil
	}
	return &endpointv1alpha1.EndpointRecordSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      generatedEndpointRecordSetName(service.Namespace, service.Name),
			Labels: map[string]string{
				managedByLabel:                 managedByValue,
				generatedLabelServiceNamespace: service.Namespace,
				generatedLabelServiceName:      service.Name,
			},
		},
		Spec: endpointv1alpha1.EndpointRecordSetSpec{
			Hostnames: hostnames,
			Targets:   targets,
		},
	}
}

func (r *Reconciler) applyEndpointRecordSet(ctx context.Context, desired *endpointv1alpha1.EndpointRecordSet) error {
	var existing endpointv1alpha1.EndpointRecordSet
	err := r.Get(ctx, client.ObjectKey{Namespace: desired.Namespace, Name: desired.Name}, &existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, desired, client.FieldOwner(fieldOwner))
	}
	if err != nil {
		return err
	}
	if !endpointRecordSetNeedsUpdate(&existing, desired) {
		return nil
	}
	existing.Labels = desired.Labels
	existing.Spec = desired.Spec
	return r.Update(ctx, &existing, client.FieldOwner(fieldOwner))
}

func (r *Reconciler) cleanupForService(ctx context.Context, endpointRecordSetNamespace, serviceNamespace, serviceName string) error {
	var existing endpointv1alpha1.EndpointRecordSetList
	if err := r.List(ctx, &existing, client.InNamespace(endpointRecordSetNamespace), client.MatchingLabels{
		generatedLabelServiceNamespace: serviceNamespace,
		generatedLabelServiceName:      serviceName,
	}); err != nil {
		return err
	}
	for i := range existing.Items {
		if err := r.Delete(ctx, &existing.Items[i]); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func endpointRecordSetNeedsUpdate(current, desired *endpointv1alpha1.EndpointRecordSet) bool {
	return !sameStringSet(current.Spec.Hostnames, desired.Spec.Hostnames) || !sameTargets(current.Spec.Targets, desired.Spec.Targets)
}

func hostnamesForService(service *corev1.Service) []string {
	if service == nil {
		return nil
	}
	value := strings.TrimSpace(service.Annotations[annotationHostnames])
	if value == "" {
		value = strings.TrimSpace(service.Annotations[legacyExternalDNSHostname])
	}
	return splitCSVSet(value)
}

func endpointTargets(ingresses []corev1.LoadBalancerIngress) []endpointv1alpha1.EndpointTarget {
	seen := map[string]struct{}{}
	targets := make([]endpointv1alpha1.EndpointTarget, 0, len(ingresses))
	for _, ingress := range ingresses {
		value := strings.TrimSuffix(strings.TrimSpace(ingress.Hostname), ".")
		targetType := endpointv1alpha1.EndpointTargetTypeHostname
		if value == "" {
			value = strings.TrimSpace(ingress.IP)
			targetType = endpointv1alpha1.EndpointTargetTypeIPAddress
		}
		if value == "" {
			continue
		}
		if targetType == endpointv1alpha1.EndpointTargetTypeIPAddress && net.ParseIP(value) == nil {
			continue
		}
		key := string(targetType) + "\x00" + value
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		targets = append(targets, endpointv1alpha1.EndpointTarget{Type: targetType, Value: value})
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].Type == targets[j].Type {
			return targets[i].Value < targets[j].Value
		}
		return targets[i].Type < targets[j].Type
	})
	return targets
}

func splitCSVSet(value string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, item := range strings.Split(value, ",") {
		hostname := strings.TrimSuffix(strings.TrimSpace(item), ".")
		if hostname == "" {
			continue
		}
		if _, ok := seen[hostname]; ok {
			continue
		}
		seen[hostname] = struct{}{}
		out = append(out, hostname)
	}
	sort.Strings(out)
	return out
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameTargets(a, b []endpointv1alpha1.EndpointTarget) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func generatedEndpointRecordSetName(serviceNamespace, serviceName string) string {
	hashInput := serviceNamespace + "/" + serviceName
	sum := sha256.Sum256([]byte(hashInput))
	hash := hex.EncodeToString(sum[:])[:10]
	candidate := fmt.Sprintf("%s-%s", serviceName, hash)
	if len(candidate) <= 63 {
		return candidate
	}
	return strings.TrimSuffix(candidate[:63], "-")
}
