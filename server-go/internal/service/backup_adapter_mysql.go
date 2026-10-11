package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// MySQLBackupAdapter backs up and restores MySQL projects on MOCO (ADR 0041
// decision 6). Completed backups are read from the bucket with the platform
// key, runs in flight from MOCO's Jobs.
type MySQLBackupAdapter struct {
	kube      k8s.KubeClient
	mysql     *provisioner.MySQLProvisioner
	storage   BackupStorageSource
	openStore ObjectDeleterFactory
	instances storage.InstanceStore
	registrar ProjectRegistrar
	probe     DatabaseProbe
	plans     RestorePlanSource
	creds     *BackupCredentialIssuer
	poller    provisioner.Poller
	now       func() time.Time
}

// MySQLBackupAdapterConfig wires the adapter; every field is required.
type MySQLBackupAdapterConfig struct {
	Kube      k8s.KubeClient
	MySQL     *provisioner.MySQLProvisioner
	Storage   BackupStorageSource
	OpenStore ObjectDeleterFactory
	Instances storage.InstanceStore
}

var (
	_ BackupAdapter           = (*MySQLBackupAdapter)(nil)
	_ BackupStatusResolver    = (*MySQLBackupAdapter)(nil)
	_ RestoreWindowAdvertiser = (*MySQLBackupAdapter)(nil)
	_ RestoreTargetChecker    = (*MySQLBackupAdapter)(nil)
)

// NewMySQLBackupAdapter builds the adapter.
func NewMySQLBackupAdapter(cfg MySQLBackupAdapterConfig) *MySQLBackupAdapter {
	return &MySQLBackupAdapter{
		kube: cfg.Kube, mysql: cfg.MySQL, storage: cfg.Storage, openStore: cfg.OpenStore, instances: cfg.Instances,
		poller: provisioner.NewPoller(defaultRestoreReadyPoll, defaultRestoreReadyTimeout),
		now:    time.Now,
	}
}

// SetClock replaces the clock manual backups and retention read.
func (a *MySQLBackupAdapter) SetClock(now func() time.Time) { a.now = now }

// SetReadyPoller replaces the bounded waits for backups and restores.
func (a *MySQLBackupAdapter) SetReadyPoller(p provisioner.Poller) { a.poller = p }

// SetRestoreReadyTimeout bounds the waits on the wall clock.
func (a *MySQLBackupAdapter) SetRestoreReadyTimeout(d time.Duration) {
	a.poller = provisioner.NewPoller(defaultRestoreReadyPoll, d)
}

// SetProjectRegistrar wires the shared registration path a restore ends in.
func (a *MySQLBackupAdapter) SetProjectRegistrar(r ProjectRegistrar) { a.registrar = r }

// SetRestorePlanSource wires what decides a restored project's plan.
func (a *MySQLBackupAdapter) SetRestorePlanSource(p RestorePlanSource) { a.plans = p }

// SetBackupCredentials wires the issuer of prefix-scoped credentials.
func (a *MySQLBackupAdapter) SetBackupCredentials(i *BackupCredentialIssuer) { a.creds = i }

// SetDatabaseProbe wires the check that a restored database serves.
func (a *MySQLBackupAdapter) SetDatabaseProbe(p DatabaseProbe) { a.probe = p }

// Configure has nothing to do: a MySQL project's schedule is its plan's.
func (a *MySQLBackupAdapter) Configure(context.Context, *domain.DatabaseInstance, string, int) error {
	return nil
}

// BackupsConfigured reports whether there is a store to write to and read from.
func (a *MySQLBackupAdapter) BackupsConfigured() bool {
	if a.storage == nil || a.openStore == nil || a.mysql == nil {
		return false
	}
	_, ok := a.storage.BackupStorage()
	return ok
}

// TriggerManual starts a run now from MOCO's CronJob.
func (a *MySQLBackupAdapter) TriggerManual(ctx context.Context, inst *domain.DatabaseInstance) (BackupRef, error) {
	cron, err := a.kube.GetCRD(ctx, k8s.CronJobGVR, inst.Namespace, k8s.MySQLBackupCronJobName(inst.ProjectID))
	if err != nil {
		return BackupRef{}, fmt.Errorf("the project's backup schedule is not running: %w", err)
	}
	started := a.now().UTC()
	job, err := k8s.BuildManualMySQLBackupJob(cron, started)
	if err != nil {
		return BackupRef{}, err
	}
	if err := a.kube.ApplyCRD(ctx, k8s.JobGVR, inst.Namespace, job); err != nil {
		return BackupRef{}, fmt.Errorf("start the backup: %w", err)
	}
	return BackupRef{
		ID: job.GetName(), ProjectID: inst.ProjectID, Type: backupTypeManual,
		Status: backupStatusInProgress, StartedAt: started.Format(time.RFC3339),
	}, nil
}

