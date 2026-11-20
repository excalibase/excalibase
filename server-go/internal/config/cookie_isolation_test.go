package config

import (
	"strings"
	"testing"
)

func isolationConfig(studio, api, appDomain, sitesBase string) AppConfig {
	return AppConfig{
		StudioURL: studio, PublicBaseURL: api, AppDomain: appDomain,
		SitesEnabled: sitesBase != "", Sites: SitesConfig{BaseDomain: sitesBase},
	}
}

func TestCookieIsolation_RefusesADomainThatContainsStudioOrTheAPI(t *testing.T) {
	cases := map[string]AppConfig{
		"app domain is the registrable domain":  isolationConfig("https://app.excalibase.io", "https://api.excalibase.io", "excalibase.io", ""),
		"app domain is the studio host":         isolationConfig("https://app.excalibase.io", "https://api.excalibase.io", "app.excalibase.io", ""),
		"app domain is the api host":            isolationConfig("https://app.excalibase.io", "https://api.excalibase.io", "api.excalibase.io", ""),
		"sites base is the registrable domain":  isolationConfig("https://app.excalibase.io", "https://api.excalibase.io", "", "excalibase.io"),
		"sites base contains studio":            isolationConfig("https://studio.apps.example.com", "", "", "apps.example.com"),
		"a public suffix that holds the studio": isolationConfig("https://app.example.co.uk", "", "co.uk", ""),
	}
	for name, cfg := range cases {
		if _, err := cfg.CheckCookieIsolation(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestCookieIsolation_AllowsASiblingSubdomainButWarns(t *testing.T) {
	cfg := isolationConfig("https://app.excalibase.io", "https://api.excalibase.io", "apps.excalibase.io", "")
	warnings, err := cfg.CheckCookieIsolation()
	if err != nil {
		t.Fatalf("the single-host default was refused: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "APP_DOMAIN") || !strings.Contains(warnings[0], "excalibase.io") {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestCookieIsolation_QuietWhenTheDomainsAreSeparate(t *testing.T) {
	cases := []AppConfig{
		isolationConfig("https://app.excalibase.io", "https://api.excalibase.io", "apps.excalibase.app", "excalibase.app"),
		isolationConfig("http://localhost:5173", "http://localhost:24005", "apps.localhost", ""),
		isolationConfig("http://192.168.1.10:5173", "", "apps.example.com", ""),
		isolationConfig("", "", "excalibase.io", ""),
	}
	for i, cfg := range cases {
		warnings, err := cfg.CheckCookieIsolation()
		if err != nil || len(warnings) != 0 {
			t.Errorf("case %d: warnings %v err %v", i, warnings, err)
		}
	}
}

func TestCookieIsolation_InsecureCookiesNeverOnHTTPS(t *testing.T) {
	cfg := AppConfig{StudioURL: "https://app.excalibase.io", InsecureCookies: true}
	if _, err := cfg.CheckCookieIsolation(); err == nil {
		t.Fatal("insecure cookies were accepted behind an https Studio")
	}
	cfg.StudioURL = "http://192.168.1.10:5173"
	warnings, err := cfg.CheckCookieIsolation()
	if err != nil || len(warnings) != 1 || !strings.Contains(warnings[0], "STUDIO_INSECURE_COOKIES") {
		t.Fatalf("http install: warnings %v err %v", warnings, err)
	}
}

func TestLoad_InsecureCookiesAreOffUnlessSwitchedOn(t *testing.T) {
	if Load().InsecureCookies {
		t.Fatal("insecure cookies are the default")
	}
	t.Setenv("STUDIO_INSECURE_COOKIES", "true")
	if !Load().InsecureCookies {
		t.Fatal("STUDIO_INSECURE_COOKIES=true was ignored")
	}
}
