package service

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

const (
	mysqlSourceProject = "proj1"
	mysqlSourceNS      = "org1-proj1"
)

type mysqlAdapterLab struct {
	mock    *k8s.MockClient
	objects *fakeObjectDeleter
	mysql   *provisioner.MySQLProvisioner
	adapter *MySQLBackupAdapter
	records *fakeBackupRecordStore
	source  *domain.DatabaseInstance
	now     time.Time
}

func newMySQLAdapterLab(t *testing.T, keys ...string) *mysqlAdapterLab {
	t.Helper()
	mock := k8s.NewMockClient()
	mock.AutoReconcileClusters = true
	mock.WildcardPodReady = true
	mock.WildcardExecOutput = "1\n"
	mysql := provisioner.NewMySQLProvisioner(mock)
	poll := provisioner.NewPoller(time.Millisecond, 100*time.Millisecond)
	mysql.SetPollers(poll, poll)
	sort.Strings(keys)
	objects := newFakeObjectDeleter(keys...)
	records := &fakeBackupRecordStore{}
	lab := &mysqlAdapterLab{mock: mock, objects: objects, mysql: mysql, records: records, now: at(13, 0)}
	lab.adapter = NewMySQLBackupAdapter(MySQLBackupAdapterConfig{
		Kube: mock, MySQL: mysql, Storage: StaticBackupStorage(platformStore()), Records: records,
		OpenStore: func(context.Context, *domain.S3Credentials) (ObjectDeleter, error) { return objects, nil },
	})
	lab.adapter.SetClock(func() time.Time { return lab.now })
	lab.adapter.SetReadyPoller(poll)
	enabled, retention, port := true, 7, 3306
	lab.source = &domain.DatabaseInstance{
		ProjectID: mysqlSourceProject, OrgID: "org1", Namespace: mysqlSourceNS, DBType: domain.MySQL,
		DeploymentMode: domain.ModeK8s, Status: "ACTIVE", MySQLVersion: "8.4.9", DatabaseName: "app",
		BackupEnabled: &enabled, BackupRetentionDays: &retention, Tier: domain.Free, StorageSize: "5Gi",
		Host: k8s.MySQLPrimaryHost(mysqlSourceProject, mysqlSourceNS), Port: &port, Username: "admin",
	}
	return lab
}

func sourceKeys() []string {
	prefix := k8s.MOCOBackupPrefix(mysqlSourceNS, mysqlSourceProject)
	return []string{
		prefix + "20261011-000000/binlog.tar.zst", prefix + "20261011-000000/dump.tar",
		prefix + "20261011-060000/binlog.tar.zst", prefix + "20261011-060000/dump.tar",
		prefix + "20261011-120000/dump.tar",
	}
}

func (lab *mysqlAdapterLab) cronJob() {
	cron := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "batch/v1", "kind": "CronJob",
		"metadata": map[string]interface{}{"name": k8s.MySQLBackupCronJobName(mysqlSourceProject), "namespace": mysqlSourceNS, "uid": "cron-uid"},
		"spec": map[string]interface{}{"jobTemplate": map[string]interface{}{
			"metadata": map[string]interface{}{"labels": map[string]interface{}{"app.kubernetes.io/name": "mysql-backup"}},
			"spec":     map[string]interface{}{"template": map[string]interface{}{}},
		}},
	}}
	lab.mock.CRDs[mysqlSourceNS+"/"+cron.GetName()] = cron
}

func (lab *mysqlAdapterLab) job(name string, status map[string]interface{}, manual bool) {
	job := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "batch/v1", "kind": "Job",
		"metadata": map[string]interface{}{"name": name, "namespace": mysqlSourceNS},
		"status":   status,
	}}
	if manual {
		job.SetAnnotations(map[string]string{"cronjob.kubernetes.io/instantiate": "manual"})
	}
	lab.mock.CRDs[mysqlSourceNS+"/"+name] = job
}

