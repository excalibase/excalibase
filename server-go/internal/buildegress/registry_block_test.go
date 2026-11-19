package buildegress

import (
	"bufio"
	"errors"
	"strings"
	"testing"
)

// ttl.sh and everything under it is refused before a lookup; made-up names only, nothing is contacted.
func TestPolicyRefusesABlockedRegistryAndItsSubdomains(t *testing.T) {
	policy := newTestPolicy(t, nil, nil)
	resolver := fakeResolver{"ttl.sh": addrs("93.184.216.34"), "x.ttl.sh": addrs("93.184.216.34"), "TTL.SH": addrs("93.184.216.34")}
	for _, host := range []string{"ttl.sh", "x.ttl.sh", "a.b.ttl.sh", "TTL.SH", "ttl.sh."} {
		for _, port := range []string{"443", "80"} {
			_, err := policy.Resolve(t.Context(), resolver, host, port)
			if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "ttl.sh") {
				t.Errorf("%s:%s: err = %v, want a refusal naming the registry", host, port, err)
			}
		}
	}
}

func TestPolicyRefusesABlockedRegistryEvenWhenTheOperatorAllowListsIt(t *testing.T) {
	policy := newTestPolicy(t, nil, []string{"ttl.sh:443", "x.ttl.sh:443"})
	resolver := fakeResolver{"ttl.sh": addrs("93.184.216.34"), "x.ttl.sh": addrs("93.184.216.34")}
	for _, host := range []string{"ttl.sh", "x.ttl.sh"} {
		if _, err := policy.Resolve(t.Context(), resolver, host, "443"); !errors.Is(err, ErrRefused) {
			t.Errorf("%s: err = %v, want refused", host, err)
		}
	}
}

func TestPolicyAdmitsAHostThatOnlyLooksLikeABlockedRegistry(t *testing.T) {
	policy := newTestPolicy(t, nil, nil)
	resolver := fakeResolver{"ttl.shop.example": addrs("93.184.216.34"), "nottl.sh": addrs("93.184.216.34"), "ttl.sh.example.com": addrs("93.184.216.34")}
	for _, host := range []string{"ttl.shop.example", "nottl.sh", "ttl.sh.example.com"} {
		if _, err := policy.Resolve(t.Context(), resolver, host, "443"); err != nil {
			t.Errorf("%s refused: %v", host, err)
		}
	}
}

func TestProxyRefusesConnectToABlockedRegistryWithoutDialling(t *testing.T) {
	upstream := upstreamServer(t)
	resolver := fakeResolver{"ttl.sh": addrs("93.184.216.34"), "x.ttl.sh": addrs("93.184.216.34"), "ttl.shop.example": addrs("93.184.216.34")}
	proxy, dialer, logs := startProxy(t, upstream.Listener.Addr().String(), resolver)
	for _, target := range []string{"ttl.sh:443", "x.ttl.sh:443"} {
		conn := connect(t, proxy, target)
		status, _ := bufio.NewReader(conn).ReadString('\n')
		conn.Close()
		if !strings.Contains(status, "403") {
			t.Fatalf("CONNECT %s answered %q", target, status)
		}
	}
	if len(dialer.dials()) != 0 {
		t.Fatalf("dialled %v", dialer.dials())
	}
	if !strings.Contains(logs.String(), "ttl.sh:443") {
		t.Fatalf("refusals not logged: %q", logs.String())
	}
	conn := connect(t, proxy, "ttl.shop.example:443")
	status, _ := bufio.NewReader(conn).ReadString('\n')
	conn.Close()
	if !strings.Contains(status, "200") {
		t.Fatalf("CONNECT ttl.shop.example answered %q, want a tunnel", status)
	}
}
