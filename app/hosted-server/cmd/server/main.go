package main

import (
	"flag"
	"os"

	"github.com/appthrust/dns-api/internal/go/providers/hosted/server"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

func main() {
	options := server.Options{}
	flag.StringVar(&options.ListenAddr, "listen-address", ":5353", "Address for authoritative DNS over UDP and TCP.")
	flag.StringVar(&options.WatchNamespace, "watch-namespace", "", "Namespace containing HostedZone snapshots; empty watches all namespaces.")
	flag.IntVar(&options.MaxZones, "max-zones", 4096, "Maximum number of served zones.")
	flag.IntVar(&options.UDPRateLimitPerSource, "udp-rate-limit", 1000, "Maximum UDP queries per second per source address.")
	flag.StringVar(&options.MetricsAddr, "metrics-bind-address", ":8080", "Metrics listener address; 0 disables the listener.")
	flag.StringVar(&options.HealthAddr, "health-probe-bind-address", ":8081", "Health listener address; 0 disables the listener.")
	zapOptions := zap.Options{}
	zapOptions.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOptions)))

	if err := server.Run(ctrl.SetupSignalHandler(), options); err != nil {
		ctrl.Log.Error(err, "problem running hosted DNS server")
		os.Exit(1)
	}
}
