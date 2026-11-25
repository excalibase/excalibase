package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

// CheckCookieIsolation keeps customer apps and sites off the hosts that carry
// Studio's cookies. A hosting domain equal to, or above, the Studio or API host
// is refused. A hosting domain that only shares their registrable domain is
// allowed (the single-host default does) and reported as a warning.
func (c AppConfig) CheckCookieIsolation() (warnings []string, err error) {
	hosts := []string{hostOf(c.StudioURL), hostOf(c.PublicBaseURL), normaliseHost(c.AdminHost)}
	if err := c.checkInsecureCookiesScheme(); err != nil {
		return nil, err
	}
	if c.InsecureCookies {
		warnings = append(warnings, "STUDIO_INSECURE_COOKIES is on: Studio's session cookie is sent without Secure and the __Host- prefix, so a sibling subdomain can plant it. Use it only for plain-http installs.")
	}
	hosting := []struct{ env, domain string }{
		{"APP_DOMAIN", normaliseHost(c.AppDomain)},
		{"SITES_BASE_DOMAIN", normaliseHost(c.Sites.BaseDomain)},
	}
	for _, setting := range hosting {
		warning, err := c.checkHostingDomain(setting.env, setting.domain, hosts)
		if err != nil {
			return nil, err
		}
		if warning != "" {
			warnings = append(warnings, warning)
		}
	}
	return warnings, nil
}

// checkInsecureCookiesScheme refuses the plain-http opt-out behind an https Studio.
func (c AppConfig) checkInsecureCookiesScheme() error {
	if !c.InsecureCookies {
		return nil
	}
	parsed, err := url.Parse(strings.TrimSpace(c.StudioURL))
	if err == nil && strings.EqualFold(parsed.Scheme, "https") {
		return fmt.Errorf("STUDIO_INSECURE_COOKIES must not be set when STUDIO_URL %q is https", c.StudioURL)
	}
	return nil
}

// checkHostingDomain refuses a domain equal to or above a Studio-side host, and
// refuses a registrable-domain sibling when cookies are insecure; otherwise a
// sibling yields a warning.
func (c AppConfig) checkHostingDomain(env, domain string, hosts []string) (string, error) {
	if domain == "" {
		return "", nil
	}
	for _, host := range hosts {
		if host != "" && (host == domain || strings.HasSuffix(host, "."+domain)) {
			return "", fmt.Errorf("%s %q must not be, or contain, the Studio or API host %q: apps there could set cookies Studio trusts", env, domain, host)
		}
	}
	shared := sharedRegistrableDomain(domain, hosts...)
	if shared == "" {
		return "", nil
	}
	if c.InsecureCookies {
		return "", fmt.Errorf("STUDIO_INSECURE_COOKIES must not be set while %s %q shares the registrable domain %q with Studio, the API or the admin host: apps there could plant the session cookie", env, domain, shared)
	}
	return fmt.Sprintf("%s %q shares the registrable domain %q with Studio, the API or the admin host: Studio's cookies are __Host- prefixed, but keep customer content on a separate domain where you can", env, domain, shared), nil
}

func hostOf(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return normaliseHost(parsed.Hostname())
}

// normaliseHost lower-cases a name, drops a trailing dot and converts an
// internationalised name to punycode, so two spellings of one host compare equal.
func normaliseHost(raw string) string {
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
	if ascii, err := idna.Lookup.ToASCII(host); err == nil {
		return ascii
	}
	return host
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
