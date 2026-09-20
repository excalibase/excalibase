package service

import (
	"context"
	"fmt"
	"time"

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

// NewBackupService keeps the single-K8s wiring used by tests. Internally
// it builds a one-entry adapter map so dispatch still works. backupStorage
// is the store backups are written to; restores read from the same one.
func NewBackupService(store storage.InstanceStore, client k8s.KubeClient, storagePath string, backupStorage BackupStorageSource) *BackupService {
	adapter := NewK8sBackupAdapter(client, storagePath, backupStorage)
	adapter.SetInstanceStore(store)
	return NewBackupServiceWithAdapters(store, map[domain.DeploymentMode]BackupAdapter{
		domain.ModeK8s: adapter,
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
// SetProjectRegistrar hands the shared registration path to every adapter
// that restores into a new project. Without it a restore refuses to run
// rather than producing a database no API route can reach.
func (s *BackupService) SetProjectRegistrar(r ProjectRegistrar) {
	for _, adapter := range s.adapters {
		if setter, ok := adapter.(interface{ SetProjectRegistrar(ProjectRegistrar) }); ok {
			setter.SetProjectRegistrar(r)
		}
	}
}

// SetDatabaseProbe hands every adapter the check that proves a recovered
// database serves queries before its project is activated.
//
// It reports an error instead of wiring nothing: a platform whose adapters
// cannot verify a restore has no business starting, and finding that out
// from a customer's first restore is finding it out too late.
func (s *BackupService) SetDatabaseProbe(p DatabaseProbe) error {
	if p == nil {
		return fmt.Errorf("%w: no probe was supplied", ErrDatabaseProbeNotConfigured)
	}
	if len(s.adapters) == 0 {
		return fmt.Errorf("%w: no backup adapters are registered", ErrDatabaseProbeNotConfigured)
	}
	for mode, adapter := range s.adapters {
		setter, ok := adapter.(interface{ SetDatabaseProbe(DatabaseProbe) })
		if !ok {
			return fmt.Errorf("%w: the %s adapter cannot verify a restore", ErrDatabaseProbeNotConfigured, mode)
		}
		setter.SetDatabaseProbe(p)
	}
	return nil
}

// SetRestoreReadyTimeout bounds how long every adapter waits for a recovered
// database to be observed ready.
func (s *BackupService) SetRestoreReadyTimeout(d time.Duration) {
	for _, adapter := range s.adapters {
		if setter, ok := adapter.(interface{ SetRestoreReadyTimeout(time.Duration) }); ok {
			setter.SetRestoreReadyTimeout(d)
		}
	}
}

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

// BackupsConfigured answers, for one project, whether a backup taken now
// would be written anywhere. Two things have to hold: the project has backups
// turned on, and the platform has an object store to put them in.
//
// The per-project half matters as much as the platform half. A project on a
// tier without backups (FREE) that is asked for one anyway gets a Backup CR
// the engine accepts and then fails asynchronously — long after whoever asked
// believed it had a recovery point (EXC-363). A row whose flag was never set
// is treated as off: nothing has claimed this project has backups.
func (s *BackupService) BackupsConfigured(projectID string) (bool, error) {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return false, fmt.Errorf("project not found: %s", projectID)
	}
	if inst.BackupEnabled == nil || !*inst.BackupEnabled {
		return false, nil
	}
	adapter, err := resolveAdapter(s.adapters, inst)
	if err != nil {
		return false, err
	}
	return adapter.BackupsConfigured(), nil
}

// BackupStatus reports one backup's current status. Listing is what syncs a
// backup record with what the engine says about it, so this is also how a
// caller watching a backup sees it finish.
func (s *BackupService) BackupStatus(ctx context.Context, projectID, backupID string) (string, error) {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return "", fmt.Errorf("project not found: %s", projectID)
	}
	adapter, err := resolveAdapter(s.adapters, inst)
	if err != nil {
		return "", err
	}
	refs, err := adapter.List(ctx, inst)
	if err != nil {
		return "", err
	}
	for _, ref := range refs {
		if ref.ID == backupID {
			return ref.Status, nil
		}
	}
	return "", fmt.Errorf("backup %s not found for project %s", backupID, projectID)
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
	if req.TargetProjectID == "" {
		return nil, ErrTargetProjectIDMissing
	}
	return adapter.Restore(ctx, inst, req)
}

// AllocateProjectID reserves the id a restore will register its new project
// under. The caller of a restore never names it — see EXC-415.
func (s *BackupService) AllocateProjectID() (string, error) {
	return allocateProjectID(s.store)
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
