// Command log-forwarder delivers projects' log batches to the destinations
// their Admins configured (EXC-658). It pulls batches from provisioning's
// internal listener and reaches destinations only through a dialer that
// refuses internal addresses; its NetworkPolicy allows nothing else.
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

	"github.com/excalibase/provisioning-poc/internal/logdrain"
	"github.com/excalibase/provisioning-poc/internal/publicaddr"
)

type config struct {
	provisioningURL string
	token           string
	deny            []netip.Prefix
}

func loadConfig(getenv func(string) string) (config, error) {
	cfg := config{provisioningURL: strings.TrimSpace(getenv("PROVISIONING_URL")), token: getenv("LOG_FORWARDER_TOKEN")}
	parsed, err := url.Parse(cfg.provisioningURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return config{}, errors.New("PROVISIONING_URL must be provisioning's internal http(s) URL")
	}
	if cfg.token == "" {
		return config{}, errors.New("LOG_FORWARDER_TOKEN is required")
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

func main() {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		log.Fatalf("log forwarder: %v", err)
	}
	// Provisioning is inside the cluster; only destinations go through the guard.
	planeClient := &http.Client{Timeout: 40 * time.Second, Transport: &http.Transport{Proxy: nil}}
	sender := logdrain.NewSender(logdrain.NewHTTPClient(publicaddr.NewDialer(nil, cfg.deny)))
	forwarder := logdrain.NewForwarder(logdrain.NewHTTPControlPlane(cfg.provisioningURL, cfg.token, planeClient), sender)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	log.Printf("log forwarder started")
	forwarder.Run(ctx)
}
