// Package server serves immutable HostedZone snapshots as authoritative DNS.
package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// Options configures the standalone authoritative DNS process. Zero values use
// the documented defaults. Set an HTTP address to "0" to disable that listener.
type Options struct {
	ListenAddr            string
	WatchNamespace        string
	MaxZones              int
	UDPRateLimitPerSource int
	// PodIdentity defaults to POD_NAMESPACE + "/" + POD_NAME. Both are required
	// when PodIdentity is not explicitly supplied.
	PodIdentity string
	MetricsAddr string
	HealthAddr  string
	// RESTConfig defaults to the in-cluster ServiceAccount configuration.
	RESTConfig *rest.Config
}

func (o Options) defaults() (Options, error) {
	if o.ListenAddr == "" {
		o.ListenAddr = ":5353"
	}
	if o.MaxZones == 0 {
		o.MaxZones = 4096
	}
	if o.UDPRateLimitPerSource == 0 {
		o.UDPRateLimitPerSource = 1000
	}
	if o.MetricsAddr == "" {
		o.MetricsAddr = ":8080"
	}
	if o.HealthAddr == "" {
		o.HealthAddr = ":8081"
	}
	if o.PodIdentity == "" {
		o.PodIdentity = os.Getenv("POD_NAMESPACE") + "/" + os.Getenv("POD_NAME")
	}
	identity := strings.Split(o.PodIdentity, "/")
	if len(identity) != 2 || identity[0] == "" || identity[1] == "" {
		return o, errors.New("pod identity must be namespace/name (set POD_NAMESPACE and POD_NAME)")
	}
	if o.MaxZones < 1 {
		return o, errors.New("max zones must be positive")
	}
	if o.UDPRateLimitPerSource < 1 {
		return o, errors.New("UDP rate limit per source must be positive")
	}
	return o, nil
}

// Run serves UDP/TCP DNS, watches HostedZone snapshots, and exposes metrics and
// health endpoints until ctx is canceled. Cancellation is a graceful success;
// startup/listener failures are returned. Shutdown removes only this pod's SSA
// status contribution, with a bounded independent cleanup context.
func Run(ctx context.Context, options Options) error {
	options, err := options.defaults()
	if err != nil {
		return err
	}
	config := options.RESTConfig
	if config == nil {
		config, err = rest.InClusterConfig()
		if err != nil {
			return fmt.Errorf("load hosted server Kubernetes configuration: %w", err)
		}
	}
	client, err := dynamic.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("create hosted snapshot client: %w", err)
	}
	metrics := newMetrics()
	index := newZoneIndex(options.MaxZones, metrics)
	watcher, err := newSnapshotWatcher(client, options.WatchNamespace, options.PodIdentity, index)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	tcp, err := (&net.ListenConfig{}).Listen(ctx, "tcp", options.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen hosted DNS TCP: %w", err)
	}
	defer tcp.Close()
	udp, err := (&net.ListenConfig{}).ListenPacket(ctx, "udp", tcp.Addr().String())
	if err != nil {
		return fmt.Errorf("listen hosted DNS UDP: %w", err)
	}
	defer udp.Close()
	h := &handler{index: index, limiter: newSourceLimiter(options.UDPRateLimitPerSource), metrics: metrics}
	started := make(chan struct{}, 2)
	servers := []*dns.Server{
		{Listener: tcp, Handler: h, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, NotifyStartedFunc: func() { started <- struct{}{} }},
		{PacketConn: udp, Handler: h, UDPSize: udpPayloadSize, NotifyStartedFunc: func() { started <- struct{}{} }},
	}
	type httpListener struct {
		server   *http.Server
		listener net.Listener
	}
	var httpListeners []httpListener
	defer func() {
		for _, listener := range httpListeners {
			_ = listener.listener.Close()
		}
	}()
	for _, endpoint := range []struct {
		address string
		handler http.Handler
	}{
		{options.MetricsAddr, promhttp.HandlerFor(metrics.registry, promhttp.HandlerOpts{})},
		{options.HealthAddr, healthHandler(&watcher.ready)},
	} {
		if endpoint.address == "0" {
			continue
		}
		listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", endpoint.address)
		if err != nil {
			return fmt.Errorf("listen hosted HTTP %s: %w", endpoint.address, err)
		}
		httpListeners = append(httpListeners, httpListener{server: &http.Server{Handler: endpoint.handler, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}, listener: listener})
	}
	failures := make(chan error, 4)
	var workers sync.WaitGroup
	defer func() {
		cancel()
		watcher.ready.Store(false)
		shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		for _, server := range servers {
			_ = server.ShutdownContext(shutdown)
		}
		_ = tcp.Close()
		_ = udp.Close()
		for _, listener := range httpListeners {
			_ = listener.server.Shutdown(shutdown)
			_ = listener.server.Close()
		}
		workers.Wait()
		cleanup, finish := context.WithTimeout(context.Background(), 5*time.Second)
		defer finish()
		watcher.removeOwnStatus(cleanup)
	}()
	for _, server := range servers {
		workers.Go(func() { failures <- server.ActivateAndServe() })
	}
	for _, listener := range httpListeners {
		workers.Go(func() { failures <- listener.server.Serve(listener.listener) })
	}
	for range servers {
		select {
		case <-started:
		case <-ctx.Done():
			return nil
		case err := <-failures:
			return fmt.Errorf("start hosted listener: %w", err)
		}
	}
	// Never publish a serving acknowledgement before both DNS transports listen.
	workers.Go(func() { watcher.run(ctx) })
	select {
	case <-ctx.Done():
		return nil
	case err := <-failures:
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("hosted listener stopped: %w", err)
	}
}

func healthHandler(ready *atomic.Bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(writer http.ResponseWriter, _ *http.Request) { _, _ = writer.Write([]byte("ok\n")) })
	mux.HandleFunc("/readyz", func(writer http.ResponseWriter, _ *http.Request) {
		if !ready.Load() {
			http.Error(writer, "initial snapshot list has not completed", http.StatusServiceUnavailable)
			return
		}
		_, _ = writer.Write([]byte("ok\n"))
	})
	return mux
}
