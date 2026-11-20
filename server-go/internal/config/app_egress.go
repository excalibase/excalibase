package config

import (
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"strings"
)

// IPv4 only: the egress policies have no IPv6 rule to deny from.
func parseEgressExtraDenyCIDRs(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	cidrs := make([]string, 0, len(parts))
	for _, part := range parts {
		cidr := strings.TrimSpace(part)
		if cidr == "" {
			continue
		}
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("%q is not a valid CIDR: %w", cidr, err)
		}
		// By notation, not by parsed address: ::ffff:203.0.113.0/64 reads as IPv4 and masks to ::/64.
		if strings.Contains(cidr, ":") {
			return nil, fmt.Errorf("%q is IPv6, but the egress policies have no IPv6 rule to deny it from", cidr)
		}
		cidrs = append(cidrs, network.String())
	}
	return cidrs, nil
}

// ExtraDenyPrefixes are the install's own off-cluster ranges (its node and
// load-balancer addresses) as prefixes, for the checks that run in the control
// plane itself: image registries and auth-hook URLs.
func (c AppConfig) ExtraDenyPrefixes() []netip.Prefix {
	prefixes := make([]netip.Prefix, 0, len(c.AppEgressExtraDenyCIDRs))
	for _, cidr := range c.AppEgressExtraDenyCIDRs {
		if prefix, err := netip.ParsePrefix(cidr); err == nil {
			prefixes = append(prefixes, prefix)
		}
	}
	return prefixes
}

func envEgressExtraDenyCIDRs(key string) []string {
	cidrs, err := parseEgressExtraDenyCIDRs(os.Getenv(key))
	if err != nil {
		log.Fatalf("%s: %v", key, err)
	}
	return cidrs
}
