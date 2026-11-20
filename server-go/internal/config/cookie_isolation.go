package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// CheckCookieIsolation keeps customer apps and sites off the hosts that carry
// Studio's cookies. A hosting domain equal to, or above, the Studio or API host
// is refused. A hosting domain that only shares their registrable domain is
// allowed (the single-host default does) and reported as a warning.
func (c AppConfig) CheckCookieIsolation() (warnings []string, err error) {
	studio, api := hostOf(c.StudioURL), hostOf(c.PublicBaseURL)
	if c.InsecureCookies {
		if strings.HasPrefix(c.StudioURL, "https://") {
			return nil, fmt.Errorf("STUDIO_INSECURE_COOKIES must not be set when STUDIO_URL %q is https", c.StudioURL)
		}
		warnings = append(warnings, "STUDIO_INSECURE_COOKIES is on: Studio's session cookie is sent without Secure and the __Host- prefix, so a sibling subdomain can plant it. Use it only for plain-http installs.")
	}
	hosting := []struct{ env, domain string }{
		{"APP_DOMAIN", strings.ToLower(strings.TrimSpace(c.AppDomain))},
		{"SITES_BASE_DOMAIN", strings.ToLower(strings.TrimSpace(c.Sites.BaseDomain))},
	}
	for _, setting := range hosting {
		if setting.domain == "" {
			continue
		}
		for _, host := range []string{studio, api} {
			if host != "" && (host == setting.domain || strings.HasSuffix(host, "."+setting.domain)) {
				return nil, fmt.Errorf("%s %q must not be, or contain, the Studio or API host %q: apps there could set cookies Studio trusts", setting.env, setting.domain, host)
			}
		}
		if shared := sharedRegistrableDomain(setting.domain, studio, api); shared != "" {
			warnings = append(warnings, fmt.Sprintf("%s %q shares the registrable domain %q with Studio or the API: Studio's cookies are __Host- prefixed, but keep customer content on a separate domain where you can", setting.env, setting.domain, shared))
		}
	}
	return warnings, nil
}

func hostOf(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

// sharedRegistrableDomain is the registrable domain domain shares with any of
// the hosts, or "" for an address, localhost or a name the public suffix list
// has no entry for.
func sharedRegistrableDomain(domain string, hosts ...string) string {
	own, err := publicsuffix.EffectiveTLDPlusOne(domain)
	if err != nil {
		return ""
	}
	for _, host := range hosts {
		if host == "" || net.ParseIP(host) != nil {
			continue
		}
		if other, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil && other == own {
			return own
		}
	}
	return ""
}
