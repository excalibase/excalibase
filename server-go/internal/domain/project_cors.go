package domain

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// ErrInvalidCorsOrigin marks an allowlist entry the data plane must not be
// handed as a browser origin.
var ErrInvalidCorsOrigin = errors.New("invalid cors origin")

// CorsWildcard is the single entry that allows every origin. It is only
// accepted on its own and only when the caller confirms it explicitly.
const CorsWildcard = "*"

// MaxCorsOrigins caps a project's allowlist. A browser app has a handful of
// deploy hosts; a longer list is a sign the project wants "*", which must be
// said out loud rather than approximated.
const MaxCorsOrigins = 32

// ParseCorsOrigins validates a project's browser-origin allowlist and returns
// it canonical: trimmed, lower-cased, default ports dropped, de-duplicated and
// sorted. Every entry must be an absolute origin — scheme://host[:port] with
// no path, query, fragment or userinfo — because the data plane compares it
// byte-for-byte with the browser's Origin header. Subdomain wildcards are not
// supported. The bare wildcard is accepted only as the single entry and only
// with allowWildcard set, so opening a project to every origin is always a
// deliberate act.
func ParseCorsOrigins(entries []string, allowWildcard bool) ([]string, error) {
	seen := make(map[string]struct{}, len(entries))
	out := make([]string, 0, len(entries))
	for _, raw := range entries {
		entry := strings.ToLower(strings.TrimSpace(raw))
		if entry == "" {
			continue
		}
		origin, err := canonicalCorsOrigin(entry)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[origin]; dup {
			continue
		}
		seen[origin] = struct{}{}
		out = append(out, origin)
	}
	if err := checkCorsWildcard(out, allowWildcard); err != nil {
		return nil, err
	}
	if len(out) > MaxCorsOrigins {
		return nil, fmt.Errorf("%w: at most %d origins are allowed", ErrInvalidCorsOrigin, MaxCorsOrigins)
	}
	sort.Strings(out)
	return out, nil
}

// IsCorsWildcard reports whether the (already parsed) list is the wildcard.
func IsCorsWildcard(origins []string) bool {
	return len(origins) == 1 && origins[0] == CorsWildcard
}

func checkCorsWildcard(origins []string, allowWildcard bool) error {
	for _, origin := range origins {
		if origin != CorsWildcard {
			continue
		}
		if len(origins) > 1 {
			return fmt.Errorf("%w: %q must be the only entry", ErrInvalidCorsOrigin, CorsWildcard)
		}
		if !allowWildcard {
			return fmt.Errorf("%w: %q requires allowWildcard", ErrInvalidCorsOrigin, CorsWildcard)
		}
	}
	return nil
}

// canonicalCorsOrigin accepts the wildcard verbatim and otherwise requires an
// absolute origin, returned as scheme://host[:port].
func canonicalCorsOrigin(entry string) (string, error) {
	if entry == CorsWildcard {
		return entry, nil
	}
	if strings.ContainsAny(entry, " \t,") {
		return "", corsOriginError(entry)
	}
	parsed, err := url.Parse(entry)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Opaque != "" {
		return "", corsOriginError(entry)
	}
	if parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.ForceQuery {
		return "", corsOriginError(entry)
	}
	if strings.Contains(parsed.Hostname(), CorsWildcard) || parsed.Hostname() == "" {
		return "", corsOriginError(entry)
	}
	port, err := canonicalCorsPort(parsed.Scheme, parsed.Port())
	if err != nil {
		return "", corsOriginError(entry)
	}
	host := parsed.Hostname()
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return parsed.Scheme + "://" + host + port, nil
}

// canonicalCorsPort returns ":port" or "" — browsers omit the scheme's default
// port from the Origin header, so the stored form must too.
func canonicalCorsPort(scheme, port string) (string, error) {
	if port == "" {
		return "", nil
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", errors.New("port out of range")
	}
	if (scheme == "https" && n == 443) || (scheme == "http" && n == 80) {
		return "", nil
	}
	return ":" + port, nil
}

func corsOriginError(entry string) error {
	return fmt.Errorf("%w: %q must be an absolute origin (scheme://host[:port]) with no path", ErrInvalidCorsOrigin, entry)
}
