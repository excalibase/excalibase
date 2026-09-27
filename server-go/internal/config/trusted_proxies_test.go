package config

import "testing"

func TestLoadTrustsNoProxyByDefault(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://studio.example.test")
	t.Setenv("TRUSTED_PROXY_CIDRS", "")
	if got := Load().TrustedProxyCIDRs; len(got) != 0 {
		t.Errorf("TrustedProxyCIDRs: got %v, want none", got)
	}
}

func TestLoadReadsTrustedProxyCIDRs(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://studio.example.test")
	t.Setenv("TRUSTED_PROXY_CIDRS", "10.42.0.0/16, fd00::/8")
	got := Load().TrustedProxyCIDRs
	if len(got) != 2 || got[0].String() != "10.42.0.0/16" || got[1].String() != "fd00::/8" {
		t.Errorf("TrustedProxyCIDRs: got %v", got)
	}
}
