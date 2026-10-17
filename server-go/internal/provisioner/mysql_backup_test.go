package provisioner

import (
	"context"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

func projectBackupCreds() *domain.S3Credentials {
	return &domain.S3Credentials{
		AccessKeyID: "tmp-key", SecretAccessKey: "tmp-secret", SessionToken: "tmp-token",
		ExpiresAt: time.Now().Add(time.Hour), Bucket: "backups", Endpoint: "https://s3.example", Region: "auto",
	}
}

func mysqlBackupRequest() domain.ProvisioningRequest {
	req := mysqlRequest("8.4.9")
	req.Backup = &domain.BackupSettings{Enabled: true, Schedule: "0 */6 * * *", Retention: 7, S3: projectBackupCreds()}
	return req
}

// ADR 0041 decision 6: a MySQL project with backups gets its credentials as
// AWS variables, the account MOCO's jobs run as, its BackupPolicy, and the
// cluster linked to it once it serves.
func TestMySQLProvision_WithBackups(t *testing.T) {
	mock, prov := newMySQLTest(t)
	prov.SetBackupPathStyle(true)
	if _, err := prov.ProvisionWithRollback(context.Background(), mysqlBackupRequest(), mysqlFree, recordingContext()); err != nil {
		t.Fatalf("ProvisionWithRollback: %v", err)
	}
	secret := mock.Secrets[mysqlTestNS+"/"+k8s.MySQLBackupAccessName]
	if string(secret["AWS_ACCESS_KEY_ID"]) != "tmp-key" || string(secret["AWS_SESSION_TOKEN"]) != "tmp-token" {
		t.Errorf("credentials secret %v", secret)
	}
	if _, ok := mock.CRDs[mysqlTestNS+"/"+k8s.MySQLBackupServiceAccount]; !ok {
		t.Error("no service account for the backup jobs")
	}
	policy, ok := mock.CRDs[mysqlTestNS+"/"+k8s.MySQLBackupPolicyName(mysqlTestProject)]
	if !ok {
		t.Fatalf("no BackupPolicy; calls %v", mock.Calls)
	}
	if schedule, _, _ := unstructured.NestedString(policy.Object, "spec", "schedule"); schedule != "0 */6 * * *" {
		t.Errorf("schedule %q", schedule)
	}
	if pathStyle, _, _ := unstructured.NestedBool(policy.Object, "spec", "jobConfig", "bucketConfig", "usePathStyle"); !pathStyle {
		t.Error("path-style addressing not passed to the job")
	}
	cluster := mock.CRDs[mysqlClusterKey]
	if name, _, _ := unstructured.NestedString(cluster.Object, "spec", "backupPolicyName"); name != k8s.MySQLBackupPolicyName(mysqlTestProject) {
		t.Errorf("cluster backupPolicyName %q", name)
	}
}

// The parent key never enters a tenant namespace: only a temporary,
// prefix-scoped credential is written.
func TestMySQLProvision_RefusesALongLivedKey(t *testing.T) {
	mock, prov := newMySQLTest(t)
	req := mysqlBackupRequest()
	req.Backup.S3.SessionToken = ""
	if _, err := prov.ProvisionWithRollback(context.Background(), req, mysqlFree, recordingContext()); err == nil {
		t.Fatal("a long-lived key was accepted")
	}
	if _, ok := mock.Secrets[mysqlTestNS+"/"+k8s.MySQLBackupAccessName]; ok {
		t.Error("the key was written into the namespace")
	}
}

func TestMySQLProvision_WithoutBackups(t *testing.T) {
	mock, prov := newMySQLTest(t)
	if _, err := prov.ProvisionWithRollback(context.Background(), mysqlRequest("8.4.9"), mysqlFree, recordingContext()); err != nil {
		t.Fatalf("ProvisionWithRollback: %v", err)
	}
	if _, ok := mock.CRDs[mysqlTestNS+"/"+k8s.MySQLBackupPolicyName(mysqlTestProject)]; ok {
		t.Error("a BackupPolicy without backups")
	}
	if _, found, _ := unstructured.NestedString(mock.CRDs[mysqlClusterKey].Object, "spec", "backupPolicyName"); found {
		t.Error("cluster linked to a policy")
	}
}

func TestMySQLProvision_RefusesBackupsWithoutScheduleOrStore(t *testing.T) {
	for name, change := range map[string]func(*domain.BackupSettings){
		"no schedule":  func(b *domain.BackupSettings) { b.Schedule = "" },
		"bad schedule": func(b *domain.BackupSettings) { b.Schedule = "every hour" },
		"no store":     func(b *domain.BackupSettings) { b.S3 = nil },
	} {
		mock, prov := newMySQLTest(t)
		req := mysqlBackupRequest()
		change(req.Backup)
		if _, err := prov.ProvisionWithRollback(context.Background(), req, mysqlFree, recordingContext()); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if _, ok := mock.CRDs[mysqlClusterKey]; ok {
			t.Errorf("%s: a cluster was created before the refusal", name)
		}
	}
}

// Free ↔ Standard moves the schedule (and the job sizing) with the plan.
func TestMySQLSetBackupPlan(t *testing.T) {
	mock, prov := newMySQLTest(t)
	if _, err := prov.ProvisionWithRollback(context.Background(), mysqlBackupRequest(), mysqlFree, recordingContext()); err != nil {
		t.Fatalf("ProvisionWithRollback: %v", err)
	}
	if err := prov.SetBackupPlan(context.Background(), mysqlTestNS, mysqlTestProject, "0 * * * *", mysqlStandard); err != nil {
		t.Fatalf("SetBackupPlan: %v", err)
	}
	policy := mock.CRDs[mysqlTestNS+"/"+k8s.MySQLBackupPolicyName(mysqlTestProject)]
	if schedule, _, _ := unstructured.NestedString(policy.Object, "spec", "schedule"); schedule != "0 * * * *" {
		t.Errorf("schedule %q", schedule)
	}
	if err := prov.ConfigureBackup(context.Background(), mysqlTestNS, mysqlTestProject, "0 */6 * * *", 7); err != nil {
		t.Fatalf("ConfigureBackup: %v", err)
	}
	policy = mock.CRDs[mysqlTestNS+"/"+k8s.MySQLBackupPolicyName(mysqlTestProject)]
	if schedule, _, _ := unstructured.NestedString(policy.Object, "spec", "schedule"); schedule != "0 */6 * * *" {
		t.Errorf("schedule after ConfigureBackup %q", schedule)
	}
}

func restoreJobOpts() k8s.MySQLBackupOpts {
	return k8s.MySQLBackupOpts{ProjectID: mysqlTestProject, Namespace: mysqlTestNS, Plan: mysqlFree, Disk: "5Gi",
		Store: k8s.MySQLBackupStore{Bucket: "backups", Endpoint: "https://s3.example"}}
}

// A restored cluster is served only once MOCO reports its data in.
func TestMySQLApplyRestoredCluster(t *testing.T) {
	mock, prov := newMySQLTest(t)
	image := mysqlTestImage(t)
	opts := k8s.MySQLClusterOpts{ProjectID: mysqlTestProject, Namespace: mysqlTestNS, Tier: mysqlFree, Image: image}
	source := k8s.MySQLRestoreSource{Namespace: "org1-proj-src", ProjectID: "proj-src", Point: time.Now(), CredentialsSecret: k8s.MySQLRestoreSourceAccessName}
	if err := prov.ApplyRestoredCluster(context.Background(), opts, source, restoreJobOpts()); err != nil {
		t.Fatalf("ApplyRestoredCluster: %v", err)
	}
	cluster := mock.CRDs[mysqlClusterKey]
	if name, _, _ := unstructured.NestedString(cluster.Object, "spec", "restore", "sourceName"); name != "proj-src-mysql" {
		t.Errorf("restore source %q", name)
	}
	if _, ok := mock.CRDs[mysqlTestNS+"/proj-my1-mysql-server"]; !ok {
		t.Error("the restored cluster has no server certificate of its own (EXC-589)")
	}
	if err := prov.AwaitRestored(context.Background(), mysqlPoller(), mysqlTestNS, mysqlTestProject, 1); err != nil {
		t.Fatalf("AwaitRestored: %v", err)
	}
}

// MOCO's restore Job does not retry: once it fails the wait ends with that.
func TestMySQLAwaitRestored_FailsWithTheRestoreJob(t *testing.T) {
	mock, prov := newMySQLTest(t)
	mock.AutoReconcileClusters = false
	opts := k8s.MySQLClusterOpts{ProjectID: mysqlTestProject, Namespace: mysqlTestNS, Tier: mysqlFree, Image: mysqlTestImage(t)}
	source := k8s.MySQLRestoreSource{Namespace: "org1-proj-src", ProjectID: "proj-src", Point: time.Now(), CredentialsSecret: k8s.MySQLRestoreSourceAccessName}
	if err := prov.ApplyRestoredCluster(context.Background(), opts, source, restoreJobOpts()); err != nil {
		t.Fatalf("ApplyRestoredCluster: %v", err)
	}
	failed := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "batch/v1", "kind": "Job",
		"metadata": map[string]interface{}{"name": k8s.MySQLRestoreJobName(mysqlTestProject), "namespace": mysqlTestNS},
		"status": map[string]interface{}{"conditions": []interface{}{
			map[string]interface{}{"type": "Failed", "status": "True", "message": "no available backup"}}},
	}}
	mock.CRDs[mysqlTestNS+"/"+failed.GetName()] = failed
	err := prov.AwaitRestored(context.Background(), mysqlPoller(), mysqlTestNS, mysqlTestProject, 1)
	if err == nil || !strings.Contains(err.Error(), "no available backup") {
		t.Fatalf("err %v, want the restore job's failure", err)
	}
}

