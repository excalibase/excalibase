package publicaddr

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"testing"
)

// liveDeny is a deny list that changes while the dialer runs, as a host's own
// addresses do (EXC-658 on a single host).
type liveDeny struct {
	mu       sync.Mutex
	prefixes []netip.Prefix
}

func (l *liveDeny) get() []netip.Prefix {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]netip.Prefix(nil), l.prefixes...)
}

func (l *liveDeny) set(raw ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prefixes = nil
	for _, value := range raw {
		l.prefixes = append(l.prefixes, netip.MustParsePrefix(value))
	}
}

func newDenySourceDialer(resolver Resolver, deny *liveDeny) (*Dialer, *recordingDialer) {
	recorder := &recordingDialer{}
	dialer := NewDialer(resolver, nil, WithDenySource(deny.get))
	dialer.dialAddress = recorder.dial
	return dialer, recorder
}

func TestDialer_DenySourceRefusesAHostAddressByNameAndLiteral(t *testing.T) {
	deny := &liveDeny{}
	deny.set("198.51.100.1/32", "2001:db8:aa::1/128")
	resolver := &fakeResolver{answers: map[string][][]netip.Addr{"own.example.com": {addrs("203.0.113.5", "198.51.100.1")}}}
	dialer, recorder := newDenySourceDialer(resolver, deny)
	for _, target := range []string{"own.example.com:443", "198.51.100.1:443", "[2001:db8:aa::1]:443", "[::ffff:198.51.100.1]:443"} {
		if _, err := dialer.DialContext(context.Background(), "tcp", target); !errors.Is(err, ErrInternalAddress) {
			t.Errorf("%s: err = %v", target, err)
		}
	}
	if len(recorder.dialed) != 0 {
		t.Errorf("dialled %v", recorder.dialed)
	}
}

func TestDialer_DenySourceIsReadOnEveryConnect(t *testing.T) {
	deny := &liveDeny{}
	dialer, recorder := newDenySourceDialer(&fakeResolver{}, deny)
	if conn, err := dialer.DialContext(context.Background(), "tcp", "198.51.100.7:443"); err != nil {
		t.Fatalf("before the host took the address: %v", err)
	} else {
		conn.Close()
	}
	deny.set("198.51.100.0/24")
	if _, err := dialer.DialContext(context.Background(), "tcp", "198.51.100.7:443"); !errors.Is(err, ErrInternalAddress) {
		t.Errorf("after: err = %v", err)
	}
	if len(recorder.dialed) != 1 {
		t.Errorf("dialled %v", recorder.dialed)
	}
}

func TestDialer_DenySourceGuardsTheSocketPeer(t *testing.T) {
	deny := &liveDeny{}
	deny.set("198.51.100.1/32")
	dialer := NewDialer(nil, nil, WithDenySource(deny.get))
	if err := dialer.control("tcp4", "198.51.100.1:443", nil); !errors.Is(err, ErrInternalAddress) {
		t.Errorf("control = %v", err)
	}
	if err := dialer.control("tcp4", "198.51.100.2:443", nil); err != nil {
		t.Errorf("control of another address = %v", err)
	}
}

func TestDialer_WithoutDenySourceKeepsTheStaticList(t *testing.T) {
	dialer := NewDialer(nil, []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")})
	if err := dialer.Check(netip.MustParseAddr("198.51.100.3")); !errors.Is(err, ErrInternalAddress) {
		t.Errorf("static deny = %v", err)
	}
	if err := dialer.Check(netip.MustParseAddr("203.0.113.3")); err != nil {
		t.Errorf("public = %v", err)
	}
}
