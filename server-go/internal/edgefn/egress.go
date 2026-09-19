package edgefn

import (
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/publicaddr"
)

// ErrInvalidEgressHost marks an allowlist entry the runtime must not be given.
var ErrInvalidEgressHost = errors.New("invalid egress host")

// MaxEgressHosts caps a project's allowlist. Deno checks the worker's net
// permission list linearly on every connect, and a longer list is a sign the
// project wants "*" — which is exactly what the allowlist exists to refuse.
const MaxEgressHosts = 64

// EgressStore persists the per-project outbound allowlist (EXC-348).
type EgressStore interface {
	// GetEgressHosts returns the stored allowlist (empty, never nil, when unset).
	GetEgressHosts(projectID string) ([]string, error)
	// SetEgressHosts replaces the allowlist. Entries must already be parsed.
	SetEgressHosts(projectID string, hosts []string) error
}

// ParseEgressHostList parses a comma-separated allowlist (the env-var form).
func ParseEgressHostList(raw string) ([]string, error) {
	return ParseEgressHosts(strings.Split(raw, ","))
}

// ParseEgressHosts validates an allowlist and returns it canonical: trimmed,
// lower-cased, de-duplicated and sorted. Entries take Deno's net-permission
// shape — `host`, `host:port`, `*.suffix`, `*.suffix:port`, or a public IP
// literal (bracketed for IPv6) with an optional port — because the list is
// handed to the worker's `net` permission verbatim. Anything the sandbox would
// interpret more loosely than intended (a bare `*`, a scheme, a path, a CIDR)
// is refused, as is every address the platform itself must never be pointed at
// (loopback, RFC-1918, link-local metadata, CGNAT, cluster-internal suffixes).
func ParseEgressHosts(entries []string) ([]string, error) {
	seen := make(map[string]struct{}, len(entries))
	out := make([]string, 0, len(entries))
	for _, raw := range entries {
		entry := strings.ToLower(strings.TrimSpace(raw))
		if entry == "" {
			continue
		}
		if err := validateEgressHost(entry); err != nil {
			return nil, err
		}
		if _, dup := seen[entry]; dup {
			continue
		}
		seen[entry] = struct{}{}
		out = append(out, entry)
	}
	if len(out) > MaxEgressHosts {
		return nil, fmt.Errorf("%w: at most %d entries are allowed", ErrInvalidEgressHost, MaxEgressHosts)
	}
	sort.Strings(out)
	return out, nil
}

func validateEgressHost(entry string) error {
	if strings.ContainsAny(entry, " \t,/\\@?#=") {
		return fmt.Errorf("%w: %q must be host[:port] with no scheme, path or list separator", ErrInvalidEgressHost, entry)
	}
	host, port, err := splitEgressHostPort(entry)
	if err != nil {
		return err
	}
	if port != "" {
		if err := validateEgressPort(entry, port); err != nil {
			return err
		}
	}
	if suffix, wildcard := strings.CutPrefix(host, "*."); wildcard {
		if _, isLiteral := parseEgressLiteral(suffix); isLiteral {
			return fmt.Errorf("%w: %q wildcard needs a hostname suffix", ErrInvalidEgressHost, entry)
		}
		return validateEgressHostname(entry, suffix)
	}
	if strings.Contains(host, "*") {
		return fmt.Errorf("%w: %q — only a leading \"*.\" wildcard is supported", ErrInvalidEgressHost, entry)
	}
	if addr, isLiteral := parseEgressLiteral(host); isLiteral {
		if err := publicaddr.ClassifyAddr(addr); err != nil {
			return fmt.Errorf("%w: %q is not a public address", ErrInvalidEgressHost, entry)
		}
		return nil
	}
	return validateEgressHostname(entry, host)
}

// splitEgressHostPort separates an optional trailing `:port`, honouring
// bracketed IPv6 literals. A bare IPv6 literal without brackets has no port.
func splitEgressHostPort(entry string) (host, port string, err error) {
	if strings.HasPrefix(entry, "[") {
		end := strings.Index(entry, "]")
		if end < 0 {
			return "", "", fmt.Errorf("%w: %q has an unterminated IPv6 literal", ErrInvalidEgressHost, entry)
		}
		rest := entry[end+1:]
		if rest == "" {
			return entry, "", nil
		}
		if !strings.HasPrefix(rest, ":") {
			return "", "", fmt.Errorf("%w: %q", ErrInvalidEgressHost, entry)
		}
		return entry[:end+1], rest[1:], nil
	}
	if strings.Count(entry, ":") > 1 {
		return entry, "", nil // unbracketed IPv6 literal
	}
	host, port, found := strings.Cut(entry, ":")
	if found && host == "" {
		return "", "", fmt.Errorf("%w: %q has no host", ErrInvalidEgressHost, entry)
	}
	return host, port, nil
}

func validateEgressPort(entry, port string) error {
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("%w: %q has an invalid port", ErrInvalidEgressHost, entry)
	}
	return nil
}

func validateEgressHostname(entry, host string) error {
	if _, isLiteral := parseEgressLiteral(host); isLiteral {
		return fmt.Errorf("%w: %q", ErrInvalidEgressHost, entry)
	}
	if err := publicaddr.ValidateHostSyntax(host); err != nil {
		return fmt.Errorf("%w: %q is not a public hostname", ErrInvalidEgressHost, entry)
	}
	return nil
}

// parseEgressLiteral parses an IP literal, with or without IPv6 brackets.
func parseEgressLiteral(host string) (netip.Addr, bool) {
	if strings.HasPrefix(host, "[") != strings.HasSuffix(host, "]") {
		return netip.Addr{}, false
	}
	addr, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"))
	if err != nil || addr.Zone() != "" {
		return netip.Addr{}, false
	}
	return addr, true
}

// MergeEgressHosts unions already-parsed lists (operator defaults + project
// allowlist) into one canonical list. Never returns nil.
func MergeEgressHosts(lists ...[]string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, list := range lists {
		for _, host := range list {
			if _, dup := seen[host]; dup {
				continue
			}
			seen[host] = struct{}{}
			out = append(out, host)
		}
	}
	sort.Strings(out)
	return out
}

// EgressEnvValue renders the list in the runtime's ALLOWED_HOSTS form. Empty
// means no egress — the default.
func EgressEnvValue(hosts []string) string {
	return strings.Join(hosts, ",")
}

// EgressHostPorts returns the distinct TCP ports the allowlist can reach,
// ascending. An entry without a port means HTTPS (443). The NetworkPolicy
// backstop under the Deno sandbox is port-scoped, so it needs this set.
func EgressHostPorts(hosts []string) []int {
	set := map[int]struct{}{}
	for _, host := range hosts {
		_, port, err := splitEgressHostPort(host)
		n := 443
		if err == nil && port != "" {
			n, _ = strconv.Atoi(port)
		}
		set[n] = struct{}{}
	}
	out := make([]int, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}
