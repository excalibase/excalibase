package byoc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/lib/pq"
)

// ErrUnresolvable marks a hostname that could not be resolved; the platform
// fails closed because it cannot prove the target is public.
var ErrUnresolvable = errors.New("host could not be resolved")

// defaultDialTimeout bounds a guarded dial when lib/pq gives no connect_timeout.
const defaultDialTimeout = 10 * time.Second

// Resolver is the subset of *net.Resolver the guard needs; tests inject fakes.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// ModeLookup answers which deployment mode a project runs in. Satisfied by
// storage.InstanceStore.
type ModeLookup interface {
	FindByProjectID(projectID string) (*domain.DatabaseInstance, error)
}

// Guard validates BYOC targets at registration and enforces the same policy
// at dial time. It satisfies pq.Dialer.
type Guard struct {
	resolver    Resolver
	policy      Policy
	dialContext func(ctx context.Context, network, address string) (net.Conn, error)
}

// NewGuard builds a guard with the given allowlist. A nil resolver uses the
// system resolver.
func NewGuard(policy Policy, resolver Resolver) *Guard {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	return &Guard{
		resolver:    resolver,
		policy:      policy,
		dialContext: (&net.Dialer{}).DialContext,
	}
}

var defaultGuard = NewGuard(Policy{}, nil)

// Default is the guard used when no operator policy is wired: system
// resolver, no allowlist, all internal ranges blocked.
func Default() *Guard { return defaultGuard }

// ValidateHost checks that host is a single public hostname or IP literal
// whose every resolved address is public and allowlisted.
func (g *Guard) ValidateHost(ctx context.Context, host string) error {
	_, err := g.resolveTarget(ctx, host)
	return err
}

// ValidateCredentials runs the syntax checks and then ValidateHost.
func (g *Guard) ValidateCredentials(ctx context.Context, creds Credentials) error {
	if err := creds.Validate(); err != nil {
		return err
	}
	return g.ValidateHost(ctx, creds.Host)
}

// ResolvePinned validates host exactly like a dial would and returns the
// address a component that cannot dial through the guard (the function
// runtime) must connect to instead of the name. current is the address the
// caller is pinned to today (zero when none): it is kept while it is still
// among the validated answers, so round-robin records do not flap the pin.
// A name that has been rebound to an internal range is refused and the
// caller keeps its previous pin.
func (g *Guard) ResolvePinned(ctx context.Context, host string, current netip.Addr) (netip.Addr, error) {
	addrs, err := g.resolveTarget(ctx, host)
	if err != nil {
		return netip.Addr{}, err
	}
	if slices.Contains(addrs, current) {
		return current, nil
	}
	return addrs[0], nil
}

// resolveTarget returns the addresses the platform may dial for host. Every
// resolved address must pass classification; one internal answer refuses the
// whole name.
func (g *Guard) resolveTarget(ctx context.Context, host string) ([]netip.Addr, error) {
	if err := ValidateHostSyntax(host); err != nil {
		return nil, err
	}
	addrs, err := g.lookup(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, addr := range addrs {
		if err := ClassifyAddr(addr); err != nil {
			return nil, fmt.Errorf("%w (%q)", err, host)
		}
	}
	if !g.policy.Permits(host, addrs) {
		return nil, fmt.Errorf("%w (%q)", ErrNotAllowlisted, host)
	}
	return addrs, nil
}

func (g *Guard) lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	if addr, ok := parseLiteral(host); ok {
		return []netip.Addr{addr}, nil
	}
	addrs, err := g.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addrs) == 0 {
		return nil, fmt.Errorf("%w (%q)", ErrUnresolvable, host)
	}
	return addrs, nil
}

// Dial implements pq.Dialer with the default timeout.
func (g *Guard) Dial(network, address string) (net.Conn, error) {
	return g.DialTimeout(network, address, defaultDialTimeout)
}

// DialTimeout implements pq.Dialer.
func (g *Guard) DialTimeout(network, address string, timeout time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return g.DialContext(ctx, network, address)
}

// DialContext implements pq.DialerContext, which lib/pq prefers when present.
// It resolves the host once, classifies the answer and dials exactly those
// addresses, so validation and dial cannot disagree. Only tcp is accepted.
func (g *Guard) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, fmt.Errorf("%w: network %q is not allowed", ErrInvalidHost, network)
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("%w: address must be host:port", ErrInvalidHost)
	}
	addrs, err := g.resolveTarget(ctx, host)
	if err != nil {
		return nil, err
	}
	return g.dialFirst(ctx, network, port, addrs)
}

func (g *Guard) dialFirst(ctx context.Context, network, port string, addrs []netip.Addr) (net.Conn, error) {
	var lastErr error
	for _, addr := range addrs {
		conn, err := g.dialContext(ctx, network, net.JoinHostPort(addr.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// OpenDB opens a lib/pq handle whose every connection is dialled through the
// guard. The DSN host stays a hostname so TLS verification is unchanged.
func (g *Guard) OpenDB(dsn string) (*sql.DB, error) {
	connector, err := pq.NewConnector(dsn)
	if err != nil {
		return nil, fmt.Errorf("open connection: %w", err)
	}
	connector.Dialer(g)
	return sql.OpenDB(connector), nil
}

// OpenProjectDB routes BYOC projects through the guard and every other
// deployment mode through the plain driver, since managed projects live on
// cluster-internal hosts that the guard would (correctly) refuse.
func (g *Guard) OpenProjectDB(modes ModeLookup, projectID, dsn string) (*sql.DB, error) {
	if IsBYOC(modes, projectID) {
		return g.OpenDB(dsn)
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open connection: %w", err)
	}
	return db, nil
}

// IsBYOC reports whether the project runs against an externally managed
// database, i.e. whether its outbound connections must go through the guard.
func IsBYOC(modes ModeLookup, projectID string) bool {
	if modes == nil {
		return false
	}
	inst, err := modes.FindByProjectID(projectID)
	return err == nil && inst != nil && inst.DeploymentMode == domain.ModeBYOC
}
