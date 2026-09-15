package byoc

import (
	"net/netip"
	"testing"
)

func addrs(list ...string) []netip.Addr {
	out := make([]netip.Addr, 0, len(list))
	for _, s := range list {
		out = append(out, netip.MustParseAddr(s))
	}
	return out
}

func TestParseAllowlist_Empty(t *testing.T) {
	for _, raw := range []string{"", "   ", ",", " , "} {
		p, err := ParseAllowlist(raw)
		if err != nil {
			t.Fatalf("ParseAllowlist(%q) error = %v", raw, err)
		}
		if !p.Empty() {
			t.Errorf("ParseAllowlist(%q) should be empty", raw)
		}
	}
}

func TestParseAllowlist_Entries(t *testing.T) {
	p, err := ParseAllowlist(" 203.0.113.0/24, 198.51.100.7 ,db.example.com, *.rds.amazonaws.com, 2001:db8::/32 ")
	if err != nil {
		t.Fatalf("ParseAllowlist error = %v", err)
	}
	if p.Empty() {
		t.Fatal("expected non-empty policy")
	}
	if got := len(p.cidrs); got != 3 {
		t.Errorf("cidrs = %d, want 3 (cidr, single ip, v6 cidr)", got)
	}
	if got := len(p.hosts); got != 2 {
		t.Errorf("hosts = %d, want 2", got)
	}
}

func TestParseAllowlist_Invalid(t *testing.T) {
	invalid := []string{
		"10.0.0.0/33",           // bad prefix length
		"not a host name",       // whitespace inside
		"*.",                    // wildcard without suffix
		"*",                     // bare wildcard is not an allowlist
		"db.example.com:5432",   // port is not part of the target
		"a.example.com,b.exa=m", // metacharacter
	}
	for _, raw := range invalid {
		if _, err := ParseAllowlist(raw); err == nil {
			t.Errorf("ParseAllowlist(%q) should fail", raw)
		}
	}
}

func TestPolicy_EmptyPermitsEverything(t *testing.T) {
	var p Policy
	if !p.Permits("anything.example", addrs("8.8.8.8")) {
		t.Error("empty policy must permit any target")
	}
}

func TestPolicy_Permits(t *testing.T) {
	p, err := ParseAllowlist("203.0.113.0/24, 198.51.100.7, db.example.com, *.rds.amazonaws.com")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		host  string
		addrs []netip.Addr
		want  bool
	}{
		{"ip in cidr", "203.0.113.10", addrs("203.0.113.10"), true},
		{"all resolved in cidr", "any.example", addrs("203.0.113.10", "203.0.113.11"), true},
		{"one resolved outside cidr", "any.example", addrs("203.0.113.10", "8.8.8.8"), false},
		{"single ip entry", "198.51.100.7", addrs("198.51.100.7"), true},
		{"single ip neighbour", "198.51.100.8", addrs("198.51.100.8"), false},
		{"exact host", "db.example.com", addrs("8.8.8.8"), true},
		{"exact host case-insensitive", "DB.Example.COM", addrs("8.8.8.8"), true},
		{"exact host trailing dot", "db.example.com.", addrs("8.8.8.8"), true},
		{"wildcard subdomain", "prod.rds.amazonaws.com", addrs("8.8.8.8"), true},
		{"wildcard deeper subdomain", "a.b.rds.amazonaws.com", addrs("8.8.8.8"), true},
		{"wildcard does not match apex", "rds.amazonaws.com", addrs("8.8.8.8"), false},
		{"wildcard does not match lookalike", "evilrds.amazonaws.com", addrs("8.8.8.8"), false},
		{"unlisted host public ip", "other.example", addrs("8.8.8.8"), false},
		{"no addresses", "db.example.com", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := p.Permits(tc.host, tc.addrs); got != tc.want {
				t.Errorf("Permits(%q, %v) = %v, want %v", tc.host, tc.addrs, got, tc.want)
			}
		})
	}
}
