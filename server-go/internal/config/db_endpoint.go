package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/publicaddr"
)

// Public database endpoints (EXC-410) share one address and take a port each.
// Everything an operator can get wrong about that — the window ports come
// from, how long a freed one is held back, the name customers dial — is read
// here and nowhere else, and a value that is present but unusable stops the
// server rather than being quietly replaced by a default.

const (
	// DefaultDBEndpointPortMin and DefaultDBEndpointPortMax bound the
	// default allocation window. A thousand ports is far more than the
	// resource limits of a cluster allow projects, which is the point: the
	// range must never be the thing that runs out, because exhausting it is
	// what would tempt a short quarantine.
	DefaultDBEndpointPortMin = 30000
	DefaultDBEndpointPortMax = 30999

	// DefaultDBEndpointPortQuarantine is how long a freed port is held back
	// before it can be handed to another tenant. A customer's application,
	// a saved connection in a GUI client or a forgotten cron job keeps
	// dialling a port for weeks after the project behind it is gone; if
	// that port has been reissued, those clients arrive at a stranger's
	// database and present credentials to it. Thirty days costs nothing.
	DefaultDBEndpointPortQuarantine = 30 * 24 * time.Hour

	// DefaultDBEndpointSharedIPKey is the value of MetalLB's
	// metallb.universe.tf/allow-shared-ip annotation every project's
	// Service carries. Services that agree on the key share one address,
	// which is what lets a port-per-project scheme live behind a single
	// public IPv4 with no edge process of our own to configure or reload.
	DefaultDBEndpointSharedIPKey = "excalibase-db-edge"
)

// parseDBEndpointPortRange reads "min-max". An empty value means "not set"
// and yields the documented default; anything present is parsed strictly.
func parseDBEndpointPortRange(raw string) (domain.PortRange, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return domain.NewPortRange(DefaultDBEndpointPortMin, DefaultDBEndpointPortMax)
	}
	parts := strings.Split(raw, "-")
	if len(parts) != 2 {
		return domain.PortRange{}, fmt.Errorf("%q is not a port range (e.g. 30000-30999)", raw)
	}
	min, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return domain.PortRange{}, fmt.Errorf("%q: lower bound is not a number", raw)
	}
	max, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return domain.PortRange{}, fmt.Errorf("%q: upper bound is not a number", raw)
	}
	return domain.NewPortRange(min, max)
}

// envDBEndpointPortRange refuses to start on a window that cannot be
// allocated from.
func envDBEndpointPortRange(key string) domain.PortRange {
	r, err := parseDBEndpointPortRange(os.Getenv(key))
	if err != nil {
		log.Fatalf("%s: %v", key, err)
	}
	return r
}

// parseDBEndpointDomain canonicalises the suffix customers' endpoint names
// hang off. Empty means the feature is not configured — the API then refuses
// to enable a public endpoint rather than inventing a name — and anything
// present must be a reachable public suffix, since a name that only resolves
// inside the cluster would be handed out as a connection string that cannot
// work.
func parseDBEndpointDomain(raw string) (string, error) {
	suffix := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
	if suffix == "" {
		return "", nil
	}
	if _, err := domain.DBEndpointHost("proj-validation", suffix); err != nil {
		return "", err
	}
	if err := publicaddr.ValidateHostSyntax(suffix); err != nil {
		return "", err
	}
	return suffix, nil
}

// envDBEndpointDomain refuses to start on a suffix no customer could dial.
func envDBEndpointDomain(key string) string {
	suffix, err := parseDBEndpointDomain(os.Getenv(key))
	if err != nil {
		log.Fatalf("%s: %v", key, err)
	}
	return suffix
}
