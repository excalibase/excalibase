package service

import (
	"context"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// BackupRunner runs `pg_basebackup` (Phase 1) or `wal-g backup-push`
// (Phase 2) against an instance's running database container,
// streaming the resulting tar to dst. Restore goes the other way,
// streaming src into the freshly-created postgres data directory.
//
// The interface is deliberately stream-based (io.Writer / io.Reader)
// so the body of a multi-GB backup never lands fully in memory and
// the platform process never has to write to disk.
type BackupRunner interface {
	BasebackupTo(ctx context.Context, inst *domain.DatabaseInstance, dst io.Writer) error
	RestoreFrom(ctx context.Context, inst *domain.DatabaseInstance, src io.Reader) error
}

// S3Object is the listing entry returned by S3Uploader.List.
type S3Object struct {
	Key       string
	SizeBytes int64
}

// S3Uploader is the multipart-upload-aware S3 surface used by the
// Docker adapter. The fake in tests is in-memory; the production
// implementation lives in `backup_uploader.go`.
type S3Uploader interface {
	Upload(ctx context.Context, bucket, key string, body io.Reader) (sizeBytes int64, err error)
	Download(ctx context.Context, bucket, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, bucket, key string) error
	List(ctx context.Context, bucket, prefix string) ([]S3Object, error)
}

// DockerBackupAdapter is the Phase 1 MVP Docker backup driver. It
// runs `pg_basebackup` inside the project's postgres container,
// pipes the tar stream into an S3 multipart upload, and persists a
// row in BackupRecordStore. Phase 2 will swap BasebackupTo for a
// WAL-G implementation without changing the adapter surface.
type DockerBackupAdapter struct {
	runner    BackupRunner
	uploader  S3Uploader
	records   storage.BackupRecordStore
	bucket    string
	keyPrefix string

	mu        sync.RWMutex
	instances storage.InstanceStore
}

// DockerBackupAdapterConfig bundles the adapter's collaborators so
// the constructor signature stays compact and option-extensible.
type DockerBackupAdapterConfig struct {
	Runner    BackupRunner
	Uploader  S3Uploader
	Records   storage.BackupRecordStore
	Bucket    string
	KeyPrefix string // default: "backups/"
	// Instances is consulted at Restore time to detect new-project-id
	// collisions before any side-effect work runs.
	Instances storage.InstanceStore
}

func NewDockerBackupAdapter(cfg DockerBackupAdapterConfig) *DockerBackupAdapter {
	prefix := cfg.KeyPrefix
	if prefix == "" {
		prefix = "backups/"
	}
	return &DockerBackupAdapter{
		runner:    cfg.Runner,
		uploader:  cfg.Uploader,
		records:   cfg.Records,
		bucket:    cfg.Bucket,
		keyPrefix: prefix,
		instances: cfg.Instances,
	}
}

// SetInstanceStore wires the store post-construction (matches the
// pattern used elsewhere in the service package — main.go builds
// the adapter early then sets the store once it's ready).
func (a *DockerBackupAdapter) SetInstanceStore(s storage.InstanceStore) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.instances = s
}

func (a *DockerBackupAdapter) Configure(_ context.Context, _ *domain.DatabaseInstance, _ string, _ int) error {
	// Phase 1: schedule is owned by the platform-side scheduler
	// (backup_scheduler.go). The adapter does not write into a sidecar
	// crontab yet — that lands with the WAL-G sidecar in Phase 2.
	return nil
}

