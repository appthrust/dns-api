package cloudflare

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	zoneunitcontroller "github.com/appthrust/dns-api/internal/go/core/controllers/zoneunit"
	zoneclass "github.com/appthrust/dns-api/internal/go/providers/cloudflare/controllers/zoneclass"
	cloudflarev1alpha1 "github.com/appthrust/dns-api/pkg/go/api/cloudflare/v1alpha1"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCloudflareZoneClassReconcilerAcceptsRootIdentityRef(t *testing.T) {
	ctx := context.Background()
	zoneClass := cloudflareZoneClass("platform", "cloudflare-public")
	k8sClient := fake.NewClientBuilder().
		WithScheme(cloudflareTestScheme(t)).
		WithObjects(cloudflareProvider(), acceptedCloudflareIdentity("platform", "cloudflare-ci"), zoneClass).
		WithStatusSubresource(&dnsv1alpha1.ZoneClass{}, &cloudflarev1alpha1.CloudflareIdentity{}).
		Build()
	reconciler := &zoneclass.ZoneClassReconciler{Client: k8sClient}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "platform", Name: "cloudflare-public"}}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var got dnsv1alpha1.ZoneClass
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "platform", Name: "cloudflare-public"}, &got); err != nil {
		t.Fatalf("ZoneClass was not found: %v", err)
	}
	assertCloudflareCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted")
}

func TestCloudflareZoneReconcilerIgnoresRoute53ZoneUnit(t *testing.T) {
	ctx := context.Background()
	unit := cloudflareZoneUnit("app", "apps-example-com")
	unit.Spec.Provider = dnsv1alpha1.ProviderReference{Name: "route53.dns.appthrust.io", Version: "v1alpha1"}
	unit.Spec.Zone.Adoption = runtime.RawExtension{Raw: []byte(`{"hostedZoneId":"ZADOPT"}`)}
	unit.Status.Zone = &dnsv1alpha1.ZoneUnitZoneStatus{
		Conditions: []metav1.Condition{
			{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted", ObservedGeneration: 1},
		},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(cloudflareTestScheme(t)).
		WithObjects(unit).
		WithStatusSubresource(&dnsv1alpha1.ZoneUnit{}).
		Build()
	reconciler := &ZoneReconciler{Client: k8sClient}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var got dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &got); err != nil {
		t.Fatalf("ZoneUnit was not found: %v", err)
	}
	assertCloudflareCondition(t, got.Status.Zone.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted")
}

func reconcileCloudflareZoneAndProject(t *testing.T, ctx context.Context, k8sClient client.Client, reconciler *ZoneReconciler, request ctrl.Request) (ctrl.Result, error) {
	t.Helper()
	result, err := reconciler.Reconcile(ctx, request)
	if err != nil {
		return result, err
	}
	if _, projectErr := (&zoneunitcontroller.ZoneUnitCompositionReconciler{Client: k8sClient}).Reconcile(ctx, request); projectErr != nil {
		return result, projectErr
	}
	return result, nil
}

func TestCloudflareZoneReconcilerCreatesFullZone(t *testing.T) {
	ctx := context.Background()
	zone := cloudflareZone("app", "apps-example-com")
	k8sClient := fake.NewClientBuilder().
		WithScheme(cloudflareTestScheme(t)).
		WithObjects(
			cloudflareProvider(),
			acceptedReadyCloudflareIdentity("platform", "cloudflare-ci"),
			acceptedCloudflareZoneClass("platform", "cloudflare-public"),
			zone,
			cloudflareZoneUnit("app", "apps-example-com"),
		).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneClass{}, &dnsv1alpha1.ZoneUnit{}, &cloudflarev1alpha1.CloudflareIdentity{}).
		Build()
	provider := &fakeCloudflareZoneProvider{
		created: CloudflareZone{
			ID:          "023e105f4ecef8ad9ca31a8372d0c353",
			AccountID:   "023e105f4ecef8ad9ca31a8372d0c354",
			Name:        "apps.example.com",
			Status:      "pending",
			Type:        "full",
			NameServers: []string{"everton.ns.cloudflare.com", "kira.ns.cloudflare.com"},
		},
	}
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider, RequeueAfter: time.Minute}

	result, err := reconcileCloudflareZoneAndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.RequeueAfter != time.Minute {
		t.Fatalf("RequeueAfter = %s, want %s", result.RequeueAfter, time.Minute)
	}
	if !provider.createCalled {
		t.Fatalf("Cloudflare zone was not created")
	}

	var got dnsv1alpha1.Zone
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &got); err != nil {
		t.Fatalf("Zone was not found: %v", err)
	}
	assertCloudflareCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionAccepted), metav1.ConditionTrue, "Accepted")
	assertCloudflareCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionTrue, "Programmed")
	if len(got.Status.NameServers) != 2 {
		t.Fatalf("status.nameServers = %#v", got.Status.NameServers)
	}
	data, err := cloudflareZoneStatusData(&got)
	if err != nil {
		t.Fatalf("cloudflareZoneStatusData returned error: %v", err)
	}
	if data.Zone.ID != "023e105f4ecef8ad9ca31a8372d0c353" || data.Zone.Type != "full" {
		t.Fatalf("status.provider.data.zone = %#v", data.Zone)
	}
	if got.Status.Provider != nil && len(got.Status.Provider.State.Raw) > 0 {
		t.Fatalf("status.provider.state = %s, want empty after successful observation", string(got.Status.Provider.State.Raw))
	}
}

