package edgefn

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseEgressHosts_Accepts(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"hostname", []string{"api.stripe.com"}, []string{"api.stripe.com"}},
		{"host with port", []string{"api.stripe.com:8443"}, []string{"api.stripe.com:8443"}},
		{"wildcard suffix", []string{"*.amazonaws.com"}, []string{"*.amazonaws.com"}},
		{"wildcard suffix with port", []string{"*.amazonaws.com:443"}, []string{"*.amazonaws.com:443"}},
		{"public ipv4", []string{"203.0.113.10"}, []string{"203.0.113.10"}},
		{"public ipv4 with port", []string{"203.0.113.10:9000"}, []string{"203.0.113.10:9000"}},
		{"public ipv6 bracketed with port", []string{"[2606:4700::1111]:443"}, []string{"[2606:4700::1111]:443"}},
		{"lowercased, trimmed, deduped, sorted", []string{" B.example.com ", "a.example.com", "b.example.com"}, []string{"a.example.com", "b.example.com"}},
		{"blank entries dropped", []string{"", "  ", "x.example.com"}, []string{"x.example.com"}},
		{"nil is empty", nil, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseEgressHosts(tc.in)
			if err != nil {
				t.Fatalf("ParseEgressHosts(%v) error: %v", tc.in, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseEgressHosts(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseEgressHosts_Rejects(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"bare wildcard", "*"},
		{"wildcard in the middle", "api.*.example.com"},
		{"wildcard with single-label suffix", "*.com"},
		{"wildcard without dot", "*example.com"},
		{"scheme", "https://api.stripe.com"},
		{"path", "api.stripe.com/v1"},
		{"port only", ":443"},
		{"port zero", "api.stripe.com:0"},
		{"port too large", "api.stripe.com:65536"},
		{"port not numeric", "api.stripe.com:https"},
		{"single label hostname", "localhost"},
		{"cluster-internal suffix", "vault.excalibase-platform.svc.cluster.local"},
		{"internal suffix", "db.internal"},
		{"loopback", "127.0.0.1"},
		{"rfc1918", "10.0.0.1:5432"},
		{"link-local metadata", "169.254.169.254"},
		{"cgnat", "100.64.1.1"},
		{"ipv6 loopback", "[::1]:80"},
		{"ipv6 ula", "fd00::1"},
		{"ipv4-mapped private", "[::ffff:192.168.1.1]:80"},
		{"unspecified", "0.0.0.0"},
		{"cidr", "203.0.113.0/24"},
		{"comma inside entry", "a.example.com,b.example.com"},
		{"whitespace inside entry", "a.example .com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseEgressHosts([]string{tc.in}); err == nil {
				t.Fatalf("ParseEgressHosts(%q) accepted, want error", tc.in)
			} else if !errors.Is(err, ErrInvalidEgressHost) {
				t.Fatalf("ParseEgressHosts(%q) error %v is not ErrInvalidEgressHost", tc.in, err)
			}
		})
	}
}

func TestParseEgressHosts_ErrorNamesEntry(t *testing.T) {
	_, err := ParseEgressHosts([]string{"ok.example.com", "10.0.0.1"})
	if err == nil || !strings.Contains(err.Error(), "10.0.0.1") {
		t.Fatalf("error should name the offending entry, got %v", err)
	}
}

func TestParseEgressHosts_CapsEntryCount(t *testing.T) {
	entries := make([]string, MaxEgressHosts+1)
	for i := range entries {
		entries[i] = "h" + strings.Repeat("x", i%5) + string(rune('a'+i%26)) + ".example.com"
	}
	if _, err := ParseEgressHosts(entries); !errors.Is(err, ErrInvalidEgressHost) {
		t.Fatalf("expected cap error, got %v", err)
	}
}

func TestParseEgressHostList_SplitsCommaString(t *testing.T) {
	got, err := ParseEgressHostList("b.example.com, a.example.com:443,,")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a.example.com:443", "b.example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestMergeEgressHosts_UnionSortedDeduped(t *testing.T) {
	got := MergeEgressHosts([]string{"b.example.com", "a.example.com"}, nil, []string{"a.example.com", "c.example.com:80"})
	want := []string{"a.example.com", "b.example.com", "c.example.com:80"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if got := MergeEgressHosts(nil, []string{}); len(got) != 0 || got == nil {
		t.Fatalf("empty merge should be a non-nil empty slice, got %#v", got)
	}
}

func TestEgressEnvValue(t *testing.T) {
	if v := EgressEnvValue(nil); v != "" {
		t.Fatalf("nil → %q, want empty", v)
	}
	if v := EgressEnvValue([]string{"a.example.com", "b.example.com:443"}); v != "a.example.com,b.example.com:443" {
		t.Fatalf("got %q", v)
	}
}

func TestEgressHostPorts(t *testing.T) {
	got := EgressHostPorts([]string{"a.example.com", "b.example.com:8443", "*.c.example.com:8443", "203.0.113.1:9000", "[2606:4700::1111]:443"})
	want := []int{443, 8443, 9000}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}
