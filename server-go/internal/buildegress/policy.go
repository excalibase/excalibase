// Package buildegress is the only way out of a git build on a single Docker or
// Podman host (EXC-671). The build's network has no route; this forward proxy,
// on that network and on an outbound one, admits HTTP(S) to the public IPv4
// internet the way the Kubernetes build NetworkPolicy does, and nothing else.
package buildegress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// ErrRefused wraps every destination the policy turns away.
var ErrRefused = errors.New("refused by the build egress policy")

// deniedRanges: private, link-local (cloud metadata), CGNAT, loopback, "this network", multicast and
// reserved, as the Kubernetes policies deny; also Azure's host endpoint (WireServer, public but answering
// only its own VMs), the IETF protocol, benchmarking and documentation ranges.
var deniedRanges = []string{
	"169.254.0.0/16", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", //NOSONAR
	"127.0.0.0/8", "0.0.0.0/8", "224.0.0.0/4", "240.0.0.0/4", //NOSONAR
	"168.63.129.16/32", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", //NOSONAR
}

// webPorts are the only ports a build reaches without an allow-list entry.
var webPorts = map[string]bool{"80": true, "443": true}

// Resolver looks names up; net.DefaultResolver satisfies it.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Policy decides which destinations a build may reach.
type Policy struct {
	denied []netip.Prefix
	// allowed are "host:port" entries the operator admits past the address check.
	allowed map[string]bool
}

// NewPolicy adds the operator's extra denied ranges and allow-listed host:port entries.
func NewPolicy(extraDeny, allow []string) (Policy, error) {
	policy := Policy{allowed: map[string]bool{}}
	for _, cidr := range append(append([]string{}, deniedRanges...), extraDeny...) {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(cidr))
		if err != nil {
			return Policy{}, fmt.Errorf("denied range %q: %w", cidr, err)
		}
		policy.denied = append(policy.denied, prefix.Masked())
	}
	for _, entry := range allow {
		key, err := allowKey(entry)
		if err != nil {
			return Policy{}, err
		}
		policy.allowed[key] = true
	}
	return policy, nil
}

func allowKey(entry string) (string, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(entry))
	if err != nil || host == "" {
		return "", fmt.Errorf("allow entry %q must be host:port", entry)
	}
	if number, err := strconv.Atoi(port); err != nil || number < 1 || number > 65535 {
		return "", fmt.Errorf("allow entry %q has no valid port", entry)
	}
	return strings.ToLower(net.JoinHostPort(host, port)), nil
}

// Resolve answers the one address to dial for host:port, or why not. A name is
// looked up once and refused if any of its addresses is denied, so the caller
// dials exactly the address checked and a second lookup cannot swap it.
func (p Policy) Resolve(ctx context.Context, resolver Resolver, host, port string) (netip.AddrPort, error) {
	host = strings.ToLower(strings.Trim(host, "[]"))
	target := net.JoinHostPort(host, port)
	allowed := p.allowed[target]
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 || (!allowed && !webPorts[port]) {
		return netip.AddrPort{}, fmt.Errorf("%w: %s: only ports 80 and 443 are open to builds", ErrRefused, target)
	}
	candidates, err := p.lookup(ctx, resolver, host)
	if err != nil {
		return netip.AddrPort{}, err
	}
	var chosen netip.Addr
	for _, addr := range candidates {
		addr = addr.Unmap()
		if !addr.Is4() {
			continue
		}
		if !allowed && p.denies(addr) {
			return netip.AddrPort{}, fmt.Errorf("%w: %s resolves to %s, a private or reserved address", ErrRefused, target, addr)
		}
		if !chosen.IsValid() {
			chosen = addr
		}
	}
	if !chosen.IsValid() {
		return netip.AddrPort{}, fmt.Errorf("%w: %s has no IPv4 address; builds reach IPv4 only", ErrRefused, target)
	}
	return netip.AddrPortFrom(chosen, uint16(number)), nil
}

func (p Policy) lookup(ctx context.Context, resolver Resolver, host string) ([]netip.Addr, error) {
	if literal, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{literal}, nil
	}
	found, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("look up %s: %w", host, err)
	}
	return found, nil
}

func (p Policy) denies(addr netip.Addr) bool {
	for _, prefix := range p.denied {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}