func TestCloudflareZoneReconcilerRecoversCreatedZoneFromCreationIntent(t *testing.T) {
	ctx := context.Background()
	zone := cloudflareZone("app", "apps-example-com")
	unit := cloudflareZoneUnitWithCreationIntent("app", "apps-example-com")
	created := CloudflareZone{
		ID:          "023e105f4ecef8ad9ca31a8372d0c353",
		AccountID:   "023e105f4ecef8ad9ca31a8372d0c354",
		Name:        "apps.example.com",
		Status:      "pending",
		Type:        "full",
		NameServers: []string{"everton.ns.cloudflare.com", "kira.ns.cloudflare.com"},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(cloudflareTestScheme(t)).
		WithObjects(
			cloudflareProvider(),
			acceptedReadyCloudflareIdentity("platform", "cloudflare-ci"),
			acceptedCloudflareZoneClass("platform", "cloudflare-public"),
			zone,
			unit,
		).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneClass{}, &dnsv1alpha1.ZoneUnit{}, &cloudflarev1alpha1.CloudflareIdentity{}).
		Build()
	provider := &fakeCloudflareZoneProvider{
		listZones: []CloudflareZone{created},
	}
	recordLister := &fakeCloudflareZoneRecordListProvider{
		fakeCloudflareZoneProvider: provider,
		dnsRecordsByZoneID: map[string][]CloudflareDNSRecord{
			created.ID: {
				{Type: "NS", Name: "apps.example.com", Content: "everton.ns.cloudflare.com"},
				{Type: "SOA", Name: "apps.example.com"},
			},
		},
	}
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: recordLister, RequeueAfter: time.Minute}

	result, err := reconcileCloudflareZoneAndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.RequeueAfter != time.Minute {
		t.Fatalf("RequeueAfter = %s, want %s", result.RequeueAfter, time.Minute)
	}
	if provider.createCalled {
		t.Fatalf("Cloudflare zone was created again")
	}

	var got dnsv1alpha1.Zone
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &got); err != nil {
		t.Fatalf("Zone was not found: %v", err)
	}
	data, err := cloudflareZoneStatusData(&got)
	if err != nil {
		t.Fatalf("cloudflareZoneStatusData returned error: %v", err)
	}
	if data.Zone.ID != created.ID {
		t.Fatalf("status.provider.data.zone.id = %q, want %q", data.Zone.ID, created.ID)
	}
	assertCloudflareCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionTrue, "Programmed")
}

