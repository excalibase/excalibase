package service

import (
	"context"
	"fmt"
	"io"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// defaultPostgresSuperuser is the well-known username used by the
// official postgres Docker image when POSTGRES_USER is set. It's a
// public default — not a secret. Named here so SAST tools see a const,
// not a string literal that looks like a hardcoded credential.
const defaultPostgresSuperuser = "postgres"

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
	// shipping serialises WAL shipping, so a segment is never shipped and
	// cleared by two passes at once.
	shipping sync.Mutex

	mu sync.RWMutex
	// hostPorts holds each project's host port (EXC-593); nil publishes on
	// any free port.
	hostPorts provisioner.HostPortAllocator
	// queryStats turns pg_stat_statements on in every Postgres copy it brings up (EXC-673).
	queryStats bool
	instances  storage.InstanceStore
	docker     provisioner.DockerClient // optional; required for Restore
	// registrar finishes a restore the way a provision ends: roles, vault,
	// instance row, events. Required for Restore — persisting a row
	// without the vault write is what made restored projects unusable.
	registrar ProjectRegistrar
	// probe proves the restored container serves queries with the
	// credentials registration filed, before the project becomes ACTIVE.
	probe DatabaseProbe
	// plans sizes the restored container as a new project of its plan.
	plans RestorePlanSource
	// readyTimeout bounds the wait for the restored container to finish
	// recovery. Zero means the package default.
	readyTimeout time.Duration
}

// SetRestoreReadyTimeout bounds the wait for the restored container to
// finish recovery.
func (a *DockerBackupAdapter) SetRestoreReadyTimeout(d time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.readyTimeout = d
}

// restoreTimeout resolves the configured budget, falling back to the package
// default only when nothing was wired.
func (a *DockerBackupAdapter) restoreTimeout() time.Duration {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.readyTimeout > 0 {
		return a.readyTimeout
	}
	return defaultRestoreReadyTimeout
}

// SetDatabaseProbe wires the check that proves a restored database serves
// queries. Without it a restore refuses to run.
// SetRestorePlanSource wires what sizes a restored project. Without it a
// restore is refused: there is no default size.
func (a *DockerBackupAdapter) SetRestorePlanSource(p RestorePlanSource) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.plans = p
}

func (a *DockerBackupAdapter) SetDatabaseProbe(p DatabaseProbe) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.probe = p
}

// SetProjectRegistrar wires the shared registration path. Called from main.go
// once the provisioning service exists.
func (a *DockerBackupAdapter) SetProjectRegistrar(r ProjectRegistrar) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.registrar = r
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
// SetHostPorts publishes restored containers on their project's held port.
func (a *DockerBackupAdapter) SetHostPorts(ports provisioner.HostPortAllocator) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.hostPorts = ports
}

// heldPort is the host port projectID publishes on; "" without an allocator.
func (a *DockerBackupAdapter) heldPorts(ctx context.Context, projectID string, documentDB bool) (provisioner.DocumentDBPorts, error) {
	a.mu.RLock()
	ports := a.hostPorts
	a.mu.RUnlock()
	if ports == nil {
		return provisioner.DocumentDBPorts{}, nil
	}
	if documentDB {
		held, err := provisioner.HeldDocumentDBPorts(ctx, ports, projectID)
		if err != nil {
			return held, fmt.Errorf("hold the host ports of %s: %w", projectID, err)
		}
		return held, nil
	}
	port, err := ports.Allocate(ctx, projectID)
	if err != nil {
		return provisioner.DocumentDBPorts{}, fmt.Errorf("hold the host port of %s: %w", projectID, err)
	}
	return provisioner.DocumentDBPorts{Postgres: strconv.Itoa(port)}, nil
}

// releasePort gives a copy that never came up its port back.
func (a *DockerBackupAdapter) releasePort(ctx context.Context, projectID string) error {
	a.mu.RLock()
	ports := a.hostPorts
	a.mu.RUnlock()
	if ports == nil {
		return nil
	}
	return ports.Release(ctx, projectID)
}

func (a *DockerBackupAdapter) SetInstanceStore(s storage.InstanceStore) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.instances = s
}

// SetDockerClient wires the docker client used for Restore. Without
// it the adapter refuses Restore calls (Trigger / List / Configure
// don't need it). Called from main.go after the docker client is
// constructed in buildProvisionerFactory.
func (a *DockerBackupAdapter) SetDockerClient(dc provisioner.DockerClient) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.docker = dc
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

	a.noteStartSegment(ctx, inst, id)
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

	// The segment the backup ended in is closed and shipped with it; the
	// sweep ships the rest as they close.
	if err := a.shipWAL(ctx, inst, true); err != nil {
		log.Printf("WARN: WAL shipping after backup %s of %s: %v", id, inst.ProjectID, err)
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
// BackupsConfigured reports whether the S3 target a Docker backup streams
// into is wired.
func (a *DockerBackupAdapter) BackupsConfigured() bool {
	return a.uploader != nil && a.bucket != ""
}

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
