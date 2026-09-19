package service

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log"
	"sort"
	"strings"
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

	mu        sync.RWMutex
	instances storage.InstanceStore
	docker    provisioner.DockerClient // optional; required for Restore
	// registrar finishes a restore the way a provision ends: roles, vault,
	// instance row, PgDog, events. Required for Restore — persisting a row
	// without the vault write is what made restored projects unusable.
	registrar ProjectRegistrar
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

	// Phase 2: ship any WALs that piled up in /walarchive (set by
	// archive_command at provision time when backup is enabled).
	// Required for PITR — recovery on restore needs the WAL stream
	// from after the basebackup, which lives in /walarchive on the
	// source. Best-effort: if archive_mode wasn't enabled, /walarchive
	// doesn't exist and we silently skip.
	a.mu.RLock()
	dc := a.docker
	a.mu.RUnlock()
	if dc != nil {
		if err := a.uploadWALArchive(ctx, dc, inst); err != nil {
			log.Printf("WARN: WAL archive upload for %s: %v", inst.ProjectID, err)
		}
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

// walArchivePath is where archive_command drops closed WAL segments
// inside the project's container. Set by ConfigureArchive at provision
// time when backup is enabled.
const walArchivePath = "/walarchive"

const pgDataPath = "/var/lib/postgresql/data"

// RefreshWALArchive ships any new WALs that have piled up in the
// project's /walarchive since the last call. Public + callable
// out-of-band so tests (and ops cron tasks) can ship WALs without
// taking a full base backup. Equivalent to the WAL upload step
// inside TriggerManual but standalone.
func (a *DockerBackupAdapter) RefreshWALArchive(ctx context.Context, inst *domain.DatabaseInstance) error {
	a.mu.RLock()
	dc := a.docker
	a.mu.RUnlock()
	if dc == nil {
		return fmt.Errorf("docker client not configured")
	}
	return a.uploadWALArchive(ctx, dc, inst)
}

// uploadWALArchive forces a WAL switch + checkpoint on the source,
// then streams /walarchive out via CopyFromContainer, gzips each
// entry, and uploads to S3 at backups/{projectId}/wals/{name}.gz.
//
// Best-effort: missing /walarchive (archive_mode never enabled),
// empty directory, or permission errors return nil so backup
// completion isn't blocked by archive plumbing.
func (a *DockerBackupAdapter) uploadWALArchive(ctx context.Context, dc provisioner.DockerClient, inst *domain.DatabaseInstance) error {
	if inst.Namespace == "" {
		return fmt.Errorf("no container id on instance")
	}
	dbName := inst.DatabaseName
	if dbName == "" {
		dbName = "postgres"
	}
	// Force WAL switch + checkpoint so the segment containing recent
	// commits closes and becomes archivable. PGPASSWORD env keeps the
	// psql exec from prompting.
	for _, sql := range []string{"SELECT pg_switch_wal();", "CHECKPOINT;"} {
		_, _ = dc.ExecInContainer(ctx, inst.Namespace,
			[]string{"sh", "-c", fmt.Sprintf("PGPASSWORD=%q psql -U postgres -d %s -c %q", inst.Password, dbName, sql)})
	}

	stream, err := dc.CopyFromContainer(ctx, inst.Namespace, walArchivePath)
	if err != nil {
		// /walarchive doesn't exist → archive_mode wasn't on. Not a
		// failure for backup itself.
		return nil
	}
	defer stream.Close()

	prefix := fmt.Sprintf("%s%s/wals/", a.keyPrefix, inst.ProjectID)
	tr := tar.NewReader(stream)
	uploaded := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read walarchive tar: %w", err)
		}
		name := walSegmentName(hdr)
		if name == "" {
			continue
		}
		if err := a.uploadOneWAL(ctx, tr, prefix, name); err != nil {
			return err
		}
		uploaded++
	}
	if uploaded > 0 {
		log.Printf("INFO: uploaded %d WAL segment(s) for %s", uploaded, inst.ProjectID)
	}
	return nil
}

// walSegmentName returns the base file name for a regular-file tar header, or
// "" for non-regular entries (directories, etc.) that should be skipped.
func walSegmentName(hdr *tar.Header) string {
	if hdr.Typeflag != tar.TypeReg {
		return ""
	}
	name := hdr.Name
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return name
}

