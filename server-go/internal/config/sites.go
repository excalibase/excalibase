package config

import (
	"fmt"
	"os"
	"regexp"
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
}

func loadSites() SitesConfig {
	return SitesConfig{
		AccountID:         strings.TrimSpace(os.Getenv("SITES_CLOUDFLARE_ACCOUNT_ID")),
		APIToken:          strings.TrimSpace(os.Getenv("SITES_CLOUDFLARE_API_TOKEN")),
		DispatchNamespace: strings.TrimSpace(os.Getenv("SITES_DISPATCH_NAMESPACE")),
		BaseDomain:        strings.ToLower(strings.TrimSpace(os.Getenv("SITES_BASE_DOMAIN"))),
	}
}

// Complete reports whether every Cloudflare setting is given.
func (s SitesConfig) Complete() bool {
	return s.AccountID != "" && s.APIToken != "" && s.DispatchNamespace != "" && s.BaseDomain != ""
}

func (s SitesConfig) String() string {
	return fmt.Sprintf("{AccountID:%s APIToken:[redacted] DispatchNamespace:%s BaseDomain:%s}", s.AccountID, s.DispatchNamespace, s.BaseDomain)
}

func (s SitesConfig) GoString() string { return "config.SitesConfig" + s.String() }

// SitesAvailable is whether this install offers Sites at all.
func (c AppConfig) SitesAvailable() bool {
	return c.SitesEnabled && c.Sites.Complete()
}

var baseDomainPattern = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// validateSites refuses a base domain no hostname could sit under.
func (c AppConfig) validateSites() error {
	if !c.SitesEnabled || c.Sites.BaseDomain == "" {
		return nil
	}
	if !baseDomainPattern.MatchString(c.Sites.BaseDomain) {
		return fmt.Errorf("SITES_BASE_DOMAIN %q is not a domain such as excalibase.app", c.Sites.BaseDomain)
	}
	return nil
}
