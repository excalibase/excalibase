package config

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/clientaddr"
)

func TestCheckPublicListener(t *testing.T) {
	edge, err := clientaddr.ParseTrustedProxies("10.42.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	valid := AppConfig{Port: "24005", PublicPort: "24006", TrustedProxyCIDRs: edge}
	if err := valid.CheckPublicListener(); err != nil {
		t.Fatalf("valid config refused: %v", err)
	}
	for name, cfg := range map[string]AppConfig{
		"no public port":      {Port: "24005", TrustedProxyCIDRs: edge},
		"public port is PORT": {Port: "24005", PublicPort: "24005", TrustedProxyCIDRs: edge},
		"not a port":          {Port: "24005", PublicPort: "edge", TrustedProxyCIDRs: edge},
		"port out of range":   {Port: "24005", PublicPort: "70000", TrustedProxyCIDRs: edge},
		"no edge to trust":    {Port: "24005", PublicPort: "24006"},
	} {
		if err := cfg.CheckPublicListener(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestLoadReadsThePublicPort(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://studio.example.test")
	t.Setenv("PUBLIC_PORT", "24006")
	if got := Load().PublicPort; got != "24006" {
		t.Fatalf("PublicPort = %q", got)
	}
}
