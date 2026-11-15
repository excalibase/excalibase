package abuse

import (
	"context"
	"errors"
	"net/netip"

	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/middleware"
	"time"
)

// DefaultSignupsPerHour is how many accounts one network may open per hour.
const DefaultSignupsPerHour = 10

// SignupNetworkKey names the network an address belongs to: its /24 for IPv4
// and its /64 for IPv6, so rotating inside a block buys nothing. An address
// that does not parse keys on itself rather than on "", which would switch
// the limit off.
func SignupNetworkKey(address string) string {
	ip, err := netip.ParseAddr(address)
	if err != nil {
		return "raw:" + address
	}
	ip = ip.Unmap()
	bits := 64
	if ip.Is4() {
		bits = 24
	}
	prefix, err := ip.Prefix(bits)
	if err != nil {
		return "raw:" + address
	}
	return "net:" + prefix.String()
}

// ErrSignupRateLimited is a network that has opened its share of accounts this hour.
var ErrSignupRateLimited = errors.New("too many sign-ups from this network; try again later")

// SignupGuard is the sign-up side of EXC-755: no throwaway mail, and a limit per network.
type SignupGuard struct {
	flags   features.Flags
	email   *EmailPolicy
	limiter *middleware.KeyLimiter
}

// NewSignupGuard allows perHour sign-ups per network per hour.
func NewSignupGuard(flags features.Flags, email *EmailPolicy, perHour int) *SignupGuard {
	if perHour < 1 {
		perHour = DefaultSignupsPerHour
	}
	return &SignupGuard{flags: flags, email: email, limiter: middleware.NewKeyLimiter(perHour, time.Hour)}
}

// CheckEmail refuses an address that may not open an account; nil guard allows all.
func (g *SignupGuard) CheckEmail(ctx context.Context, address string) error {
	if g == nil || !features.Enabled(ctx, g.flags, features.SignupGuard) {
		return nil
	}
	return g.email.Check(ctx, address)
}

// AllowSignup takes one sign-up from the address's network budget.
func (g *SignupGuard) AllowSignup(ctx context.Context, clientAddr string) bool {
	if g == nil || !features.Enabled(ctx, g.flags, features.SignupGuard) {
		return true
	}
	return g.limiter.Allow(SignupNetworkKey(clientAddr))
}
