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
	lab := &mysqlAdapterLab{mock: mock, objects: objects, mysql: mysql, now: at(13, 0)}
	lab.adapter = NewMySQLBackupAdapter(MySQLBackupAdapterConfig{
		Kube: mock, MySQL: mysql, Storage: StaticBackupStorage(platformStore()),
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
