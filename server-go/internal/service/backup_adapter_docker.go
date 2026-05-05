package service

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
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
	docker    provisioner.DockerClient // optional; required for Restore
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

// Restore provisions a NEW project seeded from `inst`'s most recent
// COMPLETED base backup in S3. Source project is untouched.
//
// Pipeline (synchronous; orchestrator step calls this):
//
//  1. Validate + check newProject doesn't already exist.
//  2. Resolve the most recent COMPLETED BackupRecord for the source
//     project; download its tar.gz from S3 to a stream.
//  3. Create the new container in stopped state with the same
//     postgres image / DB / superuser env as a fresh provision.
//  4. Gunzip + CopyToContainer into /var/lib/postgresql/data so the
//     image's initdb is skipped on first start.
//  5. Start the container + WaitForHealthy.
//  6. Persist the new instance row pointing at the new container.
//
// Returns a `RESTORING` ProvisioningResponse on success — the
// orchestrator's pipeline step interprets a nil error as restore
// completed and stamps the RestoreJob COMPLETED.
func (a *DockerBackupAdapter) Restore(ctx context.Context, inst *domain.DatabaseInstance, req domain.RestoreRequest) (*domain.ProvisioningResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	a.mu.RLock()
	dc := a.docker
	store := a.instances
	a.mu.RUnlock()

	newProject := req.GetNewProject()
	// Collision check first so the existing-target test path doesn't
	// require a docker client (matches the K8s adapter ordering).
	if store != nil {
		if existing, _ := store.FindByProjectID(newProject); existing != nil {
			return nil, fmt.Errorf("target project %q already exists", newProject)
		}
	}
	if dc == nil {
		return nil, fmt.Errorf("docker restore: docker client not configured (call SetDockerClient)")
	}

	// 2. Resolve the most recent base backup. We pick the latest
	// COMPLETED record by Timestamp; PITR will refine this when we
	// wire WAL replay in Phase 2.
	records, err := a.records.ListByProject(ctx, inst.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("list backup records: %w", err)
	}
	src, err := pickLatestCompletedBackup(records)
	if err != nil {
		return nil, err
	}
	srcKey := a.objectKey(inst.ProjectID, "manual", src.ID)
	body, err := a.uploader.Download(ctx, a.bucket, srcKey)
	if err != nil {
		return nil, fmt.Errorf("download base backup %s: %w", srcKey, err)
	}
	defer body.Close()

	// 3. Create the new container, stopped. Same image as the source
	// instance's PostgresVersion, defaulting to postgres:17 (matches
	// DockerProvisioner.Provision). Generate a fresh password — the
	// restored cluster keeps its old DB users/passwords from the
	// backup, but the platform's superuser env still needs a value
	// for the image's healthcheck path.
	containerName := fmt.Sprintf("excalibase-%s-postgres", newProject)
	dbName := inst.DatabaseName
	if dbName == "" {
		dbName = "app"
	}
	newPassword := generateRestorePassword()
	env := map[string]string{
		"POSTGRES_DB":       dbName,
		"POSTGRES_USER":     "postgres",
		"POSTGRES_PASSWORD": newPassword,
	}
	image := "postgres:17"
	if inst.PostgresVersion != "" {
		image = "postgres:" + inst.PostgresVersion
	}
	containerID, err := dc.CreateContainer(ctx, containerName, image, env, map[string]string{"5432": ""})
	if err != nil {
		return nil, fmt.Errorf("create restore container: %w", err)
	}

	// 4. Gunzip + extract into PGDATA before postgres starts. If
	// initdb were to run first it would fail on a non-empty data dir.
	gz, err := gzip.NewReader(body)
	if err != nil {
		_ = dc.RemoveContainer(ctx, containerID)
		return nil, fmt.Errorf("gunzip backup stream: %w", err)
	}
	defer gz.Close()
	if err := dc.CopyToContainer(ctx, containerID, "/var/lib/postgresql/data", gz); err != nil {
		_ = dc.RemoveContainer(ctx, containerID)
		return nil, fmt.Errorf("extract backup into container: %w", err)
	}

	// 5. Start + wait for ready.
	if err := dc.StartContainer(ctx, containerID); err != nil {
		_ = dc.RemoveContainer(ctx, containerID)
		return nil, fmt.Errorf("start restored container: %w", err)
	}
	if err := dc.WaitForHealthy(ctx, containerID); err != nil {
		// Don't auto-remove on health failure — operator may want
		// to inspect the container for diagnosis.
		return nil, fmt.Errorf("restored container did not become healthy: %w", err)
	}

	// 6. Persist the instance row. Source project untouched — same
	// shape as K8s adapter's Restore.
	now := &domain.FlexTime{Time: time.Now()}
	newInst := &domain.DatabaseInstance{
		ProjectID:      newProject,
		ProjectName:    req.GetNewProject(),
		OrgID:          inst.OrgID,
		DBType:         inst.DBType,
		Tier:           inst.Tier,
		DeploymentMode: domain.ModeDocker,
		Namespace:      containerID,
		Host:           containerName,
		DatabaseName:   dbName,
		Username:       "postgres",
		Password:       newPassword,
		Status:         "ACTIVE",
		CurrentStage:   domain.StageCompleted,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	port := 5432
	newInst.Port = &port
	if store != nil {
		if err := store.Save(newInst); err != nil {
			log.Printf("WARN: restore persist new instance: %v", err)
		}
	}

	return &domain.ProvisioningResponse{
		ProjectID:    newProject,
		Status:       "RESTORING",
		CurrentStage: domain.StageCompleted,
		Host:         containerName,
		Port:         &port,
		DatabaseName: dbName,
		Namespace:    containerID,
		CreatedAt:    now,
	}, nil
}

// pickLatestCompletedBackup picks the most recent COMPLETED record
// by Timestamp. Returns an error when the source project has no
// usable base backup yet — the caller surfaces this to the user.
func pickLatestCompletedBackup(records []domain.BackupRecord) (*domain.BackupRecord, error) {
	completed := make([]domain.BackupRecord, 0, len(records))
	for _, r := range records {
		if r.Status == "COMPLETED" {
			completed = append(completed, r)
		}
	}
	if len(completed) == 0 {
		return nil, fmt.Errorf("no completed backup available to restore from")
	}
	sort.Slice(completed, func(i, j int) bool {
		return completed[i].Timestamp > completed[j].Timestamp
	})
	r := completed[0]
	return &r, nil
}

// generateRestorePassword produces a one-shot password for the
// restored container's superuser env. The actual DB users + their
// passwords come from the backup; this only satisfies the postgres
// image's startup contract.
func generateRestorePassword() string {
	return fmt.Sprintf("restored-%d", time.Now().UnixNano())
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

