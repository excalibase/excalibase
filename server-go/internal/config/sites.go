package config

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// SitesConfig is the Cloudflare account Sites publishes through (EXC-353).
// Without every setting and SITES_ENABLED, Sites is not offered.
type SitesConfig struct {
	AccountID string
	// APIToken is a secret; String and GoString leave it out.
	APIToken          string
	DispatchNamespace string
	// BaseDomain is the user-content domain each site is a hostname under.
	BaseDomain string
	// HostsKVNamespaceID is the Workers KV namespace the dispatch Worker reads hostname → script from.
	HostsKVNamespaceID string
	// ZoneID is the user-content zone's id; custom domains (EXC-614) are offered only with it.
	ZoneID string
	// CustomTarget is the hostname customers CNAME to, the zone's fallback origin; "" is cname.<BaseDomain>.
	CustomTarget string
	// MaxZoneHostnames bounds the custom hostnames on the zone, across every project; 0 is the default.
	MaxZoneHostnames int
	// OutboundWorker names the Worker bound as the dispatch namespace's outbound
	// Worker (EXC-661); server sites are offered only once it is named.
	OutboundWorker string
}

// DefaultMaxZoneHostnames keeps the zone under the 100 custom hostnames Cloudflare includes.
const DefaultMaxZoneHostnames = 90

// DefaultCustomTargetLabel is the label of the default CNAME target under the base domain.
const DefaultCustomTargetLabel = "cname"

// CustomDomainTarget is the hostname customers point their CNAME at.
func (s SitesConfig) CustomDomainTarget() string {
	if s.CustomTarget != "" {
		return s.CustomTarget
	}
	return DefaultCustomTargetLabel + "." + s.BaseDomain
}

func loadSites() SitesConfig {
	return SitesConfig{
		AccountID:          strings.TrimSpace(os.Getenv("SITES_CLOUDFLARE_ACCOUNT_ID")),
		APIToken:           strings.TrimSpace(os.Getenv("SITES_CLOUDFLARE_API_TOKEN")),
		DispatchNamespace:  strings.TrimSpace(os.Getenv("SITES_DISPATCH_NAMESPACE")),
		BaseDomain:         strings.ToLower(strings.TrimSpace(os.Getenv("SITES_BASE_DOMAIN"))),
		HostsKVNamespaceID: strings.TrimSpace(os.Getenv("SITES_HOSTS_KV_NAMESPACE_ID")),
		ZoneID:             strings.TrimSpace(os.Getenv("SITES_ZONE_ID")),
		CustomTarget:       strings.ToLower(strings.TrimSpace(os.Getenv("SITES_CUSTOM_DOMAIN_TARGET"))),
		MaxZoneHostnames:   zoneHostnameCap(os.Getenv("SITES_MAX_ZONE_HOSTNAMES")),
		OutboundWorker:     strings.TrimSpace(os.Getenv("SITES_OUTBOUND_WORKER")),
	}
}

// zoneHostnameCap is the default when unset and -1, for Validate to refuse, when not a count above 0.
func zoneHostnameCap(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultMaxZoneHostnames
	}
	count, err := strconv.Atoi(raw)
	if err != nil || count < 1 {
		return -1
	}
	return count
}

// Complete reports whether every Cloudflare setting is given.
func (s SitesConfig) Complete() bool {
	return s.AccountID != "" && s.APIToken != "" && s.DispatchNamespace != "" && s.BaseDomain != "" && s.HostsKVNamespaceID != ""
}

func (s SitesConfig) String() string {
	return fmt.Sprintf("{AccountID:%s APIToken:[redacted] DispatchNamespace:%s BaseDomain:%s HostsKVNamespaceID:%s ZoneID:%s CustomTarget:%s MaxZoneHostnames:%d OutboundWorker:%s}",
		s.AccountID, s.DispatchNamespace, s.BaseDomain, s.HostsKVNamespaceID, s.ZoneID, s.CustomTarget, s.MaxZoneHostnames, s.OutboundWorker)
}

func (s SitesConfig) GoString() string { return "config.SitesConfig" + s.String() }

// SitesAvailable is whether this install offers Sites at all.
func (c AppConfig) SitesAvailable() bool {
	return c.SitesEnabled && c.Sites.Complete()
}

// SitesCustomDomainsAvailable is whether sites may take custom domains here.
func (c AppConfig) SitesCustomDomainsAvailable() bool {
	return c.SitesAvailable() && c.Sites.ZoneID != ""
}

var zoneIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// workerNamePattern is a Worker script name, as the Cloudflare client sends one.
var workerNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

var baseDomainPattern = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// validateSites refuses a base domain no hostname could sit under.
func (c AppConfig) validateSites() error {
	if !c.SitesEnabled || c.Sites.BaseDomain == "" {
		return nil
	}
	if !baseDomainPattern.MatchString(c.Sites.BaseDomain) {
		return fmt.Errorf("SITES_BASE_DOMAIN %q is not a domain such as excalibase.app", c.Sites.BaseDomain)
	}
	if domainsOverlap(c.Sites.BaseDomain, strings.ToLower(strings.TrimSpace(c.AppDomain))) {
		return fmt.Errorf("SITES_BASE_DOMAIN %q must not be APP_DOMAIN or share a subtree with it: a site name could take an app's hostname", c.Sites.BaseDomain)
	}
	if name := c.Sites.OutboundWorker; name != "" && !workerNamePattern.MatchString(name) {
		return fmt.Errorf("SITES_OUTBOUND_WORKER %q must be a Worker script name: lowercase letters, digits and hyphens", name)
	}
	return c.Sites.validateCustomDomains()
}

// validateCustomDomains refuses a zone id that is not Cloudflare's shape and a
// CNAME target outside the zone, which Cloudflare could not use as the fallback origin.
func (s SitesConfig) validateCustomDomains() error {
	if s.MaxZoneHostnames < 0 {
		return fmt.Errorf("SITES_MAX_ZONE_HOSTNAMES must be a whole number above 0 (default %d)", DefaultMaxZoneHostnames)
	}
	if s.ZoneID == "" {
		return nil
	}
	if !zoneIDPattern.MatchString(s.ZoneID) {
		return fmt.Errorf("SITES_ZONE_ID must be the zone's 32-hex id, not its name")
	}
	target := s.CustomDomainTarget()
	if !baseDomainPattern.MatchString(target) || !strings.HasSuffix(target, "."+s.BaseDomain) {
		return fmt.Errorf("SITES_CUSTOM_DOMAIN_TARGET %q must be a hostname under SITES_BASE_DOMAIN %q", target, s.BaseDomain)
	}
	return nil
}

// domainsOverlap is one domain equal to, or under, the other.
func domainsOverlap(first, second string) bool {
	if first == "" || second == "" {
		return false
	}
	return first == second || strings.HasSuffix(first, "."+second) || strings.HasSuffix(second, "."+first)
}
