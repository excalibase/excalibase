package apphost_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

func TestRouteHostnameUsesTheRandomPartOfAPlatformProjectID(t *testing.T) {
	route := apphost.Route{Domain: "apps.example.com"}
	got, err := route.Hostname("web", "proj-a3k9fx7b2k")
	if err != nil {
		t.Fatalf("Hostname: %v", err)
	}
	if got != "web-a3k9fx7b2k.apps.example.com" {
		t.Errorf("hostname = %q", got)
	}
}

func TestRouteHostnameKeepsASelfHostedProjectIDWhole(t *testing.T) {
	got, err := apphost.Route{Domain: "apps.local"}.Hostname("web", "proja")
	if err != nil {
		t.Fatalf("Hostname: %v", err)
	}
	if got != "web-proja.apps.local" {
		t.Errorf("hostname = %q", got)
	}
}

func TestRouteURLScheme(t *testing.T) {
	for tls, want := range map[bool]string{
		true:  "https://web-abc.apps.example.com",
		false: "http://web-abc.apps.example.com",
	} {
		got, err := apphost.Route{Domain: "apps.example.com", TLS: tls}.URL("web", "proj-abc")
		if err != nil {
			t.Fatalf("URL: %v", err)
		}
		if got != want {
			t.Errorf("TLS=%v: URL = %q, want %q", tls, got, want)
		}
	}
}

func TestRouteHostnameRefusals(t *testing.T) {
	longName := strings.Repeat("a", 50)
	cases := map[string]struct {
		domain, name, projectID string
	}{
		"no domain":                      {"", "web", "proj-abc"},
		"invalid domain":                 {"Apps_Example", "web", "proj-abc"},
		"project id not a DNS label":     {"apps.example.com", "web", "Proj_ABC"},
		"project id only the prefix":     {"apps.example.com", "web", "proj-"},
		"label longer than 63":           {"apps.example.com", longName, "proj-" + strings.Repeat("b", 13)},
		"invalid app name":               {"apps.example.com", "Web", "proj-abc"},
		"host longer than a DNS name":    {strings.Repeat("d.", 125) + "com", "web", "proj-abc"},
		"project id ending with hyphen":  {"apps.example.com", "web", "proj-abc-"},
		"project id starting with space": {"apps.example.com", "web", " proj-abc"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := apphost.Route{Domain: tc.domain}.Hostname(tc.name, tc.projectID)
			if !errors.Is(err, apphost.ErrNoRoute) {
				t.Fatalf("want ErrNoRoute, got %v", err)
			}
		})
	}
}

func TestRouteURLPropagatesRefusal(t *testing.T) {
	if _, err := (apphost.Route{}).URL("web", "proj-abc"); !errors.Is(err, apphost.ErrNoRoute) {
		t.Fatalf("want ErrNoRoute, got %v", err)
	}
}

// The longest name the model allows still fits beside a platform project id.
func TestRouteHostnameFitsTheLongestAppName(t *testing.T) {
	if _, err := (apphost.Route{Domain: "apps.example.com"}).Hostname(strings.Repeat("a", 50), "proj-a3k9fx7b2k"); err != nil {
		t.Fatalf("Hostname: %v", err)
	}
}
