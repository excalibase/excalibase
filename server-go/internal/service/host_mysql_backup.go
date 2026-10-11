package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// HostMySQLBackupAdapter backs up and restores MySQL projects on a single
// host (EXC-592): the platform takes a dump and ships the closed binlogs on
// the plan's interval, into the platform's backup bucket (EXC-591).
type HostMySQLBackupAdapter struct {
	docker    provisioner.DockerClient
	mysql     *provisioner.DockerMySQLProvisioner
	streamer  ContainerStreamer
	uploader  S3Uploader
	records   storage.BackupRecordStore
	bucket    string
	keyPrefix string
	instances storage.InstanceStore
	registrar ProjectRegistrar
	probe     DatabaseProbe
	plans     RestorePlanSource
	now       func() time.Time
	// runs serialises a project's backups and restores.
	runs sync.Map
}

// HostMySQLBackupAdapterConfig wires the adapter; every field is required.
type HostMySQLBackupAdapterConfig struct {
	Docker    provisioner.DockerClient
	MySQL     *provisioner.DockerMySQLProvisioner
	Streamer  ContainerStreamer
	Uploader  S3Uploader
	Records   storage.BackupRecordStore
	Bucket    string
	KeyPrefix string
	Instances storage.InstanceStore
}

var (
	_ BackupAdapter           = (*HostMySQLBackupAdapter)(nil)
	_ BackupStatusResolver    = (*HostMySQLBackupAdapter)(nil)
	_ RestoreWindowAdvertiser = (*HostMySQLBackupAdapter)(nil)
	_ RestoreTargetChecker    = (*HostMySQLBackupAdapter)(nil)
	_ RetentionPurger         = (*HostMySQLBackupAdapter)(nil)
	_ InPlaceCluster          = (*HostMySQLBackupAdapter)(nil)
)

// ErrHostMySQLNotRunning refuses a backup of a project whose mysqld is not up.
var ErrHostMySQLNotRunning = errors.New("the project's database is not running (paused?); resume it to back it up")

func NewHostMySQLBackupAdapter(cfg HostMySQLBackupAdapterConfig) *HostMySQLBackupAdapter {
	return &HostMySQLBackupAdapter{
		docker: cfg.Docker, mysql: cfg.MySQL, streamer: cfg.Streamer, uploader: cfg.Uploader, records: cfg.Records,
		bucket: cfg.Bucket, keyPrefix: cfg.KeyPrefix, instances: cfg.Instances, now: time.Now,
	}
}

// SetClock replaces the clock run ids, intervals and retention read.
func (a *HostMySQLBackupAdapter) SetClock(now func() time.Time) { a.now = now }

// SetProjectRegistrar wires the shared registration path a restore ends in.
func (a *HostMySQLBackupAdapter) SetProjectRegistrar(r ProjectRegistrar) { a.registrar = r }

// SetRestorePlanSource wires what sizes a restored or rebuilt container.
func (a *HostMySQLBackupAdapter) SetRestorePlanSource(p RestorePlanSource) { a.plans = p }

// SetDatabaseProbe wires the check that a restored database serves.
func (a *HostMySQLBackupAdapter) SetDatabaseProbe(p DatabaseProbe) { a.probe = p }

// Configure has nothing to do: the plan sets the interval.
func (a *HostMySQLBackupAdapter) Configure(context.Context, *domain.DatabaseInstance, string, int) error {
	return nil
}

// BackupsConfigured reports whether there is a bucket to write to.
func (a *HostMySQLBackupAdapter) BackupsConfigured() bool {
	return a.uploader != nil && a.bucket != "" && a.streamer != nil && a.docker != nil && a.mysql != nil
}

// lock serialises the project's runs and restores.
func (a *HostMySQLBackupAdapter) lock(projectID string) func() {
	held, _ := a.runs.LoadOrStore(projectID, &sync.Mutex{})
	mutex := held.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
}

// TriggerManual backs the project up now and returns once the run is complete.
func (a *HostMySQLBackupAdapter) TriggerManual(ctx context.Context, inst *domain.DatabaseInstance) (BackupRef, error) {
	return a.backup(ctx, inst, backupTypeManual)
}

// RunScheduled is the platform's own run on the plan's interval.
func (a *HostMySQLBackupAdapter) RunScheduled(ctx context.Context, inst *domain.DatabaseInstance) (BackupRef, error) {
	return a.backup(ctx, inst, backupTypeScheduled)
}

// BackupDue reports whether the project needs a run now: its server has none
// yet (new, or replaced by a restore) or its newest is an interval old.
func (a *HostMySQLBackupAdapter) BackupDue(ctx context.Context, inst *domain.DatabaseInstance) (bool, error) {
	interval, err := config.MySQLBackupInterval(inst.Tier)
	if err != nil {
		return false, err
	}
	server, err := a.serverID(ctx, inst.Namespace)
	if err != nil {
		return false, err
	}
	runs, err := a.listRuns(ctx, inst.ProjectID)
	if err != nil {
		return false, err
	}
	var newest time.Time
	for _, run := range completedHostMySQLRuns(runs) {
		if run.Server == server && run.At.After(newest) {
			newest = run.At
		}
	}
	return newest.IsZero() || !a.now().Before(newest.Add(interval)), nil
}

