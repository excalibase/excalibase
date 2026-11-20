package registryblock

import "testing"

func TestIsHost_MatchesOnlyTheExactHost(t *testing.T) {
	for _, host := range []string{"ttl.sh", "TTL.SH", "ttl.sh."} {
		if !IsHost(host) {
			t.Errorf("%q passed, want blocked", host)
		}
	}
	for _, host := range []string{"x.ttl.sh", "ttl.shop.example", "nottl.sh", "ttl.sh.example.com", "", "ghcr.io"} {
		if IsHost(host) {
			t.Errorf("%q blocked, want allowed", host)
		}
	}
}

func TestIsHostOrSubdomain_MatchesTheHostAndEverythingUnderIt(t *testing.T) {
	for _, host := range []string{"ttl.sh", "x.ttl.sh", "a.b.TTL.SH", "x.ttl.sh."} {
		if !IsHostOrSubdomain(host) {
			t.Errorf("%q passed, want blocked", host)
		}
	}
	for _, host := range []string{"ttl.shop.example", "nottl.sh", "xttl.sh", "ttl.sh.example.com", "sh", "", "ghcr.io"} {
		if IsHostOrSubdomain(host) {
			t.Errorf("%q blocked, want allowed", host)
		}
	}
}

func TestHosts_ReturnsACopy(t *testing.T) {
	got := Hosts()
	got[0] = "changed.example"
	if !IsHost("ttl.sh") {
		t.Error("changing the returned slice changed the list")
	}
}
