package k8s

import (
	"errors"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func backupOpts(plan string) MySQLBackupOpts {
	tier := freePlan
	if plan == "standard" {
		tier = standardPlan
	}
	return MySQLBackupOpts{
		ProjectID: "proj-abc", Namespace: "org-proj-abc", Schedule: "0 */6 * * *", Plan: tier,
		Disk: tier.StorageSize, StorageClass: "excalibase-tenant",
		Store: MySQLBackupStore{Bucket: "backups", Endpoint: "https://s3.example", Region: "auto", PathStyle: true},
	}
}

// MOCO keys a cluster's backups moco/<namespace>/<cluster>/<time>/
// (backup/key.go calcPrefix at v0.37.0), so each project's prefix is its own.
func TestMOCOBackupPrefix(t *testing.T) {
	if got := MOCOBackupPrefix("org-proj-abc", "proj-abc"); got != "moco/org-proj-abc/proj-abc-mysql/" {
		t.Errorf("prefix %q", got)
	}
}

// ADR 0041 decision 6: one BackupPolicy per project, never two runs at once,
// the store addressed as the platform addresses it, credentials only through
// the project's own Secret.
func TestBuildMySQLBackupPolicy(t *testing.T) {
	policy, err := BuildMySQLBackupPolicy(backupOpts("free"))
	if err != nil {
		t.Fatalf("BuildMySQLBackupPolicy: %v", err)
	}
	if policy.GetName() != "proj-abc-mysql-backup" || policy.GetNamespace() != "org-proj-abc" || policy.GetKind() != "BackupPolicy" {
		t.Errorf("policy %s/%s %s", policy.GetNamespace(), policy.GetName(), policy.GetKind())
	}
	spec := policy.Object["spec"].(map[string]interface{})
	if spec["schedule"] != "0 */6 * * *" || spec["concurrencyPolicy"] != "Forbid" {
		t.Errorf("schedule %v concurrency %v", spec["schedule"], spec["concurrencyPolicy"])
	}
	job := spec["jobConfig"].(map[string]interface{})
	if job["serviceAccountName"] != MySQLBackupServiceAccount {
		t.Errorf("service account %v", job["serviceAccountName"])
	}
	bucket := job["bucketConfig"].(map[string]interface{})
	if bucket["bucketName"] != "backups" || bucket["endpointURL"] != "https://s3.example" ||
		bucket["region"] != "auto" || bucket["usePathStyle"] != true || bucket["backendType"] != "s3" {
		t.Errorf("bucket %v", bucket)
	}
	envFrom, _, _ := unstructured.NestedSlice(job, "envFrom")
	if len(envFrom) != 1 || envFrom[0].(map[string]interface{})["secretRef"].(map[string]interface{})["name"] != MySQLBackupAccessName {
		t.Errorf("envFrom %v", envFrom)
	}
	if _, ok := job["workVolume"]; !ok {
		t.Error("the job needs a work volume")
	}
}

// MOCO's own job default is 4 CPUs and 4Gi; a Free backup must fit beside a
// 1Gi mysqld on a small node, so every bound is set explicitly by plan.
func TestBuildMySQLBackupPolicy_SizedByPlan(t *testing.T) {
	for _, tc := range []struct{ plan, memory, maxMemory string }{
		{"free", "256Mi", "1Gi"}, {"standard", "1Gi", "2Gi"},
	} {
		policy, err := BuildMySQLBackupPolicy(backupOpts(tc.plan))
		if err != nil {
			t.Fatalf("%s: %v", tc.plan, err)
		}
		job, _, _ := unstructured.NestedMap(policy.Object, "spec", "jobConfig")
		for _, key := range []string{"cpu", "maxCpu", "memory", "maxMemory", "threads"} {
			if _, ok := job[key]; !ok {
				t.Errorf("%s: %s not set", tc.plan, key)
			}
		}
		if job["memory"] != tc.memory || job["maxMemory"] != tc.maxMemory {
			t.Errorf("%s: memory %v max %v", tc.plan, job["memory"], job["maxMemory"])
		}
	}
}

func TestBuildMySQLBackupPolicy_RefusesIncomplete(t *testing.T) {
	for name, change := range map[string]func(*MySQLBackupOpts){
		"no schedule": func(o *MySQLBackupOpts) { o.Schedule = "" },
		"no bucket":   func(o *MySQLBackupOpts) { o.Store.Bucket = "" },
		"no endpoint": func(o *MySQLBackupOpts) { o.Store.Endpoint = "" },
		"no disk":     func(o *MySQLBackupOpts) { o.Disk = "" },
		"bad cron":    func(o *MySQLBackupOpts) { o.Schedule = "0 0 */6 * * *" },
	} {
		opts := backupOpts("free")
		change(&opts)
		if _, err := BuildMySQLBackupPolicy(opts); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The job reads AWS_* from its environment (AWS SDK default chain); the
// metadata renewal reads sits beside them. A long-lived key is refused.
func TestMySQLBackupAccessNameData(t *testing.T) {
	creds := &domain.S3Credentials{
		AccessKeyID: "AK", SecretAccessKey: "SK", SessionToken: "ST", Region: "auto",
		ExpiresAt: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC), IssuedBy: "abc", Bucket: "backups", Endpoint: "https://s3.example",
	}
	data, err := MySQLBackupAccessNameData(creds)
	if err != nil {
		t.Fatalf("data: %v", err)
	}
	for key, want := range map[string]string{
		"AWS_ACCESS_KEY_ID": "AK", "AWS_SECRET_ACCESS_KEY": "SK", "AWS_SESSION_TOKEN": "ST", "AWS_REGION": "auto",
		BackupCredentialsExpiresAtKey: "2026-10-11T12:00:00Z", BackupCredentialsIssuedByKey: "abc",
		BackupCredentialsBucketKey: "backups", BackupCredentialsEndpointKey: "https://s3.example",
	} {
		if string(data[key]) != want {
			t.Errorf("%s = %q, want %q", key, data[key], want)
		}
	}
	creds.SessionToken = ""
	if _, err := MySQLBackupAccessNameData(creds); !errors.Is(err, ErrLongLivedBackupKey) {
		t.Errorf("long-lived key: %v", err)
	}
}

func TestWithMySQLBackupPolicy(t *testing.T) {
	cluster, _ := BuildMySQLCluster(mysqlOpts(freePlan))
	attached := WithMySQLBackupPolicy(cluster, "proj-abc")
	if name, _, _ := unstructured.NestedString(attached.Object, "spec", "backupPolicyName"); name != "proj-abc-mysql-backup" {
		t.Errorf("backupPolicyName %q", name)
	}
	if _, found, _ := unstructured.NestedString(cluster.Object, "spec", "backupPolicyName"); found {
		t.Error("the original was changed")
	}
	detached := WithoutMySQLBackupPolicy(attached)
	if _, found, _ := unstructured.NestedString(detached.Object, "spec", "backupPolicyName"); found {
		t.Error("policy still attached")
	}
}

// Restore = a cluster with spec.restore naming the source and the point, its
// job reading the source's prefix with the credentials Secret named.
func TestWithMySQLRestore(t *testing.T) {
	cluster, _ := BuildMySQLCluster(mysqlOpts(freePlan))
	point := time.Date(2026, 10, 11, 9, 30, 15, 0, time.FixedZone("x", 3600))
	opts := backupOpts("free")
	restored, err := WithMySQLRestore(cluster, MySQLRestoreSource{
		Namespace: "org-src", ProjectID: "proj-src", Point: point, CredentialsSecret: MySQLRestoreSourceAccessName,
	}, opts)
	if err != nil {
		t.Fatalf("WithMySQLRestore: %v", err)
	}
	restore, _, _ := unstructured.NestedMap(restored.Object, "spec", "restore")
	if restore["sourceName"] != "proj-src-mysql" || restore["sourceNamespace"] != "org-src" || restore["restorePoint"] != "2026-10-11T08:30:15Z" {
		t.Errorf("restore %v", restore)
	}
	envFrom, _, _ := unstructured.NestedSlice(restore, "jobConfig", "envFrom")
	if len(envFrom) != 1 || envFrom[0].(map[string]interface{})["secretRef"].(map[string]interface{})["name"] != MySQLRestoreSourceAccessName {
		t.Errorf("envFrom %v", envFrom)
	}
	if _, found, _ := unstructured.NestedString(restored.Object, "spec", "backupPolicyName"); found {
		t.Error("a restoring cluster must not back up before its data is in")
	}
	if MySQLClusterRestored(restored) {
		t.Error("not restored before MOCO says so")
	}
	_ = unstructured.SetNestedField(restored.Object, "2026-10-11T10:00:00Z", "status", "restoredTime")
	if !MySQLClusterRestored(restored) {
		t.Error("restoredTime set")
	}
	if !MySQLClusterRestored(cluster) {
		t.Error("a cluster that restores nothing is restored")
	}
}

// A manual backup is a Job from the CronJob's template, owned by it like
// `kubectl create job --from=cronjob` makes one.
func TestBuildManualMySQLBackupJob(t *testing.T) {
	cron := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "batch/v1", "kind": "CronJob",
		"metadata": map[string]interface{}{"name": "moco-backup-proj-abc-mysql", "namespace": "org-proj-abc", "uid": "u-1"},
		"spec": map[string]interface{}{"jobTemplate": map[string]interface{}{
			"metadata": map[string]interface{}{"labels": map[string]interface{}{"app.kubernetes.io/name": "mysql-backup"}},
			"spec":     map[string]interface{}{"template": map[string]interface{}{"spec": map[string]interface{}{}}},
		}},
	}}
	at := time.Date(2026, 10, 11, 15, 4, 5, 0, time.UTC)
	job, err := BuildManualMySQLBackupJob(cron, at)
	if err != nil {
		t.Fatalf("BuildManualMySQLBackupJob: %v", err)
	}
	if job.GetName() != "moco-backup-proj-abc-mysql-manual-20261011150405" || job.GetKind() != "Job" {
		t.Errorf("job %s %s", job.GetName(), job.GetKind())
	}
	owners := job.GetOwnerReferences()
	if len(owners) != 1 || owners[0].Kind != "CronJob" || owners[0].UID != "u-1" || owners[0].Controller == nil || !*owners[0].Controller {
		t.Errorf("owners %+v", owners)
	}
	if job.GetLabels()["app.kubernetes.io/name"] != "mysql-backup" || job.GetAnnotations()[manualBackupAnnotation] != "manual" {
		t.Errorf("labels %v annotations %v", job.GetLabels(), job.GetAnnotations())
	}
	if _, found, _ := unstructured.NestedMap(job.Object, "spec", "template"); !found {
		t.Error("job has no pod template")
	}
}

