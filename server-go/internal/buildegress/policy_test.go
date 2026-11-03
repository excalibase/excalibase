package buildegress

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
)

type fakeResolver map[string][]netip.Addr

func (f fakeResolver) LookupNetIP(_ context.Context, _ string, host string) ([]netip.Addr, error) {
	addrs, ok := f[host]
	if !ok {
		return nil, errors.New("no such host")
	}
	return addrs, nil
}

func addrs(raw ...string) []netip.Addr {
	out := make([]netip.Addr, 0, len(raw))
	for _, item := range raw {
		out = append(out, netip.MustParseAddr(item))
	}
	return out
}

func newTestPolicy(t *testing.T, extraDeny, allow []string) Policy {
	t.Helper()
	policy, err := NewPolicy(extraDeny, allow)
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	return policy
}

func TestPolicyAdmitsPublicWebPorts(t *testing.T) {
	policy := newTestPolicy(t, nil, nil)
	resolver := fakeResolver{"registry.npmjs.org": addrs("104.16.1.34", "2606:4700::6810:122")}
	for _, port := range []string{"443", "80"} {
		target, err := policy.Resolve(context.Background(), resolver, "registry.npmjs.org", port)
		if err != nil {
			t.Fatalf("port %s refused: %v", port, err)
		}
		if target.String() != "104.16.1.34:"+port {
			t.Fatalf("dials %s, want the checked IPv4 address on %s", target, port)
		}
	}
}

func TestPolicyRefusesOtherPorts(t *testing.T) {
	policy := newTestPolicy(t, nil, nil)
	resolver := fakeResolver{"smtp.example.com": addrs("93.184.216.34")}
	for _, port := range []string{"25", "22", "8080", "0", "x"} {
		if _, err := policy.Resolve(context.Background(), resolver, "smtp.example.com", port); !errors.Is(err, ErrRefused) {
			t.Fatalf("port %s: got %v, want a refusal", port, err)
		}
	}
}

func TestPolicyRefusesPrivateAndSpecialAddresses(t *testing.T) {
	policy := newTestPolicy(t, []string{"203.0.113.0/24"}, nil)
	cases := map[string]string{
		"metadata": "169.254.169.254", "loopback": "127.0.0.1", "ten": "10.1.2.3", "docker bridge": "172.17.0.1",
		"lan": "192.168.1.10", "cgnat": "100.64.0.1", "this network": "0.0.0.0", "multicast": "224.0.0.1",
		"reserved": "240.0.0.1", "extra deny": "203.0.113.9", "broadcast": "255.255.255.255",
		"azure wireserver": "168.63.129.16", "benchmarking": "198.18.0.1", "ietf protocol": "192.0.0.170",
		"documentation": "198.51.100.7",
	}
	for name, ip := range cases {
		t.Run(name, func(t *testing.T) {
			resolver := fakeResolver{"target.example": addrs(ip)}
			_, err := policy.Resolve(context.Background(), resolver, "target.example", "443")
			if !errors.Is(err, ErrRefused) {
				t.Fatalf("%s reached: %v", ip, err)
			}
			if _, err := policy.Resolve(context.Background(), resolver, ip, "443"); !errors.Is(err, ErrRefused) {
				t.Fatalf("literal %s reached: %v", ip, err)
			}
		})
	}
}

// A name answering one public and one private address is refused whole: the
// client could otherwise pick the private one on a retry.
func TestPolicyRefusesANameWithAnyDeniedAddress(t *testing.T) {
	policy := newTestPolicy(t, nil, nil)
	resolver := fakeResolver{"mixed.example": addrs("93.184.216.34", "10.0.0.5")}
	if _, err := policy.Resolve(context.Background(), resolver, "mixed.example", "443"); !errors.Is(err, ErrRefused) {
		t.Fatalf("got %v, want a refusal", err)
	}
}

func TestPolicyIsIPv4Only(t *testing.T) {
	policy := newTestPolicy(t, nil, nil)
	resolver := fakeResolver{"v6only.example": addrs("2606:4700::6810:122")}
	if _, err := policy.Resolve(context.Background(), resolver, "v6only.example", "443"); !errors.Is(err, ErrRefused) {
		t.Fatalf("got %v, want a refusal", err)
	}
	if _, err := policy.Resolve(context.Background(), resolver, "::1", "443"); !errors.Is(err, ErrRefused) {
		t.Fatalf("an IPv6 literal reached: %v", err)
	}
	mapped := fakeResolver{"mapped.example": addrs("::ffff:10.0.0.1")}
	if _, err := policy.Resolve(context.Background(), mapped, "mapped.example", "443"); !errors.Is(err, ErrRefused) {
		t.Fatalf("an IPv4-mapped private address reached: %v", err)
	}
}

func TestPolicyAllowListAdmitsAPrivateHostOnItsPort(t *testing.T) {
	policy := newTestPolicy(t, nil, []string{"git.lan:8080", "10.0.0.7:443"})
	resolver := fakeResolver{"git.lan": addrs("192.168.1.20")}
	target, err := policy.Resolve(context.Background(), resolver, "GIT.lan", "8080")
	if err != nil || target.String() != "192.168.1.20:8080" {
		t.Fatalf("allow-listed host: %v %v", target, err)
	}
	if _, err := policy.Resolve(context.Background(), resolver, "git.lan", "443"); !errors.Is(err, ErrRefused) {
		t.Fatalf("an allow-listed host on another port reached: %v", err)
	}
	if target, err := policy.Resolve(context.Background(), resolver, "10.0.0.7", "443"); err != nil || target.String() != "10.0.0.7:443" {
		t.Fatalf("allow-listed address: %v %v", target, err)
	}
}

func TestPolicyRejectsBadSettings(t *testing.T) {
	if _, err := NewPolicy([]string{"not-a-cidr"}, nil); err == nil {
		t.Fatal("a bad CIDR was accepted")
	}
	for _, entry := range []string{"no-port", "host:0", "host:99999", ":443"} {
		if _, err := NewPolicy(nil, []string{entry}); err == nil {
			t.Fatalf("allow entry %q was accepted", entry)
		}
	}
}

func TestPolicyRefusalNamesTheReason(t *testing.T) {
	policy := newTestPolicy(t, nil, nil)
	_, err := policy.Resolve(context.Background(), fakeResolver{}, "169.254.169.254", "80")
	if err == nil || !strings.Contains(err.Error(), "169.254.169.254") {
		t.Fatalf("refusal %v does not name the address", err)
	}
	if _, err := policy.Resolve(context.Background(), fakeResolver{}, "unknown.example", "443"); err == nil || errors.Is(err, ErrRefused) {
		t.Fatalf("a failed lookup is not a policy refusal: %v", err)
	}
}
