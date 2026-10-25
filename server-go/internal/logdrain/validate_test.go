package logdrain

import (
	"errors"
	"strings"
	"testing"
)

func TestParseEndpoint_AcceptsPublicHTTPS(t *testing.T) {
	cases := map[string]string{
		"https://logs.example.com/ingest":           "https://logs.example.com/ingest",
		"https://LOGS.Example.com:443/a?b=c":        "https://logs.example.com/a?b=c",
		"https://logs.example.com":                  "https://logs.example.com",
		"  https://intake.example.org/v1/logs  ":    "https://intake.example.org/v1/logs",
		"https://203.0.113.10/collect":              "https://203.0.113.10/collect",
		"https://logs.example.com/path%20with%20sp": "https://logs.example.com/path%20with%20sp",
	}
	for raw, want := range cases {
		got, err := ParseEndpoint(raw)
		if err != nil {
			t.Errorf("%q: %v", raw, err)
			continue
		}
		if got != want {
			t.Errorf("%q → %q, want %q", raw, got, want)
		}
	}
}

func TestParseEndpoint_RefusesWhatTheForwarderCannotSafelyReach(t *testing.T) {
	refused := []string{
		"",
		"http://logs.example.com/ingest",
		"ftp://logs.example.com",
		"https://logs.example.com:8443/ingest",
		"https://user:pass@logs.example.com/",
		"https://127.0.0.1/ingest",
		"https://10.0.0.5/ingest",
		"https://169.254.169.254/latest/meta-data",
		"https://[fd00:ec2::254]/",
		"https://[::1]/",
		"https://localhost/ingest",
		"https://loki.platform.svc.cluster.local/ingest",
		"https://metadata.google.internal/",
		"https://printer.local/",
		"https://singlelabel/",
		"https://logs.example.com/#frag",
		"//logs.example.com/ingest",
		"https://logs.example.com/" + strings.Repeat("a", 2048),
		"https://logs.example.com/\nInjected: yes",
	}
	for _, raw := range refused {
		if _, err := ParseEndpoint(raw); !errors.Is(err, ErrInvalidDrain) {
			t.Errorf("%q: err = %v, want ErrInvalidDrain", raw, err)
		}
	}
}

func TestDatadogIntake_OnlyOfficialSites(t *testing.T) {
	want := map[string]string{
		"us1":     "https://http-intake.logs.datadoghq.com/api/v2/logs",
		"us3":     "https://http-intake.logs.us3.datadoghq.com/api/v2/logs",
		"us5":     "https://http-intake.logs.us5.datadoghq.com/api/v2/logs",
		"eu1":     "https://http-intake.logs.datadoghq.eu/api/v2/logs",
		"ap1":     "https://http-intake.logs.ap1.datadoghq.com/api/v2/logs",
		"ap2":     "https://http-intake.logs.ap2.datadoghq.com/api/v2/logs",
		"us1-fed": "https://http-intake.logs.ddog-gov.com/api/v2/logs",
	}
	for site, url := range want {
		got, err := DatadogIntakeURL(site)
		if err != nil || got != url {
			t.Errorf("%s → %q, %v", site, got, err)
		}
	}
	for _, site := range []string{"", "US1", "evil.example.com", "us1.evil", "https://http-intake.logs.datadoghq.com"} {
		if _, err := DatadogIntakeURL(site); !errors.Is(err, ErrInvalidDrain) {
			t.Errorf("%q: err = %v", site, err)
		}
	}
	if len(DatadogSites()) != len(want) {
		t.Errorf("sites = %v", DatadogSites())
	}
}

func TestValidateHeaderName(t *testing.T) {
	for _, ok := range []string{"Authorization", "X-API-Key", "x-insert-key", "Api_Key"} {
		if err := ValidateHeaderName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Host", "content-length", "Content-Type", "Transfer-Encoding", "Connection",
		"Cookie", "X Bad", "X-Bad:", "X-\nBad", strings.Repeat("a", 65), "Proxy-Authorization", "Te", "Upgrade"} {
		if err := ValidateHeaderName(bad); !errors.Is(err, ErrInvalidDrain) {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
}

func TestValidateName(t *testing.T) {
	if err := ValidateName("Datadog prod"); err != nil {
		t.Errorf("ok name: %v", err)
	}
	for _, bad := range []string{"", "   ", strings.Repeat("n", 65), "bad\nname", "bad\u202ename"} {
		if err := ValidateName(bad); !errors.Is(err, ErrInvalidDrain) {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
}

func TestValidateSecret(t *testing.T) {
	if err := ValidateSecret("abc123"); err != nil {
		t.Errorf("ok secret: %v", err)
	}
	for _, bad := range []string{"", strings.Repeat("s", 4097), "line\r\nX-Injected: 1", "nul\x00"} {
		if err := ValidateSecret(bad); !errors.Is(err, ErrInvalidDrain) {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
}

func TestRedact(t *testing.T) {
	got := Redact("destination said key=supersecret was rejected", "supersecret")
	if strings.Contains(got, "supersecret") || !strings.Contains(got, "[redacted]") {
		t.Errorf("Redact = %q", got)
	}
	if got := Redact("plain", ""); got != "plain" {
		t.Errorf("empty secret: %q", got)
	}
	long := Redact(strings.Repeat("x", 500)+"\x01", "")
	if len(long) > maxErrorLength || strings.ContainsRune(long, '\x01') {
		t.Errorf("long/control not trimmed: %d", len(long))
	}
}

// Headers that would let a tenant speak for the platform's edge are refused.
func TestValidateHeaderName_RefusesForwardingHeaders(t *testing.T) {
	for _, bad := range []string{"X-Forwarded-For", "x-forwarded-host", "X-Forwarded-Proto", "Forwarded", "X-Real-IP", "Via"} {
		if err := ValidateHeaderName(bad); !errors.Is(err, ErrInvalidDrain) {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
}

func TestIsDatadogIntake(t *testing.T) {
	for _, site := range DatadogSites() {
		target, _ := DatadogIntakeURL(site)
		if !IsDatadogIntake(target) {
			t.Errorf("%s: %s refused", site, target)
		}
	}
	for _, target := range []string{"https://evil.example.com/api/v2/logs", "https://http-intake.logs.datadoghq.com.evil.example/api/v2/logs",
		"https://http-intake.logs.datadoghq.com/other", "http://http-intake.logs.datadoghq.com/api/v2/logs"} {
		if IsDatadogIntake(target) {
			t.Errorf("%s accepted", target)
		}
	}
}

// What the API returns and audits never carries the URL's path or query.
func TestDisplayURL(t *testing.T) {
	cases := map[string]string{
		"https://logs.example.com":                         "https://logs.example.com",
		"https://logs.example.com/":                        "https://logs.example.com/...",
		"https://logs.example.com/receiver/v1/http/SECRET": "https://logs.example.com/...",
		"https://logs.example.com?source_token=SECRET":     "https://logs.example.com/...",
		"https://[2001:db8::1]/in":                         "https://[2001:db8::1]/...",
	}
	for raw, want := range cases {
		if got := DisplayURL(raw); got != want {
			t.Errorf("%s → %q, want %q", raw, got, want)
		}
	}
}

func TestEncodedSize(t *testing.T) {
	cases := map[string]int{"abc": 5, `a"b`: 6, "<>&": 5, "\x01": 8, "é": 4}
	for line, want := range cases {
		if got := EncodedSize(line); got != want {
			t.Errorf("%q: %d, want %d", line, got, want)
		}
	}
}
