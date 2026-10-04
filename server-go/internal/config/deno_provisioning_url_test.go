package config

import "testing"

// A project's function runtime calls provisioning for ctx.storage; on k8s it
// learns the address only from DENO_PROVISIONING_URL, so an install running
// functions there must name it (EXC-518).
func TestCheckDenoProvisioningURL(t *testing.T) {
	const url = "http://provisioning.excalibase-platform.svc.cluster.local:24005"
	for name, cfg := range map[string]AppConfig{
		"k8s functions with the address":    {ProvisionerMode: "k8s", DenoRuntimeSecret: "s", DenoProvisioningURL: url},
		"functions not configured":          {ProvisionerMode: "k8s"},
		"docker runtime is not per project": {ProvisionerMode: "docker", DenoRuntimeSecret: "s"},
	} {
		if err := cfg.CheckDenoProvisioningURL(); err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
	}
	for name, cfg := range map[string]AppConfig{
		"k8s functions without the address": {ProvisionerMode: "k8s", DenoRuntimeSecret: "s"},
		"not a URL":                         {ProvisionerMode: "k8s", DenoRuntimeSecret: "s", DenoProvisioningURL: "provisioning:24005"},
		"not http":                          {ProvisionerMode: "k8s", DenoRuntimeSecret: "s", DenoProvisioningURL: "ftp://provisioning:24005"},
		"with a path":                       {ProvisionerMode: "k8s", DenoRuntimeSecret: "s", DenoProvisioningURL: url + "/internal"},
	} {
		if err := cfg.CheckDenoProvisioningURL(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestLoadReadsTheDenoProvisioningURL(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://studio.example.test")
	t.Setenv("DENO_PROVISIONING_URL", "http://provisioning.ns.svc:24005")
	if got := Load().DenoProvisioningURL; got != "http://provisioning.ns.svc:24005" {
		t.Fatalf("DenoProvisioningURL = %q", got)
	}
}