func mysqlTestImage(t *testing.T) string {
	t.Helper()
	image, err := config.MySQLImage("8.4.9")
	if err != nil {
		t.Fatal(err)
	}
	return image
}

// Removing the database from a namespace the apps keep removes its backup
// policy and the credential with it; the backups in the bucket stay.
func TestMySQLRemoveDatabase_RemovesBackupObjects(t *testing.T) {
	mock, prov := newMySQLTest(t)
	if _, err := prov.ProvisionWithRollback(context.Background(), mysqlBackupRequest(), mysqlFree, recordingContext()); err != nil {
		t.Fatalf("ProvisionWithRollback: %v", err)
	}
	if err := prov.RemoveDatabase(context.Background(), mysqlTestNS, mysqlTestProject); err != nil {
		t.Fatalf("RemoveDatabase: %v", err)
	}
	if _, ok := mock.CRDs[mysqlTestNS+"/"+k8s.MySQLBackupPolicyName(mysqlTestProject)]; ok {
		t.Error("backup policy left behind")
	}
	if _, ok := mock.Secrets[mysqlTestNS+"/"+k8s.MySQLBackupAccessName]; ok {
		t.Error("backup credentials left behind")
	}
}

func backupCronJobKey() string {
	return mysqlTestNS + "/" + k8s.MySQLBackupCronJobName(mysqlTestProject)
}

