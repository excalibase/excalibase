package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

type OperatorSetupService struct {
	k8sClient k8s.KubeClient
	// complete caches the answer GET /api/setup/status serves, because that
	// route takes no credential and must not reach the cluster per request.
	completeMu    sync.RWMutex
	complete      bool
	completeUntil time.Time
}

func NewOperatorSetupService(k8sClient k8s.KubeClient) *OperatorSetupService {
	return &OperatorSetupService{k8sClient: k8sClient}
}

var operatorURLs = map[domain.DatabaseType]string{
	domain.PostgreSQL: "https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-1.23/releases/cnpg-1.23.0.yaml",
	domain.MySQL:      "https://raw.githubusercontent.com/planetscale/vitess-operator/main/deploy/operator.yaml",
	domain.MongoDB:    "https://raw.githubusercontent.com/mongodb/mongodb-kubernetes-operator/master/config/manager/manager.yaml",
}

var operatorDeployments = map[domain.DatabaseType]struct {
	name      string
	namespace string
}{
	domain.PostgreSQL: {name: "cnpg-controller-manager", namespace: "cnpg-system"},
	domain.MySQL:      {name: "vitess-operator", namespace: "vitess"},
	domain.MongoDB:    {name: "mongodb-kubernetes-operator", namespace: "mongodb"},
}

func (s *OperatorSetupService) InstallOperator(ctx context.Context, dbType domain.DatabaseType) error {
	url, ok := operatorURLs[dbType]
	if !ok {
		return fmt.Errorf("unsupported database type: %s", dbType)
	}

	if err := s.k8sClient.ApplyManifestURL(ctx, url); err != nil {
		return fmt.Errorf("install operator: %w", err)
	}
	s.forgetSetupComplete()

	return s.waitForOperator(ctx, dbType, 2*time.Minute)
}

func (s *OperatorSetupService) IsOperatorInstalled(ctx context.Context, dbType domain.DatabaseType) bool {
	deploy, ok := operatorDeployments[dbType]
	if !ok {
		return false
	}

	found, err := s.k8sClient.GetDeployment(ctx, deploy.namespace, deploy.name)
	if err != nil {
		return false
	}
	return found
}

func (s *OperatorSetupService) GetStatus(ctx context.Context) domain.OperatorStatus {
	return domain.OperatorStatus{
		PostgreSQL: s.IsOperatorInstalled(ctx, domain.PostgreSQL),
		MySQL:      s.IsOperatorInstalled(ctx, domain.MySQL),
		MongoDB:    s.IsOperatorInstalled(ctx, domain.MongoDB),
	}
}

// setupCompleteTTL bounds how stale the cached answer may be. The state it
// reports changes once per cluster lifetime, and an install refreshes it
// immediately, so a minute of staleness costs nothing.
const setupCompleteTTL = time.Minute

// SetupComplete reports whether the platform can provision. Postgres is the
// engine it provisions, so its operator being up is the whole condition —
// the other operators are optional add-ons, not part of "installed".
//
// The answer is cached: GET /api/setup/status is unauthenticated, so without
// a cache anyone could turn a loop on it into a loop on the apiserver.
func (s *OperatorSetupService) SetupComplete(ctx context.Context) bool {
	s.completeMu.RLock()
	if time.Now().Before(s.completeUntil) {
		complete := s.complete
		s.completeMu.RUnlock()
		return complete
	}
	s.completeMu.RUnlock()

	complete := s.IsOperatorInstalled(ctx, domain.PostgreSQL)
	s.completeMu.Lock()
	s.complete, s.completeUntil = complete, time.Now().Add(setupCompleteTTL)
	s.completeMu.Unlock()
	return complete
}

// forgetSetupComplete drops the cached answer, so the next read asks the
// cluster. Installing an operator is the one event that changes it.
func (s *OperatorSetupService) forgetSetupComplete() {
	s.completeMu.Lock()
	s.completeUntil = time.Time{}
	s.completeMu.Unlock()
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
