package config

import (
	"testing"
	"time"
)

func TestParseDBEndpointPortRangeDefaultsWhenUnset(t *testing.T) {
	r, err := parseDBEndpointPortRange("")
	if err != nil {
		t.Fatalf("parseDBEndpointPortRange: %v", err)
	}
	if r.Min != DefaultDBEndpointPortMin || r.Max != DefaultDBEndpointPortMax {
		t.Fatalf("range = %+v, want the documented default", r)
	}
}

func TestParseDBEndpointPortRangeReadsMinDashMax(t *testing.T) {
	r, err := parseDBEndpointPortRange(" 40000 - 40099 ")
	if err != nil {
		t.Fatalf("parseDBEndpointPortRange: %v", err)
	}
	if r.Min != 40000 || r.Max != 40099 {
		t.Fatalf("range = %+v", r)
	}
}

func TestParseDBEndpointPortRangeRefusesUnusableValues(t *testing.T) {
	for _, raw := range []string{"40000", "40000-", "-40000", "a-b", "40000-39999", "80-1000", "60000-70000", "1-2-3"} {
		if _, err := parseDBEndpointPortRange(raw); err == nil {
			t.Fatalf("parseDBEndpointPortRange(%q) succeeded, want error", raw)
		}
	}
}

func TestParseDBEndpointQuarantineDefaultsToThirtyDays(t *testing.T) {
	d, err := parseDuration("", DefaultDBEndpointPortQuarantine)
	if err != nil {
		t.Fatalf("parseDuration: %v", err)
	}
	if d != 30*24*time.Hour {
		t.Fatalf("quarantine = %v, want 30 days", d)
	}
}

func TestParseDBEndpointDomainRefusesAnUnreachableSuffix(t *testing.T) {
	for _, raw := range []string{"localhost", "db.internal", "db.excalibase.io:5432", "db excalibase io", "io"} {
		if _, err := parseDBEndpointDomain(raw); err == nil {
			t.Fatalf("parseDBEndpointDomain(%q) succeeded, want error", raw)
		}
	}
}

func TestParseDBEndpointDomainAcceptsEmptyAsUnconfigured(t *testing.T) {
	got, err := parseDBEndpointDomain("")
	if err != nil {
		t.Fatalf("parseDBEndpointDomain: %v", err)
	}
	if got != "" {
		t.Fatalf("domain = %q, want empty", got)
	}
}

func TestParseDBEndpointDomainCanonicalises(t *testing.T) {
	got, err := parseDBEndpointDomain("  DB.Excalibase.IO. ")
	if err != nil {
		t.Fatalf("parseDBEndpointDomain: %v", err)
	}
	if got != "db.excalibase.io" {
		t.Fatalf("domain = %q", got)
	}
}
