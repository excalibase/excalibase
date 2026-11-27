package config

import "testing"

func TestLoadAppTmpPercent(t *testing.T) {
	t.Setenv("APP_TMP_PERCENT", "")
	if got := Load().AppTmpPercent; got != 25 {
		t.Errorf("default = %d, want 25", got)
	}
	t.Setenv("APP_TMP_PERCENT", "40")
	if got := Load().AppTmpPercent; got != 40 {
		t.Errorf("AppTmpPercent = %d, want 40", got)
	}
}

func TestValidateAppTmpPercentRefusesOutOfRange(t *testing.T) {
	for _, percent := range []int{0, -1, 101} {
		cfg := AppConfig{ProvisionerMode: "docker", AppTmpPercent: percent}
		if err := cfg.validateAppTmpPercent(); err == nil {
			t.Errorf("%d must be refused", percent)
		}
	}
	for _, percent := range []int{1, 25, 100} {
		cfg := AppConfig{ProvisionerMode: "k8s", AppTmpPercent: percent}
		if err := cfg.validateAppTmpPercent(); err != nil {
			t.Errorf("%d refused: %v", percent, err)
		}
	}
}

func TestLoadAppTmpPercentMarksJunkAndZeroInvalid(t *testing.T) {
	for _, raw := range []string{"0", "-3", "many"} {
		t.Setenv("APP_TMP_PERCENT", raw)
		cfg := Load()
		cfg.ProvisionerMode = "k8s"
		if err := cfg.validateAppTmpPercent(); err == nil {
			t.Errorf("%q passed validation", raw)
		}
	}
}