func TestMySQLBackupJobState(t *testing.T) {
	job := func(status map[string]interface{}) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]interface{}{"metadata": map[string]interface{}{"name": "j"}, "status": status}}
	}
	for name, tc := range map[string]struct {
		status map[string]interface{}
		want   string
	}{
		"running":   {map[string]interface{}{"active": int64(1), "startTime": "2026-10-11T10:00:00Z"}, MySQLJobRunning},
		"pending":   {map[string]interface{}{}, MySQLJobRunning},
		"succeeded": {map[string]interface{}{"succeeded": int64(1), "completionTime": "2026-10-11T10:05:00Z"}, MySQLJobSucceeded},
		"failed": {map[string]interface{}{"failed": int64(1), "conditions": []interface{}{
			map[string]interface{}{"type": "Failed", "status": "True", "message": "BackoffLimitExceeded"}}}, MySQLJobFailed},
	} {
		if got := MySQLBackupJobState(job(tc.status)); got.Phase != tc.want {
			t.Errorf("%s: %s, want %s", name, got.Phase, tc.want)
		}
	}
}

// A plan change moves the schedule and the job sizing; the store stays.
func TestWithMySQLBackupSchedule(t *testing.T) {
	policy, _ := BuildMySQLBackupPolicy(backupOpts("free"))
	moved, err := WithMySQLBackupSchedule(policy, "0 * * * *", standardPlan)
	if err != nil {
		t.Fatalf("WithMySQLBackupSchedule: %v", err)
	}
	if schedule, _, _ := unstructured.NestedString(moved.Object, "spec", "schedule"); schedule != "0 * * * *" {
		t.Errorf("schedule %q", schedule)
	}
	job, _, _ := unstructured.NestedMap(moved.Object, "spec", "jobConfig")
	if job["memory"] != "1Gi" || job["bucketConfig"].(map[string]interface{})["bucketName"] != "backups" {
		t.Errorf("job %v", job)
	}
	if schedule, _, _ := unstructured.NestedString(policy.Object, "spec", "schedule"); schedule != "0 */6 * * *" {
		t.Error("the original was changed")
	}
	if _, err := WithMySQLBackupSchedule(policy, "nonsense", standardPlan); err == nil {
		t.Error("a bad schedule was accepted")
	}
}
