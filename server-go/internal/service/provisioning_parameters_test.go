package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

func provisionWithParameters(t *testing.T, params map[string]string) (*k8s.MockClient, int, error) {
	t.Helper()
	svc, store, mock := setupProvisioningTest(t)
	_, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		PostgresVersion: "17",
		ProjectName:     "params-db",
		OrgID:           "org1",
		DBType:          domain.PostgreSQL,
		Parameters:      params,
	})
	all, _ := store.FindAll()
	return mock, len(all), err
}

func TestProvisionRefusesAPlatformOwnedParameterBeforeAnythingIsCreated(t *testing.T) {
	mock, projects, err := provisionWithParameters(t, map[string]string{"statement_timeout": "0"})
	if !errors.Is(err, config.ErrTenantParameter) {
		t.Fatalf("got %v, want ErrTenantParameter", err)
	}
	if projects != 0 {
		t.Errorf("a refused request left %d project rows", projects)
	}
	if len(mock.CRDs) != 0 {
		t.Errorf("a refused request applied %d CRDs", len(mock.CRDs))
	}
}

func TestProvisionRefusesATunableParameterPastTheTiersBounds(t *testing.T) {
	_, _, err := provisionWithParameters(t, map[string]string{"work_mem": "1GB"})
	if !errors.Is(err, config.ErrTenantParameter) {
		t.Fatalf("got %v, want ErrTenantParameter", err)
	}
}

func TestProvisionAcceptsATunableParameterWithinBounds(t *testing.T) {
	_, projects, err := provisionWithParameters(t, map[string]string{"work_mem": "8MB"})
	if err != nil {
		t.Fatalf("got %v", err)
	}
	if projects != 1 {
		t.Errorf("got %d project rows, want 1", projects)
	}
}
