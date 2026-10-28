package domain

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// A project's database allow-list (EXC-665) names the client addresses that may
// reach its public port. It is rendered as loadBalancerSourceRanges on every
// public Service of the project, where the datapath drops anything else before
// it reaches the database.

// ErrInvalidDBSourceAllowList marks a list that cannot be applied as asked.
var ErrInvalidDBSourceAllowList = errors.New("invalid database allow-list")

// MaxDBAllowedSources bounds one project's list. The window holds at most one
// Service per port, so the datapath never carries more than ports x this.
const MaxDBAllowedSources = 20

// Narrower prefixes than these are "allow any" written in two lines; that
// choice is made with the explicit switch instead.
const (
	minIPv4Prefix = 8
	minIPv6Prefix = 16
)

// DBSourceAccess is who may reach the public port.
type DBSourceAccess string

const (
	// DBSourceAccessUnset is a project that never chose; its port, if
	// already open, stays open to any address as it was.
	DBSourceAccessUnset DBSourceAccess = ""
	// DBSourceAccessAny is an explicit choice to admit every address.
	DBSourceAccessAny DBSourceAccess = "any"
	// DBSourceAccessListed admits only Sources.
	DBSourceAccessListed DBSourceAccess = "listed"
	// DBSourceAccessHost is a single host's database, on its loopback only.
	DBSourceAccessHost DBSourceAccess = "host"
)

// DBSourceAllowList is one project's choice. Sources are canonical prefixes,
// sorted, without duplicates, and empty unless Access is listed.
type DBSourceAllowList struct {
	ProjectID string
	Access    DBSourceAccess
	Sources   []string
}

// Chosen reports whether the project has decided who may connect.
func (l DBSourceAllowList) Chosen() bool {
	return l.Access == DBSourceAccessAny || l.Access == DBSourceAccessListed
}

// SourceRanges is what the Services carry: the list, or nil for any address.
func (l DBSourceAllowList) SourceRanges() []string {
	if l.Access != DBSourceAccessListed || len(l.Sources) == 0 {
		return nil
	}
	return slices.Clone(l.Sources)
}

// ParseDBSourceAllowList validates a request. An empty list is refused rather
// than read as "nobody": an empty loadBalancerSourceRanges admits everyone.
func ParseDBSourceAllowList(allowAny bool, sources []string) (DBSourceAllowList, error) {
	if allowAny {
		if len(sources) > 0 {
			return DBSourceAllowList{}, fmt.Errorf("%w: either allow any address or list addresses, not both", ErrInvalidDBSourceAllowList)
		}
		return DBSourceAllowList{Access: DBSourceAccessAny}, nil
	}
	if len(sources) == 0 {
		return DBSourceAllowList{}, fmt.Errorf("%w: name at least one address, allow any address, or close the public port", ErrInvalidDBSourceAllowList)
	}
	seen := make(map[string]bool, len(sources))
	canonical := make([]string, 0, len(sources))
	for _, raw := range sources {
		prefix, err := parseAllowedSource(raw)
		if err != nil {
			return DBSourceAllowList{}, err
		}
		if text := prefix.String(); !seen[text] {
			seen[text] = true
			canonical = append(canonical, text)
		}
	}
	if len(canonical) > MaxDBAllowedSources {
		return DBSourceAllowList{}, fmt.Errorf("%w: at most %d addresses or ranges per project", ErrInvalidDBSourceAllowList, MaxDBAllowedSources)
	}
	slices.SortFunc(canonical, compareSources)
	return DBSourceAllowList{Access: DBSourceAccessListed, Sources: canonical}, nil
}

func parseAllowedSource(raw string) (netip.Prefix, error) {
	text := strings.TrimSpace(raw)
	prefix, err := parsePrefixOrAddr(text)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("%w: %q is not an address or CIDR range", ErrInvalidDBSourceAllowList, text)
	}
	if err := checkAllowedPrefix(text, prefix); err != nil {
		return netip.Prefix{}, err
	}
	return prefix, nil
}

func parsePrefixOrAddr(text string) (netip.Prefix, error) {
	if strings.Contains(text, "/") {
		return netip.ParsePrefix(text)
	}
	addr, err := netip.ParseAddr(text)
	if err != nil {
		return netip.Prefix{}, err
	}
	if addr.Zone() != "" {
		return netip.Prefix{}, errors.New("zoned address")
	}
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

func checkAllowedPrefix(text string, prefix netip.Prefix) error {
	addr := prefix.Addr()
	if addr.Is4In6() {
		return fmt.Errorf("%w: %q: write the address in its IPv4 form", ErrInvalidDBSourceAllowList, text)
	}
	if prefix.Bits() == 0 {
		return fmt.Errorf("%w: %q is every address; use allow any address instead", ErrInvalidDBSourceAllowList, text)
	}
	if masked := prefix.Masked(); masked != prefix {
		return fmt.Errorf("%w: %q has host bits set; did you mean %s?", ErrInvalidDBSourceAllowList, text, masked)
	}
	if addr.Is4() && prefix.Bits() < minIPv4Prefix {
		return fmt.Errorf("%w: %q is wider than /%d; use allow any address instead", ErrInvalidDBSourceAllowList, text, minIPv4Prefix)
	}
	if addr.Is6() && prefix.Bits() < minIPv6Prefix {
		return fmt.Errorf("%w: %q is wider than /%d; use allow any address instead", ErrInvalidDBSourceAllowList, text, minIPv6Prefix)
	}
	if reason := unreachableSource(addr); reason != "" {
		return fmt.Errorf("%w: %q is a %s address, which never reaches the public port", ErrInvalidDBSourceAllowList, text, reason)
	}
	return nil
}

// unreachableSource names the address kinds no outside client connects from.
func unreachableSource(addr netip.Addr) string {
	switch {
	case addr.IsLoopback():
		return "loopback"
	case addr.IsUnspecified():
		return "unspecified"
	case addr.IsLinkLocalUnicast():
		return "link-local"
	case addr.IsMulticast():
		return "multicast"
	}
	return ""
}

// compareSources orders IPv4 before IPv6, then by address and prefix length.
func compareSources(left, right string) int {
	a, b := netip.MustParsePrefix(left), netip.MustParsePrefix(right)
	if order := a.Addr().Compare(b.Addr()); order != 0 {
		return order
	}
	return a.Bits() - b.Bits()
}
