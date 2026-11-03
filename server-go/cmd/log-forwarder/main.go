// Command log-forwarder delivers projects' log batches to the destinations
// their Admins configured (EXC-658). It pulls batches from provisioning's
// internal listener and reaches destinations only through a dialer that
// refuses internal addresses; on Kubernetes its NetworkPolicy allows nothing
// else, and on a single host the dialer also refuses the host's own addresses.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/excalibase/provisioning-poc/internal/hostaddr"
	"github.com/excalibase/provisioning-poc/internal/logdrain"
	"github.com/excalibase/provisioning-poc/internal/publicaddr"
)

// hostRefresh is how often the host's own addresses are read again.
const hostRefresh = 5 * time.Second

// ownFibTrie is the forwarder's own routing table, which the host's must not be.
const ownFibTrie = "/proc/self/net/fib_trie"

type config struct {
	provisioningURL string
	token           string
	deny            []netip.Prefix
	// hostTables are the host's /proc/net tables, mounted on a single host.
	hostTables hostaddr.Files
}

func loadConfig(getenv func(string) string) (config, error) {
	cfg := config{provisioningURL: strings.TrimSpace(getenv("PROVISIONING_URL")), token: getenv("LOG_FORWARDER_TOKEN"),
		hostTables: hostaddr.Files{FibTrie: getenv("LOG_FORWARDER_HOST_FIB_TRIE"), IfInet6: getenv("LOG_FORWARDER_HOST_IF_INET6")}}
	if cfg.hostTables.FibTrie != "" {
		cfg.hostTables.Self = ownFibTrie
	}
	parsed, err := url.Parse(cfg.provisioningURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return config{}, errors.New("PROVISIONING_URL must be provisioning's internal http(s) URL")
	}
	if cfg.token == "" {
		return config{}, errors.New("LOG_FORWARDER_TOKEN is required")
	}
	if cfg.hostTables.IfInet6 != "" && cfg.hostTables.FibTrie == "" {
		return config{}, errors.New("LOG_FORWARDER_HOST_IF_INET6 needs LOG_FORWARDER_HOST_FIB_TRIE")
	}
	for _, raw := range strings.Split(getenv("LOG_FORWARDER_EXTRA_DENY_CIDRS"), ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return config{}, fmt.Errorf("LOG_FORWARDER_EXTRA_DENY_CIDRS: %q is not a CIDR", raw)
		}
		cfg.deny = append(cfg.deny, prefix.Masked())
	}
	return cfg, nil
}

// newDestinationDialer refuses the configured ranges and, when the host's
// tables are mounted, the host's own addresses as last read; it fails rather
// than start without them.
func newDestinationDialer(cfg config) (*publicaddr.Dialer, *hostaddr.Source, error) {
	if cfg.hostTables.FibTrie == "" {
		return publicaddr.NewDialer(nil, cfg.deny), nil, nil
	}
	hosts, err := hostaddr.Load(cfg.hostTables)
	if err != nil {
		return nil, nil, fmt.Errorf("the host's own addresses: %w", err)
	}
	return publicaddr.NewDialer(nil, cfg.deny, publicaddr.WithDenySource(hosts.Prefixes)), hosts, nil
}

func main() {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		log.Fatalf("log forwarder: %v", err)
	}
	dialer, hosts, err := newDestinationDialer(cfg)
	if err != nil {
		log.Fatalf("log forwarder: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	if hosts != nil {
		go hosts.Run(ctx, hostRefresh)
		log.Printf("log forwarder: refusing the host's own %d addresses", len(hosts.Prefixes()))
	}
	// Provisioning is inside the cluster; only destinations go through the guard.
	planeClient := &http.Client{Timeout: 40 * time.Second, Transport: &http.Transport{Proxy: nil}}
	sender := logdrain.NewSender(logdrain.NewHTTPClient(dialer))
	forwarder := logdrain.NewForwarder(logdrain.NewHTTPControlPlane(cfg.provisioningURL, cfg.token, planeClient), sender)
	log.Printf("log forwarder started")
	forwarder.Run(ctx)
}
