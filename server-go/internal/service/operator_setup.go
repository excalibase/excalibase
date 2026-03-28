package service

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

type OperatorSetupService struct{}

func NewOperatorSetupService() *OperatorSetupService {
	return &OperatorSetupService{}
}

var operatorURLs = map[domain.DatabaseType]string{
	domain.PostgreSQL: "https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-1.23/releases/cnpg-1.23.0.yaml",
	domain.MySQL:      "https://raw.githubusercontent.com/planetscale/vitess-operator/main/deploy/operator.yaml",
	domain.MongoDB:    "https://raw.githubusercontent.com/mongodb/mongodb-kubernetes-operator/master/config/manager/manager.yaml",
}

func (s *OperatorSetupService) InstallOperator(ctx context.Context, dbType domain.DatabaseType) error {
	url, ok := operatorURLs[dbType]
	if !ok {
		return fmt.Errorf("unsupported database type: %s", dbType)
	}

	cmd := exec.CommandContext(ctx, "kubectl", "apply", "--server-side", "-f", url)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("install operator: %s: %w", string(out), err)
	}

	// Wait for deployment
	return s.waitForOperator(ctx, dbType, 2*time.Minute)
}

func (s *OperatorSetupService) IsOperatorInstalled(ctx context.Context, dbType domain.DatabaseType) bool {
	var deployName, namespace string
	switch dbType {
	case domain.PostgreSQL:
		deployName = "cnpg-controller-manager"
		namespace = "cnpg-system"
	case domain.MySQL:
		deployName = "vitess-operator"
		namespace = "vitess"
	case domain.MongoDB:
		deployName = "mongodb-kubernetes-operator"
		namespace = "mongodb"
	default:
		return false
	}

	cmd := exec.CommandContext(ctx, "kubectl", "get", "deployment", deployName, "-n", namespace, "--no-headers")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), deployName)
}

func (s *OperatorSetupService) GetStatus(ctx context.Context) domain.SetupStatusResponse {
	return domain.SetupStatusResponse{
		PostgreSQL: s.IsOperatorInstalled(ctx, domain.PostgreSQL),
		MySQL:      s.IsOperatorInstalled(ctx, domain.MySQL),
		MongoDB:    s.IsOperatorInstalled(ctx, domain.MongoDB),
	}
}

func (s *OperatorSetupService) waitForOperator(ctx context.Context, dbType domain.DatabaseType, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s.IsOperatorInstalled(ctx, dbType) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	return fmt.Errorf("operator %s did not become ready in %v", dbType, timeout)
}
