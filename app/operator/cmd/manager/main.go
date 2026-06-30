package main

import (
	"flag"
	"os"

	endpointrecordsetcontroller "github.com/appthrust/dns-api/internal/go/apps/endpoint/controllers/endpointrecordset"
	gatewayendpointcontroller "github.com/appthrust/dns-api/internal/go/apps/gatewayendpoint/controllers/gateway"
	zoneunitcontroller "github.com/appthrust/dns-api/internal/go/core/controllers/zoneunit"
	corewebhook "github.com/appthrust/dns-api/internal/go/core/webhook"
	cloudflareidentity "github.com/appthrust/dns-api/internal/go/providers/cloudflare/controllers/identity"
	cloudflarezoneclass "github.com/appthrust/dns-api/internal/go/providers/cloudflare/controllers/zoneclass"
	cloudflarezoneunit "github.com/appthrust/dns-api/internal/go/providers/cloudflare/controllers/zoneunit"
	cloudflarewebhook "github.com/appthrust/dns-api/internal/go/providers/cloudflare/webhook"
	route53identity "github.com/appthrust/dns-api/internal/go/providers/route53/controllers/identity"
	route53zoneclass "github.com/appthrust/dns-api/internal/go/providers/route53/controllers/zoneclass"
	route53zoneunit "github.com/appthrust/dns-api/internal/go/providers/route53/controllers/zoneunit"
	route53conversion "github.com/appthrust/dns-api/internal/go/providers/route53/conversion"
	route53webhook "github.com/appthrust/dns-api/internal/go/providers/route53/webhook"
	cloudflarev1alpha1 "github.com/appthrust/dns-api/pkg/go/api/cloudflare/v1alpha1"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	endpointconversionv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/conversion/v1alpha1"
	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=create;get;list;update;watch
// +kubebuilder:rbac:groups=cloudflare.dns.appthrust.io,resources=cloudflareidentities,verbs=get;list;patch;update;watch
// +kubebuilder:rbac:groups=cloudflare.dns.appthrust.io,resources=cloudflareidentities/status,verbs=get;patch;update
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=providers,verbs=get;list;watch
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=zoneclasses,verbs=get;list;watch;patch;update
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=zoneclasses/status,verbs=get;patch;update
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=zones,verbs=get;list;watch;patch;update
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=zones/finalizers,verbs=update
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=zones/status,verbs=get;patch;update
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=recordsets,verbs=create;delete;get;list;watch;patch;update
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=recordsets/finalizers,verbs=update
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=recordsets/status,verbs=get;patch;update
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=zoneunits,verbs=create;delete;get;list;watch;patch;update
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=zoneunits/finalizers,verbs=update
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=zoneunits/status,verbs=get;watch;patch;update
// +kubebuilder:rbac:groups=endpoint.dns.appthrust.io,resources=endpointprovidercapabilities,verbs=get;list;watch
// +kubebuilder:rbac:groups=endpoint.dns.appthrust.io,resources=endpointrecordsets,verbs=create;delete;get;list;watch;patch;update
// +kubebuilder:rbac:groups=endpoint.dns.appthrust.io,resources=endpointrecordsets/status,verbs=get;patch;update
// +kubebuilder:rbac:groups=endpoint.route53.dns.appthrust.io,resources=endpointrecordsetconversions,verbs=create
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch;update
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes/finalizers,verbs=update
// +kubebuilder:rbac:groups=route53.dns.appthrust.io,resources=route53identities,verbs=get;list;patch;update;watch
// +kubebuilder:rbac:groups=route53.dns.appthrust.io,resources=route53identities/status,verbs=get;patch;update

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(cloudflarev1alpha1.AddToScheme(scheme))
	utilruntime.Must(dnsv1alpha1.AddToScheme(scheme))
	utilruntime.Must(endpointv1alpha1.AddToScheme(scheme))
	utilruntime.Must(endpointconversionv1alpha1.AddToScheme(scheme))
	utilruntime.Must(gatewayv1.Install(scheme))
	utilruntime.Must(route53v1alpha1.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var probeAddr string
	var leaderElection bool
	endpointRecordSetNamespace := envOrDefault("ENDPOINT_RECORDSET_NAMESPACE", "")
	route53ControllerName := envOrDefault("ROUTE53_CONTROLLER_NAME", route53zoneunit.DefaultControllerName)
	route53ProviderName := envOrDefault("ROUTE53_PROVIDER_NAME", route53v1alpha1.ProviderName)
	route53ProviderVersion := envOrDefault("ROUTE53_PROVIDER_VERSION", route53v1alpha1.ProviderVersion)
	cloudflareControllerName := envOrDefault("CLOUDFLARE_CONTROLLER_NAME", cloudflarezoneunit.DefaultControllerName)
	cloudflareProviderName := envOrDefault("CLOUDFLARE_PROVIDER_NAME", cloudflarev1alpha1.ProviderName)
	cloudflareProviderVersion := envOrDefault("CLOUDFLARE_PROVIDER_VERSION", cloudflarev1alpha1.ProviderVersion)

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&leaderElection, "leader-elect", false, "Enable leader election for controller manager.")
	flag.StringVar(&endpointRecordSetNamespace, "endpoint-recordset-namespace", endpointRecordSetNamespace, "Namespace where generated EndpointRecordSet and RecordSet resources are stored. Defaults to the source namespace.")
	flag.StringVar(&route53ControllerName, "route53-controller-name", route53ControllerName, "ZoneClass.spec.controllerName handled by the Route 53 controller.")
	flag.StringVar(&route53ProviderName, "route53-provider-name", route53ProviderName, "Provider.metadata.name handled by the Route 53 controller.")
	flag.StringVar(&route53ProviderVersion, "route53-provider-version", route53ProviderVersion, "Provider version handled by the Route 53 controller.")
	flag.StringVar(&cloudflareControllerName, "cloudflare-controller-name", cloudflareControllerName, "ZoneClass.spec.controllerName handled by the Cloudflare controller.")
	flag.StringVar(&cloudflareProviderName, "cloudflare-provider-name", cloudflareProviderName, "Provider.metadata.name handled by the Cloudflare controller.")
	flag.StringVar(&cloudflareProviderVersion, "cloudflare-provider-version", cloudflareProviderVersion, "Provider version handled by the Cloudflare controller.")

	zapOpts := zap.Options{Development: true}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         leaderElection,
		LeaderElectionID:       "dns-api.appthrust.io",
	})
	if err != nil {
		ctrl.Log.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err := (&route53identity.IdentityReconciler{
		Client:   mgr.GetClient(),
		Resolver: route53identity.NewAWSIdentityResolver(),
		Recorder: mgr.GetEventRecorderFor("route53-identity-controller"),
	}).SetupWithManager(mgr); err != nil {
		ctrl.Log.Error(err, "unable to set up Route 53 Identity controller")
		os.Exit(1)
	}

	if err := (&cloudflareidentity.IdentityReconciler{
		Client:   mgr.GetClient(),
		Resolver: cloudflareidentity.NewCloudflareAPIClient(),
		Recorder: mgr.GetEventRecorderFor("cloudflare-identity-controller"),
	}).SetupWithManager(mgr); err != nil {
		ctrl.Log.Error(err, "unable to set up Cloudflare Identity controller")
		os.Exit(1)
	}

	if err := (&route53zoneclass.ZoneClassReconciler{
		ControllerName:  route53ControllerName,
		Client:          mgr.GetClient(),
		ProviderName:    route53ProviderName,
		ProviderVersion: route53ProviderVersion,
	}).SetupWithManager(mgr); err != nil {
		ctrl.Log.Error(err, "unable to set up ZoneClass controller")
		os.Exit(1)
	}

	if err := (&cloudflarezoneclass.ZoneClassReconciler{
		ControllerName:  cloudflareControllerName,
		Client:          mgr.GetClient(),
		ProviderName:    cloudflareProviderName,
		ProviderVersion: cloudflareProviderVersion,
	}).SetupWithManager(mgr); err != nil {
		ctrl.Log.Error(err, "unable to set up Cloudflare ZoneClass controller")
		os.Exit(1)
	}

	if err := (&route53zoneunit.ZoneReconciler{
		Client:          mgr.GetClient(),
		Scheme:          mgr.GetScheme(),
		ProviderFactory: route53zoneunit.NewAWSProviderFactory(),
		ControllerName:  route53ControllerName,
		ProviderName:    route53ProviderName,
		ProviderVersion: route53ProviderVersion,
		Recorder:        mgr.GetEventRecorderFor("route53-zoneunit-controller"),
	}).SetupWithManager(mgr); err != nil {
		ctrl.Log.Error(err, "unable to set up Route 53 ZoneUnit controller")
		os.Exit(1)
	}

	if err := (&cloudflarezoneunit.ZoneReconciler{
		Client:          mgr.GetClient(),
		Scheme:          mgr.GetScheme(),
		ProviderFactory: cloudflarezoneunit.NewCloudflareAPIClient(),
		ControllerName:  cloudflareControllerName,
		ProviderName:    cloudflareProviderName,
		ProviderVersion: cloudflareProviderVersion,
		Recorder:        mgr.GetEventRecorderFor("cloudflare-zoneunit-controller"),
	}).SetupWithManager(mgr); err != nil {
		ctrl.Log.Error(err, "unable to set up Cloudflare ZoneUnit controller")
		os.Exit(1)
	}

	if err := (&zoneunitcontroller.ZoneUnitCompositionReconciler{
		Client: mgr.GetClient(),
	}).SetupWithManager(mgr); err != nil {
		ctrl.Log.Error(err, "unable to set up ZoneUnit composition controller")
		os.Exit(1)
	}

	if err := (&endpointrecordsetcontroller.Reconciler{
		Client:             mgr.GetClient(),
		Scheme:             mgr.GetScheme(),
		RESTConfig:         mgr.GetConfig(),
		RecordSetNamespace: endpointRecordSetNamespace,
	}).SetupWithManager(mgr); err != nil {
		ctrl.Log.Error(err, "unable to set up EndpointRecordSet controller")
		os.Exit(1)
	}

	if err := (&gatewayendpointcontroller.Reconciler{
		Client:                     mgr.GetClient(),
		Scheme:                     mgr.GetScheme(),
		EndpointRecordSetNamespace: endpointRecordSetNamespace,
	}).SetupWithManager(mgr); err != nil {
		ctrl.Log.Error(err, "unable to set up Gateway Endpoint controller")
		os.Exit(1)
	}

	if err := corewebhook.SetupCoreValidationWebhookWithManager(mgr); err != nil {
		ctrl.Log.Error(err, "unable to set up core validation webhook")
		os.Exit(1)
	}
	if err := route53webhook.SetupValidationWebhookWithManager(mgr); err != nil {
		ctrl.Log.Error(err, "unable to set up Route 53 validation webhook")
		os.Exit(1)
	}
	if err := cloudflarewebhook.SetupValidationWebhookWithManager(mgr); err != nil {
		ctrl.Log.Error(err, "unable to set up Cloudflare validation webhook")
		os.Exit(1)
	}
	route53conversion.NewHandler().Register(mgr.GetWebhookServer())

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		ctrl.Log.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		ctrl.Log.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	ctrl.Log.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		ctrl.Log.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
