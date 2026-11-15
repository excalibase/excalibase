package config

import (
	"strings"
	"testing"
)

func TestCheckAdminHost(t *testing.T) {
	cases := []struct {
		name string
		cfg  AppConfig
		ok   bool
	}{
		{"unset is fine", AppConfig{}, true},
		{"a host alone is fine", AppConfig{AdminHost: "admin.excalibase.io"}, true},
		{"a host behind Access is fine", AppConfig{AdminHost: "admin.excalibase.io", AdminAccessTeamDomain: "team.cloudflareaccess.com", AdminAccessAud: "aud"}, true},
		{"a URL is not a host", AppConfig{AdminHost: "https://admin.excalibase.io"}, false},
		{"a path is not a host", AppConfig{AdminHost: "admin.excalibase.io/x"}, false},
		{"a team URL is not a team domain", AppConfig{AdminHost: "a.test", AdminAccessTeamDomain: "https://team.cloudflareaccess.com", AdminAccessAud: "aud"}, false},
		{"a team without an AUD would trust any application", AppConfig{AdminHost: "a.test", AdminAccessTeamDomain: "team.cloudflareaccess.com"}, false},
		{"an AUD without a team", AppConfig{AdminHost: "a.test", AdminAccessAud: "aud"}, false},
		{"Access without a host to fence", AppConfig{AdminAccessTeamDomain: "team.cloudflareaccess.com", AdminAccessAud: "aud"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cfg.CheckAdminHost(); (err == nil) != tc.ok {
				t.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

// EXC-757: a cloud install holds other customers' data, so the fence fails
// closed. With no ADMIN_HOST the install boots with no admin console at all;
// with a host it boots only behind Access (team and AUD). Self-hosted is unchanged.
func TestCheckAdminHost_CloudFailsClosed(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg AppConfig
		ok  bool
	}{
		"no host: boots with the console off": {AppConfig{DeploymentMode: "cloud"}, true},
		"host behind Access":                  {AppConfig{DeploymentMode: "cloud", AdminHost: "admin.excalibase.io", AdminAccessTeamDomain: "team.cloudflareaccess.com", AdminAccessAud: "aud"}, true},
		"host alone refuses to boot":          {AppConfig{DeploymentMode: "cloud", AdminHost: "admin.excalibase.io"}, false},
		"host without AUD refuses to boot":    {AppConfig{DeploymentMode: "cloud", AdminHost: "admin.excalibase.io", AdminAccessTeamDomain: "team.cloudflareaccess.com"}, false},
		"host without team refuses to boot":   {AppConfig{DeploymentMode: "cloud", AdminHost: "admin.excalibase.io", AdminAccessAud: "aud"}, false},
		"Access without a host refuses":       {AppConfig{DeploymentMode: "cloud", AdminAccessTeamDomain: "team.cloudflareaccess.com", AdminAccessAud: "aud"}, false},
		"self-hosted with nothing boots":      {AppConfig{DeploymentMode: "selfhosted"}, true},
		"self-hosted with a host alone boots": {AppConfig{DeploymentMode: "selfhosted", AdminHost: "admin.example.com"}, true},
	} {
		t.Run(name, func(t *testing.T) {
			err := tc.cfg.CheckAdminHost()
			if (err == nil) != tc.ok {
				t.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
			if err != nil && !strings.Contains(err.Error(), "ADMIN_") {
				t.Errorf("the refusal does not name what to set: %v", err)
			}
		})
	}
}

func TestAdminConsoleDisabled(t *testing.T) {
	if !(AppConfig{DeploymentMode: "cloud"}).AdminConsoleDisabled() {
		t.Fatal("a cloud install with no admin host has no console")
	}
	for name, cfg := range map[string]AppConfig{
		"cloud with a host": {DeploymentMode: "cloud", AdminHost: "admin.excalibase.io"},
		"self-hosted":       {DeploymentMode: "selfhosted"},
	} {
		if cfg.AdminConsoleDisabled() {
			t.Errorf("%s: the console must stay available", name)
		}
	}
}

func TestAdminAccessEnabled(t *testing.T) {
	if (AppConfig{AdminHost: "a.test"}).AdminAccessEnabled() {
		t.Fatal("a host alone is not Access")
	}
	if !(AppConfig{AdminHost: "a.test", AdminAccessTeamDomain: "t.cloudflareaccess.com", AdminAccessAud: "x"}).AdminAccessEnabled() {
		t.Fatal("host, team and AUD together are Access")
	}
}

func TestAdminOriginIsDerivedFromTheHost(t *testing.T) {
	if got := (AppConfig{AdminHost: "admin.excalibase.io"}).AdminOrigin(); got != "https://admin.excalibase.io" {
		t.Fatalf("got %q", got)
	}
	if got := (AppConfig{}).AdminOrigin(); got != "" {
		t.Fatalf("no host must trust no origin, got %q", got)
	}
}
