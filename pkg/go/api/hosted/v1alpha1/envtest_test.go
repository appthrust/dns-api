package v1alpha1

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// The in-process schema validator does not apply the apiserver's
// format-aware bound check; a uint32 serial generated with format int32 and
// maximum 4294967295 was rejected live ("Maximum boundary value must be of
// type integer with format int32"). Admit the full serial range through a
// real apiserver.
func TestHostedZoneSerialRangeAdmittedByAPIServer(t *testing.T) {
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	if assets == "" {
		t.Skip("KUBEBUILDER_ASSETS is required for the real CRD admission test")
	}
	environment := &envtest.Environment{
		BinaryAssetsDirectory:    assets,
		CRDDirectoryPaths:        []string{filepath.Join("..", "..", "..", "..", "..", "app", "operator", "config", "crd", "bases")},
		ErrorIfCRDPathMissing:    true,
		ControlPlaneStartTimeout: 30 * time.Second,
		ControlPlaneStopTimeout:  15 * time.Second,
	}
	t.Cleanup(func() {
		if err := environment.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})
	cfg, err := environment.Start()
	if err != nil {
		t.Fatal(err)
	}
	s := runtime.NewScheme()
	if err := scheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(cfg, client.Options{Scheme: s})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "dns"}}); err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, serial := range []uint32{1, 2147483648, ^uint32(0)} {
		zone := &HostedZone{
			ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "dns"},
			Spec: HostedZoneSpec{
				DomainName:    "example.test",
				Serial:        serial,
				SOA:           SOA{MName: "ns.example.test", RName: "hostmaster.example.test", Refresh: 3600, Retry: 900, Expire: 1209600, Minimum: 300},
				NameServers:   []NameServer{{Name: "ns.example.test", Addresses: []string{"192.0.2.53"}}},
				Records:       []HostedZoneRecord{{Name: "@", Type: "A", TTL: 60, Values: []string{"192.0.2.1"}}},
				ContentDigest: digest,
			},
		}
		if err := c.Create(ctx, zone); err != nil {
			t.Fatalf("serial %d rejected: %v", serial, err)
		}
		zone.Status.Servers = []HostedZoneServerStatus{{Endpoint: "dns/server", Serial: serial, ContentDigest: digest, ObservedAt: metav1.Now()}}
		if err := c.Status().Update(ctx, zone); err != nil {
			t.Fatalf("status serial %d rejected: %v", serial, err)
		}
		if err := c.Delete(ctx, zone); err != nil {
			t.Fatal(err)
		}
	}
}
