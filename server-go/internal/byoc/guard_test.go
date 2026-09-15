package byoc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// fakeResolver answers from a fixed table, or from a scripted sequence of
// answers per host (for rebind scenarios). It counts lookups so tests can
// assert that validation and dial share exactly one resolution.
type fakeResolver struct {
	mu       sync.Mutex
	table    map[string][]string
	sequence map[string][][]string
	calls    map[string]int
}

func newFakeResolver(table map[string][]string) *fakeResolver {
	return &fakeResolver{table: table, sequence: map[string][][]string{}, calls: map[string]int{}}
}

func (f *fakeResolver) LookupNetIP(_ context.Context, _ string, host string) ([]netip.Addr, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[host]++
	var answer []string
	if seq, ok := f.sequence[host]; ok && len(seq) > 0 {
		answer = seq[0]
		f.sequence[host] = seq[1:]
	} else {
		var ok bool
		answer, ok = f.table[host]
		if !ok {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
	}
	return addrs(answer...), nil
}

func (f *fakeResolver) lookups(host string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[host]
}

// recordingDialer captures the address the guard decided to dial and hands
// back one end of a pipe so no real socket is opened.
type recordingDialer struct {
	mu    sync.Mutex
	addrs []string
}

func (d *recordingDialer) dial(_ context.Context, _ string, address string) (net.Conn, error) {
	d.mu.Lock()
	d.addrs = append(d.addrs, address)
	d.mu.Unlock()
	client, server := net.Pipe()
	go func() { _ = server.Close() }()
	return client, nil
}

func (d *recordingDialer) dialed() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.addrs...)
}

func newTestGuard(t *testing.T, policy Policy, resolver Resolver) (*Guard, *recordingDialer) {
	t.Helper()
	rec := &recordingDialer{}
	g := NewGuard(policy, resolver)
	g.dialContext = rec.dial
	return g, rec
}

func TestGuard_ValidateHost(t *testing.T) {
	resolver := newFakeResolver(map[string][]string{
		"public.example":        {"203.0.113.10"},
		"public-v6.example":     {"2001:4860:4860::8888"},
		"private.example":       {"10.0.0.5"},
		"metadata.example":      {"169.254.169.254"},
		"metadata-v6.example":   {"fd00:ec2::254"},
		"mapped.example":        {"::ffff:127.0.0.1"},
		"mixed.example":         {"203.0.113.9", "127.0.0.1"},
		"link-local-v6.example": {"fe80::1"},
	})
	g, _ := newTestGuard(t, Policy{}, resolver)
	ctx := context.Background()

	accepted := []string{"public.example", "public-v6.example", "8.8.8.8", "2001:4860:4860::8888", "[2001:4860:4860::8888]"}
	for _, host := range accepted {
		if err := g.ValidateHost(ctx, host); err != nil {
			t.Errorf("ValidateHost(%q) = %v, want nil", host, err)
		}
	}

	rejected := []struct {
		host string
		want error
	}{
		{"private.example", ErrInternalAddress},
		{"metadata.example", ErrInternalAddress},
		{"metadata-v6.example", ErrInternalAddress},
		{"mapped.example", ErrInternalAddress},
		{"mixed.example", ErrInternalAddress}, // ANY internal answer blocks
		{"link-local-v6.example", ErrInternalAddress},
		{"missing.example", ErrUnresolvable},
		{"localhost", ErrInvalidHost},
		{"db.localhost", ErrInvalidHost},
		{"svc.internal", ErrInvalidHost},
		{"node.local", ErrInvalidHost},
		{"a.example.com,b.example.com", ErrInvalidHost},
		{"127.0.0.1", ErrInternalAddress},
		{"::1", ErrInternalAddress},
		{"[::1]", ErrInternalAddress},
		{"::ffff:10.0.0.1", ErrInternalAddress},
		{"fe80::1%eth0", ErrInvalidHost},
		{"0.0.0.0", ErrInternalAddress},
	}
	for _, tc := range rejected {
		err := g.ValidateHost(ctx, tc.host)
		if !errors.Is(err, tc.want) {
			t.Errorf("ValidateHost(%q) = %v, want %v", tc.host, err, tc.want)
		}
	}
}

func TestGuard_ValidateHost_ErrorsDoNotLeakResolvedAddresses(t *testing.T) {
	resolver := newFakeResolver(map[string][]string{"rebind.example": {"10.42.7.9"}})
	g, _ := newTestGuard(t, Policy{}, resolver)
	err := g.ValidateHost(context.Background(), "rebind.example")
	if err == nil {
		t.Fatal("expected rejection")
	}
	if strings.Contains(err.Error(), "10.42.7.9") {
		t.Errorf("user-facing error leaks the resolved internal address: %v", err)
	}
}

