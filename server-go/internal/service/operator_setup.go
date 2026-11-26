package service

import (
	"context"
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

var operatorDeployments = map[domain.DatabaseType]struct {
	name      string
	namespace string
}{
	domain.PostgreSQL: {name: "cnpg-controller-manager", namespace: "cnpg-system"},
	domain.MySQL:      {name: "moco-controller", namespace: "moco-system"},
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
	}
}

// setupCompleteTTL bounds how stale the cached answer may be. The state it
// reports changes once per cluster lifetime, so a minute of staleness costs
// nothing.
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