func TestCloudflareZoneReconcilerDoesNotRecoverSameNameZoneWithoutSafeCreationIntent(t *testing.T) {
	tests := []struct {
		name    string
		unit    *dnsv1alpha1.ZoneUnit
		records []CloudflareDNSRecord
	}{
		{
			name: "no creation intent",
			unit: cloudflareZoneUnit("app", "apps-example-com"),
		},
		{
			name: "user DNS record exists",
			unit: cloudflareZoneUnitWithCreationIntent("app", "apps-example-com"),
			records: []CloudflareDNSRecord{
				{ID: "023e105f4ecef8ad9ca31a8372d0c354", Type: "A", Name: "www.apps.example.com", Content: "192.0.2.10"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			zone := cloudflareZone("app", "apps-example-com")
			existing := CloudflareZone{
				ID:          "023e105f4ecef8ad9ca31a8372d0c353",
				AccountID:   "023e105f4ecef8ad9ca31a8372d0c354",
				Name:        "apps.example.com",
				Status:      "pending",
				Type:        "full",
				NameServers: []string{"everton.ns.cloudflare.com"},
			}
			k8sClient := fake.NewClientBuilder().
				WithScheme(cloudflareTestScheme(t)).
				WithObjects(
					cloudflareProvider(),
					acceptedReadyCloudflareIdentity("platform", "cloudflare-ci"),
					acceptedCloudflareZoneClass("platform", "cloudflare-public"),
					zone,
					tt.unit,
				).
				WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneClass{}, &dnsv1alpha1.ZoneUnit{}, &cloudflarev1alpha1.CloudflareIdentity{}).
				Build()
			provider := &fakeCloudflareZoneProvider{
				listZones: []CloudflareZone{existing},
			}
			recordLister := &fakeCloudflareZoneRecordListProvider{
				fakeCloudflareZoneProvider: provider,
				dnsRecordsByZoneID:         map[string][]CloudflareDNSRecord{existing.ID: tt.records},
			}
			reconciler := &ZoneReconciler{Client: k8sClient, Provider: recordLister}

			if _, err := reconcileCloudflareZoneAndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}}); err != nil {
				t.Fatalf("Reconcile returned error: %v", err)
			}
			if provider.createCalled {
				t.Fatalf("Cloudflare zone was created")
			}

			var got dnsv1alpha1.Zone
			if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &got); err != nil {
				t.Fatalf("Zone was not found: %v", err)
			}
			assertCloudflareCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ProviderConflict")
			if got.Status.Provider != nil {
				data, err := cloudflareZoneStatusData(&got)
				if err != nil {
					t.Fatalf("cloudflareZoneStatusData returned error: %v", err)
				}
				if data.Zone.ID != "" {
					t.Fatalf("status.provider.data.zone.id = %q, want empty", data.Zone.ID)
				}
			}
		})
	}
}

func TestCloudflareZoneReconcilerPollsInitializingZoneQuickly(t *testing.T) {
	ctx := context.Background()
	k8sClient := fake.NewClientBuilder().
		WithScheme(cloudflareTestScheme(t)).
		WithObjects(
			cloudflareProvider(),
			acceptedReadyCloudflareIdentity("platform", "cloudflare-ci"),
			acceptedCloudflareZoneClass("platform", "cloudflare-public"),
			cloudflareZone("app", "apps-example-com"),
			cloudflareZoneUnit("app", "apps-example-com"),
		).
		WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneClass{}, &dnsv1alpha1.ZoneUnit{}, &cloudflarev1alpha1.CloudflareIdentity{}).
		Build()
	provider := &fakeCloudflareZoneProvider{
		created: CloudflareZone{
			ID:          "023e105f4ecef8ad9ca31a8372d0c353",
			AccountID:   "023e105f4ecef8ad9ca31a8372d0c354",
			Name:        "apps.example.com",
			Status:      "initializing",
			Type:        "full",
			NameServers: []string{"everton.ns.cloudflare.com"},
		},
	}
	reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider, RequeueAfter: time.Minute, PendingPollAfter: 15 * time.Second}

	result, err := reconcileCloudflareZoneAndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.RequeueAfter != 15*time.Second {
		t.Fatalf("RequeueAfter = %s, want %s", result.RequeueAfter, 15*time.Second)
	}

	var got dnsv1alpha1.Zone
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &got); err != nil {
		t.Fatalf("Zone was not found: %v", err)
	}
	assertCloudflareCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ProviderChangePending")
}