func TestGuard_Dial_RefusesRebindToInternal(t *testing.T) {
	resolver := newFakeResolver(nil)
	resolver.sequence["rebind.example"] = [][]string{
		{"203.0.113.10"},    // answer at validation time
		{"169.254.169.254"}, // answer at dial time (rebound)
	}
	g, rec := newTestGuard(t, Policy{}, resolver)

	if err := g.ValidateHost(context.Background(), "rebind.example"); err != nil {
		t.Fatalf("validation should pass on the public answer: %v", err)
	}
	conn, err := g.Dial("tcp", "rebind.example:5432")
	if !errors.Is(err, ErrInternalAddress) {
		t.Fatalf("Dial after rebind = %v, want ErrInternalAddress", err)
	}
	if conn != nil {
		t.Error("no connection must be returned on refusal")
	}
	if got := rec.dialed(); len(got) != 0 {
		t.Errorf("nothing must be dialed on refusal, dialed %v", got)
	}
}

func TestGuard_Dial_UsesTheAddressItValidated(t *testing.T) {
	resolver := newFakeResolver(map[string][]string{"db.example": {"203.0.113.10", "203.0.113.11"}})
	g, rec := newTestGuard(t, Policy{}, resolver)

	conn, err := g.DialTimeout("tcp", "db.example:5432", time.Second)
	if err != nil {
		t.Fatalf("DialTimeout = %v", err)
	}
	_ = conn.Close()
	if got := rec.dialed(); len(got) != 1 || got[0] != "203.0.113.10:5432" {
		t.Errorf("dialed %v, want exactly the validated address 203.0.113.10:5432", got)
	}
	if n := resolver.lookups("db.example"); n != 1 {
		t.Errorf("dial must resolve exactly once (validate + dial share one answer), got %d", n)
	}
}

func TestGuard_Dial_IPv6LiteralIsBracketed(t *testing.T) {
	g, rec := newTestGuard(t, Policy{}, newFakeResolver(nil))
	conn, err := g.Dial("tcp", "[2001:4860:4860::8888]:5432")
	if err != nil {
		t.Fatalf("Dial = %v", err)
	}
	_ = conn.Close()
	if got := rec.dialed(); len(got) != 1 || got[0] != "[2001:4860:4860::8888]:5432" {
		t.Errorf("dialed %v, want [2001:4860:4860::8888]:5432", got)
	}
}

func TestGuard_Dial_RefusesMixedAnswerAndLiterals(t *testing.T) {
	resolver := newFakeResolver(map[string][]string{"mixed.example": {"203.0.113.9", "10.0.0.1"}})
	g, rec := newTestGuard(t, Policy{}, resolver)
	cases := []struct {
		address string
		want    error
	}{
		{"mixed.example:5432", ErrInternalAddress},
		{"127.0.0.1:5432", ErrInternalAddress},
		{"[::1]:5432", ErrInternalAddress},
		{"[::ffff:169.254.169.254]:5432", ErrInternalAddress},
		{"missing.example:5432", ErrUnresolvable},
		{"no-port.example", ErrInvalidHost},
		{"a.example,b.example:5432", ErrInvalidHost},
		{"/var/run/postgresql/.s.PGSQL.5432", ErrInvalidHost},
	}
	for _, tc := range cases {
		if _, err := g.Dial("tcp", tc.address); !errors.Is(err, tc.want) {
			t.Errorf("Dial(%q) = %v, want %v", tc.address, err, tc.want)
		}
	}
	if got := rec.dialed(); len(got) != 0 {
		t.Errorf("nothing must be dialed, dialed %v", got)
	}
}

func TestGuard_Dial_RefusesNonTCPNetworks(t *testing.T) {
	g, rec := newTestGuard(t, Policy{}, newFakeResolver(nil))
	for _, network := range []string{"unix", "udp", "unixgram"} {
		if _, err := g.Dial(network, "8.8.8.8:5432"); !errors.Is(err, ErrInvalidHost) {
			t.Errorf("Dial(%q) = %v, want ErrInvalidHost", network, err)
		}
	}
	if got := rec.dialed(); len(got) != 0 {
		t.Errorf("nothing must be dialed, dialed %v", got)
	}
}

