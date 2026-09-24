package config

import "testing"

func TestParseEgressExtraDenyCIDRsEmptyMeansNone(t *testing.T) {
	cidrs, err := parseEgressExtraDenyCIDRs("")
	if err != nil {
		t.Fatalf("parseEgressExtraDenyCIDRs: %v", err)
	}
	if cidrs != nil {
		t.Fatalf("cidrs = %v, want none", cidrs)
	}
}

func TestParseEgressExtraDenyCIDRsReadsCommaSeparatedList(t *testing.T) {
	cidrs, err := parseEgressExtraDenyCIDRs(" 203.0.113.9/32 , 198.51.100.0/24 ")
	if err != nil {
		t.Fatalf("parseEgressExtraDenyCIDRs: %v", err)
	}
	want := []string{"203.0.113.9/32", "198.51.100.0/24"}
	if len(cidrs) != len(want) {
		t.Fatalf("cidrs = %v, want %v", cidrs, want)
	}
	for i, w := range want {
		if cidrs[i] != w {
			t.Errorf("cidrs[%d] = %q, want %q", i, cidrs[i], w)
		}
	}
}

func TestParseEgressExtraDenyCIDRsRefusesUnusableValues(t *testing.T) {
	for _, raw := range []string{"not-a-cidr", "203.0.113.9", "203.0.113.9/33", "2001:db8::/32", "203.0.113.9/32,not-a-cidr"} {
		if _, err := parseEgressExtraDenyCIDRs(raw); err == nil {
			t.Fatalf("parseEgressExtraDenyCIDRs(%q) succeeded, want error", raw)
		}
	}
}