// List is every complete run in the bucket plus the runs in flight or failed.
func (a *HostMySQLBackupAdapter) List(ctx context.Context, inst *domain.DatabaseInstance) ([]BackupRef, error) {
	runs, err := a.listRuns(ctx, inst.ProjectID)
	if err != nil {
		return nil, err
	}
	records, err := a.records.ListByProject(ctx, inst.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("list backup records: %w", err)
	}
	kinds := map[string]domain.BackupRecord{}
	for _, record := range records {
		kinds[record.ID] = record
	}
	refs := []BackupRef{}
	complete := map[string]bool{}
	for _, run := range completedHostMySQLRuns(runs) {
		kind := backupTypeScheduled
		if record, ok := kinds[run.ID]; ok {
			kind = record.Type
		}
		complete[run.ID] = true
		refs = append(refs, BackupRef{ID: run.ID, ProjectID: inst.ProjectID, Type: kind, Status: backupStatusCompleted,
			StartedAt: run.At.UTC().Format(time.RFC3339), SizeBytes: run.Size})
	}
	for _, record := range records {
		if complete[record.ID] || record.Status == backupStatusCompleted || record.ProjectID != inst.ProjectID {
			continue
		}
		refs = append(refs, BackupRef{ID: record.ID, ProjectID: inst.ProjectID, Type: record.Type, Status: record.Status, StartedAt: record.Timestamp})
	}
	sort.SliceStable(refs, func(i, j int) bool { return refs[i].StartedAt < refs[j].StartedAt })
	return refs, nil
}

// BackupStatusOf answers a run by its id.
func (a *HostMySQLBackupAdapter) BackupStatusOf(ctx context.Context, inst *domain.DatabaseInstance, backupID string) (string, error) {
	refs, err := a.List(ctx, inst)
	if err != nil {
		return "", err
	}
	for _, ref := range refs {
		if ref.ID == backupID {
			return ref.Status, nil
		}
	}
	return "", fmt.Errorf("backup %s not found for project %s", backupID, inst.ProjectID)
}

// RestoreWindow is the span the project's runs reach, or nil before the first.
func (a *HostMySQLBackupAdapter) RestoreWindow(ctx context.Context, inst *domain.DatabaseInstance) (*RestoreWindow, error) {
	runs, err := a.listRuns(ctx, inst.ProjectID)
	if err != nil {
		return nil, err
	}
	window, ok := hostMySQLWindow(runs)
	if !ok {
		return nil, nil
	}
	return &window, nil
}

// CheckRestoreTarget refuses a target the runs cannot reach.
func (a *HostMySQLBackupAdapter) CheckRestoreTarget(ctx context.Context, inst *domain.DatabaseInstance, req domain.RestoreRequest) error {
	runs, err := a.listRuns(ctx, inst.ProjectID)
	if err != nil {
		return err
	}
	_, err = resolveHostMySQLPoint(runs, req)
	return err
}

// projectPrefix is where everything of the project's lives in the bucket.
func (a *HostMySQLBackupAdapter) projectPrefix(projectID string) string {
	return a.keyPrefix + projectID + "/mysql/"
}

func (a *HostMySQLBackupAdapter) runsPrefix(projectID string) string {
	return a.projectPrefix(projectID) + "runs/"
}

func (a *HostMySQLBackupAdapter) binlogsPrefix(projectID, server string) string {
	return a.projectPrefix(projectID) + "binlogs/" + server + "/"
}

func (a *HostMySQLBackupAdapter) runKey(projectID string, run hostMySQLRun, file string) string {
	return a.runsPrefix(projectID) + hostMySQLRunDirectory(run.ID, run.Server) + "/" + file
}

func (a *HostMySQLBackupAdapter) listRuns(ctx context.Context, projectID string) ([]hostMySQLRun, error) {
	if !a.BackupsConfigured() {
		return nil, ErrBackupStorageNotConfigured
	}
	objects, err := a.uploader.List(ctx, a.bucket, a.runsPrefix(projectID))
	if err != nil {
		return nil, fmt.Errorf("list the backups of %s: %w", projectID, err)
	}
	return readHostMySQLRuns(objects, a.runsPrefix(projectID)), nil
}

// nextRunID is the run's time; a second run within the same second waits for
// the next one so ids stay unique.
func (a *HostMySQLBackupAdapter) nextRunID(runs []hostMySQLRun) string {
	id := a.now().UTC().Format(k8s.MOCOBackupTimeFormat)
	for _, run := range runs {
		if run.ID == id {
			return a.now().UTC().Add(time.Second).Format(k8s.MOCOBackupTimeFormat)
		}
	}
	return id
}