func TestGuard_Allowlist_EnforcedOnValidateAndDial(t *testing.T) {
	policy, err := ParseAllowlist("203.0.113.0/24, *.rds.amazonaws.com")
	if err != nil {
		t.Fatal(err)
	}
	resolver := newFakeResolver(map[string][]string{
		"listed.rds.amazonaws.com": {"8.8.8.8"},
		"unlisted.example":         {"8.8.4.4"},
		"in-cidr.example":          {"203.0.113.77"},
	})
	g, rec := newTestGuard(t, policy, resolver)
	ctx := context.Background()

	for _, host := range []string{"listed.rds.amazonaws.com", "in-cidr.example", "203.0.113.5"} {
		if err := g.ValidateHost(ctx, host); err != nil {
			t.Errorf("ValidateHost(%q) = %v, want nil (allowlisted)", host, err)
		}
	}
	for _, host := range []string{"unlisted.example", "8.8.8.8"} {
		if err := g.ValidateHost(ctx, host); !errors.Is(err, ErrNotAllowlisted) {
			t.Errorf("ValidateHost(%q) = %v, want ErrNotAllowlisted", host, err)
		}
	}
	if _, err := g.Dial("tcp", "unlisted.example:5432"); !errors.Is(err, ErrNotAllowlisted) {
		t.Errorf("Dial(unlisted) = %v, want ErrNotAllowlisted", err)
	}
	if got := rec.dialed(); len(got) != 0 {
		t.Errorf("nothing must be dialed for an unlisted target, dialed %v", got)
	}
	// Allowlist never overrides the internal block.
	private, _ := ParseAllowlist("10.0.0.0/8")
	g2, _ := newTestGuard(t, private, newFakeResolver(nil))
	if err := g2.ValidateHost(ctx, "10.1.2.3"); !errors.Is(err, ErrInternalAddress) {
		t.Errorf("allowlisted private range must still be blocked, got %v", err)
	}
}

func TestGuard_ValidateCredentials_CombinesSyntaxAndNetworkChecks(t *testing.T) {
	resolver := newFakeResolver(map[string][]string{"db.example": {"203.0.113.10"}, "bad.example": {"127.0.0.1"}})
	g, _ := newTestGuard(t, Policy{}, resolver)
	ctx := context.Background()

	good := Credentials{Host: "db.example", Database: "app", Username: "u", Password: "p"}
	if err := g.ValidateCredentials(ctx, good); err != nil {
		t.Errorf("ValidateCredentials(good) = %v", err)
	}
	if err := g.ValidateCredentials(ctx, Credentials{Host: "bad.example", Database: "app", Username: "u", Password: "p"}); !errors.Is(err, ErrInternalAddress) {
		t.Errorf("internal host = %v, want ErrInternalAddress", err)
	}
	injected := good
	injected.Username = "u host=127.0.0.1"
	if err := g.ValidateCredentials(ctx, injected); !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("injected username = %v, want ErrInvalidCredential", err)
	}
	if n := resolver.lookups("db.example"); n != 1 {
		t.Errorf("good credentials should resolve exactly once, got %d", n)
	}
}

type fakeModes struct {
	inst *domain.DatabaseInstance
	err  error
}

func (f fakeModes) FindByProjectID(string) (*domain.DatabaseInstance, error) { return f.inst, f.err }

func TestGuard_OpenProjectDB_GuardsBYOCOnly(t *testing.T) {
	g, rec := newTestGuard(t, Policy{}, newFakeResolver(nil))
	dsn := "host=127.0.0.1 port=1 user=u password=p dbname=d sslmode=disable connect_timeout=1"

	byocDB, err := g.OpenProjectDB(fakeModes{inst: &domain.DatabaseInstance{DeploymentMode: domain.ModeBYOC}}, "p1", dsn)
	if err != nil {
		t.Fatalf("OpenProjectDB(byoc) = %v", err)
	}
	defer byocDB.Close()
	if err := byocDB.PingContext(context.Background()); !errors.Is(err, ErrInternalAddress) {
		t.Errorf("BYOC ping through the guard = %v, want ErrInternalAddress", err)
	}
	if got := rec.dialed(); len(got) != 0 {
		t.Errorf("nothing must be dialed for a refused BYOC target, dialed %v", got)
	}

	managedDB, err := g.OpenProjectDB(fakeModes{inst: &domain.DatabaseInstance{DeploymentMode: domain.ModeK8s}}, "p2", dsn)
	if err != nil {
		t.Fatalf("OpenProjectDB(managed) = %v", err)
	}
	defer managedDB.Close()
	if err := managedDB.PingContext(context.Background()); errors.Is(err, ErrInternalAddress) {
		t.Errorf("managed projects dial cluster-internal hosts and must not be guarded: %v", err)
	}
}