// List is every completed run in the bucket plus the runs in flight or failed.
func (a *MySQLBackupAdapter) List(ctx context.Context, inst *domain.DatabaseInstance) ([]BackupRef, error) {
	backups, err := a.backups(ctx, inst)
	if err != nil {
		return nil, err
	}
	jobs, err := a.backupJobs(ctx, inst)
	if err != nil {
		return nil, err
	}
	refs := completedRefs(inst.ProjectID, completedMySQLBackups(backups), jobs)
	for _, job := range jobs {
		state := k8s.MySQLBackupJobState(job)
		if state.Phase == k8s.MySQLJobSucceeded {
			continue
		}
		refs = append(refs, jobRef(inst.ProjectID, job, state))
	}
	sort.SliceStable(refs, func(i, j int) bool { return refs[i].StartedAt < refs[j].StartedAt })
	return refs, nil
}

// completedRefs names each run by its time; a run a manual Job made is MANUAL.
func completedRefs(projectID string, completed []mysqlBackup, jobs []*unstructured.Unstructured) []BackupRef {
	refs := make([]BackupRef, 0, len(completed))
	for _, backup := range completed {
		kind := backupTypeScheduled
		if madeByManualJob(backup.At, jobs) {
			kind = backupTypeManual
		}
		refs = append(refs, BackupRef{
			ID: backup.ID, ProjectID: projectID, Type: kind, Status: backupStatusCompleted,
			StartedAt: backup.At.UTC().Format(time.RFC3339),
		})
	}
	return refs
}

func madeByManualJob(at time.Time, jobs []*unstructured.Unstructured) bool {
	for _, job := range jobs {
		state := k8s.MySQLBackupJobState(job)
		if !k8s.IsManualMySQLBackupJob(job) || state.Phase != k8s.MySQLJobSucceeded {
			continue
		}
		if !at.Before(state.Started) && !at.After(state.Finished) {
			return true
		}
	}
	return false
}

func jobRef(projectID string, job *unstructured.Unstructured, state k8s.MySQLJobState) BackupRef {
	ref := BackupRef{ID: job.GetName(), ProjectID: projectID, Type: backupTypeScheduled, Status: backupStatusInProgress}
	if k8s.IsManualMySQLBackupJob(job) {
		ref.Type = backupTypeManual
	}
	if !state.Started.IsZero() {
		ref.StartedAt = state.Started.UTC().Format(time.RFC3339)
	}
	if state.Phase == k8s.MySQLJobFailed {
		ref.Status, ref.Error = backupStatusFailed, state.Reason
	}
	return ref
}

// BackupStatusOf answers a run by its time or a Job by its name.
func (a *MySQLBackupAdapter) BackupStatusOf(ctx context.Context, inst *domain.DatabaseInstance, backupID string) (string, error) {
	if _, err := time.Parse(k8s.MOCOBackupTimeFormat, backupID); err == nil {
		backups, err := a.backups(ctx, inst)
		if err != nil {
			return "", err
		}
		for _, backup := range completedMySQLBackups(backups) {
			if backup.ID == backupID {
				return backupStatusCompleted, nil
			}
		}
		return "", fmt.Errorf("backup %s not found for project %s", backupID, inst.ProjectID)
	}
	job, err := a.kube.GetCRD(ctx, k8s.JobGVR, inst.Namespace, backupID)
	if err != nil || !k8s.IsMySQLBackupJob(job, inst.ProjectID) {
		return "", fmt.Errorf("backup %s not found for project %s", backupID, inst.ProjectID)
	}
	switch k8s.MySQLBackupJobState(job).Phase {
	case k8s.MySQLJobSucceeded:
		return backupStatusCompleted, nil
	case k8s.MySQLJobFailed:
		return backupStatusFailed, nil
	}
	return backupStatusInProgress, nil
}

