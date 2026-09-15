package byoc

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

// ErrNotAllowlisted marks a target outside the operator's egress allowlist.
var ErrNotAllowlisted = errors.New("host is not in the operator's BYOC egress allowlist")

// Policy is the optional operator egress allowlist. The zero value permits
// every (public) target.
type Policy struct {
	cidrs []netip.Prefix
	hosts []hostPattern
}

// hostPattern is an exact hostname or a "*.suffix" wildcard that matches any
// name strictly below suffix.
type hostPattern struct {
	name     string
	wildcard bool
}

// ParseAllowlist parses a comma-separated list of CIDRs, IP literals,
// hostnames and "*.suffix" wildcards. Blank entries are ignored.
func ParseAllowlist(raw string) (Policy, error) {
	var policy Policy
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if err := policy.add(entry); err != nil {
			return Policy{}, err
		}
	}
	return policy, nil
}

func (p *Policy) add(entry string) error {
	if prefix, err := netip.ParsePrefix(entry); err == nil {
		p.cidrs = append(p.cidrs, prefix.Masked())
		return nil
	}
	if addr, err := netip.ParseAddr(entry); err == nil {
		p.cidrs = append(p.cidrs, netip.PrefixFrom(addr, addr.BitLen()))
		return nil
	}
	if strings.Contains(entry, "/") {
		return fmt.Errorf("allowlist entry %q is not a valid CIDR", entry)
	}
	name, wildcard := strings.CutPrefix(entry, "*.")
	if err := ValidateHostSyntax(name); err != nil {
		return fmt.Errorf("allowlist entry %q: %w", entry, err)
	}
	p.hosts = append(p.hosts, hostPattern{name: canonicalName(name), wildcard: wildcard})
	return nil
}

// Empty reports whether the policy imposes no restriction.
func (p Policy) Empty() bool { return len(p.cidrs) == 0 && len(p.hosts) == 0 }

// Permits reports whether host (a hostname or IP literal) with its resolved
// addresses is allowed. A hostname entry matches by name; otherwise every
// resolved address must fall inside an allowlisted CIDR.
func (p Policy) Permits(host string, addrs []netip.Addr) bool {
	if p.Empty() {
		return true
	}
	if addr, ok := parseLiteral(host); ok {
		return p.containsAll([]netip.Addr{addr})
	}
	if p.matchesName(canonicalName(host)) {
		return true
	}
	return len(addrs) > 0 && p.containsAll(addrs)
}

func (p Policy) matchesName(name string) bool {
	for _, pattern := range p.hosts {
		if pattern.wildcard && strings.HasSuffix(name, "."+pattern.name) {
			return true
		}
		if !pattern.wildcard && name == pattern.name {
			return true
		}
	}
	return false
}

func (p Policy) containsAll(addrs []netip.Addr) bool {
	for _, addr := range addrs {
		if !p.containsAddr(addr.Unmap()) {
			return false
		}
	}
	return true
}

func (p Policy) containsAddr(addr netip.Addr) bool {
	for _, prefix := range p.cidrs {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}
