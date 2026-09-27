// Package clientaddr resolves the address a request really came from. The
// X-Forwarded-For header is believed only from configured trusted proxies and
// only from the right: entries a client wrote itself sit on the left.
package clientaddr

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
)

type ctxKey struct{}

// ParseTrustedProxies parses a comma-separated CIDR list (bare IPs are hosts).
// Empty trusts nobody. A catch-all range is refused: every client could then
// write its own address.
func ParseTrustedProxies(raw string) ([]*net.IPNet, error) {
	var nets []*net.IPNet
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !strings.Contains(part, "/") {
			part = hostCIDR(part)
		}
		_, network, err := net.ParseCIDR(part)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy cidr %q: %w", part, err)
		}
		if ones, _ := network.Mask.Size(); ones == 0 {
			return nil, fmt.Errorf("trusted proxy cidr %q trusts every address", part)
		}
		nets = append(nets, network)
	}
	return nets, nil
}

func hostCIDR(ip string) string {
	if strings.Contains(ip, ":") {
		return ip + "/128"
	}
	return ip + "/32"
}

// Resolve returns the client address. The peer is authoritative unless it is
// a trusted proxy; then X-Forwarded-For is walked from the right, skipping
// trusted hops, and the first untrusted hop is the client. An unparseable hop
// reached before that falls back to the peer.
func Resolve(r *http.Request, trusted []*net.IPNet) string {
	peer := peerIP(r.RemoteAddr)
	if len(trusted) == 0 || !contains(trusted, net.ParseIP(peer)) {
		return peer
	}
	hops := forwardedHops(r.Header.Values("X-Forwarded-For"))
	var leftmost net.IP
	for i := len(hops) - 1; i >= 0; i-- {
		ip := net.ParseIP(hops[i])
		if ip == nil {
			return peer
		}
		if !contains(trusted, ip) {
			return ip.String()
		}
		leftmost = ip
	}
	if leftmost == nil {
		return peer
	}
	return leftmost.String()
}

// Middleware resolves the client address once and records it on the request.
func Middleware(trusted []*net.IPNet) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), ctxKey{}, Resolve(r, trusted))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// FromRequest returns the address Middleware recorded, or the peer address
// when the middleware did not run. It never reads X-Forwarded-For itself.
func FromRequest(r *http.Request) string {
	if addr, ok := r.Context().Value(ctxKey{}).(string); ok {
		return addr
	}
	return peerIP(r.RemoteAddr)
}

func forwardedHops(lines []string) []string {
	var hops []string
	for _, line := range lines {
		for _, part := range strings.Split(line, ",") {
			if part = strings.TrimSpace(part); part != "" {
				hops = append(hops, part)
			}
		}
	}
	return hops
}

func peerIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

func contains(nets []*net.IPNet, ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, network := range nets {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}
