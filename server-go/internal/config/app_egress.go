package config

import (
	"fmt"
	"log"
	"net"
	"os"
	"strings"
)

// IPv4 only: the app egress policy has no IPv6 rule to deny from.
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
		ip, network, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("%q is not a valid CIDR: %w", cidr, err)
		}
		if ip.To4() == nil {
			return nil, fmt.Errorf("%q is IPv6, but the app egress policy has no IPv6 rule to deny it from", cidr)
		}
		cidrs = append(cidrs, network.String())
	}
	return cidrs, nil
}

func envEgressExtraDenyCIDRs(key string) []string {
	cidrs, err := parseEgressExtraDenyCIDRs(os.Getenv(key))
	if err != nil {
		log.Fatalf("%s: %v", key, err)
	}
	return cidrs
}
