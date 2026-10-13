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
//	PROXY_EDGE_CONTAINER  the edge, the one unmanaged container that may join project networks (optional)
//	PROXY_NETNS_JOIN_IMAGES digest-pinned images that may join a managed
//	                      container's network namespace (optional; the DocumentDB gateway)
package main

import (
	"context"
	"errors"
	"fmt"
	"k8s.io/apimachinery/pkg/util/validation"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/engineproxy"
)

// containerPrefix is the name every provisioned container starts with.
const containerPrefix = "excalibase-"

var containerName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

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
	edge := getenv("PROXY_EDGE_CONTAINER")
	if edge != "" && !containerName.MatchString(edge) {
		return config{}, fmt.Errorf("PROXY_EDGE_CONTAINER %q is not a container name", edge)
	}
	aliases, err := edgeAliases(getenv("PROXY_EDGE_ALIASES"), edge)
	if err != nil {
		return config{}, err
	}
	joinImages, err := digestPinned(getenv, "PROXY_NETNS_JOIN_IMAGES")
	if err != nil {
		return config{}, err
	}
	cdcNetwork, cdcImages, err := busNetwork(getenv, networks, networkPrefix)
	if err != nil {
		return config{}, err
	}
	hostPorts, err := hostPortRange(getenv("PROXY_HOST_PORTS"))
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
			HostPorts:       hostPorts,
			Runtimes:        splitList(getenv("PROXY_RUNTIMES")),
			ContainerPrefix: containerPrefix,
			EdgeContainer:   edge,
			EdgeAliases:     aliases,
			NetnsJoinImages: joinImages,
			CDCNetwork:      cdcNetwork,
			CDCImages:       cdcImages,
		},
	}, nil
}

// busNetwork reads the network CDC watchers reach NATS on (EXC-612) and the
// digest-pinned images admitted to it; one without the other is refused, and
// so is a network every container may join.
func busNetwork(getenv func(string) string, networks []string, networkPrefix string) (string, []string, error) {
	network := strings.TrimSpace(getenv("PROXY_CDC_NETWORK"))
	images, err := digestPinned(getenv, "PROXY_CDC_IMAGES")
	if err != nil {
		return "", nil, err
	}
	if (network == "") != (len(images) == 0) {
		return "", nil, errors.New("PROXY_CDC_NETWORK and PROXY_CDC_IMAGES go together: the bus network admits only the watcher image")
	}
	if slices.Contains(networks, network) || (network != "" && networkPrefix != "" && strings.HasPrefix(network, networkPrefix)) {
		return "", nil, fmt.Errorf("PROXY_CDC_NETWORK %q is one every container may join (PROXY_NETWORKS or PROXY_NETWORK_PREFIX)", network)
	}
	return network, images, nil
}

// edgeAliases reads the hostnames the edge may answer to on project networks; they need the edge.
func edgeAliases(raw, edge string) ([]string, error) {
	aliases := splitList(raw)
	if len(aliases) > 0 && edge == "" {
		return nil, errors.New("PROXY_EDGE_ALIASES needs PROXY_EDGE_CONTAINER: only the edge may take a name")
	}
	for _, alias := range aliases {
		if problems := validation.IsDNS1123Subdomain(alias); len(problems) > 0 {
			return nil, fmt.Errorf("PROXY_EDGE_ALIASES: %q is not a hostname: %s", alias, strings.Join(problems, "; "))
		}
	}
	return aliases, nil
}

// hostPortRange reads "low-high", the host ports tenants may be published
// on by number; unset admits none. Privileged ports are never admitted.
func hostPortRange(raw string) (engineproxy.HostPortRange, error) {
	if strings.TrimSpace(raw) == "" {
		return engineproxy.HostPortRange{}, nil
	}
	lowRaw, highRaw, found := strings.Cut(raw, "-")
	low, lowErr := strconv.Atoi(strings.TrimSpace(lowRaw))
	high, highErr := strconv.Atoi(strings.TrimSpace(highRaw))
	if !found || lowErr != nil || highErr != nil || low < 1024 || high > 65535 || high < low {
		return engineproxy.HostPortRange{}, fmt.Errorf("PROXY_HOST_PORTS %q is not a range of unprivileged ports like 30000-30999", raw)
	}
	return engineproxy.HostPortRange{Low: low, High: high}, nil
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

// pinnedImage is a registry-qualified image reference fixed by its digest.
var pinnedImage = regexp.MustCompile(`^[a-z0-9.-]+(:[0-9]+)?/[^@\s:]+@sha256:[0-9a-f]{64}$`)

// digestPinned reads a list of images, each pinned by digest: a tag would let
// the bits that are admitted change without the setting changing.
func digestPinned(getenv func(string) string, key string) ([]string, error) {
	images := splitList(getenv(key))
	for _, image := range images {
		if !pinnedImage.MatchString(image) {
			return nil, fmt.Errorf("%s: %q must be a registry-qualified image pinned by digest (name@sha256:...)", key, image)
		}
	}
	return images, nil
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
