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
	records   storage.BackupRecordStore
	registrar ProjectRegistrar
	probe     DatabaseProbe
	plans     RestorePlanSource
	creds     *BackupCredentialIssuer
	poller    provisioner.Poller
	streamer  PodStreamer
	now       func() time.Time
}

// MySQLBackupAdapterConfig wires the adapter; every field is required.
type MySQLBackupAdapterConfig struct {
	Kube      k8s.KubeClient
	MySQL     *provisioner.MySQLProvisioner
	Storage   BackupStorageSource
	OpenStore ObjectDeleterFactory
	Instances storage.InstanceStore
	// Records keeps which runs were started by hand once MOCO drops their Jobs.
	Records storage.BackupRecordStore
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
		records: cfg.Records,
		poller:  provisioner.NewPoller(defaultRestoreReadyPoll, defaultRestoreReadyTimeout),
		now:     time.Now,
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
	if err := a.recordManualRun(ctx, inst.ProjectID, job.GetName(), started); err != nil {
		return BackupRef{}, err
	}
	if err := a.kube.ApplyCRD(ctx, k8s.JobGVR, inst.Namespace, job); err != nil {
		return BackupRef{}, errors.Join(fmt.Errorf("start the backup: %w", err),
			a.records.UpdateStatus(ctx, job.GetName(), backupStatusFailed))
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
	completed := completedMySQLBackups(backups)
	manual, err := a.manualRuns(ctx, inst.ProjectID, completed, jobs)
	if err != nil {
		return nil, err
	}
	refs := completedRefs(inst.ProjectID, completed, manual)
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

// completedRefs names each run by its time; a run started by hand is MANUAL,
// listed under the id its trigger returned.
func completedRefs(projectID string, completed []mysqlBackup, manual map[string]string) []BackupRef {
	refs := make([]BackupRef, 0, len(completed))
	for _, backup := range completed {
		id, kind := backup.ID, backupTypeScheduled
		if listed, ok := manual[backup.ID]; ok {
			id, kind = listed, backupTypeManual
		}
		refs = append(refs, BackupRef{
			ID: id, ProjectID: projectID, Type: kind, Status: backupStatusCompleted,
			StartedAt: backup.At.UTC().Format(time.RFC3339), SizeBytes: backup.Size,
		})
	}
	return refs
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
		return a.recordedStatusOf(ctx, inst, backupID)
	}
	switch k8s.MySQLBackupJobState(job).Phase {
	case k8s.MySQLJobSucceeded:
		return backupStatusCompleted, nil
	case k8s.MySQLJobFailed:
		return backupStatusFailed, nil
	}
	return backupStatusInProgress, nil
}

// recordedStatusOf answers a manual run whose Job the CronJob's history limit
// removed, from its record and the bucket.
func (a *MySQLBackupAdapter) recordedStatusOf(ctx context.Context, inst *domain.DatabaseInstance, backupID string) (string, error) {
	records, err := a.manualRecords(ctx, inst.ProjectID)
	if err != nil {
		return "", err
	}
	record, ok := records[backupID]
	if !ok {
		return "", fmt.Errorf("backup %s not found for project %s", backupID, inst.ProjectID)
	}
	if record.Status == backupStatusFailed {
		return backupStatusFailed, nil
	}
	backups, err := a.backups(ctx, inst)
	if err != nil {
		return "", err
	}
	if _, found, err := a.manualRunOf(ctx, record, nil, completedMySQLBackups(backups)); err != nil || found {
		return backupStatusCompleted, err
	}
	return backupStatusInProgress, nil
}

// restorePoint resolves req against the project's runs; a manual run may be
// named by its trigger id.
func (a *MySQLBackupAdapter) restorePoint(ctx context.Context, inst *domain.DatabaseInstance, req domain.RestoreRequest) ([]mysqlBackup, map[string]mysqlShipMark, time.Time, error) {
	backups, marks, err := a.catalog(ctx, inst)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	if req.BackupID != "" {
		if req.BackupID, err = a.runIDOf(ctx, inst, req.BackupID, completedMySQLBackups(backups)); err != nil {
			return nil, nil, time.Time{}, err
		}
	}
	point, err := resolveMySQLRestorePoint(backups, marks, req)
	return backups, marks, point, err
}

// RestoreWindow is the span the project's runs reach, or nil before the first.
func (a *MySQLBackupAdapter) RestoreWindow(ctx context.Context, inst *domain.DatabaseInstance) (*RestoreWindow, error) {
	backups, marks, err := a.catalog(ctx, inst)
	if err != nil {
		return nil, err
	}
	window, ok := mysqlRestoreWindow(backups, marks)
	if !ok {
		return nil, nil
	}
	return &window, nil
}

// CheckRestoreTarget refuses a target outside the window, or not a time.
func (a *MySQLBackupAdapter) CheckRestoreTarget(ctx context.Context, inst *domain.DatabaseInstance, req domain.RestoreRequest) error {
	_, _, _, err := a.restorePoint(ctx, inst, req)
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
	expired := expiredMySQLBackups(backups, cutoff)
	var doomed []string
	for _, backup := range expired {
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
	return len(doomed), a.dropManualRecordsThrough(ctx, inst.ProjectID, expired)
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
	list := listObjectsOf(objects)
	var all []StoredObject
	token := ""
	for {
		page, next, err := list(ctx, store.Bucket, prefix, token, maxDeleteBatch)
		if err != nil {
			return nil, fmt.Errorf("list the backups of %s: %w", inst.ProjectID, err)
		}
		all = append(all, page...)
		if next == "" {
			return readMySQLBackupObjects(all, prefix), nil
		}
		token = next
	}
}

// listObjectsOf lists with sizes where the store reports them, else keys
// alone and runs carry no size.
func listObjectsOf(objects ObjectDeleter) func(context.Context, string, string, string, int32) ([]StoredObject, string, error) {
	if lister, ok := objects.(ObjectLister); ok {
		return lister.ListObjects
	}
	return func(ctx context.Context, bucket, prefix, token string, maxKeys int32) ([]StoredObject, string, error) {
		keys, next, err := objects.ListKeys(ctx, bucket, prefix, token, maxKeys)
		page := make([]StoredObject, 0, len(keys))
		for _, key := range keys {
			page = append(page, StoredObject{Key: key})
		}
		return page, next, err
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
