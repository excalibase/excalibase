package publicaddr

import (
	"errors"
	"net/netip"
)

// ErrInternalAddress marks a target that resolves to a non-public address.
var ErrInternalAddress = errors.New("host is not a publicly routable address; the platform only dials publicly reachable hosts")

// blockedPrefixes covers ranges that netip's Is* helpers do not: this-network,
// CGNAT (Alibaba metadata lives here), IETF protocol assignments, benchmarking,
// reserved + broadcast, IPv6 site-local, Teredo tunnels and the discard prefix.
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("fec0::/10"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("100::/64"),
}

var (
	prefixNAT64 = netip.MustParsePrefix("64:ff9b::/96")
	prefix6to4  = netip.MustParsePrefix("2002::/16")
	prefixV4Cmp = netip.MustParsePrefix("::/96")
)

// ClassifyAddr returns ErrInternalAddress when addr must never be dialled by
// the platform on a user's behalf. Addresses that encapsulate an IPv4 address
// are judged by the embedded IPv4.
func ClassifyAddr(addr netip.Addr) error {
	addr = addr.Unmap()
	if addr.IsLoopback() || addr.IsUnspecified() || addr.IsPrivate() ||
		addr.IsMulticast() || addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() || addr.IsInterfaceLocalMulticast() {
		return ErrInternalAddress
	}
	if embedded, ok := embeddedIPv4(addr); ok {
		return ClassifyAddr(embedded)
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(addr) {
			return ErrInternalAddress
		}
	}
	return nil
}

// embeddedIPv4 extracts the IPv4 address carried by NAT64, 6to4 and
// deprecated v4-compatible IPv6 addresses.
func embeddedIPv4(addr netip.Addr) (netip.Addr, bool) {
	if !addr.Is6() {
		return netip.Addr{}, false
	}
	raw := addr.As16()
	switch {
	case prefixNAT64.Contains(addr), prefixV4Cmp.Contains(addr):
		return netip.AddrFrom4([4]byte{raw[12], raw[13], raw[14], raw[15]}), true
	case prefix6to4.Contains(addr):
		return netip.AddrFrom4([4]byte{raw[2], raw[3], raw[4], raw[5]}), true
	}
	return netip.Addr{}, false
}