func refsByID(refs []BackupRef) map[string]BackupRef {
	out := map[string]BackupRef{}
	for _, ref := range refs {
		out[ref.ID] = ref
	}
	return out
}

// Completed backups come from the bucket, runs still going or failed from
// MOCO's Jobs; a finished Job is its backup, never listed twice.
func TestMySQLAdapterListsBucketRunsAndLiveJobs(t *testing.T) {
	lab := newMySQLAdapterLab(t, sourceKeys()...)
	cron := k8s.MySQLBackupCronJobName(mysqlSourceProject)
	lab.job(cron+"-29000000", map[string]interface{}{"active": int64(1), "startTime": "2026-10-11T12:59:00Z"}, false)
	lab.job(cron+"-28999000", map[string]interface{}{"conditions": []interface{}{
		map[string]interface{}{"type": "Failed", "status": "True", "message": "BackoffLimitExceeded"}}}, false)
	lab.job(cron+"-manual-20261011120000", map[string]interface{}{
		"succeeded": int64(1), "startTime": "2026-10-11T11:59:50Z", "completionTime": "2026-10-11T12:02:00Z"}, true)
	lab.job("someone-else", map[string]interface{}{"active": int64(1)}, false)

	refs, err := lab.adapter.List(context.Background(), lab.source)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	byID := refsByID(refs)
	if len(refs) != 5 {
		t.Fatalf("refs %+v", refs)
	}
	if ref := byID["20261011-060000"]; ref.Status != backupStatusCompleted || ref.Type != backupTypeScheduled || ref.StartedAt != "2026-10-11T06:00:00Z" {
		t.Errorf("scheduled run %+v", ref)
	}
	if ref := byID["20261011-120000"]; ref.Type != backupTypeManual {
		t.Errorf("the run a manual Job made %+v", ref)
	}
	if ref := byID[cron+"-29000000"]; ref.Status != backupStatusInProgress {
		t.Errorf("running job %+v", ref)
	}
	if ref := byID[cron+"-28999000"]; ref.Status != backupStatusFailed || ref.Error == "" {
		t.Errorf("failed job %+v", ref)
	}
	if !sort.SliceIsSorted(refs, func(i, j int) bool { return refs[i].StartedAt < refs[j].StartedAt }) {
		t.Error("not in time order")
	}
}

// A manual backup is a Job from the CronJob; its id is followed from
// IN_PROGRESS to COMPLETED, which is how the pre-pause backup waits.
func TestMySQLAdapterManualBackupIsFollowedByItsJob(t *testing.T) {
	lab := newMySQLAdapterLab(t)
	lab.cronJob()
	ref, err := lab.adapter.TriggerManual(context.Background(), lab.source)
	if err != nil {
		t.Fatalf("TriggerManual: %v", err)
	}
	if ref.Status != backupStatusInProgress || ref.Type != backupTypeManual {
		t.Errorf("ref %+v", ref)
	}
	job, ok := lab.mock.CRDs[mysqlSourceNS+"/"+ref.ID]
	if !ok || job.GetKind() != "Job" {
		t.Fatalf("no Job %s", ref.ID)
	}
	status, err := lab.adapter.BackupStatusOf(context.Background(), lab.source, ref.ID)
	if err != nil || status != backupStatusInProgress {
		t.Errorf("status %q %v", status, err)
	}
	_ = unstructured.SetNestedField(job.Object, int64(1), "status", "succeeded")
	if status, _ := lab.adapter.BackupStatusOf(context.Background(), lab.source, ref.ID); status != backupStatusCompleted {
		t.Errorf("status after success %q", status)
	}
	if _, err := lab.adapter.BackupStatusOf(context.Background(), lab.source, "nope"); err == nil {
		t.Error("an unknown backup had a status")
	}
}

func TestMySQLAdapterManualBackupNeedsTheSchedule(t *testing.T) {
	lab := newMySQLAdapterLab(t)
	if _, err := lab.adapter.TriggerManual(context.Background(), lab.source); err == nil {
		t.Fatal("a backup was started without MOCO's CronJob")
	}
}