func hasEgressWait(cronJob *unstructured.Unstructured) bool {
	inits, _, _ := unstructured.NestedSlice(cronJob.Object, "spec", "jobTemplate", "spec", "template", "spec", "initContainers")
	for _, raw := range inits {
		if container, ok := raw.(map[string]interface{}); ok && container["name"] == k8s.MySQLEgressWaitContainerName {
			return true
		}
	}
	return false
}

// EXC-586: provisioning returns once MOCO's backup CronJob exists and its runs
// wait for their egress, so not even the first scheduled run dials too early.
func TestMySQLProvision_BackupRunsWaitForEgress(t *testing.T) {
	mock, prov := newMySQLTest(t)
	if _, err := prov.ProvisionWithRollback(context.Background(), mysqlBackupRequest(), mysqlFree, recordingContext()); err != nil {
		t.Fatalf("ProvisionWithRollback: %v", err)
	}
	cron, ok := mock.CRDs[backupCronJobKey()]
	if !ok || !hasEgressWait(cron) {
		t.Fatalf("backup CronJob %v does not wait for egress", cron)
	}
}

// A reattached policy (in-place restore) gets the wait on MOCO's new CronJob.
func TestMySQLLinkBackupPolicy_WaitsForEgress(t *testing.T) {
	mock, prov := newMySQLTest(t)
	if _, err := prov.ProvisionWithRollback(context.Background(), mysqlBackupRequest(), mysqlFree, recordingContext()); err != nil {
		t.Fatalf("ProvisionWithRollback: %v", err)
	}
	cluster := mock.CRDs[mysqlClusterKey]
	if err := mock.UpdateCRD(context.Background(), k8s.MOCOClusterGVR, mysqlTestNS, k8s.WithoutMySQLBackupPolicy(cluster)); err != nil {
		t.Fatal(err)
	}
	if _, ok := mock.CRDs[backupCronJobKey()]; ok {
		t.Fatal("the mock's MOCO kept the CronJob of an unlinked cluster")
	}
	if err := prov.LinkBackupPolicy(context.Background(), mysqlTestNS, mysqlTestProject); err != nil {
		t.Fatalf("LinkBackupPolicy: %v", err)
	}
	if cron, ok := mock.CRDs[backupCronJobKey()]; !ok || !hasEgressWait(cron) {
		t.Errorf("relinked CronJob %v does not wait for egress", cron)
	}
}

