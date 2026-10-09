// Command engine-proxy serves provisioning the slice of the Docker or Podman
// API it needs, so provisioning never holds the engine socket itself.
//
//	ENGINE_SOCKET         the engine's API socket (default /var/run/docker.sock)
//	LISTEN_ADDR           where provisioning connects (default :2375)
//	PROXY_NETWORKS        networks containers may join (required, comma-separated)
//	PROXY_NETWORK_PREFIX  per-project networks containers may join (optional)
//	PROXY_VOLUME_PREFIX   named volumes containers may mount (optional; none without it)
//	PROXY_PORT_BIND_IPS   host addresses ports may publish on (default 127.0.0.1)
//	PROXY_RUNTIMES        OCI runtimes containers may ask for (optional, e.g. runsc)
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/engineproxy"
)

// containerPrefix is the name every provisioned container starts with.
const containerPrefix = "excalibase-"

type config struct {
	socket string
	listen string
	policy engineproxy.Policy
}

func main() {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		log.Fatalf("engine proxy: %v", err)
	}
	if _, err := os.Stat(cfg.socket); err != nil {
		log.Fatalf("engine proxy: engine socket: %v", err)
	}
	dial := func(ctx context.Context) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", cfg.socket)
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) },
	}
	target := &url.URL{Scheme: "http", Host: "engine"}
	server := &http.Server{
		Addr:              cfg.listen,
		Handler:           engineproxy.NewHandler(cfg.policy, target, transport).WithUpgradeDial(dial),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("engine proxy: %s -> %s, networks %v prefix %q", cfg.listen, cfg.socket, cfg.policy.Networks, cfg.policy.NetworkPrefix)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("engine proxy: %v", err)
	}
}

func loadConfig(getenv func(string) string) (config, error) {
	networks := splitList(getenv("PROXY_NETWORKS"))
	if len(networks) == 0 {
		return config{}, errors.New("PROXY_NETWORKS is required: the networks provisioned containers may join")
	}
	networkPrefix, err := scopedPrefix(getenv, "PROXY_NETWORK_PREFIX")
	if err != nil {
		return config{}, err
	}
	volumePrefix, err := scopedPrefix(getenv, "PROXY_VOLUME_PREFIX")
	if err != nil {
		return config{}, err
	}
	bindIPs := splitList(getenv("PROXY_PORT_BIND_IPS"))
	if len(bindIPs) == 0 {
		bindIPs = []string{"127.0.0.1"}
	}
	return config{
		socket: envOr(getenv, "ENGINE_SOCKET", "/var/run/docker.sock"),
		listen: envOr(getenv, "LISTEN_ADDR", ":2375"),
		policy: engineproxy.Policy{
			ManagedLabel:    engineproxy.ManagedLabel,
			Networks:        networks,
			NetworkPrefix:   networkPrefix,
			VolumePrefix:    volumePrefix,
			PortBindIPs:     bindIPs,
			Runtimes:        splitList(getenv("PROXY_RUNTIMES")),
			ContainerPrefix: containerPrefix,
		},
	}, nil
}

// scopedPrefix reads a per-project name prefix. It must extend the container
// prefix past a hyphen-ended segment other than "platform", so it cannot match
// the platform's own networks or volumes.
func scopedPrefix(getenv func(string) string, key string) (string, error) {
	prefix := getenv(key)
	if prefix == "" {
		return "", nil
	}
	if !strings.HasPrefix(prefix, containerPrefix) || len(prefix) <= len(containerPrefix) ||
		!strings.HasSuffix(prefix, "-") || strings.HasPrefix(prefix, containerPrefix+"platform") {
		return "", fmt.Errorf("%s %q must extend %q with a project segment ending in '-'", key, prefix, containerPrefix)
	}
	return prefix, nil
}

func envOr(getenv func(string) string, key, fallback string) string {
	if value := getenv(key); value != "" {
		return value
	}
	return fallback
}

func splitList(raw string) []string {
	var out []string
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