func TestMySQLAdapterRestoreWindowAndTargetCheck(t *testing.T) {
	lab := newMySQLAdapterLab(t, sourceKeys()...)
	window, err := lab.adapter.RestoreWindow(context.Background(), lab.source)
	if err != nil || window == nil || !window.Earliest.Equal(at(0, 0)) || !window.Latest.Equal(at(12, 0)) {
		t.Fatalf("window %+v %v", window, err)
	}
	if err := lab.adapter.CheckRestoreTarget(context.Background(), lab.source, domain.RestoreRequest{TargetTime: zoned(at(13, 0))}); !errors.Is(err, ErrRestoreTargetInvalid) {
		t.Errorf("a time after the window: %v", err)
	}
	if err := lab.adapter.CheckRestoreTarget(context.Background(), lab.source, domain.RestoreRequest{TargetTime: zoned(at(9, 0))}); err != nil {
		t.Errorf("a time inside: %v", err)
	}
	empty := newMySQLAdapterLab(t)
	if window, err := empty.adapter.RestoreWindow(context.Background(), empty.source); err != nil || window != nil {
		t.Errorf("no backups yet: %+v %v", window, err)
	}
}

// MOCO never deletes a backup: retention does, keeping what the window needs.
func TestMySQLAdapterPurgesRunsPastRetention(t *testing.T) {
	lab := newMySQLAdapterLab(t, sourceKeys()...)
	days := 1
	lab.source.BackupRetentionDays = &days
	lab.now = at(7, 0).Add(24 * time.Hour)
	deleted, err := lab.adapter.PurgeExpired(context.Background(), lab.source)
	if err != nil {
		t.Fatalf("PurgeExpired: %v", err)
	}
	if deleted != 2 {
		t.Errorf("deleted %d objects, want the first run's two", deleted)
	}
	remaining := readMySQLBackups(lab.objects.keys, k8s.MOCOBackupPrefix(mysqlSourceNS, mysqlSourceProject))
	if len(remaining) != 2 || remaining[0].ID != "20261011-060000" {
		t.Errorf("remaining %+v", remaining)
	}
	lab.source.BackupRetentionDays = nil
	if _, err := lab.adapter.PurgeExpired(context.Background(), lab.source); err == nil {
		t.Error("a project with no retention was purged by a guess")
	}
}

// EXC-586: a run's size is what its directory holds in the bucket.
func TestMySQLAdapterReportsEachRunsSize(t *testing.T) {
	lab := newMySQLAdapterLab(t, sourceKeys()...)
	prefix := k8s.MOCOBackupPrefix(mysqlSourceNS, mysqlSourceProject)
	lab.objects.sizes = map[string]int64{
		prefix + "20261011-060000/dump.tar": 4096, prefix + "20261011-060000/binlog.tar.zst": 512,
		prefix + "20261011-120000/dump.tar": 8192,
	}
	refs, err := lab.adapter.List(context.Background(), lab.source)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	byID := refsByID(refs)
	if got := byID["20261011-060000"].SizeBytes; got != 4608 {
		t.Errorf("dump plus binlogs: %d", got)
	}
	if got := byID["20261011-120000"].SizeBytes; got != 8192 {
		t.Errorf("dump only: %d", got)
	}
}