// Managed (k8s/docker) projects live on cluster-internal hosts, so the guard
// keys strictly off DeploymentMode == byoc. Anything else — legacy rows with
// an empty mode, a missing row, or a handler with no store wired — takes the
// plain dial path exactly as before this change.
func TestGuard_OpenProjectDB_NonBYOCIsNotGuarded(t *testing.T) {
	g, _ := newTestGuard(t, Policy{}, newFakeResolver(nil))
	dsn := "host=127.0.0.1 port=1 user=u password=p dbname=d sslmode=disable connect_timeout=1"
	cases := []struct {
		name  string
		modes ModeLookup
	}{
		{"missing row", fakeModes{err: fmt.Errorf("not found")}},
		{"nil instance", fakeModes{}},
		{"legacy empty mode", fakeModes{inst: &domain.DatabaseInstance{}}},
		{"no store wired", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, err := g.OpenProjectDB(tc.modes, "p", dsn)
			if err != nil {
				t.Fatalf("OpenProjectDB = %v", err)
			}
			defer db.Close()
			if err := db.PingContext(context.Background()); errors.Is(err, ErrInternalAddress) {
				t.Errorf("non-BYOC project must not be guarded, got %v", err)
			}
		})
	}
}

func TestDefaultGuard_UsesRealResolverAndEmptyPolicy(t *testing.T) {
	g := Default()
	if g == nil || g.resolver == nil {
		t.Fatal("Default() must return a usable guard")
	}
	if !g.policy.Empty() {
		t.Error("Default() must not carry an allowlist")
	}
	if err := g.ValidateHost(context.Background(), "127.0.0.1"); !errors.Is(err, ErrInternalAddress) {
		t.Errorf("Default().ValidateHost(loopback) = %v", err)
	}
}

func TestGuard_ResolvePinned_ReturnsTheValidatedAddress(t *testing.T) {
	resolver := newFakeResolver(map[string][]string{"db.example": {"203.0.113.10", "203.0.113.11"}})
	g, _ := newTestGuard(t, Policy{}, resolver)

	addr, err := g.ResolvePinned(context.Background(), "db.example", netip.Addr{})
	if err != nil {
		t.Fatalf("ResolvePinned = %v", err)
	}
	if addr.String() != "203.0.113.10" {
		t.Errorf("pinned %s, want the first validated answer 203.0.113.10", addr)
	}
	if n := resolver.lookups("db.example"); n != 1 {
		t.Errorf("pinning must resolve exactly once, got %d lookups", n)
	}
}

func TestGuard_ResolvePinned_KeepsTheCurrentPinWhileItIsStillAnAnswer(t *testing.T) {
	resolver := newFakeResolver(map[string][]string{"db.example": {"203.0.113.10", "203.0.113.11"}})
	g, _ := newTestGuard(t, Policy{}, resolver)

	current := netip.MustParseAddr("203.0.113.11")
	addr, err := g.ResolvePinned(context.Background(), "db.example", current)
	if err != nil || addr != current {
		t.Fatalf("ResolvePinned = %s, %v; want the current pin %s", addr, err, current)
	}
	stale := netip.MustParseAddr("203.0.113.99")
	addr, err = g.ResolvePinned(context.Background(), "db.example", stale)
	if err != nil || addr.String() != "203.0.113.10" {
		t.Fatalf("ResolvePinned with a stale pin = %s, %v; want the first answer", addr, err)
	}
}

func TestGuard_ResolvePinned_RefusesRebindToInternal(t *testing.T) {
	resolver := newFakeResolver(nil)
	resolver.sequence["rebind.example"] = [][]string{
		{"203.0.113.10"}, // registration
		{"10.0.0.5"},     // deploy time (rebound)
	}
	g, _ := newTestGuard(t, Policy{}, resolver)
	if err := g.ValidateHost(context.Background(), "rebind.example"); err != nil {
		t.Fatalf("registration should pass: %v", err)
	}
	addr, err := g.ResolvePinned(context.Background(), "rebind.example", netip.MustParseAddr("203.0.113.10"))
	if !errors.Is(err, ErrInternalAddress) {
		t.Fatalf("ResolvePinned after rebind = %v, want ErrInternalAddress", err)
	}
	if addr.IsValid() {
		t.Errorf("no address must be pinned on refusal, got %s", addr)
	}
	if strings.Contains(err.Error(), "10.0.0.5") {
		t.Errorf("error leaks the resolved address: %v", err)
	}
}

func TestIsBYOC(t *testing.T) {
	byocProject := fakeModes{inst: &domain.DatabaseInstance{ProjectID: "byoc", DeploymentMode: domain.ModeBYOC}}
	managed := fakeModes{inst: &domain.DatabaseInstance{ProjectID: "managed", DeploymentMode: domain.ModeK8s}}
	missing := fakeModes{err: errors.New("not found")}
	if !IsBYOC(byocProject, "byoc") || IsBYOC(managed, "managed") || IsBYOC(missing, "missing") || IsBYOC(nil, "byoc") {
		t.Error("IsBYOC must be true only for a known project in BYOC mode")
	}
}
