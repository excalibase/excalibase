package config

import "testing"

func TestCheckStudioURL(t *testing.T) {
	valid := []string{"https://studio.example.com", "http://localhost:5173"}
	for _, raw := range valid {
		if err := (AppConfig{StudioURL: raw}).CheckStudioURL(); err != nil {
			t.Errorf("%q: %v", raw, err)
		}
	}
	invalid := []string{"", "studio.example.com", "ftp://studio.example.com", "https://", "https://studio.example.com/app?x=1", "javascript:alert(1)"}
	for _, raw := range invalid {
		if err := (AppConfig{StudioURL: raw}).CheckStudioURL(); err == nil {
			t.Errorf("%q accepted", raw)
		}
	}
}

func TestLoadReadsStudioURLWithoutATrailingSlash(t *testing.T) {
	t.Setenv("STUDIO_URL", "https://studio.example.com/")
	if got := Load().StudioURL; got != "https://studio.example.com" {
		t.Fatalf("StudioURL = %q", got)
	}
}