func TestCloudflareZoneReconcilerOnlyRequeuesTemporaryProviderErrors(t *testing.T) {
	tests := []struct {
		name                string
		err                 error
		wantRequeueAfter    time.Duration
		wantConditionReason string
	}{
		{
			name:                "temporary",
			err:                 &identityReasonError{reason: "ProviderUnavailable", message: "Cloudflare API returned HTTP 429"},
			wantRequeueAfter:    7 * time.Second,
			wantConditionReason: "ProviderUnavailable",
		},
		{
			name:                "invalid request",
			err:                 &identityReasonError{reason: "ProviderInvalidRequest", message: "Cloudflare API rejected the request"},
			wantRequeueAfter:    0,
			wantConditionReason: "ProviderInvalidRequest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			k8sClient := fake.NewClientBuilder().
				WithScheme(cloudflareTestScheme(t)).
				WithObjects(
					cloudflareProvider(),
					acceptedReadyCloudflareIdentity("platform", "cloudflare-ci"),
					acceptedCloudflareZoneClass("platform", "cloudflare-public"),
					cloudflareZone("app", "apps-example-com"),
					cloudflareZoneUnit("app", "apps-example-com"),
				).
				WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneClass{}, &dnsv1alpha1.ZoneUnit{}, &cloudflarev1alpha1.CloudflareIdentity{}).
				Build()
			provider := &fakeCloudflareZoneProvider{listErr: tt.err}
			reconciler := &ZoneReconciler{Client: k8sClient, Provider: provider, TemporaryRetryAfter: 7 * time.Second}

			result, err := reconcileCloudflareZoneAndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}})
			if err != nil {
				t.Fatalf("Reconcile returned error: %v", err)
			}
			if result.RequeueAfter != tt.wantRequeueAfter {
				t.Fatalf("RequeueAfter = %s, want %s", result.RequeueAfter, tt.wantRequeueAfter)
			}

			var got dnsv1alpha1.Zone
			if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &got); err != nil {
				t.Fatalf("Zone was not found: %v", err)
			}
			assertCloudflareCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, tt.wantConditionReason)
		})
	}
}

func TestCloudflareZoneReconcilerRejectsInvalidObservedZoneIDs(t *testing.T) {
	tests := []struct {
		name     string
		zone     func() *dnsv1alpha1.Zone
		unit     func() *dnsv1alpha1.ZoneUnit
		provider func() *fakeCloudflareZoneProvider
	}{
		{
			name: "created zone",
			zone: func() *dnsv1alpha1.Zone {
				return cloudflareZone("app", "apps-example-com")
			},
			unit: func() *dnsv1alpha1.ZoneUnit {
				return cloudflareZoneUnit("app", "apps-example-com")
			},
			provider: func() *fakeCloudflareZoneProvider {
				return &fakeCloudflareZoneProvider{created: invalidIDCloudflareZone("NOT-LOWERCASE-HEX")}
			},
		},
		{
			name: "adopted zone",
			zone: func() *dnsv1alpha1.Zone {
				zone := cloudflareZone("app", "apps-example-com")
				zone.Spec.Adoption = cloudflareRaw(t, map[string]any{"zoneID": "023e105f4ecef8ad9ca31a8372d0c353"})
				return zone
			},
			unit: func() *dnsv1alpha1.ZoneUnit {
				unit := cloudflareZoneUnit("app", "apps-example-com")
				unit.Spec.Zone.Adoption = cloudflareRaw(t, map[string]any{"zoneID": "023e105f4ecef8ad9ca31a8372d0c353"})
				return unit
			},
			provider: func() *fakeCloudflareZoneProvider {
				return &fakeCloudflareZoneProvider{zonesByID: map[string]CloudflareZone{
					"023e105f4ecef8ad9ca31a8372d0c353": invalidIDCloudflareZone("023e105f4ecef8ad9ca31a8372d0C353"),
				}}
			},
		},
		{
			name: "managed zone re-observation",
			zone: func() *dnsv1alpha1.Zone {
				return cloudflareProgrammedZone("app", "apps-example-com")
			},
			unit: func() *dnsv1alpha1.ZoneUnit {
				unit := cloudflareZoneUnit("app", "apps-example-com")
				unit.Status.Zone = &dnsv1alpha1.ZoneUnitZoneStatus{
					Provider: cloudflareProgrammedZone("app", "apps-example-com").Status.Provider,
				}
				return unit
			},
			provider: func() *fakeCloudflareZoneProvider {
				return &fakeCloudflareZoneProvider{zonesByID: map[string]CloudflareZone{
					"023e105f4ecef8ad9ca31a8372d0c353": invalidIDCloudflareZone("023e105f4ecef8ad9ca31a8372d0c35"),
				}}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			k8sClient := fake.NewClientBuilder().
				WithScheme(cloudflareTestScheme(t)).
				WithObjects(
					cloudflareProvider(),
					acceptedReadyCloudflareIdentity("platform", "cloudflare-ci"),
					acceptedCloudflareZoneClass("platform", "cloudflare-public"),
					tt.zone(),
					tt.unit(),
				).
				WithStatusSubresource(&dnsv1alpha1.Zone{}, &dnsv1alpha1.ZoneClass{}, &dnsv1alpha1.ZoneUnit{}, &cloudflarev1alpha1.CloudflareIdentity{}).
				Build()
			reconciler := &ZoneReconciler{Client: k8sClient, Provider: tt.provider()}

			if _, err := reconcileCloudflareZoneAndProject(t, ctx, k8sClient, reconciler, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "apps-example-com"}}); err != nil {
				t.Fatalf("Reconcile returned error: %v", err)
			}

			var got dnsv1alpha1.Zone
			if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: "app", Name: "apps-example-com"}, &got); err != nil {
				t.Fatalf("Zone was not found: %v", err)
			}
			assertCloudflareCondition(t, got.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ProviderInvalidRequest")
			if got.Status.Provider != nil {
				if data, err := cloudflareZoneStatusData(&got); err == nil && data.Zone.ID != "" && !cloudflareIDPattern.MatchString(data.Zone.ID) {
					t.Fatalf("invalid status.provider.data.zone.id was written: %#v", data.Zone.ID)
				}
			}
		})
	}
}

