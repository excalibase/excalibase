package main

import (
	"slices"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/imagedigest"
)

// EXC-732: the platform may read image volumes from its own registry at its in-cluster address, and
// from the operator's local registries, and from nowhere else on a private address.
func TestImageRegistryRoutes(t *testing.T) {
	cfg := config.AppConfig{
		AppRegistry: config.AppRegistryConfig{
			Host: "registry.example.com", Aliases: []string{"app-registry.ns.svc.cluster.local:5000"}, URL: "http://app-registry.ns.svc:5000",
		},
		AppLocalRegistries: []string{"localhost:5000", "registry.lan"},
	}
	want := []imagedigest.RegistryRoute{
		{Name: "registry.example.com", Endpoint: "app-registry.ns.svc:5000", PlainHTTP: true},
		{Name: "app-registry.ns.svc.cluster.local:5000", Endpoint: "app-registry.ns.svc:5000", PlainHTTP: true},
		{Name: "localhost:5000", Endpoint: "localhost:5000", PlainHTTP: true},
		{Name: "registry.lan", Endpoint: "registry.lan:443", PlainHTTP: false},
	}
	if got := imageRegistryRoutes(cfg); !slices.Equal(got, want) {
		t.Fatalf("routes\n got %+v\nwant %+v", got, want)
	}
}

func TestNoRegistriesConfiguredMeansNoRoutes(t *testing.T) {
	if got := imageRegistryRoutes(config.AppConfig{}); len(got) != 0 {
		t.Fatalf("routes %+v, want none", got)
	}
}

func TestAnHTTPSInternalRegistryIsReachedOverHTTPS(t *testing.T) {
	cfg := config.AppConfig{AppRegistry: config.AppRegistryConfig{Host: "registry.example.com", URL: "https://zot.ns.svc"}}
	got := imageRegistryRoutes(cfg)
	if len(got) != 1 || got[0].PlainHTTP || got[0].Endpoint != "zot.ns.svc:443" {
		t.Fatalf("routes %+v, want https on 443", got)
	}
}
