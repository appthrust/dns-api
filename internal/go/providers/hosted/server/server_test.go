package server

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	hostedv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/hosted/v1alpha1"
	"github.com/miekg/dns"
	"k8s.io/client-go/rest"
)

func TestRunServesAndCleansUpOwnStatus(t *testing.T) {
	zone := testZone()
	zone.APIVersion = hostedv1alpha1.SchemeGroupVersion.String()
	zone.Kind = "HostedZone"
	zone.ResourceVersion = "1"
	applies := make(chan hostedv1alpha1.HostedZone, 10)
	api := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Path != "/apis/hosted.dns.appthrust.io/v1alpha1/namespaces/dns/hostedzones" && request.URL.Path != "/apis/hosted.dns.appthrust.io/v1alpha1/namespaces/dns/hostedzones/example/status" {
			t.Errorf("unexpected API path %s", request.URL.Path)
			http.NotFound(writer, request)
			return
		}
		if request.Method == http.MethodPatch {
			if request.URL.Query().Get("fieldManager") != "hosted-server/server-1" || request.Header.Get("Content-Type") != "application/apply-patch+yaml" {
				t.Errorf("incorrect SSA request: %s %v", request.URL, request.Header)
			}
			var patch hostedv1alpha1.HostedZone
			if err := json.NewDecoder(request.Body).Decode(&patch); err != nil {
				t.Error(err)
			}
			applies <- patch
			_ = json.NewEncoder(writer).Encode(patch)
			return
		}
		if request.URL.Query().Get("watch") == "true" {
			writer.WriteHeader(http.StatusOK)
			if request.URL.Query().Get("sendInitialEvents") == "true" {
				_ = json.NewEncoder(writer).Encode(map[string]any{"type": "ADDED", "object": zone})
				_ = json.NewEncoder(writer).Encode(map[string]any{"type": "BOOKMARK", "object": map[string]any{"apiVersion": zone.APIVersion, "kind": zone.Kind, "metadata": map[string]any{"resourceVersion": "1", "annotations": map[string]string{"k8s.io/initial-events-end": "true"}}}})
			}
			writer.(http.Flusher).Flush()
			<-request.Context().Done()
			return
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"apiVersion": zone.APIVersion, "kind": "HostedZoneList", "metadata": map[string]string{"resourceVersion": "1"}, "items": []*hostedv1alpha1.HostedZone{zone}})
	}))
	defer api.Close()
	listen, health, metrics := availableAddress(t), availableAddress(t), availableAddress(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- Run(ctx, Options{ListenAddr: listen, WatchNamespace: "dns", PodIdentity: "dns/server-1", MetricsAddr: metrics, HealthAddr: health, RESTConfig: &rest.Config{Host: api.URL}})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-result:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(7 * time.Second):
			t.Error("Run did not finish")
		}
	})
	select {
	case patch := <-applies:
		if len(patch.Status.Servers) != 1 || patch.Status.Servers[0].Serial != 7 {
			t.Fatalf("initial status = %#v", patch.Status)
		}
	case err := <-result:
		result <- err
		t.Fatalf("Run stopped early: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not load initial snapshot")
	}
	for _, network := range []string{"udp", "tcp"} {
		request := new(dns.Msg)
		request.SetQuestion("WwW.Example.Test.", dns.TypeA)
		response, _, err := (&dns.Client{Net: network, Timeout: time.Second}).Exchange(request, listen)
		if err != nil {
			t.Fatal(err)
		}
		if len(response.Answer) != 1 || response.Answer[0].(*dns.A).A.String() != "192.0.2.1" || !response.Authoritative || response.Answer[0].Header().Name != request.Question[0].Name {
			t.Fatalf("%s response = %v", network, response)
		}
	}
	for _, path := range []string{"/healthz", "/readyz"} {
		response, err := http.Get("http://" + health + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d", path, response.StatusCode)
		}
	}
	response, err := http.Get("http://" + metrics + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dns_hosted_queries_total", "dns_hosted_query_duration_seconds", "dns_hosted_zone_serial", "dns_hosted_zones_loaded", "dns_hosted_udp_rate_limited_total", "dns_hosted_snapshots_rejected_total"} {
		if !strings.Contains(string(body), name) {
			t.Fatalf("missing metric %s", name)
		}
	}
	cancel()
	select {
	case patch := <-applies:
		if len(patch.Status.Servers) != 0 || patch.UID != zone.UID {
			t.Fatalf("shutdown apply = %#v", patch)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not remove own acknowledgement")
	}
}

func availableAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}
