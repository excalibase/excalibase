package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

const (
	errProjectNotFoundFmt = "project not found: %s"
	primaryPodSuffix      = "-postgres-1"
)

type AuditService struct {
	store     storage.InstanceStore
	k8sClient k8s.KubeClient
}

func NewAuditService(store storage.InstanceStore, client k8s.KubeClient) *AuditService {
	return &AuditService{store: store, k8sClient: client}
}

func (s *AuditService) EnableAudit(ctx context.Context, projectID string, config domain.AuditConfig) error {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return fmt.Errorf(errProjectNotFoundFmt, projectID)
	}

	pod := projectID + primaryPodSuffix
	_, err = s.k8sClient.ExecInPod(ctx, inst.Namespace, pod, "postgres",
		[]string{"psql", "-U", "postgres", "-c", "CREATE EXTENSION IF NOT EXISTS pgaudit"})
	return err
}

func (s *AuditService) GetAuditLogs(ctx context.Context, projectID string, lines int) (string, error) {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return "", fmt.Errorf(errProjectNotFoundFmt, projectID)
	}

	pod := projectID + primaryPodSuffix
	out, err := s.k8sClient.ExecInPod(ctx, inst.Namespace, pod, "postgres",
		[]string{"sh", "-c", fmt.Sprintf("cat /controller/log/postgres.csv | grep AUDIT | tail -%d", lines)})
	if err != nil {
		return "", err
	}
	return out, nil
}

func (s *AuditService) GetAuditConfig(ctx context.Context, projectID string) (*domain.AuditConfig, error) {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return nil, fmt.Errorf(errProjectNotFoundFmt, projectID)
	}

	pod := projectID + primaryPodSuffix
	out, err := s.k8sClient.ExecInPod(ctx, inst.Namespace, pod, "postgres",
		[]string{"psql", "-U", "postgres", "-t", "-A", "-c",
			"SELECT name, setting FROM pg_settings WHERE name LIKE 'pgaudit.%'"})
	if err != nil {
		return &domain.AuditConfig{Enabled: false}, nil
	}

	settings := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) == 2 {
			settings[parts[0]] = parts[1]
		}
	}

	return &domain.AuditConfig{
		Enabled:  len(settings) > 0,
		Settings: settings,
	}, nil
}