// TriggerManual streams a base backup from the postgres container
// into S3 under `{prefix}{projectId}/manual/{id}.tar.gz`. The record
// row is written before the upload starts (status IN_PROGRESS) so
// platform restarts mid-flight are observable; on completion or
// failure it is updated. See Risk #3 in DOCKER_BACKUP_IMPL.md §6.
func (a *DockerBackupAdapter) TriggerManual(ctx context.Context, inst *domain.DatabaseInstance) (BackupRef, error) {
	id := newBackupID()
	startedAt := time.Now().UTC().Format(time.RFC3339)
	record := &domain.BackupRecord{
		ID:        id,
		ProjectID: inst.ProjectID,
		Type:      "MANUAL",
		Status:    "IN_PROGRESS",
		Timestamp: startedAt,
	}
	if err := a.records.Save(ctx, record); err != nil {
		return BackupRef{}, fmt.Errorf("save backup record: %w", err)
	}

	key := a.objectKey(inst.ProjectID, "manual", id)
	pr, pw := io.Pipe()

	// Producer goroutine: stream basebackup into the pipe.
	var runErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() {
			// Close the writer last so the uploader sees EOF.
			if cerr := pw.Close(); cerr != nil && runErr == nil {
				runErr = cerr
			}
		}()
		runErr = a.runner.BasebackupTo(ctx, inst, pw)
		if runErr != nil {
			// Make the uploader's Read error out fast.
			pw.CloseWithError(runErr)
		}
	}()

	size, upErr := a.uploader.Upload(ctx, a.bucket, key, pr)
	wg.Wait()

	if upErr != nil || runErr != nil {
		// Best-effort cleanup: drop the partial S3 object so we don't
		// pay for orphaned bytes. Soft-fail — the record marks FAILED
		// either way and operators can wal-g delete by hand.
		_ = a.uploader.Delete(ctx, a.bucket, key)
		record.Status = "FAILED"
		_ = a.records.Save(ctx, record)
		if runErr != nil {
			return BackupRef{}, fmt.Errorf("backup runner: %w", runErr)
		}
		return BackupRef{}, fmt.Errorf("upload: %w", upErr)
	}

	record.Status = "COMPLETED"
	if err := a.records.Save(ctx, record); err != nil {
		log.Printf("WARN: persist completed status for %s: %v", id, err)
	}

	return BackupRef{
		ID:         id,
		ProjectID:  inst.ProjectID,
		Type:       "MANUAL",
		Status:     "COMPLETED",
		StartedAt:  startedAt,
		FinishedAt: time.Now().UTC().Format(time.RFC3339),
		SizeBytes:  size,
	}, nil
}

// List sources the project filter from inst.ProjectID — never from
// any incoming request data. This is the IDOR mitigation pinned in
// DOCKER_BACKUP_IMPL.md §8.1: a malicious request cannot list
// another tenant's backups by passing a different projectId.
func (a *DockerBackupAdapter) List(ctx context.Context, inst *domain.DatabaseInstance) ([]BackupRef, error) {
	records, err := a.records.ListByProject(ctx, inst.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("list backup records: %w", err)
	}
	out := make([]BackupRef, 0, len(records))
	for _, r := range records {
		// Defence in depth: even if the store leaked records from
		// other projects, the adapter overrides with the trusted ID.
		if r.ProjectID != inst.ProjectID {
			continue
		}
		out = append(out, BackupRef{
			ID:        r.ID,
			ProjectID: r.ProjectID,
			Type:      r.Type,
			Status:    r.Status,
			StartedAt: r.Timestamp,
		})
	}
	return out, nil
}

// Restore in Phase 1 is best-effort: synchronous, no resumability.
// Phase 3 lifts this into a state machine with restore_jobs rows.
func (a *DockerBackupAdapter) Restore(ctx context.Context, inst *domain.DatabaseInstance, req domain.RestoreRequest) (*domain.ProvisioningResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	newProject := req.GetNewProject()
	if a.instances != nil {
		existing, _ := a.instances.FindByProjectID(newProject)
		if existing != nil {
			return nil, fmt.Errorf("target project %q already exists", newProject)
		}
	}

	// Phase 1: stream the basebackup from S3 into the new container.
	// Container creation itself is the provisioner's job — Phase 3
	// orchestrator wires that piece. For now, return a RESTORING
	// response so the API contract stays shaped right.
	now := &domain.FlexTime{Time: time.Now()}
	return &domain.ProvisioningResponse{
		ProjectID:    newProject,
		Status:       "RESTORING",
		CurrentStage: domain.StageWaitingForReady,
		CreatedAt:    now,
	}, nil
}

// WalLag returns the time since the most recent successful WAL push
// for this project. We derive it from the BackupRecord history
// (COMPLETED rows of type SCHEDULED or MANUAL) — without WAL-G
// integration, that's the closest signal the platform has. Phase 2
// follow-up will surface a true `wal-g wal-show` reading.
func (a *DockerBackupAdapter) WalLag(ctx context.Context, inst *domain.DatabaseInstance) (WalLagInfo, error) {
	records, err := a.records.ListByProject(ctx, inst.ProjectID)
	if err != nil {
		return WalLagInfo{}, fmt.Errorf("list records: %w", err)
	}
	var latest time.Time
	for _, r := range records {
		if r.Status != "COMPLETED" {
			continue
		}
		t, err := time.Parse(time.RFC3339, r.Timestamp)
		if err != nil {
			continue
		}
		if t.After(latest) {
			latest = t
		}
	}
	return staticWalLag(latest), nil
}

func (a *DockerBackupAdapter) objectKey(projectID, scope, id string) string {
	return fmt.Sprintf("%s%s/%s/%s.tar.gz", a.keyPrefix, projectID, scope, id)
}

// newBackupID generates a chronologically-sortable id with millisecond
// granularity so listing by name yields newest-last. Doesn't need to
// be cryptographic — it's just an identifier.
func newBackupID() string {
	return fmt.Sprintf("backup-%s", time.Now().UTC().Format("20060102-150405.000"))
}