// Attaching fails when MOCO never makes the CronJob: no backup would run.
func TestMySQLAttachBackups_FailsWithoutMOCOsCronJob(t *testing.T) {
	mock, prov := newMySQLTest(t)
	if _, err := prov.ProvisionWithRollback(context.Background(), mysqlRequest("8.4.9"), mysqlFree, recordingContext()); err != nil {
		t.Fatalf("ProvisionWithRollback: %v", err)
	}
	mock.AutoReconcileClusters = false
	opts := k8s.MySQLBackupOpts{ProjectID: mysqlTestProject, Namespace: mysqlTestNS, Schedule: "0 */6 * * *",
		Plan: mysqlFree, Disk: "1Gi", Store: prov.BackupStore(projectBackupCreds())}
	err := prov.AttachBackups(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "backup schedule") {
		t.Errorf("AttachBackups without a CronJob: %v", err)
	}
}

// The sweep's entry point: absent is no error, present gets the wait once.
func TestMySQLEnsureBackupEgressWait(t *testing.T) {
	mock, prov := newMySQLTest(t)
	ctx := context.Background()
	if found, err := prov.EnsureBackupEgressWait(ctx, mysqlTestNS, mysqlTestProject); err != nil || found {
		t.Fatalf("no CronJob yet: found=%v err=%v", found, err)
	}
	if _, err := prov.ProvisionWithRollback(ctx, mysqlBackupRequest(), mysqlFree, recordingContext()); err != nil {
		t.Fatalf("ProvisionWithRollback: %v", err)
	}
	// A CronJob made before EXC-586, as MOCO renders it.
	cron := mock.CRDs[backupCronJobKey()].DeepCopy()
	unstructured.RemoveNestedField(cron.Object, "spec", "jobTemplate", "spec", "template", "spec", "initContainers")
	mock.CRDs[backupCronJobKey()] = cron
	if found, err := prov.EnsureBackupEgressWait(ctx, mysqlTestNS, mysqlTestProject); err != nil || !found {
		t.Fatalf("EnsureBackupEgressWait: found=%v err=%v", found, err)
	}
	if !hasEgressWait(mock.CRDs[backupCronJobKey()]) {
		t.Error("CronJob not given the wait")
	}
	mock.Calls = nil
	if _, err := prov.EnsureBackupEgressWait(ctx, mysqlTestNS, mysqlTestProject); err != nil {
		t.Fatal(err)
	}
	for _, call := range mock.Calls {
		if strings.HasPrefix(call, "UpdateCRD:") {
			t.Errorf("a CronJob that waits was written again: %v", mock.Calls)
		}
	}
}

// MOCO writes the CronJob too: a conflict is re-read and retried, a lasting
// one is reported.
func TestMySQLEnsureBackupEgressWait_RetriesConflicts(t *testing.T) {
	mock, prov := newMySQLTest(t)
	ctx := context.Background()
	applyTestCluster(t, mock, mysqlFree)
	seedBackupCronJob(mock, false)
	mock.UpdateCRDConflicts = 1
	if found, err := prov.EnsureBackupEgressWait(ctx, mysqlTestNS, mysqlTestProject); err != nil || !found {
		t.Fatalf("one conflict: found=%v err=%v", found, err)
	}
	if !hasEgressWait(mock.CRDs[backupCronJobKey()]) {
		t.Error("CronJob not given the wait after a conflict")
	}
	seedBackupCronJob(mock, false)
	mock.UpdateCRDConflicts = mysqlCronJobWriteAttempts
	if _, err := prov.EnsureBackupEgressWait(ctx, mysqlTestNS, mysqlTestProject); err == nil {
		t.Error("a lasting conflict was not reported")
	}
}