func cloudflareRaw(t *testing.T, value any) runtime.RawExtension {
	t.Helper()
	bytes, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return runtime.RawExtension{Raw: bytes}
}

func invalidIDCloudflareZone(id string) CloudflareZone {
	return CloudflareZone{
		ID:          id,
		AccountID:   "023e105f4ecef8ad9ca31a8372d0c354",
		Name:        "apps.example.com",
		Status:      "pending",
		Type:        "full",
		NameServers: []string{"everton.ns.cloudflare.com"},
	}
}

func cloudflareProvider() *dnsv1alpha1.Provider {
	return &dnsv1alpha1.Provider{
		ObjectMeta: metav1.ObjectMeta{Name: cloudflarev1alpha1.ProviderName},
		Spec: dnsv1alpha1.ProviderSpec{
			Display: dnsv1alpha1.ProviderDisplay{Name: "Cloudflare DNS"},
			Versions: []dnsv1alpha1.ProviderVersion{
				{
					Name:    cloudflarev1alpha1.ProviderVersion,
					Served:  true,
					Storage: true,
					Identity: &dnsv1alpha1.ProviderIdentity{
						Resource: dnsv1alpha1.ProviderIdentityResource{
							Group: "cloudflare.dns.appthrust.io",
							Kind:  "CloudflareIdentity",
							Scope: "Namespaced",
						},
					},
				},
			},
		},
	}
}

func cloudflareZoneClass(namespace, name string) *dnsv1alpha1.ZoneClass {
	raw, err := json.Marshal(cloudflarev1alpha1.CloudflareZoneClassParameters{
		ZoneCreationPolicy: cloudflarev1alpha1.ZoneCreationPolicyCreate,
		ZoneDeletionPolicy: cloudflarev1alpha1.ZoneDeletionPolicyRetain,
	})
	if err != nil {
		panic(err)
	}
	return &dnsv1alpha1.ZoneClass{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Generation: 1},
		Spec: dnsv1alpha1.ZoneClassSpec{
			Provider:       cloudflarev1alpha1.ProviderRef,
			ControllerName: DefaultControllerName,
			IdentityRef:    dnsv1alpha1.LocalObjectReference{Name: "cloudflare-ci"},
			Parameters:     runtime.RawExtension{Raw: raw},
			AllowedZones: dnsv1alpha1.ZoneClassAllowedZones{
				Namespaces: dnsv1alpha1.NamespacePolicy{From: dnsv1alpha1.NamespacesFromAll},
			},
		},
	}
}

func acceptedCloudflareZoneClass(namespace, name string) *dnsv1alpha1.ZoneClass {
	zoneClass := cloudflareZoneClass(namespace, name)
	zoneClass.Status.Conditions = []metav1.Condition{
		{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted", ObservedGeneration: 1},
	}
	return zoneClass
}

func cloudflareZone(namespace, name string) *dnsv1alpha1.Zone {
	platformNamespace := "platform"
	return &dnsv1alpha1.Zone{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Generation: 1},
		Spec: dnsv1alpha1.ZoneSpec{
			DomainName: "apps.example.com",
			ZoneClassRef: dnsv1alpha1.ZoneClassReference{
				Namespace: &platformNamespace,
				Name:      "cloudflare-public",
			},
			Provider: cloudflarev1alpha1.ProviderRef,
		},
	}
}

