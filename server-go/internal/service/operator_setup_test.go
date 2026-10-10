package service

import (
	"context"
	"slices"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// ADR 0041: MySQL projects run on MOCO, pinned to a release, not Vitess.
func TestInstallOperator_MySQLAppliesPinnedMOCORelease(t *testing.T) {
	mock := k8s.NewMockClient()
	svc := NewOperatorSetupService(mock)

	if err := svc.InstallOperator(context.Background(), domain.MySQL); err != nil {
		t.Fatalf("InstallOperator: %v", err)
	}

	want := "ApplyManifestURL:https://github.com/cybozu-go/moco/releases/download/v0.37.0/moco.yaml"
	if !slices.Contains(mock.Calls, want) {
		t.Errorf("expected %q in calls, got %v", want, mock.Calls)
	}
}

func TestIsOperatorInstalled_MySQLChecksMOCOController(t *testing.T) {
	mock := k8s.NewMockClient()
	svc := NewOperatorSetupService(mock)

	if !svc.IsOperatorInstalled(context.Background(), domain.MySQL) {
		t.Fatal("expected the mock's deployment to count as installed")
	}
	if !slices.Contains(mock.Calls, "GetDeployment:moco-system/moco-controller") {
		t.Errorf("expected the MOCO controller deployment to be checked, got %v", mock.Calls)
	}
}

// MOCO is an add-on beside CNPG: setup is complete on Postgres alone.
func TestSetupComplete_DoesNotAskForMOCO(t *testing.T) {
	mock := k8s.NewMockClient()
	svc := NewOperatorSetupService(mock)

	if !svc.SetupComplete(context.Background()) {
		t.Fatal("expected setup complete")
	}
	for _, call := range mock.Calls {
		if call == "GetDeployment:moco-system/moco-controller" {
			t.Errorf("setup completeness must not depend on MOCO, got %v", mock.Calls)
		}
	}
}
