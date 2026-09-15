package config

import "testing"

func TestLoad_AutoPauseEnabledFollowsDeploymentMode(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "http://localhost")
	t.Setenv("EXCALIBASE_AUTOPAUSE_ENABLED", "")

	t.Setenv("DEPLOYMENT_MODE", "cloud")
	if !Load().AutoPauseEnabled {
		t.Error("cloud mode must default auto-pause on")
	}
	t.Setenv("DEPLOYMENT_MODE", "selfhosted")
	if Load().AutoPauseEnabled {
		t.Error("self-hosted mode must default auto-pause off")
	}
}

func TestLoad_AutoPauseEnabledOverride(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "http://localhost")
	cases := []struct {
		mode, raw string
		want      bool
	}{
		{"cloud", "false", false},
		{"cloud", "0", false},
		{"cloud", "off", false},
		{"selfhosted", "true", true},
		{"selfhosted", "1", true},
		{"selfhosted", "garbage", false},
	}
	for _, c := range cases {
		t.Setenv("DEPLOYMENT_MODE", c.mode)
		t.Setenv("EXCALIBASE_AUTOPAUSE_ENABLED", c.raw)
		if got := Load().AutoPauseEnabled; got != c.want {
			t.Errorf("mode=%s raw=%q: got %v, want %v", c.mode, c.raw, got, c.want)
		}
	}
}
