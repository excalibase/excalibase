package service

import (
	"context"
	"errors"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// BackupRef is the mode-agnostic descriptor of a single backup.
// Every adapter (K8s/CNPG, Docker/WAL-G, future MySQL) returns the same
// shape so handler responses stay uniform regardless of mode.
type BackupRef struct {
	ID         string `json:"id"`
	ProjectID  string `json:"projectId"`
	Type       string `json:"type"`   // MANUAL | SCHEDULED
	Status     string `json:"status"` // IN_PROGRESS | COMPLETED | FAILED
	StartedAt  string `json:"startedAt,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
	SizeBytes  int64  `json:"sizeBytes,omitempty"`
}

// BackupAdapter routes backup/restore lifecycle to the deployment-mode
// implementation. K8s wraps the existing CNPG flow; Docker drives a
// pg_basebackup → R2 path (Phase 1) that Phase 2 swaps for WAL-G.
type BackupAdapter interface {
	// Configure persists the schedule + retention onto the underlying
	// store (CNPG ScheduledBackup CRD or sidecar crontab in Docker mode).
	Configure(ctx context.Context, inst *domain.DatabaseInstance, schedule string, retentionDays int) error

	// TriggerManual kicks off an ad-hoc backup. Returns immediately with
	// an IN_PROGRESS ref; status is updated asynchronously.
	TriggerManual(ctx context.Context, inst *domain.DatabaseInstance) (BackupRef, error)

	// List returns every known backup for a project. Implementations
	// must source the project filter from inst.ProjectID, never from
	// untrusted request data — see DOCKER_BACKUP_IMPL.md §8.1 (IDOR).
	List(ctx context.Context, inst *domain.DatabaseInstance) ([]BackupRef, error)

	// BackupsConfigured reports whether this adapter has somewhere to write
	// a project's backups. A caller that must not proceed without a backup
	// asks first rather than discovering it from a failure.
	BackupsConfigured() bool

	// Restore provisions a NEW project seeded from a backup. The source
	// project is untouched. Returns the new project's provisioning
	// response with status RESTORING.
	Restore(ctx context.Context, inst *domain.DatabaseInstance, req domain.RestoreRequest) (*domain.ProvisioningResponse, error)
}

// defaultRestoreDatabase is the database name a restored project falls back
// to when the source row carries none — matches the provisioners' default.
const defaultRestoreDatabase = "app"

// ErrUnsupportedBackupMode is returned when a project's DeploymentMode
// has no adapter registered — a mode with no
// backup surface (operator owns it).
var ErrUnsupportedBackupMode = errors.New("backup not supported for this deployment mode")

// ErrDocumentDBRestoreNotSupported is returned when a restore is asked for a
// DocumentDB project. The recovered cluster from a plain CNPG/pg_basebackup
// restore carries none of the DocumentDB setup (preload, gateway plugin,
// gateway credentials, the project's DocumentDB flag), so it would come back
// half-working. Refused before anything is created, until that setup is
// carried through a restore.
var ErrDocumentDBRestoreNotSupported = errors.New("restoring a DocumentDB project is not supported yet")

// resolveAdapter looks up the adapter for an instance's mode, treating
// the empty string as ModeK8s for legacy rows that pre-date the
// deployment_mode column migration.
func resolveAdapter(adapters map[domain.DeploymentMode]BackupAdapter, inst *domain.DatabaseInstance) (BackupAdapter, error) {
	mode := inst.DeploymentMode
	if mode == "" {
		mode = domain.ModeK8s
	}
	adapter, ok := adapters[mode]
	if !ok {
		return nil, ErrUnsupportedBackupMode
	}
	return adapter, nil
}
