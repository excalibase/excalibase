package imagedigest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/publicaddr"
)

// countingServer is a registry on a loopback address, which the platform may not dial unless told to.
func countingServer(t *testing.T, registry *blobRegistry) (hostPort string, dialled *atomic.Int32) {
	t.Helper()
	dialled = &atomic.Int32{}
	server := httptest.NewServer(registry)
	t.Cleanup(server.Close)
	server.Config.Handler = countHandler{next: server.Config.Handler, hits: dialled}
	return strings.TrimPrefix(server.URL, "http://"), dialled
}

type countHandler struct {
	next interface {
		ServeHTTP(http.ResponseWriter, *http.Request)
	}
	hits *atomic.Int32
}

func (c countHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.hits.Add(1)
	c.next.ServeHTTP(w, r)
}

func volumesRegistry(t *testing.T) *blobRegistry {
	t.Helper()
	registry := newBlobRegistry()
	imageWithVolumes(t, registry, false, "/var/lib/app")
	return registry
}

// EXC-732: the platform's own git-build registry is reached at its in-cluster address, though
// images name it by its public host, and over plain HTTP where it is configured that way.
func TestVolumesReadsThePlatformsInternalRegistry(t *testing.T) {
	address, dialled := countingServer(t, volumesRegistry(t))
	resolver := NewResolverWithRoutes(nil, []RegistryRoute{{Name: "registry.internal.test", Endpoint: address, PlainHTTP: true}})

	got, _, err := resolver.Volumes(context.Background(), "registry.internal.test/acme/web:v1", nil)
	if err != nil || !slices.Equal(got, []string{"/var/lib/app"}) {
		t.Fatalf("volumes %v (%v), want /var/lib/app", got, err)
	}
	if dialled.Load() == 0 {
		t.Fatal("the registry at its endpoint was never asked")
	}
}

func TestVolumesReadsAnOperatorsLocalRegistry(t *testing.T) {
	address, _ := countingServer(t, volumesRegistry(t))
	resolver := NewResolverWithRoutes(nil, []RegistryRoute{{Name: address, Endpoint: address, PlainHTTP: true}})

	got, _, err := resolver.Volumes(context.Background(), address+"/acme/web:v1", nil)
	if err != nil || !slices.Equal(got, []string{"/var/lib/app"}) {
		t.Fatalf("volumes %v (%v), want /var/lib/app", got, err)
	}
}

// Only the configured hosts are dialled: any other private address is still refused, and so is a
// host that merely sits beside a configured one.
func TestVolumesStillRefusesAnyOtherPrivateAddress(t *testing.T) {
	address, dialled := countingServer(t, volumesRegistry(t))
	configured := NewResolverWithRoutes(nil, []RegistryRoute{{Name: "registry.internal.test", Endpoint: "10.9.9.9:5000", PlainHTTP: true}})
	for name, resolver := range map[string]*Resolver{"routes for other hosts": configured, "no routes": NewResolver(nil)} {
		_, _, err := resolver.Volumes(context.Background(), address+"/acme/web:v1", nil)
		if !errors.Is(err, ErrNotPublic) {
			t.Errorf("%s: %v, want ErrNotPublic", name, err)
		}
	}
	if dialled.Load() != 0 {
		t.Fatalf("a private address that is not configured was dialled %d times", dialled.Load())
	}
}

// The internal client dials a configured endpoint and nothing else, whatever a redirect or a token realm names.
func TestTheInternalClientDialsOnlyTheConfiguredEndpoints(t *testing.T) {
	address, dialled := countingServer(t, volumesRegistry(t))
	resolver := NewResolverWithRoutes(nil, []RegistryRoute{{Name: "registry.internal.test", Endpoint: "10.9.9.9:5000", PlainHTTP: true}})
	response, err := resolver.internal.Get("http://" + address + "/v2/")
	if err == nil {
		response.Body.Close()
	}
	if !errors.Is(err, publicaddr.ErrInternalAddress) || dialled.Load() != 0 {
		t.Fatalf("dialled an address that is not configured: %v (%d requests)", err, dialled.Load())
	}
}
