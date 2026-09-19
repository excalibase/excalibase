package storage

import (
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func TestCheckDeploymentMode(t *testing.T) {
	operable := []domain.DeploymentMode{"", domain.ModeK8s, domain.ModeDocker}
	for _, mode := range operable {
		if err := CheckDeploymentMode("proj-1", mode); err != nil {
			t.Errorf("CheckDeploymentMode(%q) = %v, want nil", mode, err)
		}
	}

	err := CheckDeploymentMode("proj-1", "byoc")
	if !errors.Is(err, ErrUnsupportedDeploymentMode) {
		t.Fatalf("CheckDeploymentMode(byoc) = %v, want ErrUnsupportedDeploymentMode", err)
	}
	if !strings.Contains(err.Error(), "proj-1") || !strings.Contains(err.Error(), "byoc") {
		t.Errorf("the error must name the project and the mode an operator has to fix: %v", err)
	}
}
