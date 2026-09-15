package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateSiteURL_EmptyIsUnset(t *testing.T) {
	got, err := ValidateSiteURL("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Fatalf("empty input must canonicalise to empty, got %q", got)
	}
	got, err = ValidateSiteURL("   ")
	if err != nil || got != "" {
		t.Fatalf("whitespace-only input must canonicalise to empty, got %q err %v", got, err)
	}
}

func TestValidateSiteURL_AcceptsAndCanonicalises(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"https no path", "https://app.example.com", "https://app.example.com"},
		{"http scheme", "http://app.example.com", "http://app.example.com"},
		{"uppercase scheme and host lower-cased", "HTTPS://App.Example.com", "https://app.example.com"},
		{"path allowed without trailing slash", "https://app.example.com/callback", "https://app.example.com/callback"},
		{"port preserved", "https://app.example.com:8443", "https://app.example.com:8443"},
		{"trimmed", "  https://app.example.com  ", "https://app.example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateSiteURL(tc.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestValidateSiteURL_Rejects(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"no scheme", "app.example.com"},
		{"unsupported scheme", "ftp://app.example.com"},
		{"scheme only, no host", "https://"},
		{"userinfo", "https://user:pw@app.example.com"},
		{"query", "https://app.example.com?x=1"},
		{"fragment", "https://app.example.com#top"},
		{"trailing slash root", "https://app.example.com/"},
		{"trailing slash with path", "https://app.example.com/callback/"},
		{"whitespace inside", "https://app example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidateSiteURL(tc.input)
			if !errors.Is(err, ErrInvalidSiteURL) {
				t.Fatalf("%q: want ErrInvalidSiteURL, got %v", tc.input, err)
			}
		})
	}
}

func TestValidateSiteURL_RejectsOversizedInput(t *testing.T) {
	huge := "https://app.example.com/" + strings.Repeat("a", MaxSiteURLLength)
	_, err := ValidateSiteURL(huge)
	if !errors.Is(err, ErrInvalidSiteURL) {
		t.Fatalf("want ErrInvalidSiteURL for oversized input, got %v", err)
	}
}

func TestValidateSiteURL_AcceptsExactlyMaxLength(t *testing.T) {
	prefix := "https://app.example.com/"
	padding := strings.Repeat("a", MaxSiteURLLength-len(prefix))
	input := prefix + padding
	if len(input) != MaxSiteURLLength {
		t.Fatalf("test setup: input length = %d want %d", len(input), MaxSiteURLLength)
	}
	got, err := ValidateSiteURL(input)
	if err != nil {
		t.Fatalf("exactly max length must be accepted: %v", err)
	}
	if got != input {
		t.Fatalf("got %q want %q", got, input)
	}
}

func TestValidateSiteURL_ErrorNamesEntry(t *testing.T) {
	_, err := ValidateSiteURL("app.example.com")
	if err == nil || !strings.Contains(err.Error(), "app.example.com") {
		t.Fatalf("error must name the offending entry, got %v", err)
	}
}
