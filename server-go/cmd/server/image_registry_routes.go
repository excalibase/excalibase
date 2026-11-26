package main

import (
	"net"
	"net/url"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/imagedigest"
)

// imageRegistryRoutes are the registries on private addresses the platform may read an image's
// volumes from, and only those (EXC-732): its own git-build registry, reached at its in-cluster
// URL whatever name an image uses for it, and the operator's APP_LOCAL_REGISTRIES. A local
// registry speaks plain HTTP when it is on localhost or names a port other than 443.
func imageRegistryRoutes(cfg config.AppConfig) []imagedigest.RegistryRoute {
	var routes []imagedigest.RegistryRoute
	if endpoint, plain, ok := internalEndpoint(cfg.AppRegistry.URL); ok {
		for _, name := range append([]string{cfg.AppRegistry.Host}, cfg.AppRegistry.Aliases...) {
			if name != "" {
				routes = append(routes, imagedigest.RegistryRoute{Name: name, Endpoint: endpoint, PlainHTTP: plain})
			}
		}
	}
	for _, host := range cfg.AppLocalRegistries {
		name, port, hasPort := strings.Cut(host, ":")
		plain := name == "localhost" || (hasPort && port != "443")
		endpoint := host
		if !hasPort {
			endpoint = net.JoinHostPort(name, "443")
		}
		routes = append(routes, imagedigest.RegistryRoute{Name: host, Endpoint: endpoint, PlainHTTP: plain})
	}
	return routes
}

// internalEndpoint is host:port of the registry's in-cluster URL and whether it is plain HTTP.
func internalEndpoint(raw string) (endpoint string, plain, ok bool) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return "", false, false
	}
	plain = parsed.Scheme == "http"
	port := parsed.Port()
	if port == "" {
		port = "443"
		if plain {
			port = "80"
		}
	}
	return net.JoinHostPort(parsed.Hostname(), port), plain, true
}
