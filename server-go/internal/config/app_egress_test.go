package config

import (
	"strings"
	"testing"
)

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
	for _, raw := range []string{"not-a-cidr", "203.0.113.9", "203.0.113.9/33", "2001:db8::/32", "::ffff:203.0.113.0/64", "203.0.113.9/32,not-a-cidr"} {
		if _, err := parseEgressExtraDenyCIDRs(raw); err == nil {
			t.Fatalf("parseEgressExtraDenyCIDRs(%q) succeeded, want error", raw)
		}
	}
}

func TestBuildAndAppDenyListsAreSeparate(t *testing.T) {
	t.Setenv("APP_EGRESS_EXTRA_DENY_CIDRS", "198.51.100.1/32")
	t.Setenv("BUILD_EGRESS_EXTRA_DENY_CIDRS", "203.0.113.1/32")
	if got := envEgressExtraDenyCIDRs("BUILD_EGRESS_EXTRA_DENY_CIDRS"); len(got) != 1 || got[0] != "203.0.113.1/32" {
		t.Fatalf("builds' list = %v", got)
	}
	cfg := load()
	if len(cfg.BuildEgressExtraDenyCIDRs) != 1 || cfg.BuildEgressExtraDenyCIDRs[0] != "203.0.113.1/32" {
		t.Fatalf("builds' list = %v", cfg.BuildEgressExtraDenyCIDRs)
	}
	if len(cfg.AppEgressExtraDenyCIDRs) != 1 || cfg.AppEgressExtraDenyCIDRs[0] != "198.51.100.1/32" {
		t.Fatalf("apps' list = %v", cfg.AppEgressExtraDenyCIDRs)
	}
}

func TestBuildDenyListHasNoFallbackToTheApps(t *testing.T) {
	t.Setenv("APP_EGRESS_EXTRA_DENY_CIDRS", "198.51.100.1/32")
	t.Setenv("BUILD_EGRESS_EXTRA_DENY_CIDRS", "")
	if got := load().BuildEgressExtraDenyCIDRs; got != nil {
		t.Fatalf("builds' list fell back to the apps': %v", got)
	}
}

// The message serves both lists: it may not call itself the apps'.
func TestParseEgressExtraDenyCIDRsIPv6ErrorNamesNoOwner(t *testing.T) {
	_, err := parseEgressExtraDenyCIDRs("2001:db8::/32")
	if err == nil || strings.Contains(err.Error(), "app egress") {
		t.Fatalf("error %v", err)
	}
}

func TestExtraDenyPrefixes(t *testing.T) {
	cfg := AppConfig{AppEgressExtraDenyCIDRs: []string{"203.0.113.0/28", "198.51.100.7/32"}}
	got := cfg.ExtraDenyPrefixes()
	if len(got) != 2 || got[0].String() != "203.0.113.0/28" || got[1].String() != "198.51.100.7/32" {
		t.Fatalf("prefixes = %v", got)
	}
	if len((AppConfig{}).ExtraDenyPrefixes()) != 0 {
		t.Fatal("no ranges configured must deny nothing extra")
	}
}
