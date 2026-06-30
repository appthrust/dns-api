package service

import (
	"context"
	"testing"

	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestReconcilerCreatesEndpointRecordSetForAnnotatedLoadBalancerService(t *testing.T) {
	ctx := context.Background()
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "app",
			Name:      "web",
			Annotations: map[string]string{
				annotationHostnames: "api.example.com, www.example.com.",
			},
		},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{
			{Hostname: "k8s-public-123.ap-northeast-1.elb.amazonaws.com."},
		}}},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(service).
		Build()
	reconciler := &Reconciler{Client: k8sClient, Scheme: testScheme(t), EndpointRecordSetNamespace: "dns-api-system"}

	if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(service)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var list endpointv1alpha1.EndpointRecordSetList
	if err := k8sClient.List(ctx, &list, client.InNamespace("dns-api-system")); err != nil {
		t.Fatalf("list EndpointRecordSets: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("EndpointRecordSets = %d, want 1", len(list.Items))
	}
	got := list.Items[0]
	if got.Labels[managedByLabel] != managedByValue {
		t.Fatalf("managed-by label = %q", got.Labels[managedByLabel])
	}
	if got.Labels[generatedLabelServiceNamespace] != "app" || got.Labels[generatedLabelServiceName] != "web" {
		t.Fatalf("source labels = %#v", got.Labels)
	}
	if len(got.Spec.Hostnames) != 2 || got.Spec.Hostnames[0] != "api.example.com" || got.Spec.Hostnames[1] != "www.example.com" {
		t.Fatalf("hostnames = %#v", got.Spec.Hostnames)
	}
	if len(got.Spec.Targets) != 1 || got.Spec.Targets[0].Type != endpointv1alpha1.EndpointTargetTypeHostname || got.Spec.Targets[0].Value != "k8s-public-123.ap-northeast-1.elb.amazonaws.com" {
		t.Fatalf("targets = %#v", got.Spec.Targets)
	}

	var updated corev1.Service
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(service), &updated); err != nil {
		t.Fatalf("get updated Service: %v", err)
	}
	if !containsString(updated.Finalizers, serviceFinalizer) {
		t.Fatalf("Service finalizers = %#v, want %q", updated.Finalizers, serviceFinalizer)
	}
}

func TestReconcilerAcceptsLegacyExternalDNSHostnameAnnotationForMigration(t *testing.T) {
	ctx := context.Background()
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "app",
			Name:      "web",
			Annotations: map[string]string{
				legacyExternalDNSHostname: "api.example.com",
			},
		},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{
			{IP: "203.0.113.10"},
		}}},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(service).
		Build()
	reconciler := &Reconciler{Client: k8sClient, Scheme: testScheme(t)}

	if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(service)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var list endpointv1alpha1.EndpointRecordSetList
	if err := k8sClient.List(ctx, &list, client.InNamespace("app")); err != nil {
		t.Fatalf("list EndpointRecordSets: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("EndpointRecordSets = %d, want 1", len(list.Items))
	}
	if got := list.Items[0].Spec.Hostnames; len(got) != 1 || got[0] != "api.example.com" {
		t.Fatalf("hostnames = %#v", got)
	}
	if got := list.Items[0].Spec.Targets; len(got) != 1 || got[0].Type != endpointv1alpha1.EndpointTargetTypeIPAddress || got[0].Value != "203.0.113.10" {
		t.Fatalf("targets = %#v", got)
	}
}

func TestReconcilerCleansEndpointRecordSetWhenServiceDNSIntentIsRemoved(t *testing.T) {
	ctx := context.Background()
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "app",
			Name:      "web",
			Annotations: map[string]string{
				annotationHostnames: "api.example.com",
			},
		},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{
			{Hostname: "lb.example.net"},
		}}},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(service).
		Build()
	reconciler := &Reconciler{Client: k8sClient, Scheme: testScheme(t)}

	if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(service)}); err != nil {
		t.Fatalf("initial Reconcile returned error: %v", err)
	}

	var updated corev1.Service
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(service), &updated); err != nil {
		t.Fatalf("get updated Service: %v", err)
	}
	updated.Annotations = nil
	if err := k8sClient.Update(ctx, &updated); err != nil {
		t.Fatalf("update Service annotations: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(service)}); err != nil {
		t.Fatalf("cleanup Reconcile returned error: %v", err)
	}

	var list endpointv1alpha1.EndpointRecordSetList
	if err := k8sClient.List(ctx, &list, client.InNamespace("app")); err != nil {
		t.Fatalf("list EndpointRecordSets: %v", err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("EndpointRecordSets = %d, want 0", len(list.Items))
	}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(service), &updated); err != nil {
		t.Fatalf("get cleaned Service: %v", err)
	}
	if containsString(updated.Finalizers, serviceFinalizer) {
		t.Fatalf("Service finalizers = %#v, want finalizer removed", updated.Finalizers)
	}
}

func TestReconcilerDoesNotCreateEndpointRecordSetWithoutLoadBalancerTarget(t *testing.T) {
	ctx := context.Background()
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "app",
			Name:      "web",
			Annotations: map[string]string{
				annotationHostnames: "api.example.com",
			},
		},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(service).
		Build()
	reconciler := &Reconciler{Client: k8sClient, Scheme: testScheme(t)}

	if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(service)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var list endpointv1alpha1.EndpointRecordSetList
	if err := k8sClient.List(ctx, &list, client.InNamespace("app")); err != nil {
		t.Fatalf("list EndpointRecordSets: %v", err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("EndpointRecordSets = %d, want 0", len(list.Items))
	}
}

func TestCleanupForDeletedServiceRemovesGeneratedEndpointRecordSets(t *testing.T) {
	ctx := context.Background()
	endpointRecordSet := &endpointv1alpha1.EndpointRecordSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "dns-api-system",
			Name:      "web-123",
			Labels: map[string]string{
				generatedLabelServiceNamespace: "app",
				generatedLabelServiceName:      "web",
			},
		},
		Spec: endpointv1alpha1.EndpointRecordSetSpec{
			Hostnames: []string{"api.example.com"},
			Targets:   []endpointv1alpha1.EndpointTarget{{Type: endpointv1alpha1.EndpointTargetTypeHostname, Value: "lb.example.net"}},
		},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(endpointRecordSet).
		Build()
	reconciler := &Reconciler{Client: k8sClient, Scheme: testScheme(t), EndpointRecordSetNamespace: "dns-api-system"}

	if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKey{Namespace: "app", Name: "web"}}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var got endpointv1alpha1.EndpointRecordSet
	err := k8sClient.Get(ctx, client.ObjectKeyFromObject(endpointRecordSet), &got)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("EndpointRecordSet get error = %v, want NotFound", err)
	}
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	if err := endpointv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add endpoint scheme: %v", err)
	}
	return scheme
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
