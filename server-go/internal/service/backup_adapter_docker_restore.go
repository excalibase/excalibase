package service

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

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
	dc, store, registrar, probe, plans := a.docker, a.instances, a.registrar, a.probe, a.plans
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
	if probe == nil {
		return nil, ErrDatabaseProbeNotConfigured
	}
	plan, limits, err := restorePlanOf(ctx, plans, inst)
	if err != nil {
		return nil, err
	}
	pc := provisioner.NewProvisionContext(nil, nil)
	ports, err := a.heldPorts(ctx, newProject, inst.DocumentDB)
	if err != nil {
		return nil, err
	}
	pc.RegisterCleanup("release the copy's host port", func(ctx context.Context) error {
		return a.releasePort(ctx, newProject)
	})
	recovered, err := a.recoverContainer(ctx, dc, inst, req, recoveryTarget{
		projectID:     newProject,
		containerName: provisioner.DatabaseContainerName(newProject),
		limits:        limits,
		ports:         ports,
		command:       provisioner.PostgresCommand(plan.Config),
	})
	if err != nil {
		pc.Rollback(ctx)
		return nil, err
	}
	pc.RegisterCleanup("remove restored container", func(ctx context.Context) error {
		return dc.RemoveContainer(ctx, recovered.containerID)
	})
	if err := a.bringUp(ctx, dc, recovered, plan.Backup != nil && plan.Backup.Enabled); err != nil {
		return nil, failRestore(ctx, pc, newProject, err)
	}
	newInst := restoredDockerInstance(inst, restoredDockerSpec{
		projectID:     newProject,
		projectName:   req.NewProjectName,
		containerID:   recovered.containerID,
		containerName: recovered.spec.containerName,
		dbName:        recovered.spec.dbName,
		password:      provisioner.NewOwnerPassword(),
		backupID:      recovered.backupID,
		backup:        plan.Backup,
	})
	deps := restoreDeps{dc: dc, gatewayLimits: recovered.spec.gatewayLimits()}
	if err := startRestoredGateway(ctx, pc, deps, newInst, recovered.containerID); err != nil {
		return nil, failRestore(ctx, pc, newProject, err)
	}
	if err := registerVerifiedProject(ctx, pc, registrar, store, probe, newInst,
		RegistrationOptions{ResetRolePasswords: true, ResetAdminPassword: true}); err != nil {
		return nil, failRestore(ctx, pc, newProject, err)
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

// restoreDeps are the collaborators a docker restore finishes through.
type restoreDeps struct {
	dc provisioner.DockerClient
	// gatewayLimits sizes a DocumentDB project's gateway.
	gatewayLimits provisioner.ContainerLimits
}

// recoveryTarget is the container a recovery builds.
type recoveryTarget struct {
	projectID     string
	containerName string
	limits        provisioner.ContainerLimits
	// ports are the host ports it publishes on; "" for any.
	ports provisioner.DocumentDBPorts
	// command starts postgres as the tier's new projects start (EXC-612).
	command []string
}

// recovered is a container recovered from a project's backups.
type recovered struct {
	containerID string
	backupID    string
	owner       provisioner.FileOwner
	spec        restoreContainerSpec
}

// recoverContainer creates target and seeds it from source's backups up to
// req, ready for bringUp. A failure removes the container it created.
func (a *DockerBackupAdapter) recoverContainer(ctx context.Context, dc provisioner.DockerClient, source *domain.DatabaseInstance,
	req domain.RestoreRequest, target recoveryTarget) (recovered, error) {
	base, err := a.resolveSourceBackup(ctx, source, req)
	if err != nil {
		return recovered{}, err
	}
	key := a.objectKey(source.ProjectID, "manual", base.ID)
	body, err := a.uploader.Download(ctx, a.bucket, key)
	if err != nil {
		return recovered{}, fmt.Errorf("download base backup %s: %w", key, err)
	}
	defer body.Close()
	spec, err := recoverySpec(source, req, target, body)
	if err != nil {
		return recovered{}, err
	}
	if spec.walsPrefix, err = a.walsPrefix(ctx, source); err != nil {
		return recovered{}, err
	}
	containerID, owner, err := a.createAndSeedRestoreContainer(ctx, dc, spec)
	if err != nil {
		return recovered{}, err
	}
	return recovered{containerID: containerID, backupID: base.ID, owner: owner, spec: spec}, nil
}

// errRestoreSourceHasNoOwner refuses a source with no usable owner login on
// record: the copy would be registered with none, or with the superuser.
var errRestoreSourceHasNoOwner = errors.New("the source project has no owner login on record")

func recoverySpec(source *domain.DatabaseInstance, req domain.RestoreRequest, target recoveryTarget, body io.Reader) (restoreContainerSpec, error) {
	if provisioner.ValidateOwner(source.Username) != nil {
		return restoreContainerSpec{}, fmt.Errorf("restore %s: %w", source.ProjectID, errRestoreSourceHasNoOwner)
	}
	dbName := source.DatabaseName
	if dbName == "" {
		dbName = defaultRestoreDatabase
	}
	spec := restoreContainerSpec{
		containerName:   target.containerName,
		dbName:          dbName,
		newPassword:     generateRestorePassword(),
		newProject:      target.projectID,
		sourceProjectID: source.ProjectID,
		body:            body,
		req:             req,
		limits:          target.limits,
		documentDB:      source.DocumentDB,
		major:           source.PostgresVersion,
		owner:           source.Username,
		ports:           target.ports,
		command:         target.command,
	}
	if !source.DocumentDB {
		image, err := restoreImage(source.PostgresVersion)
		if err != nil {
			return restoreContainerSpec{}, fmt.Errorf("restore %s: %w", source.ProjectID, err)
		}
		spec.image = image
	}
	return spec, nil
}

// bringUp starts a seeded container and waits until postgres has finished
// recovery in it; with archive, it then archives WAL as a new project does.
func (a *DockerBackupAdapter) bringUp(ctx context.Context, dc provisioner.DockerClient, seeded recovered, archive bool) error {
	containerID, dbName := seeded.containerID, seeded.spec.dbName
	if err := provisioner.StartHoldingPort(ctx, dc, restoreStartPoller, containerID); err != nil {
		return fmt.Errorf("start restored container: %w", err)
	}
	if err := dc.WaitForHealthy(ctx, containerID); err != nil {
		return fmt.Errorf("restored container did not become healthy: %w", err)
	}
	if err := a.waitForPromotedPostgres(ctx, dc, containerID, dbName); err != nil {
		return err
	}
	if !archive {
		return nil
	}
	if err := provisioner.NewDockerPostgreSQLProvisioner(dc).EnableWALArchive(ctx, containerID, seeded.owner); err != nil {
		return fmt.Errorf("enable WAL archiving on the restored database: %w", err)
	}
	return a.waitForPromotedPostgres(ctx, dc, containerID, dbName)
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
	// backup is the plan's backups for the new project; nil has none.
	backup *domain.BackupSettings
}

// restoredDockerInstance builds the project row for a restored container,
// inheriting the source project's org, owner and type. Its tier is the org's
// current plan, set when it is registered.
func restoredDockerInstance(src *domain.DatabaseInstance, spec restoredDockerSpec) *domain.DatabaseInstance {
	port := 5432
	restored := &domain.DatabaseInstance{
		ProjectID:             spec.projectID,
		ProjectName:           spec.projectName,
		OrgID:                 src.OrgID,
		OwnerID:               src.OwnerID,
		DBType:                src.DBType,
		DeploymentMode:        domain.ModeDocker,
		Namespace:             spec.containerID,
		Host:                  spec.containerName,
		Port:                  &port,
		DatabaseName:          spec.dbName,
		Username:              src.Username,
		Password:              spec.password,
		PostgresVersion:       src.PostgresVersion,
		RestoredFromProjectID: src.ProjectID,
		RestoredFromBackupID:  spec.backupID,
		DocumentDB:            src.DocumentDB,
		CreatedAt:             &domain.FlexTime{Time: time.Now()},
	}
	if spec.backup != nil {
		restored.BackupEnabled = boolPtr(spec.backup.Enabled)
		restored.BackupSchedule = spec.backup.Schedule
		restored.BackupRetentionDays = intPtr(spec.backup.Retention)
	}
	return restored
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
	timeout := a.restoreTimeout()
	deadline := time.Now().Add(timeout)
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
	return fmt.Errorf("restored postgres did not finish recovery within %v", timeout)
}

// dockerRestoreReadyPoll is the gap between promotion probes. Shorter than the
// K8s poll because exec into a local container is cheap.
const dockerRestoreReadyPoll = time.Second

// resolveSourceBackup is the base backup a restore of req starts from: the
// one it names, else for a point in time the last one taken before it (a
// later one cannot be rolled back), else the newest.
//
// Only backups taken on source.PostgresVersion are candidates: a restore runs
// that major, and after a major upgrade the others belong to another one.
func (a *DockerBackupAdapter) resolveSourceBackup(ctx context.Context, source *domain.DatabaseInstance, req domain.RestoreRequest) (*domain.BackupRecord, error) {
	records, err := a.majorRecords(ctx, source)
	if err != nil {
		return nil, err
	}
	if req.BackupID != "" {
		for i := range records {
			if records[i].ID == req.BackupID && records[i].Status == "COMPLETED" {
				r := records[i]
				return &r, nil
			}
		}
		return nil, fmt.Errorf("backup %q not found or not COMPLETED", req.BackupID)
	}
	if req.TargetTime != nil {
		return lastBackupBefore(records, req.TargetTime.Time)
	}
	return pickLatestCompletedBackup(records)
}

// lastBackupBefore is the newest completed backup that started before point.
func lastBackupBefore(records []domain.BackupRecord, point time.Time) (*domain.BackupRecord, error) {
	var chosen *domain.BackupRecord
	var chosenAt time.Time
	for i := range records {
		at, err := time.Parse(time.RFC3339, records[i].Timestamp)
		if err != nil || records[i].Status != "COMPLETED" || !at.Before(point) {
			continue
		}
		if chosen == nil || at.After(chosenAt) {
			chosen, chosenAt = &records[i], at
		}
	}
	if chosen == nil {
		return nil, &RestoreTargetError{Message: fmt.Sprintf("no backup was taken before %s; restore to a later point", point.UTC().Format(time.RFC3339))}
	}
	r := *chosen
	return &r, nil
}

// restoreImage returns the postgres image for the restored container. A
// restore runs on the major the backup was taken from; if that major is
// unknown or unsupported the restore fails rather than silently landing the
// data on a different one.
func restoreImage(postgresVersion string) (string, error) {
	return config.DockerPostgresImage(postgresVersion)
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
	limits          provisioner.ContainerLimits
	// documentDB restores into the catalogue image of major with a gateway.
	documentDB bool
	major      string
	// owner is the project's login, which a DocumentDB gateway opens sessions as.
	owner string
	// ports are the host ports it publishes on; "" for any.
	ports provisioner.DocumentDBPorts
	// command replaces the official image's, as Provision's does.
	command []string
	// walsPrefix is the source archive's WAL, on the source's major.
	walsPrefix string
}

// restoreStartPoller bounds the wait for a held host port to come free.
var restoreStartPoller = provisioner.NewPoller(time.Second, 30*time.Second)

// restorePlanOf is the plan a new project of the source's org gets, and the
// container limits it sizes a restored container by.
func restorePlanOf(ctx context.Context, plans RestorePlanSource, src *domain.DatabaseInstance) (RestorePlan, provisioner.ContainerLimits, error) {
	if plans == nil {
		return RestorePlan{}, provisioner.ContainerLimits{}, ErrRestorePlanNotConfigured
	}
	plan, err := plans.RestorePlan(ctx, src)
	if err != nil {
		return RestorePlan{}, provisioner.ContainerLimits{}, fmt.Errorf("restore %s: %w", src.ProjectID, err)
	}
	limits, err := provisioner.LimitsForTier(plan.Config)
	if err != nil {
		return RestorePlan{}, provisioner.ContainerLimits{}, fmt.Errorf("restore %s at tier %s: %w", src.ProjectID, plan.Tier, err)
	}
	return plan, limits, nil
}

// createAndSeedRestoreContainer creates the stopped restore container, extracts
// the base backup into PGDATA, downloads archived WALs, and writes recovery
// directives. On any failure it removes the partially-created container and
// returns a wrapped error. The returned containerID is left stopped for the
// caller to start.
func (a *DockerBackupAdapter) createAndSeedRestoreContainer(ctx context.Context, dc provisioner.DockerClient, spec restoreContainerSpec) (string, provisioner.FileOwner, error) {
	log.Printf("docker restore: project %q from %q (DocumentDB %v)", spec.newProject, spec.sourceProjectID, spec.documentDB)
	containerID, owner, err := createRestoreContainer(ctx, dc, spec)
	if err != nil {
		return "", owner, err
	}
	if err := a.seedRestoreContainer(ctx, dc, containerID, owner, spec); err != nil {
		_ = dc.RemoveContainer(ctx, containerID)
		return "", owner, err
	}
	return containerID, owner, nil
}

// seedRestoreContainer extracts the base backup into PGDATA before postgres
// starts (initdb would refuse a non-empty data dir), downloads archived WALs
// and writes recovery directives, all owned by the image's postgres user.
func (a *DockerBackupAdapter) seedRestoreContainer(ctx context.Context, dc provisioner.DockerClient, containerID string, owner provisioner.FileOwner, spec restoreContainerSpec) error {
	if err := prepareDataDir(ctx, dc, containerID, spec); err != nil {
		return err
	}
	gz, err := gzip.NewReader(spec.body)
	if err != nil {
		return fmt.Errorf("gunzip backup stream: %w", err)
	}
	defer gz.Close()
	if err := dc.CopyToContainer(ctx, containerID, pgDataPath, gz); err != nil {
		return fmt.Errorf("extract backup into container: %w", err)
	}
	// PITR: download every archived WAL segment for the source project so
	// recovery has the complete WAL stream from backup-time forward.
	if err := a.downloadWALsIntoContainer(ctx, dc, containerID, spec.walsPrefix, owner); err != nil {
		return fmt.Errorf("download archived WALs: %w", err)
	}
	// Recovery directives: recovery.signal + postgresql.auto.conf land in
	// PGDATA so postgres enters archive recovery and stops at the target.
	if recoveryTarBytes := buildRecoveryTarFor(spec.req, owner); recoveryTarBytes != nil {
		if err := dc.CopyToContainer(ctx, containerID, pgDataPath, bytes.NewReader(recoveryTarBytes)); err != nil {
			return fmt.Errorf("write recovery config: %w", err)
		}
	}
	return nil
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
func (a *DockerBackupAdapter) downloadWALsIntoContainer(ctx context.Context, dc provisioner.DockerClient, containerID, prefix string, owner provisioner.FileOwner) error {
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
		if err := a.addWALToTar(ctx, tw, obj.Key, owner); err != nil {
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
func (a *DockerBackupAdapter) addWALToTar(ctx context.Context, tw *tar.Writer, objectKey string, owner provisioner.FileOwner) error {
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
		Uid:  owner.UID, Gid: owner.GID,
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

// generateRestorePassword is the restored container's superuser placeholder;
// a seeded data directory keeps its own, and neither is handed out.
func generateRestorePassword() string {
	return generatePassword(32)
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
	return buildRecoveryTarFor(req, officialImageOwner)
}

// buildRecoveryTarFor writes the recovery files owned by the image's user.
func buildRecoveryTarFor(req domain.RestoreRequest, owner provisioner.FileOwner) []byte {
	var directives string
	switch {
	case req.TargetTime != nil:
		// Microsecond precision matches pg_xact_commit_timestamp's
		// resolution. Without it, recovery_target_time gets
		// interpreted as the start of the second, which can land
		// BEFORE a target commit that happened within that second. The zone
		// is explicit so the server's TimeZone setting cannot move it.
		directives = fmt.Sprintf("recovery_target_time = '%s+00'\n", req.TargetTime.Time.UTC().Format("2006-01-02 15:04:05.000000"))
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
		Uid:  owner.UID, Gid: owner.GID,
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
		Uid:  owner.UID, Gid: owner.GID,
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