// EXC-586: a manual run stays MANUAL after the CronJob's history limit
// removes its Job, and the scheduled run after it stays SCHEDULED.
func TestMySQLAdapterManualRunOutlivesItsJob(t *testing.T) {
	lab := newMySQLAdapterLab(t, sourceKeys()...)
	lab.cronJob()
	lab.now = at(12, 0).Add(-10 * time.Second)
	ref, err := lab.adapter.TriggerManual(context.Background(), lab.source)
	if err != nil {
		t.Fatalf("TriggerManual: %v", err)
	}
	job := lab.mock.CRDs[mysqlSourceNS+"/"+ref.ID]
	_ = unstructured.SetNestedField(job.Object, map[string]interface{}{
		"succeeded": int64(1), "startTime": "2026-10-11T11:59:55Z", "completionTime": "2026-10-11T12:02:00Z"}, "status")
	if _, err := lab.adapter.List(context.Background(), lab.source); err != nil {
		t.Fatalf("List while the Job is there: %v", err)
	}
	delete(lab.mock.CRDs, mysqlSourceNS+"/"+ref.ID)
	prefix := k8s.MOCOBackupPrefix(mysqlSourceNS, mysqlSourceProject)
	lab.objects.keys = append(lab.objects.keys, prefix+"20261011-180000/dump.tar")

	refs, err := lab.adapter.List(context.Background(), lab.source)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	byID := refsByID(refs)
	if got := byID["20261011-120000"].Type; got != backupTypeManual {
		t.Errorf("the manual run after its Job went: %s", got)
	}
	for _, id := range []string{"20261011-060000", "20261011-180000"} {
		if got := byID[id].Type; got != backupTypeScheduled {
			t.Errorf("scheduled run %s: %s", id, got)
		}
	}
}

// A manual run whose Job was never seen claims the first run after it was
// asked for, but only within the time a Job takes to start.
func TestMySQLAdapterUnseenManualRunClaimsTheNextRunOnly(t *testing.T) {
	lab := newMySQLAdapterLab(t, sourceKeys()...)
	lab.cronJob()
	lab.now = at(11, 58)
	if _, err := lab.adapter.TriggerManual(context.Background(), lab.source); err != nil {
		t.Fatalf("TriggerManual: %v", err)
	}
	lab.now = at(3, 0)
	if _, err := lab.adapter.TriggerManual(context.Background(), lab.source); err != nil {
		t.Fatalf("TriggerManual: %v", err)
	}
	for name := range lab.mock.CRDs {
		if k8s.IsMySQLBackupJob(lab.mock.CRDs[name], mysqlSourceProject) {
			delete(lab.mock.CRDs, name)
		}
	}
	byID := refsByID(mustList(t, lab))
	if got := byID["20261011-120000"].Type; got != backupTypeManual {
		t.Errorf("the run two minutes after the request: %s", got)
	}
	if got := byID["20261011-060000"].Type; got != backupTypeScheduled {
		t.Errorf("a run three hours after a request: %s", got)
	}
}

// A manual Job that failed made no run, so it claims none.
func TestMySQLAdapterFailedManualRunClaimsNothing(t *testing.T) {
	lab := newMySQLAdapterLab(t, sourceKeys()...)
	lab.cronJob()
	lab.now = at(11, 59)
	ref, err := lab.adapter.TriggerManual(context.Background(), lab.source)
	if err != nil {
		t.Fatalf("TriggerManual: %v", err)
	}
	job := lab.mock.CRDs[mysqlSourceNS+"/"+ref.ID]
	_ = unstructured.SetNestedField(job.Object, map[string]interface{}{"conditions": []interface{}{
		map[string]interface{}{"type": "Failed", "status": "True", "message": "BackoffLimitExceeded"}}}, "status")
	mustList(t, lab)
	delete(lab.mock.CRDs, mysqlSourceNS+"/"+ref.ID)
	if got := refsByID(mustList(t, lab))["20261011-120000"].Type; got != backupTypeScheduled {
		t.Errorf("the scheduled run after a failed manual one: %s", got)
	}
}

// A manual backup that cannot be recorded is not started: it would be listed
// as scheduled once its Job went.
func TestMySQLAdapterManualBackupNeedsItsRecord(t *testing.T) {
	lab := newMySQLAdapterLab(t)
	lab.cronJob()
	lab.adapter.records = &failingBackupRecords{}
	if _, err := lab.adapter.TriggerManual(context.Background(), lab.source); err == nil {
		t.Fatal("a manual backup started without its record")
	}
	for name := range lab.mock.CRDs {
		if k8s.IsMySQLBackupJob(lab.mock.CRDs[name], mysqlSourceProject) {
			t.Errorf("Job %s started", name)
		}
	}
}

