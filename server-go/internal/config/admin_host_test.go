package config

import "testing"

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

func TestAdminAccessEnabled(t *testing.T) {
	if (AppConfig{AdminHost: "a.test"}).AdminAccessEnabled() {
		t.Fatal("a host alone is not Access")
	}
	if !(AppConfig{AdminHost: "a.test", AdminAccessTeamDomain: "t.cloudflareaccess.com", AdminAccessAud: "x"}).AdminAccessEnabled() {
		t.Fatal("host, team and AUD together are Access")
	}
}