// uploadOneWAL gzips a single WAL segment read from tr and uploads it under
// prefix+name+".gz".
func (a *DockerBackupAdapter) uploadOneWAL(ctx context.Context, tr io.Reader, prefix, name string) error {
	data, err := io.ReadAll(tr)
	if err != nil {
		return fmt.Errorf("read WAL %s: %w", name, err)
	}
	var gzBuf bytes.Buffer
	gw := gzip.NewWriter(&gzBuf)
	if _, err := gw.Write(data); err != nil {
		return fmt.Errorf("gzip WAL %s: %w", name, err)
	}
	gw.Close()

	key := prefix + name + ".gz"
	if _, err := a.uploader.Upload(ctx, a.bucket, key, &gzBuf); err != nil {
		return fmt.Errorf("upload WAL %s: %w", name, err)
	}
	return nil
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
//  6. Register it as a project through the shared registration path.
//
// Returns an ACTIVE ProvisioningResponse on success — the orchestrator's
// pipeline step interprets a nil error as restore completed and stamps the
// RestoreJob COMPLETED. A failure after the container exists leaves it
// running for inspection with no project row, and fails the job.
func (a *DockerBackupAdapter) Restore(ctx context.Context, inst *domain.DatabaseInstance, req domain.RestoreRequest) (*domain.ProvisioningResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	a.mu.RLock()
	dc := a.docker
	store := a.instances
	registrar := a.registrar
	a.mu.RUnlock()

	newProject := req.TargetProjectID
	// Collision check first so nothing is created for a target id that is
	// taken (matches the K8s adapter ordering).
	if err := assertProjectIDAvailable(store, newProject); err != nil {
		return nil, err
	}
	if dc == nil {
		return nil, fmt.Errorf("docker restore: docker client not configured (call SetDockerClient)")
	}
	if registrar == nil {
		return nil, ErrProjectRegistrarNotConfigured
	}

	// 2. Resolve the source backup. Default: most recent COMPLETED
	// record by Timestamp. When req.BackupID is set, restore from
	// that specific backup — required for PITR scenarios where the
	// target lives in WALs accumulated AFTER an older backup.
	srcRec, err := a.resolveSourceBackup(ctx, inst.ProjectID, req.BackupID)
	if err != nil {
		return nil, err
	}
	srcKey := a.objectKey(inst.ProjectID, "manual", srcRec.ID)
	body, err := a.uploader.Download(ctx, a.bucket, srcKey)
	if err != nil {
		return nil, fmt.Errorf("download base backup %s: %w", srcKey, err)
	}
	defer body.Close()

	// 3-4c. Create the new container, seed PGDATA from the base backup,
	// download archived WALs, and write recovery directives.
	dbName := inst.DatabaseName
	if dbName == "" {
		dbName = defaultRestoreDatabase
	}
	containerName := fmt.Sprintf("excalibase-%s-postgres", newProject)
	newPassword := generateRestorePassword()
	containerID, err := a.createAndSeedRestoreContainer(ctx, dc, restoreContainerSpec{
		containerName:   containerName,
		dbName:          dbName,
		newPassword:     newPassword,
		image:           restoreImage(inst.PostgresVersion),
		newProject:      newProject,
		sourceProjectID: inst.ProjectID,
		body:            body,
		req:             req,
	})
	if err != nil {
		return nil, err
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
	if err := a.waitForPromotedPostgres(ctx, dc, containerID, dbName); err != nil {
		return nil, err
	}

	// 6. Register the restored database as a project, exactly the way a
	// provision ends: roles + vault credentials, the ACTIVE row, PgDog and
	// the project-created event. The container keeps the source's superuser
	// password (the seeded data dir ignores POSTGRES_PASSWORD), so the admin
	// role is reset to the password stamped on the row.
	newInst := restoredDockerInstance(inst, restoredDockerSpec{
		projectID:     newProject,
		projectName:   req.NewProjectName,
		containerID:   containerID,
		containerName: containerName,
		dbName:        dbName,
		password:      newPassword,
		backupID:      srcRec.ID,
	})
	opts := RegistrationOptions{ResetRolePasswords: true, ResetAdminPassword: true}
	if err := registrar.RegisterProject(ctx, newInst, opts); err != nil {
		return nil, fmt.Errorf("register restored project: %w", err)
	}

	return &domain.ProvisioningResponse{
		ProjectID:    newInst.ProjectID,
		ProjectName:  newInst.ProjectName,
		Status:       newInst.Status,
		CurrentStage: newInst.CurrentStage,
		Host:         newInst.Host,
		Port:         newInst.Port,
		DatabaseName: newInst.DatabaseName,
		Namespace:    newInst.Namespace,
		CreatedAt:    newInst.CreatedAt,
	}, nil
}

// restoredDockerSpec carries what the restore learned about the new container.
type restoredDockerSpec struct {
	projectID     string
	projectName   string
	containerID   string
	containerName string
	dbName        string
	password      string
	backupID      string
}

// restoredDockerInstance builds the project row for a restored container,
// inheriting the source project's org, owner, type and tier.
func restoredDockerInstance(src *domain.DatabaseInstance, spec restoredDockerSpec) *domain.DatabaseInstance {
	port := 5432
	return &domain.DatabaseInstance{
		ProjectID:             spec.projectID,
		ProjectName:           spec.projectName,
		OrgID:                 src.OrgID,
		OwnerID:               src.OwnerID,
		DBType:                src.DBType,
		Tier:                  src.Tier,
		DeploymentMode:        domain.ModeDocker,
		Namespace:             spec.containerID,
		Host:                  spec.containerName,
		Port:                  &port,
		DatabaseName:          spec.dbName,
		Username:              defaultPostgresSuperuser,
		Password:              spec.password,
		PostgresVersion:       src.PostgresVersion,
		RestoredFromProjectID: src.ProjectID,
		RestoredFromBackupID:  spec.backupID,
		CreatedAt:             &domain.FlexTime{Time: time.Now()},
	}
}

// promotedProbeSQL fails unless postgres is out of recovery. The exec API only
// surfaces an exit code, so the probe has to signal through an error rather
// than a result row.
const promotedProbeSQL = `DO $$ BEGIN IF pg_is_in_recovery() THEN RAISE EXCEPTION 'still recovering'; END IF; END $$;`

// waitForPromotedPostgres blocks until the restored container accepts queries
// and has left recovery. Container health only says the process started:
// seeding PGDATA from a base backup means postgres replays WAL first, and a
// PITR restore promotes only once it reaches its target. Registration creates
// roles, which needs a writable primary.
func (a *DockerBackupAdapter) waitForPromotedPostgres(ctx context.Context, dc provisioner.DockerClient, containerID, dbName string) error {
	probe := []string{"psql", "-U", defaultPostgresSuperuser, "-d", dbName, "-v", "ON_ERROR_STOP=1", "-c", promotedProbeSQL}
	deadline := time.Now().Add(defaultRestoreReadyTimeout)
	for time.Now().Before(deadline) {
		if code, err := dc.ExecInContainer(ctx, containerID, probe); err == nil && code == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(dockerRestoreReadyPoll):
		}
	}
	return fmt.Errorf("restored postgres did not finish recovery within %v (container kept for inspection)", defaultRestoreReadyTimeout)
}

// dockerRestoreReadyPoll is the gap between promotion probes. Shorter than the
// K8s poll because exec into a local container is cheap.
const dockerRestoreReadyPoll = time.Second

// resolveSourceBackup returns the BackupRecord to restore from. When backupID
// is set it must reference a COMPLETED record; otherwise the most recent
// COMPLETED record for the project is used.
func (a *DockerBackupAdapter) resolveSourceBackup(ctx context.Context, sourceProjectID, backupID string) (*domain.BackupRecord, error) {
	records, err := a.records.ListByProject(ctx, sourceProjectID)
	if err != nil {
		return nil, fmt.Errorf("list backup records: %w", err)
	}
	if backupID == "" {
		return pickLatestCompletedBackup(records)
	}
	for i := range records {
		if records[i].ID == backupID && records[i].Status == "COMPLETED" {
			r := records[i]
			return &r, nil
		}
	}
	return nil, fmt.Errorf("backup %q not found or not COMPLETED", backupID)
}

// restoreImage returns the postgres image tag for the restored container,
// defaulting to postgres:17 when the source version is unknown.
func restoreImage(postgresVersion string) string {
	if postgresVersion != "" {
		return "postgres:" + postgresVersion
	}
	return "postgres:17"
}

// restoreContainerSpec bundles the inputs to createAndSeedRestoreContainer so
// the helper signature stays readable.
type restoreContainerSpec struct {
	containerName   string
	dbName          string
	newPassword     string
	image           string
	newProject      string
	sourceProjectID string
	body            io.Reader
	req             domain.RestoreRequest
}

// createAndSeedRestoreContainer creates the stopped restore container, extracts
// the base backup into PGDATA, downloads archived WALs, and writes recovery
// directives. On any failure it removes the partially-created container and
// returns a wrapped error. The returned containerID is left stopped for the
// caller to start.
func (a *DockerBackupAdapter) createAndSeedRestoreContainer(ctx context.Context, dc provisioner.DockerClient, spec restoreContainerSpec) (string, error) {
	// Generate a fresh password — the restored cluster keeps its old DB
	// users/passwords from the backup, but the platform's superuser env
	// still needs a value for the image's healthcheck path.
	env := map[string]string{
		"POSTGRES_DB":       spec.dbName,
		"POSTGRES_USER":     "postgres",
		"POSTGRES_PASSWORD": spec.newPassword,
	}
	log.Printf("docker restore: image=%q for new project %q", spec.image, spec.newProject)
	containerID, err := dc.CreateContainer(ctx, spec.containerName, spec.image, env, map[string]string{"5432": ""})
	if err != nil {
		return "", fmt.Errorf("create restore container: %w", err)
	}

	// Gunzip + extract into PGDATA before postgres starts. If initdb were
	// to run first it would fail on a non-empty data dir.
	gz, err := gzip.NewReader(spec.body)
	if err != nil {
		_ = dc.RemoveContainer(ctx, containerID)
		return "", fmt.Errorf("gunzip backup stream: %w", err)
	}
	defer gz.Close()
	if err := dc.CopyToContainer(ctx, containerID, pgDataPath, gz); err != nil {
		_ = dc.RemoveContainer(ctx, containerID)
		return "", fmt.Errorf("extract backup into container: %w", err)
	}

	// PITR: download every archived WAL segment for the source project so
	// recovery has the complete WAL stream from backup-time forward.
	if err := a.downloadWALsIntoContainer(ctx, dc, containerID, spec.sourceProjectID); err != nil {
		_ = dc.RemoveContainer(ctx, containerID)
		return "", fmt.Errorf("download archived WALs: %w", err)
	}

	// Recovery directives: recovery.signal + postgresql.auto.conf land in
	// PGDATA so postgres enters archive recovery and stops at the target.
	if recoveryTarBytes := buildRecoveryTar(spec.req); recoveryTarBytes != nil {
		if err := dc.CopyToContainer(ctx, containerID, pgDataPath, bytes.NewReader(recoveryTarBytes)); err != nil {
			_ = dc.RemoveContainer(ctx, containerID)
			return "", fmt.Errorf("write recovery config: %w", err)
		}
	}
	return containerID, nil
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

// downloadWALsIntoContainer lists every WAL object under the source
// project's wals/ prefix, gunzips each, and packs them into a single
// tar that gets CopyToContainer'd into the new container's
// PGDATA/wal_restore/ directory. Postgres recovery uses restore_command
// (set by buildRecoveryTar) to fetch them on demand.
//
// Why a separate dir instead of pg_wal/? Because postgres requires a
// non-empty restore_command whenever recovery.signal is present
// (archive recovery), and restore_command's source path must NOT be
// pg_wal — postgres copies into pg_wal as it replays.
//
// Empty when the project never produced archived WALs (archive_mode
// wasn't enabled). Returns nil so restore continues without PITR-able
// WALs — recovery will replay only the basebackup-bundled segments.
func (a *DockerBackupAdapter) downloadWALsIntoContainer(ctx context.Context, dc provisioner.DockerClient, containerID, sourceProjectID string) error {
	prefix := fmt.Sprintf("%s%s/wals/", a.keyPrefix, sourceProjectID)
	objects, err := a.uploader.List(ctx, a.bucket, prefix)
	if err != nil {
		return fmt.Errorf("list WAL objects: %w", err)
	}
	if len(objects) == 0 {
		return nil
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, obj := range objects {
		if err := a.addWALToTar(ctx, tw, obj.Key); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("close tar: %w", err)
	}
	if err := dc.CopyToContainer(ctx, containerID, pgDataPath, &buf); err != nil {
		return fmt.Errorf("copy WALs into container: %w", err)
	}
	return nil
}

// addWALToTar downloads + gunzips a single archived WAL object and writes it
// into tw under wal_restore/<segment>. Objects with an empty base name (e.g. a
// directory marker) are skipped.
func (a *DockerBackupAdapter) addWALToTar(ctx context.Context, tw *tar.Writer, objectKey string) error {
	// objectKey is e.g. "backups/{srcProjectId}/wals/000000010000000000000003.gz"
	base := objectKey
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	base = strings.TrimSuffix(base, ".gz")
	if base == "" {
		return nil
	}
	body, err := a.uploader.Download(ctx, a.bucket, objectKey)
	if err != nil {
		return fmt.Errorf("download WAL %s: %w", objectKey, err)
	}
	gr, err := gzip.NewReader(body)
	if err != nil {
		body.Close()
		return fmt.Errorf("gunzip WAL %s: %w", objectKey, err)
	}
	data, err := io.ReadAll(gr)
	gr.Close()
	body.Close()
	if err != nil {
		return fmt.Errorf("read WAL %s: %w", objectKey, err)
	}
	hdr := &tar.Header{
		Name: "wal_restore/" + base,
		Mode: 0600,
		Size: int64(len(data)),
		Uid:  999, Gid: 999,
		ModTime: time.Now(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("tar header %s: %w", base, err)
	}
	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("tar body %s: %w", base, err)
	}
	return nil
}

// generateRestorePassword produces a one-shot password for the
// restored container's superuser env. The actual DB users + their
// passwords come from the backup; this only satisfies the postgres
// image's startup contract.
func generateRestorePassword() string {
	return fmt.Sprintf("restored-%d", time.Now().UnixNano())
}

// buildRecoveryTar produces a tar archive containing the two files
// PostgreSQL needs to enter recovery mode + stop at a specific point:
//
//   - recovery.signal  (empty file; presence triggers archive recovery)
//   - postgresql.auto.conf  (recovery_target_* + recovery_target_action='promote')
//
// Returns nil when the request has no target (latest restore — pg
// replays all WALs in pg_wal/ and starts normally without a signal).
//
// Layout: relative paths so when CopyToContainer'd into
// /var/lib/postgresql/data, files land at the PGDATA root next to
// pg_wal/, base/, etc. Mode 0600 owned by uid:gid 999:999 — the
// postgres image's user. Without those permissions postgres refuses
// to start (PGDATA must be 0700; files within must be owned by pg).
func buildRecoveryTar(req domain.RestoreRequest) []byte {
	var directives string
	switch {
	case req.TargetTime != nil:
		// Microsecond precision matches pg_xact_commit_timestamp's
		// resolution. Without it, recovery_target_time gets
		// interpreted as the start of the second, which can land
		// BEFORE a target commit that happened within that second.
		directives = fmt.Sprintf("recovery_target_time = '%s'\n", req.TargetTime.Time.UTC().Format("2006-01-02 15:04:05.000000"))
	case req.TargetXID != "":
		directives = fmt.Sprintf("recovery_target_xid = '%s'\n", req.TargetXID)
	case req.TargetLSN != "":
		directives = fmt.Sprintf("recovery_target_lsn = '%s'\n", req.TargetLSN)
	case req.TargetName != "":
		directives = fmt.Sprintf("recovery_target_name = '%s'\n", req.TargetName)
	default:
		// No target → latest restore, no recovery.signal needed.
		return nil
	}
	directives += "recovery_target_action = 'promote'\n"
	// Postgres requires restore_command whenever recovery.signal is
	// present (archive recovery mode), even if all WALs are already
	// in pg_wal/. Point at PGDATA/wal_restore/ where
	// downloadWALsIntoContainer placed the archived segments.
	// `|| true` makes a missing-WAL exit cleanly so recovery promotes
	// at the target instead of treating it as a fatal error.
	directives += "restore_command = 'cp /var/lib/postgresql/data/wal_restore/%f %p 2>/dev/null || true'\n"

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	// recovery.signal — empty file, mode 0600, owned by postgres uid.
	hdr := &tar.Header{
		Name: "recovery.signal",
		Mode: 0600,
		Size: 0,
		Uid:  999, Gid: 999, // postgres image's uid:gid (Debian-based)
		ModTime: time.Now(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return nil
	}

	// postgresql.auto.conf — overwrites the basebackup's copy. PG
	// reads auto.conf last so directives here win. We deliberately
	// don't try to merge with the existing file: extracting the tar
	// into PGDATA would need a read-modify-write cycle, and the only
	// thing in a fresh cluster's auto.conf that matters is what we
	// just wrote anyway.
	body := []byte(directives)
	hdr = &tar.Header{
		Name: "postgresql.auto.conf",
		Mode: 0600,
		Size: int64(len(body)),
		Uid:  999, Gid: 999,
		ModTime: time.Now(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return nil
	}
	if _, err := tw.Write(body); err != nil {
		return nil
	}
	if err := tw.Close(); err != nil {
		return nil
	}
	return buf.Bytes()
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
