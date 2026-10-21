package main

import (
	"flag"
	"os"
	"strconv"

	endpointrecordsetcontroller "github.com/appthrust/dns-api/internal/go/apps/endpoint/controllers/endpointrecordset"
	gatewayendpointcontroller "github.com/appthrust/dns-api/internal/go/apps/gatewayendpoint/controllers/gateway"
	serviceendpointcontroller "github.com/appthrust/dns-api/internal/go/apps/serviceendpoint/controllers/service"
	zoneunitcontroller "github.com/appthrust/dns-api/internal/go/core/controllers/zoneunit"
	externalprobe "github.com/appthrust/dns-api/internal/go/core/dnsprobe"
	corewebhook "github.com/appthrust/dns-api/internal/go/core/webhook"
	cloudflareidentity "github.com/appthrust/dns-api/internal/go/providers/cloudflare/controllers/identity"
	cloudflarezoneclass "github.com/appthrust/dns-api/internal/go/providers/cloudflare/controllers/zoneclass"
	cloudflarezoneunit "github.com/appthrust/dns-api/internal/go/providers/cloudflare/controllers/zoneunit"
	cloudflarewebhook "github.com/appthrust/dns-api/internal/go/providers/cloudflare/webhook"
	externalzoneclass "github.com/appthrust/dns-api/internal/go/providers/external/controllers/zoneclass"
	externalzoneunit "github.com/appthrust/dns-api/internal/go/providers/external/controllers/zoneunit"
	externalconversion "github.com/appthrust/dns-api/internal/go/providers/external/conversion"
	hostedzoneclass "github.com/appthrust/dns-api/internal/go/providers/hosted/controllers/zoneclass"
	hostedzoneunit "github.com/appthrust/dns-api/internal/go/providers/hosted/controllers/zoneunit"
	hostedconversion "github.com/appthrust/dns-api/internal/go/providers/hosted/conversion"
	route53identity "github.com/appthrust/dns-api/internal/go/providers/route53/controllers/identity"
	route53zoneclass "github.com/appthrust/dns-api/internal/go/providers/route53/controllers/zoneclass"
	route53zoneunit "github.com/appthrust/dns-api/internal/go/providers/route53/controllers/zoneunit"
	route53conversion "github.com/appthrust/dns-api/internal/go/providers/route53/conversion"
	route53webhook "github.com/appthrust/dns-api/internal/go/providers/route53/webhook"
	cloudflarev1alpha1 "github.com/appthrust/dns-api/pkg/go/api/cloudflare/v1alpha1"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	endpointconversionv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/conversion/v1alpha1"
	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	externalv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/external/v1alpha1"
	hostedv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/hosted/v1alpha1"
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
// +kubebuilder:rbac:groups=endpoint.dns.appthrust.io,resources=endpointrecordsets/finalizers,verbs=update
// +kubebuilder:rbac:groups=endpoint.dns.appthrust.io,resources=endpointrecordsets/status,verbs=get;patch;update
// +kubebuilder:rbac:groups=endpoint.route53.dns.appthrust.io,resources=endpointrecordsetconversions,verbs=create
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;update
// +kubebuilder:rbac:groups="",resources=services/finalizers,verbs=update
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
	utilruntime.Must(externalv1alpha1.AddToScheme(scheme))
	utilruntime.Must(gatewayv1.Install(scheme))
	utilruntime.Must(hostedv1alpha1.AddToScheme(scheme))
	utilruntime.Must(route53v1alpha1.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var probeAddr string
	var leaderElection bool
	var gatewayEndpointControllerEnabled bool
	var serviceEndpointControllerEnabled bool
	route53ControllerEnabled := envBoolOrDefault("ROUTE53_CONTROLLER_ENABLED", true)
	cloudflareControllerEnabled := envBoolOrDefault("CLOUDFLARE_CONTROLLER_ENABLED", true)
	externalControllerEnabled := envBoolOrDefault("EXTERNAL_CONTROLLER_ENABLED", false)
	hostedControllerEnabled := envBoolOrDefault("HOSTED_CONTROLLER_ENABLED", false)
	route53EndpointConversionEnabled := envBoolOrDefault("ROUTE53_ENDPOINT_CONVERSION_ENABLED", true)
	endpointRecordSetNamespace := envOrDefault("ENDPOINT_RECORDSET_NAMESPACE", "")
	route53ControllerName := envOrDefault("ROUTE53_CONTROLLER_NAME", route53zoneunit.DefaultControllerName)
	route53ProviderName := envOrDefault("ROUTE53_PROVIDER_NAME", route53v1alpha1.ProviderName)
	route53ProviderVersion := envOrDefault("ROUTE53_PROVIDER_VERSION", route53v1alpha1.ProviderVersion)
	cloudflareControllerName := envOrDefault("CLOUDFLARE_CONTROLLER_NAME", cloudflarezoneunit.DefaultControllerName)
	cloudflareProviderName := envOrDefault("CLOUDFLARE_PROVIDER_NAME", cloudflarev1alpha1.ProviderName)
	cloudflareProviderVersion := envOrDefault("CLOUDFLARE_PROVIDER_VERSION", cloudflarev1alpha1.ProviderVersion)
	externalControllerName := envOrDefault("EXTERNAL_CONTROLLER_NAME", externalv1alpha1.ControllerName)
	externalProviderName := envOrDefault("EXTERNAL_PROVIDER_NAME", externalv1alpha1.ProviderName)
	externalProviderVersion := envOrDefault("EXTERNAL_PROVIDER_VERSION", externalv1alpha1.ProviderVersion)
	hostedControllerName := envOrDefault("HOSTED_CONTROLLER_NAME", hostedv1alpha1.ControllerName)
	hostedProviderName := envOrDefault("HOSTED_PROVIDER_NAME", hostedv1alpha1.ProviderName)
	hostedProviderVersion := envOrDefault("HOSTED_PROVIDER_VERSION", hostedv1alpha1.ProviderVersion)
	hostedEndpointTTL := envUintOrDefault("HOSTED_ENDPOINT_TTL", uint(hostedv1alpha1.DefaultTTL))
	var externalDNSQPS float64
	var externalDNSBurst int

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&leaderElection, "leader-elect", false, "Enable leader election for controller manager.")
	flag.BoolVar(&gatewayEndpointControllerEnabled, "gateway-endpoint-controller-enabled", true, "Watch Gateway API resources and synthesize EndpointRecordSets from HTTPRoutes.")
	flag.BoolVar(&serviceEndpointControllerEnabled, "service-endpoint-controller-enabled", true, "Watch Services and synthesize EndpointRecordSets from their endpoint annotations.")
	flag.StringVar(&endpointRecordSetNamespace, "endpoint-recordset-namespace", endpointRecordSetNamespace, "Namespace where generated EndpointRecordSet and RecordSet resources are stored. Defaults to the source namespace.")
	flag.BoolVar(&route53ControllerEnabled, "route53-controller-enabled", route53ControllerEnabled, "Enable Route 53 controllers and identity admission.")
	flag.BoolVar(&cloudflareControllerEnabled, "cloudflare-controller-enabled", cloudflareControllerEnabled, "Enable Cloudflare controllers and identity admission.")
	flag.BoolVar(&externalControllerEnabled, "external-controller-enabled", externalControllerEnabled, "Enable external DNS verification controllers and endpoint conversion.")
	flag.BoolVar(&route53EndpointConversionEnabled, "route53-endpoint-conversion-enabled", route53EndpointConversionEnabled, "Serve endpoint conversion when the Route 53 provider is enabled.")
	flag.StringVar(&route53ControllerName, "route53-controller-name", route53ControllerName, "ZoneClass.spec.controllerName handled by the Route 53 controller.")
	flag.StringVar(&route53ProviderName, "route53-provider-name", route53ProviderName, "Provider.metadata.name handled by the Route 53 controller.")
	flag.StringVar(&route53ProviderVersion, "route53-provider-version", route53ProviderVersion, "Provider version handled by the Route 53 controller.")
	flag.StringVar(&cloudflareControllerName, "cloudflare-controller-name", cloudflareControllerName, "ZoneClass.spec.controllerName handled by the Cloudflare controller.")
	flag.StringVar(&cloudflareProviderName, "cloudflare-provider-name", cloudflareProviderName, "Provider.metadata.name handled by the Cloudflare controller.")
	flag.StringVar(&cloudflareProviderVersion, "cloudflare-provider-version", cloudflareProviderVersion, "Provider version handled by the Cloudflare controller.")
	flag.StringVar(&externalControllerName, "external-controller-name", externalControllerName, "ZoneClass.spec.controllerName handled by the external controller.")
	flag.StringVar(&externalProviderName, "external-provider-name", externalProviderName, "Provider.metadata.name handled by the external controller.")
	flag.StringVar(&externalProviderVersion, "external-provider-version", externalProviderVersion, "Provider version handled by the external controller.")
	flag.Float64Var(&externalDNSQPS, "external-dns-qps", externalprobe.DefaultQPS, "Maximum external DNS query rate per controller process.")
	flag.IntVar(&externalDNSBurst, "external-dns-burst", externalprobe.DefaultBurst, "Maximum external DNS query burst per controller process.")
	flag.BoolVar(&hostedControllerEnabled, "hosted-controller-enabled", hostedControllerEnabled, "Enable hosted authoritative DNS controllers and endpoint conversion.")
	flag.StringVar(&hostedControllerName, "hosted-controller-name", hostedControllerName, "ZoneClass.spec.controllerName handled by the hosted controller.")
	flag.StringVar(&hostedProviderName, "hosted-provider-name", hostedProviderName, "Provider.metadata.name handled by the hosted controller.")
	flag.StringVar(&hostedProviderVersion, "hosted-provider-version", hostedProviderVersion, "Provider version handled by the hosted controller.")
	flag.UintVar(&hostedEndpointTTL, "hosted-endpoint-ttl", hostedEndpointTTL, "TTL in seconds for hosted endpoint-converted records (1-86400).")

	zapOpts := zap.Options{Development: true}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))
	if hostedControllerEnabled && (hostedEndpointTTL < 1 || hostedEndpointTTL > 86400) {
		ctrl.Log.Error(nil, "hosted endpoint TTL must be between 1 and 86400 seconds")
		os.Exit(1)
	}

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

	if route53ControllerEnabled {
		if err := (&route53identity.IdentityReconciler{
			Client:   mgr.GetClient(),
			Resolver: route53identity.NewAWSIdentityResolver(),
			Recorder: mgr.GetEventRecorderFor("route53-identity-controller"),
		}).SetupWithManager(mgr); err != nil {
			ctrl.Log.Error(err, "unable to set up Route 53 Identity controller")
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
	}

	if cloudflareControllerEnabled {
		if err := (&cloudflareidentity.IdentityReconciler{
			Client:   mgr.GetClient(),
			Resolver: cloudflareidentity.NewCloudflareAPIClient(),
			Recorder: mgr.GetEventRecorderFor("cloudflare-identity-controller"),
		}).SetupWithManager(mgr); err != nil {
			ctrl.Log.Error(err, "unable to set up Cloudflare Identity controller")
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
	}

	if externalControllerEnabled {
		if err := (&externalzoneclass.ZoneClassReconciler{
			Client:          mgr.GetClient(),
			ControllerName:  externalControllerName,
			ProviderName:    externalProviderName,
			ProviderVersion: externalProviderVersion,
		}).SetupWithManager(mgr); err != nil {
			ctrl.Log.Error(err, "unable to set up external ZoneClass controller")
			os.Exit(1)
		}

		// NewReconciler supplies the wire resolver, its effective pod DNS policy,
		// time.Now, and random jitter when their injection hooks are unset.
		externalReconciler, err := externalzoneunit.NewReconciler(mgr.GetClient(), externalzoneunit.Options{
			ControllerName:  externalControllerName,
			ProviderName:    externalProviderName,
			ProviderVersion: externalProviderVersion,
			QPS:             externalDNSQPS,
			Burst:           externalDNSBurst,
		})
		if err != nil {
			ctrl.Log.Error(err, "unable to configure external DNS resolver")
			os.Exit(1)
		}
		if err := externalReconciler.SetupWithManager(mgr); err != nil {
			ctrl.Log.Error(err, "unable to set up external ZoneUnit controller")
			os.Exit(1)
		}
	}

	if hostedControllerEnabled {
		if err := (&hostedzoneclass.ZoneClassReconciler{
			Client:          mgr.GetClient(),
			ControllerName:  hostedControllerName,
			ProviderName:    hostedProviderName,
			ProviderVersion: hostedProviderVersion,
		}).SetupWithManager(mgr); err != nil {
			ctrl.Log.Error(err, "unable to set up hosted ZoneClass controller")
			os.Exit(1)
		}

		hostedReconciler, err := hostedzoneunit.NewReconciler(mgr.GetClient(), hostedzoneunit.Options{
			ControllerName:  hostedControllerName,
			ProviderName:    hostedProviderName,
			ProviderVersion: hostedProviderVersion,
			DefaultViews: []string{envOrDefault("HOSTED_SERVER_ADDRESS",
				"dns-api-hosted-server."+envOrDefault("POD_NAMESPACE", "dns-api-system")+".svc:53")},
		})
		if err != nil {
			ctrl.Log.Error(err, "unable to configure hosted DNS resolver")
			os.Exit(1)
		}
		if err := hostedReconciler.SetupWithManager(mgr); err != nil {
			ctrl.Log.Error(err, "unable to set up hosted ZoneUnit controller")
			os.Exit(1)
		}
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

	if gatewayEndpointControllerEnabled {
		if err := (&gatewayendpointcontroller.Reconciler{
			Client:                     mgr.GetClient(),
			Scheme:                     mgr.GetScheme(),
			EndpointRecordSetNamespace: endpointRecordSetNamespace,
		}).SetupWithManager(mgr); err != nil {
			ctrl.Log.Error(err, "unable to set up Gateway Endpoint controller")
			os.Exit(1)
		}
	}

	if serviceEndpointControllerEnabled {
		if err := (&serviceendpointcontroller.Reconciler{
			Client:                     mgr.GetClient(),
			Scheme:                     mgr.GetScheme(),
			EndpointRecordSetNamespace: endpointRecordSetNamespace,
		}).SetupWithManager(mgr); err != nil {
			ctrl.Log.Error(err, "unable to set up Service Endpoint controller")
			os.Exit(1)
		}
	}

	if err := corewebhook.SetupCoreValidationWebhookWithManager(mgr); err != nil {
		ctrl.Log.Error(err, "unable to set up core validation webhook")
		os.Exit(1)
	}
	if route53ControllerEnabled {
		if err := route53webhook.SetupValidationWebhookWithManager(mgr); err != nil {
			ctrl.Log.Error(err, "unable to set up Route 53 validation webhook")
			os.Exit(1)
		}
		if route53EndpointConversionEnabled {
			route53conversion.NewHandler().Register(mgr.GetWebhookServer())
		}
	}
	if cloudflareControllerEnabled {
		if err := cloudflarewebhook.SetupValidationWebhookWithManager(mgr); err != nil {
			ctrl.Log.Error(err, "unable to set up Cloudflare validation webhook")
			os.Exit(1)
		}
	}
	if externalControllerEnabled {
		externalconversion.NewHandler().Register(mgr.GetWebhookServer())
	}
	if hostedControllerEnabled {
		hostedconversion.NewHandler(uint32(hostedEndpointTTL)).Register(mgr.GetWebhookServer())
	}

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

func envBoolOrDefault(name string, fallback bool) bool {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	enabled, err := strconv.ParseBool(value)
	if err != nil {
		ctrl.Log.Error(err, "invalid boolean environment variable", "name", name)
		os.Exit(1)
	}
	return enabled
}

func envUintOrDefault(name string, fallback uint) uint {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		ctrl.Log.Error(err, "invalid unsigned integer environment variable", "name", name)
		os.Exit(1)
	}
	return uint(parsed)
}
