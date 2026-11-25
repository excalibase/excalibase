package service

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func TestWithProjectSSLMode_ASingleHostMigratesWithoutTLS(t *testing.T) {
	inst := &domain.DatabaseInstance{DeploymentMode: domain.ModeDocker, SSLMode: domain.SSLModeDisable}
	if got := withProjectSSLMode(dsnOverrides{}, inst).sslmode; got != "disable" {
		t.Errorf("single host sslmode = %q, want disable", got)
	}
	k8s := &domain.DatabaseInstance{DeploymentMode: domain.ModeK8s, SSLMode: domain.SSLModeDisable}
	if got := withProjectSSLMode(dsnOverrides{}, k8s).sslmode; got != "" {
		t.Errorf("kubernetes sslmode = %q, want it left to the default", got)
	}
	if got := withProjectSSLMode(dsnOverrides{host: "h", sslmode: "require"}, inst); got.sslmode != "require" || got.host != "h" {
		t.Errorf("an env override was replaced: %+v", got)
	}
}