type failingBackupRecords struct{ fakeBackupRecordStore }

func (*failingBackupRecords) Save(context.Context, *domain.BackupRecord) error {
	return errors.New("platform database down")
}

func mustList(t *testing.T, lab *mysqlAdapterLab) []BackupRef {
	t.Helper()
	refs, err := lab.adapter.List(context.Background(), lab.source)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return refs
}

// A Job that could not be created made no run, and its record says so.
func TestMySQLAdapterUnstartedManualBackupIsRecordedFailed(t *testing.T) {
	lab := newMySQLAdapterLab(t, sourceKeys()...)
	lab.cronJob()
	lab.now = at(11, 59)
	lab.mock.CRDError = errors.New("quota exceeded")
	if _, err := lab.adapter.TriggerManual(context.Background(), lab.source); err == nil {
		t.Fatal("no error when the Job was refused")
	}
	lab.mock.CRDError = nil
	if len(lab.records.records) != 1 || lab.records.records[0].Status != backupStatusFailed {
		t.Fatalf("records %+v", lab.records.records)
	}
	if got := refsByID(mustList(t, lab))["20261011-120000"].Type; got != backupTypeScheduled {
		t.Errorf("the next run after a refused manual one: %s", got)
	}
}

// A store that lists keys only still lists every run, without sizes.
func TestMySQLAdapterListsRunsFromAKeysOnlyStore(t *testing.T) {
	lab := newMySQLAdapterLab(t, sourceKeys()...)
	lab.adapter.openStore = func(context.Context, *domain.S3Credentials) (ObjectDeleter, error) {
		return keysOnly{lab.objects}, nil
	}
	refs := mustList(t, lab)
	if len(refs) != 3 || refs[0].SizeBytes != 0 {
		t.Fatalf("refs %+v", refs)
	}
}

type keysOnly struct{ store *fakeObjectDeleter }

func (k keysOnly) ListKeys(ctx context.Context, bucket, prefix, token string, maxKeys int32) ([]string, string, error) {
	return k.store.ListKeys(ctx, bucket, prefix, token, maxKeys)
}

func (k keysOnly) DeleteKeys(ctx context.Context, bucket string, keys []string) error {
	return k.store.DeleteKeys(ctx, bucket, keys)
}

// EXC-586: retention takes the records of the manual runs it deleted with
// them, and keeps the records of runs still in the bucket.
func TestMySQLAdapterRetentionDropsTheRecordsOfPurgedRuns(t *testing.T) {
	lab := newMySQLAdapterLab(t, sourceKeys()...)
	ctx := context.Background()
	for id, asked := range map[string]time.Time{"job-purged": at(0, 0).Add(-time.Minute), "job-kept": at(5, 59)} {
		_ = lab.records.Save(ctx, &domain.BackupRecord{ID: id, ProjectID: mysqlSourceProject,
			Timestamp: asked.Format(time.RFC3339), Type: backupTypeManual, Status: backupStatusInProgress})
	}
	_ = lab.records.Save(ctx, &domain.BackupRecord{ID: "other-project", ProjectID: "proj2",
		Timestamp: at(0, 0).Format(time.RFC3339), Type: backupTypeManual, Status: backupStatusCompleted})
	days := 1
	lab.source.BackupRetentionDays = &days
	lab.now = at(7, 0).Add(24 * time.Hour)
	if _, err := lab.adapter.PurgeExpired(ctx, lab.source); err != nil {
		t.Fatalf("PurgeExpired: %v", err)
	}
	left := map[string]bool{}
	for _, record := range lab.records.records {
		left[record.ID] = true
	}
	if left["job-purged"] || !left["job-kept"] || !left["other-project"] {
		t.Errorf("records left %v", left)
	}
}
