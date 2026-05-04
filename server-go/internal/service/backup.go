package service

import (
	"context"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

const (
	s3CredsKey     = "backup-s3-creds"
	warnMarshalFmt = "WARN: marshal backup record %s: %v"
	warnWriteFmt   = "WARN: write %s: %v"
)

// BackupService is the entry point all HTTP handlers call. It owns
// nothing mode-specific — every Trigger/List/Restore call resolves the
// adapter for the project's DeploymentMode and forwards. CNPG, Docker,
// and (future) MySQL adapters all live behind this dispatch.
type BackupService struct {
	store    storage.InstanceStore
	adapters map[domain.DeploymentMode]BackupAdapter
}

// NewBackupService keeps the legacy single-K8s wiring used by tests
// and main.go before the adapter refactor. Internally it builds a
// one-entry adapter map so dispatch still works.
func NewBackupService(store storage.InstanceStore, client k8s.KubeClient, storagePath string) *BackupService {
	return NewBackupServiceWithAdapters(store, map[domain.DeploymentMode]BackupAdapter{
		domain.ModeK8s: NewK8sBackupAdapter(client, storagePath),
	}, storagePath)
}

// NewBackupServiceWithAdapters is the explicit constructor — main.go
// uses it to wire both K8s and Docker adapters when the platform is
// configured for both modes.
func NewBackupServiceWithAdapters(store storage.InstanceStore, adapters map[domain.DeploymentMode]BackupAdapter, _ string) *BackupService {
	return &BackupService{store: store, adapters: adapters}
}

// RegisterAdapter adds or replaces an adapter at runtime. Used when
// the Docker client is wired post-construction (matches the rest of
// the service's setter-style dependency injection).
func (s *BackupService) RegisterAdapter(mode domain.DeploymentMode, adapter BackupAdapter) {
	if s.adapters == nil {
		s.adapters = make(map[domain.DeploymentMode]BackupAdapter)
	}
	s.adapters[mode] = adapter
}

func (s *BackupService) TriggerManualBackup(ctx context.Context, projectID string) (map[string]interface{}, error) {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}
	adapter, err := resolveAdapter(s.adapters, inst)
	if err != nil {
		return nil, err
	}
	ref, err := adapter.TriggerManual(ctx, inst)
	if err != nil {
		return nil, err
	}
	return refToMap(ref), nil
}

func (s *BackupService) ListBackups(projectID string) ([]map[string]interface{}, error) {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return []map[string]interface{}{}, nil
	}
	adapter, err := resolveAdapter(s.adapters, inst)
	if err != nil {
		return nil, err
	}
	refs, err := adapter.List(context.Background(), inst)
	if err != nil {
		return nil, err
	}
	result := make([]map[string]interface{}, 0, len(refs))
	for _, ref := range refs {
		result = append(result, refToMap(ref))
	}
	return result, nil
}

func (s *BackupService) RestoreFromBackup(ctx context.Context, projectID string, req domain.RestoreRequest) (*domain.ProvisioningResponse, error) {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}
	adapter, err := resolveAdapter(s.adapters, inst)
	if err != nil {
		return nil, err
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	return adapter.Restore(ctx, inst, req)
}

func (s *BackupService) GetInstance(projectID string) (*domain.DatabaseInstance, error) {
	return s.store.FindByProjectID(projectID)
}

// refToMap preserves the JSON shape pre-refactor handlers and Playwright
// specs depend on (lower-camelCase keys, no zero-value fields surfaced).
func refToMap(ref BackupRef) map[string]interface{} {
	m := map[string]interface{}{
		"id":        ref.ID,
		"projectId": ref.ProjectID,
		"type":      ref.Type,
		"status":    ref.Status,
	}
	if ref.StartedAt != "" {
		m["timestamp"] = ref.StartedAt
		m["startedAt"] = ref.StartedAt
	}
	if ref.FinishedAt != "" {
		m["finishedAt"] = ref.FinishedAt
	}
	if ref.SizeBytes > 0 {
		m["sizeBytes"] = ref.SizeBytes
	}
	return m
}
