// Package logdrain forwards a project's logs to the destination its Admins
// configured: Datadog's logs intake or any public HTTPS endpoint (EXC-658).
// Provisioning reads the lines from Loki and owns each drain's cursor; the
// forwarder pod only POSTs batches, through a dialer that refuses internal
// addresses on every connect.
package logdrain

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/excalibase/provisioning-poc/internal/publicaddr"
)

// ErrInvalidDrain is a drain setting the platform refuses.
var ErrInvalidDrain = errors.New("invalid log drain")

// Drain kinds.
const (
	KindDatadog = "datadog"
	KindHTTPS   = "https"
)

const (
	maxNameLength     = 64
	maxEndpointLength = 2048
	maxHeaderLength   = 64
	maxSecretLength   = 4096
	maxErrorLength    = 200
	httpsPort         = "443"
)

// datadogIntakeHosts are Datadog's official logs intake hosts per site; no
// other host is ever sent a Datadog API key.
var datadogIntakeHosts = map[string]string{
	"us1":     "http-intake.logs.datadoghq.com",
	"us3":     "http-intake.logs.us3.datadoghq.com",
	"us5":     "http-intake.logs.us5.datadoghq.com",
	"eu1":     "http-intake.logs.datadoghq.eu",
	"ap1":     "http-intake.logs.ap1.datadoghq.com",
	"ap2":     "http-intake.logs.ap2.datadoghq.com",
	"us1-fed": "http-intake.logs.ddog-gov.com",
}

// DatadogSites lists the accepted site names, sorted.
func DatadogSites() []string {
	sites := make([]string, 0, len(datadogIntakeHosts))
	for site := range datadogIntakeHosts {
		sites = append(sites, site)
	}
	sort.Strings(sites)
	return sites
}

// DatadogIntakeURL is the logs intake endpoint of a Datadog site.
func DatadogIntakeURL(site string) (string, error) {
	host, ok := datadogIntakeHosts[site]
	if !ok {
		return "", fmt.Errorf("%w: datadog site must be one of %s", ErrInvalidDrain, strings.Join(DatadogSites(), ", "))
	}
	return "https://" + host + "/api/v2/logs", nil
}

// IsDatadogIntake reports whether target is exactly an official intake URL.
func IsDatadogIntake(target string) bool {
	for site := range datadogIntakeHosts {
		if intake, _ := DatadogIntakeURL(site); intake == target {
			return true
		}
	}
	return false
}

// DisplayURL is an endpoint as the API returns and audits it: scheme and host
// only, since log intakes often carry their token in the path or query.
func DisplayURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return ""
	}
	shown := parsed.Scheme + "://" + parsed.Host
	if parsed.Path != "" || parsed.RawQuery != "" {
		shown += "/..."
	}
	return shown
}

// ParseEndpoint accepts an https URL on port 443 naming one public host, with
// no credentials or fragment, and returns it normalized. Whether the name
// resolves publicly is decided again on every connect.
func ParseEndpoint(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxEndpointLength || strings.ContainsFunc(raw, unicode.IsControl) {
		return "", fmt.Errorf("%w: the endpoint must be an https URL of at most %d characters", ErrInvalidDrain, maxEndpointLength)
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", fmt.Errorf("%w: the endpoint must be an https:// URL", ErrInvalidDrain)
	}
	if parsed.User != nil || parsed.Fragment != "" || parsed.Opaque != "" {
		return "", fmt.Errorf("%w: the endpoint must not carry credentials or a fragment; use the header instead", ErrInvalidDrain)
	}
	if port := parsed.Port(); port != "" && port != httpsPort {
		return "", fmt.Errorf("%w: the endpoint must use port 443", ErrInvalidDrain)
	}
	host := strings.ToLower(parsed.Hostname())
	if err := checkHost(host); err != nil {
		return "", err
	}
	parsed.Host = host
	if strings.Contains(host, ":") {
		parsed.Host = "[" + host + "]"
	}
	return parsed.String(), nil
}

func checkHost(host string) error {
	if err := publicaddr.ValidateHostSyntax(host); err != nil {
		return fmt.Errorf("%w: the endpoint must name one public host", ErrInvalidDrain)
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if publicaddr.ClassifyAddr(addr) != nil {
			return fmt.Errorf("%w: the endpoint must be a public address", ErrInvalidDrain)
		}
	}
	return nil
}

// reservedHeaders are set by the transport or the forwarder, or would turn
// the custom header into a smuggling or credential-forwarding vector.
var reservedHeaders = map[string]bool{
	"host": true, "content-length": true, "content-type": true, "content-encoding": true,
	"transfer-encoding": true, "connection": true, "keep-alive": true, "te": true, "trailer": true,
	"upgrade": true, "cookie": true, "proxy-authorization": true, "proxy-connection": true,
	"expect": true, "dd-api-key": true, "forwarded": true, "via": true, "x-real-ip": true,
}

// ValidateHeaderName accepts one HTTP token the platform does not set itself.
func ValidateHeaderName(name string) error {
	lower := strings.ToLower(name)
	if name == "" || len(name) > maxHeaderLength || reservedHeaders[lower] || strings.HasPrefix(lower, "x-forwarded-") {
		return fmt.Errorf("%w: the header must be a custom header name such as Authorization", ErrInvalidDrain)
	}
	for _, r := range name {
		if !isTokenRune(r) {
			return fmt.Errorf("%w: the header name has a character HTTP does not allow", ErrInvalidDrain)
		}
	}
	return nil
}

func isTokenRune(r rune) bool {
	return r < utf8.RuneSelf && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
		strings.ContainsRune("!#$%&'*+-.^_`|~", r))
}

// ValidateName accepts a printable label of at most 64 characters.
func ValidateName(name string) error {
	if strings.TrimSpace(name) == "" || utf8.RuneCountInString(name) > maxNameLength || !utf8.ValidString(name) ||
		strings.ContainsFunc(name, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) {
		return fmt.Errorf("%w: the name must be printable text of at most %d characters", ErrInvalidDrain, maxNameLength)
	}
	return nil
}

// ValidateSecret accepts an API key or header value that can travel in a header.
func ValidateSecret(secret string) error {
	if secret == "" || len(secret) > maxSecretLength || strings.ContainsFunc(secret, unicode.IsControl) {
		return fmt.Errorf("%w: the key must be 1 to %d characters with no line breaks", ErrInvalidDrain, maxSecretLength)
	}
	return nil
}

// Redact removes secret from msg, drops control characters and bounds the
// length, so an error is safe to store and show.
func Redact(msg, secret string) string {
	if secret != "" {
		msg = strings.ReplaceAll(msg, secret, "[redacted]")
	}
	msg = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, msg)
	msg = strings.TrimSpace(msg)
	if len(msg) > maxErrorLength {
		cut := maxErrorLength
		for cut > 0 && !utf8.RuneStart(msg[cut]) {
			cut--
		}
		msg = msg[:cut]
	}
	return msg
}