func cloudflareZoneUnit(namespace, name string) *dnsv1alpha1.ZoneUnit {
	return &dnsv1alpha1.ZoneUnit{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Generation: 1},
		Spec: dnsv1alpha1.ZoneUnitSpec{
			Provider: cloudflarev1alpha1.ProviderRef,
			Zone: dnsv1alpha1.ZoneUnitZoneSpec{
				Ref: dnsv1alpha1.ObjectReference{
					Namespace: namespace,
					Name:      name,
				},
				DomainName: "apps.example.com",
				ZoneClassRef: dnsv1alpha1.ObjectReference{
					Namespace: "platform",
					Name:      "cloudflare-public",
				},
			},
		},
	}
}

func cloudflareZoneUnitWithCreationIntent(namespace, name string) *dnsv1alpha1.ZoneUnit {
	unit := cloudflareZoneUnit(namespace, name)
	state := cloudflarev1alpha1.CloudflareZoneState{
		CreationIntent: &cloudflarev1alpha1.CloudflareZoneCreationIntent{
			AccountID: "023e105f4ecef8ad9ca31a8372d0c354",
			Name:      "apps.example.com",
		},
	}
	raw, err := json.Marshal(state)
	if err != nil {
		panic(err)
	}
	unit.Status.Zone = &dnsv1alpha1.ZoneUnitZoneStatus{
		Provider: &dnsv1alpha1.ProviderStatus{State: runtime.RawExtension{Raw: raw}},
	}
	return unit
}

func acceptedCloudflareIdentity(namespace, name string) *cloudflarev1alpha1.CloudflareIdentity {
	identity := validCloudflareIdentity()
	identity.Namespace = namespace
	identity.Name = name
	identity.Generation = 1
	identity.Status.Conditions = []metav1.Condition{
		{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted", ObservedGeneration: 1},
	}
	return identity
}

func acceptedReadyCloudflareIdentity(namespace, name string) *cloudflarev1alpha1.CloudflareIdentity {
	identity := acceptedCloudflareIdentity(namespace, name)
	identity.Status.Account = &cloudflarev1alpha1.CloudflareAccountStatus{ID: "023e105f4ecef8ad9ca31a8372d0c354", Name: "ci", Type: "standard"}
	identity.Status.Conditions = append(identity.Status.Conditions,
		metav1.Condition{Type: string(dnsv1alpha1.ConditionReady), Status: metav1.ConditionTrue, Reason: "Ready", ObservedGeneration: 1},
	)
	return identity
}

type fakeCloudflareZoneProvider struct {
	created      CloudflareZone
	createCalled bool
	listErr      error
	zonesByID    map[string]CloudflareZone
	listZones    []CloudflareZone
}

func (p *fakeCloudflareZoneProvider) GetZone(_ context.Context, id string) (CloudflareZone, error) {
	if p.zonesByID != nil {
		if zone, ok := p.zonesByID[id]; ok {
			return zone, nil
		}
	}
	return CloudflareZone{}, &identityReasonError{reason: "ExternalResourceNotFound", message: "not found"}
}

func (p *fakeCloudflareZoneProvider) ListZonesByName(_ context.Context, accountID, name string) ([]CloudflareZone, error) {
	if p.listErr != nil {
		return nil, p.listErr
	}
	var zones []CloudflareZone
	for _, zone := range p.listZones {
		if zone.AccountID == accountID && zone.Name == name {
			zones = append(zones, zone)
		}
	}
	return zones, nil
}

func (p *fakeCloudflareZoneProvider) CreateZone(context.Context, string, string) (CloudflareZone, error) {
	p.createCalled = true
	return p.created, nil
}

func (p *fakeCloudflareZoneProvider) DeleteZone(context.Context, string) error {
	return nil
}

type fakeCloudflareZoneRecordListProvider struct {
	*fakeCloudflareZoneProvider
	dnsRecordsByZoneID map[string][]CloudflareDNSRecord
}

func (p *fakeCloudflareZoneRecordListProvider) ListDNSRecords(_ context.Context, zoneID, name string) ([]CloudflareDNSRecord, error) {
	var records []CloudflareDNSRecord
	for _, record := range p.dnsRecordsByZoneID[zoneID] {
		if name == "" || record.Name == name {
			records = append(records, record)
		}
	}
	return records, nil
}