// RestoreWindow is the span the project's runs reach, or nil before the first.
func (a *MySQLBackupAdapter) RestoreWindow(ctx context.Context, inst *domain.DatabaseInstance) (*RestoreWindow, error) {
	backups, err := a.backups(ctx, inst)
	if err != nil {
		return nil, err
	}
	window, ok := mysqlRestoreWindow(backups)
	if !ok {
		return nil, nil
	}
	return &window, nil
}

// CheckRestoreTarget refuses a target outside the window, or not a time.
func (a *MySQLBackupAdapter) CheckRestoreTarget(ctx context.Context, inst *domain.DatabaseInstance, req domain.RestoreRequest) error {
	backups, err := a.backups(ctx, inst)
	if err != nil {
		return err
	}
	_, err = resolveMySQLRestorePoint(backups, req)
	return err
}

// ErrMySQLRetentionUnknown refuses to purge a project whose retention was
// never recorded; there is no default to guess.
var ErrMySQLRetentionUnknown = errors.New("the project's backup retention is not recorded")

// PurgeExpired deletes the runs retention no longer keeps, and returns how
// many objects went.
func (a *MySQLBackupAdapter) PurgeExpired(ctx context.Context, inst *domain.DatabaseInstance) (int, error) {
	if inst.BackupRetentionDays == nil || *inst.BackupRetentionDays <= 0 {
		return 0, fmt.Errorf("%w: %s", ErrMySQLRetentionUnknown, inst.ProjectID)
	}
	backups, err := a.backups(ctx, inst)
	if err != nil {
		return 0, err
	}
	cutoff := a.now().Add(-time.Duration(*inst.BackupRetentionDays) * 24 * time.Hour)
	var doomed []string
	for _, backup := range expiredMySQLBackups(backups, cutoff) {
		doomed = append(doomed, backup.Keys...)
	}
	store, objects, err := a.openBucket(ctx)
	if err != nil {
		return 0, err
	}
	for start := 0; start < len(doomed); start += maxDeleteBatch {
		end := min(start+maxDeleteBatch, len(doomed))
		if err := objects.DeleteKeys(ctx, store.Bucket, doomed[start:end]); err != nil {
			return start, fmt.Errorf("delete expired backups of %s: %w", inst.ProjectID, err)
		}
	}
	return len(doomed), nil
}

// backups reads the project's runs from the bucket with the platform key.
func (a *MySQLBackupAdapter) backups(ctx context.Context, inst *domain.DatabaseInstance) ([]mysqlBackup, error) {
	if inst.Namespace == "" {
		return nil, fmt.Errorf("project %s has no namespace to find its backups by", inst.ProjectID)
	}
	store, objects, err := a.openBucket(ctx)
	if err != nil {
		return nil, err
	}
	prefix := k8s.MOCOBackupPrefix(inst.Namespace, inst.ProjectID)
	var keys []string
	token := ""
	for {
		page, next, err := objects.ListKeys(ctx, store.Bucket, prefix, token, maxDeleteBatch)
		if err != nil {
			return nil, fmt.Errorf("list the backups of %s: %w", inst.ProjectID, err)
		}
		keys = append(keys, page...)
		if next == "" {
			return readMySQLBackups(keys, prefix), nil
		}
		token = next
	}
}

func (a *MySQLBackupAdapter) openBucket(ctx context.Context) (*domain.S3Credentials, ObjectDeleter, error) {
	if !a.BackupsConfigured() {
		return nil, nil, ErrBackupStorageNotConfigured
	}
	store, _ := a.storage.BackupStorage()
	if store.Bucket == "" {
		return nil, nil, fmt.Errorf("%w: no bucket", ErrBackupStorageNotConfigured)
	}
	objects, err := a.openStore(ctx, store)
	if err != nil {
		return nil, nil, fmt.Errorf("open the backup store: %w", err)
	}
	return store, objects, nil
}

func (a *MySQLBackupAdapter) backupJobs(ctx context.Context, inst *domain.DatabaseInstance) ([]*unstructured.Unstructured, error) {
	all, err := a.kube.ListCRDs(ctx, k8s.JobGVR, inst.Namespace)
	if err != nil {
		return nil, fmt.Errorf("list the backup jobs of %s: %w", inst.ProjectID, err)
	}
	jobs := make([]*unstructured.Unstructured, 0, len(all))
	for _, job := range all {
		if k8s.IsMySQLBackupJob(job, inst.ProjectID) {
			jobs = append(jobs, job)
		}
	}
	return jobs, nil
}
