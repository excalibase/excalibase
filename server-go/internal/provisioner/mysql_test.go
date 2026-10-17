package provisioner

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

const (
	mysqlTestProject = "proj-my1"
	mysqlTestOrg     = "org1"
	mysqlTestNS      = "org1-proj-my1"
	mysqlClusterKey  = mysqlTestNS + "/proj-my1-mysql"
)

var (
	mysqlFree     = config.TierConfig{Instances: 1, StorageSize: "5Gi", CPU: "0.5", Memory: "512Mi", StatementTimeout: "15s"}
	mysqlStandard = config.TierConfig{Instances: 3, StorageSize: "50Gi", CPU: "2", Memory: "4Gi", StatementTimeout: "30s"}
)

func mysqlPoller() Poller { return NewPoller(time.Millisecond, 50*time.Millisecond) }

func newMySQLTest(t *testing.T) (*k8s.MockClient, *MySQLProvisioner) {
	t.Helper()
	mock := k8s.NewMockClient()
	mock.AutoReconcileClusters = true
	mock.WildcardPodReady = true
	mock.ExecOutput[mysqlTestNS+"/moco-proj-my1-mysql-0"] = "1\n"
	prov := NewMySQLProvisioner(mock)
	prov.SetPollers(mysqlPoller(), mysqlPoller())
	return mock, prov
}

func mysqlRequest(version string) domain.ProvisioningRequest {
	return domain.ProvisioningRequest{ProjectName: mysqlTestProject, OrgID: mysqlTestOrg, DBType: domain.MySQL, MySQLVersion: version}
}

func recordingContext() *ProvisionContext {
	return NewProvisionContext(func(domain.ProvisioningStage) {}, func(string) {})
}

func execsMatching(mock *k8s.MockClient, fragment string) []string {
	var out []string
	for _, cmd := range mock.ExecCommands {
		if strings.Contains(cmd, fragment) {
			out = append(out, cmd)
		}
	}
	return out
}

// ADR 0041: a MySQL project is one MySQLCluster in its namespace, on the
// catalogue's pinned image, served once its pods are ready AND a real query
// answers on the primary.
func TestMySQLProvision_FreeCluster(t *testing.T) {
	mock, prov := newMySQLTest(t)
	result, err := prov.ProvisionWithRollback(context.Background(), mysqlRequest("8.4.9"), mysqlFree, recordingContext())
	if err != nil {
		t.Fatalf("ProvisionWithRollback: %v", err)
	}
	cluster, ok := mock.CRDs[mysqlClusterKey]
	if !ok {
		t.Fatalf("no MySQLCluster applied; calls %v", mock.Calls)
	}
	wantImage, _ := config.MySQLImage("8.4.9")
	if got := k8s.MySQLClusterImage(cluster); got != wantImage {
		t.Errorf("image %s, want %s", got, wantImage)
	}
	if _, ok := mock.CRDs[mysqlTestNS+"/"+k8s.MySQLConfigName(mysqlTestProject)]; !ok {
		t.Error("the mysqld settings ConfigMap was not applied")
	}
	if !mock.Namespaces[mysqlTestNS] {
		t.Error("the project namespace was not created")
	}
	if len(execsMatching(mock, "SELECT 1")) == 0 {
		t.Errorf("no real query proved the database answers: %v", mock.ExecCommands)
	}
	if len(execsMatching(mock, "CREATE DATABASE IF NOT EXISTS `app`")) == 0 {
		t.Errorf("the project's database was not created: %v", mock.ExecCommands)
	}
	if result.Host != k8s.MySQLPrimaryHost(mysqlTestProject, mysqlTestNS) || result.Port != 3306 ||
		result.ReadOnlyHost != k8s.MySQLReplicaHost(mysqlTestProject, mysqlTestNS) ||
		result.DatabaseName != "app" || result.SSLMode != "VERIFY_IDENTITY" || result.Namespace != mysqlTestNS {
		t.Errorf("result %+v", result)
	}
}

func TestMySQLProvision_DefaultsToNewestVersionAndRefusesUnknown(t *testing.T) {
	mock, prov := newMySQLTest(t)
	if _, err := prov.ProvisionWithRollback(context.Background(), mysqlRequest(""), mysqlFree, recordingContext()); err != nil {
		t.Fatalf("no version: %v", err)
	}
	newest, _ := config.MySQLImage(config.DefaultMySQLVersion())
	if got := k8s.MySQLClusterImage(mock.CRDs[mysqlClusterKey]); got != newest {
		t.Errorf("image %s, want the newest %s", got, newest)
	}
	_, prov = newMySQLTest(t)
	if _, err := prov.ProvisionWithRollback(context.Background(), mysqlRequest("8.0.46"), mysqlFree, recordingContext()); err == nil {
		t.Error("a version outside the catalogue was provisioned")
	}
}

// DocumentDB is a Postgres extension; a MySQL request for it is refused
// rather than silently ignored.
func TestMySQLProvision_RefusesDocumentDB(t *testing.T) {
	_, prov := newMySQLTest(t)
	req := mysqlRequest("")
	req.DocumentDB = true
	if _, err := prov.ProvisionWithRollback(context.Background(), req, mysqlFree, recordingContext()); !errors.Is(err, ErrMySQLUnsupported) {
		t.Errorf("documentdb: %v", err)
	}
	req = mysqlRequest("")
	req.DatabaseName = "app; DROP"
	if _, err := prov.ProvisionWithRollback(context.Background(), req, mysqlFree, recordingContext()); err == nil {
		t.Error("an unsafe database name was accepted")
	}
}

// A database that never answers fails the build, and the rollback removes
// the namespace the build made.
func TestMySQLProvision_UnansweredQueryFailsAndRollsBack(t *testing.T) {
	mock, prov := newMySQLTest(t)
	mock.WildcardExecError = errors.New("ERROR 2002: can't connect")
	pc := recordingContext()
	if _, err := prov.ProvisionWithRollback(context.Background(), mysqlRequest(""), mysqlFree, pc); err == nil {
		t.Fatal("a database that never answered was reported provisioned")
	}
	pc.Rollback(context.Background())
	if mock.Namespaces[mysqlTestNS] {
		t.Error("the rollback left the namespace")
	}
}

func TestMySQLProvision_StandardWaitsForEveryInstance(t *testing.T) {
	mock, prov := newMySQLTest(t)
	if _, err := prov.ProvisionWithRollback(context.Background(), mysqlRequest(""), mysqlStandard, recordingContext()); err != nil {
		t.Fatalf("ProvisionWithRollback: %v", err)
	}
	for _, pod := range []string{"moco-proj-my1-mysql-0", "moco-proj-my1-mysql-1", "moco-proj-my1-mysql-2"} {
		if !slices.Contains(mock.Calls, "IsPodReady:"+mysqlTestNS+"/"+pod) {
			t.Errorf("instance %s was never checked", pod)
		}
	}
}

func applyTestCluster(t *testing.T, mock *k8s.MockClient, plan config.TierConfig) {
	t.Helper()
	image, _ := config.MySQLImage("8.4.9")
	cluster, err := k8s.BuildMySQLCluster(k8s.MySQLClusterOpts{ProjectID: mysqlTestProject, Namespace: mysqlTestNS, Tier: plan, Image: image})
	if err != nil {
		t.Fatal(err)
	}
	_ = mock.ApplyCRD(context.Background(), k8s.MOCOClusterGVR, mysqlTestNS, cluster)
}

func backupCronJob(suspend bool) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "batch/v1", "kind": "CronJob",
		"metadata": map[string]interface{}{"name": "moco-backup-proj-my1-mysql", "namespace": mysqlTestNS},
		"spec":     map[string]interface{}{"suspend": suspend, "schedule": "0 * * * *"},
	}}
}

// Pause stops the instances (MOCO offline keeps the volumes) and suspends
// MOCO's backup CronJob, which offline alone leaves running (ADR 0041).
func TestMySQLPause_OfflineAndSuspendsBackups(t *testing.T) {
	mock, prov := newMySQLTest(t)
	applyTestCluster(t, mock, mysqlFree)
	mock.CRDs[mysqlTestNS+"/moco-backup-proj-my1-mysql"] = backupCronJob(false)
	if err := prov.StopReplication(context.Background(), mysqlTestNS, mysqlTestProject); err != nil {
		t.Fatalf("StopReplication: %v", err)
	}
	if err := prov.Pause(context.Background(), mysqlTestNS, mysqlTestProject); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if offline, _, _ := unstructured.NestedBool(mock.CRDs[mysqlClusterKey].Object, "spec", "offline"); !offline {
		t.Error("the cluster was not taken offline")
	}
	if suspended, _, _ := unstructured.NestedBool(mock.CRDs[mysqlTestNS+"/moco-backup-proj-my1-mysql"].Object, "spec", "suspend"); !suspended {
		t.Error("the backup CronJob still runs while paused")
	}
	stopped, err := prov.WorkloadStopped(context.Background(), mysqlTestNS, mysqlTestProject)
	if err != nil || !stopped {
		t.Errorf("WorkloadStopped = %v %v", stopped, err)
	}
}

// A pause is not done while an instance pod still exists.
func TestMySQLPause_WaitsForPodsToGo(t *testing.T) {
	mock, prov := newMySQLTest(t)
	applyTestCluster(t, mock, mysqlFree)
	mock.Pods[mysqlTestNS] = []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "moco-proj-my1-mysql-0", Namespace: mysqlTestNS}}}
	if err := prov.Pause(context.Background(), mysqlTestNS, mysqlTestProject); !errors.Is(err, ErrWaitTimeout) {
		t.Errorf("Pause with a pod still running = %v, want a timeout", err)
	}
}

func TestMySQLResume_OnlineResumesBackupsAndQueries(t *testing.T) {
	mock, prov := newMySQLTest(t)
	applyTestCluster(t, mock, mysqlFree)
	mock.CRDs[mysqlClusterKey] = k8s.WithMySQLOffline(mock.CRDs[mysqlClusterKey], true)
	mock.CRDs[mysqlTestNS+"/moco-backup-proj-my1-mysql"] = backupCronJob(true)
	if err := prov.Resume(context.Background(), mysqlTestNS, mysqlTestProject); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if offline, _, _ := unstructured.NestedBool(mock.CRDs[mysqlClusterKey].Object, "spec", "offline"); offline {
		t.Error("the cluster is still offline")
	}
	if suspended, _, _ := unstructured.NestedBool(mock.CRDs[mysqlTestNS+"/moco-backup-proj-my1-mysql"].Object, "spec", "suspend"); suspended {
		t.Error("the backup CronJob is still suspended")
	}
	if len(execsMatching(mock, "SELECT 1")) == 0 {
		t.Error("resume did not prove the database answers")
	}
}

func TestMySQLDeprovision_RemovesClusterThenNamespace(t *testing.T) {
	mock, prov := newMySQLTest(t)
	prov.SetPollers(mysqlPoller(), mysqlPoller())
	if _, err := prov.ProvisionWithRollback(context.Background(), mysqlRequest(""), mysqlFree, recordingContext()); err != nil {
		t.Fatal(err)
	}
	if err := prov.Deprovision(context.Background(), mysqlTestNS, mysqlTestProject); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
	if _, ok := mock.CRDs[mysqlClusterKey]; ok {
		t.Error("the cluster is still there")
	}
	if mock.Namespaces[mysqlTestNS] {
		t.Error("the namespace is still there")
	}
	clusterGone := slices.Index(mock.Calls, "DeleteCRD:"+mysqlClusterKey)
	nsGone := slices.Index(mock.Calls, "DeleteNamespace:"+mysqlTestNS)
	if clusterGone < 0 || nsGone < 0 || clusterGone > nsGone {
		t.Errorf("the cluster must go before the namespace: %v", mock.Calls)
	}
}

// A minor upgrade re-pins the pod template and is done once every instance
// runs the new image and the database answers.
func TestMySQLUpgrade_RepinsImageAndWaitsForRollout(t *testing.T) {
	mock, prov := newMySQLTest(t)
	applyTestCluster(t, mock, mysqlFree)
	next, _ := config.MySQLImage("8.4.10")
	mock.Pods[mysqlTestNS] = []corev1.Pod{{
		ObjectMeta: metav1.ObjectMeta{Name: "moco-proj-my1-mysql-0", Namespace: mysqlTestNS},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "mysqld", Image: next}}},
	}}
	if err := prov.UpgradeMinor(context.Background(), mysqlTestNS, mysqlTestProject, next); err != nil {
		t.Fatalf("UpgradeMinor: %v", err)
	}
	if got := k8s.MySQLClusterImage(mock.CRDs[mysqlClusterKey]); got != next {
		t.Errorf("image %s", got)
	}
	old, _ := config.MySQLImage("8.4.9")
	mock.Pods[mysqlTestNS][0].Spec.Containers[0].Image = old
	if err := prov.UpgradeMinor(context.Background(), mysqlTestNS, mysqlTestProject, next); !errors.Is(err, ErrWaitTimeout) {
		t.Errorf("an instance still on the old image = %v, want a timeout", err)
	}
}

func TestMySQLApplyPlan_ScalesUpAndRefusesDown(t *testing.T) {
	mock, prov := newMySQLTest(t)
	applyTestCluster(t, mock, mysqlFree)
	standard := mysqlStandard
	standard.StorageSize = "5Gi"
	if err := prov.ApplyPlan(context.Background(), mysqlTestNS, mysqlTestProject, standard, "5Gi"); err != nil {
		t.Fatalf("1 → 3: %v", err)
	}
	if replicas, _, _ := unstructured.NestedInt64(mock.CRDs[mysqlClusterKey].Object, "spec", "replicas"); replicas != 3 {
		t.Errorf("replicas %d", replicas)
	}
	data, _, _ := unstructured.NestedStringMap(mock.CRDs[mysqlTestNS+"/"+k8s.MySQLConfigName(mysqlTestProject)].Object, "data")
	if _, small := data["innodb_buffer_pool_size"]; small {
		t.Error("the Standard plan kept the Free buffer pool")
	}
	if err := prov.ApplyPlan(context.Background(), mysqlTestNS, mysqlTestProject, mysqlFree, "5Gi"); !errors.Is(err, k8s.ErrMySQLScaleDown) {
		t.Errorf("3 → 1: %v", err)
	}
}

func TestMySQLGetStatus(t *testing.T) {
	mock, prov := newMySQLTest(t)
	applyTestCluster(t, mock, mysqlFree)
	status, err := prov.GetStatus(context.Background(), mysqlTestNS, mysqlTestProject)
	if err != nil || !status.Ready || status.Phase != "Running" {
		t.Errorf("status %+v %v", status, err)
	}
	mock.WildcardExecError = errors.New("down")
	status, _ = prov.GetStatus(context.Background(), mysqlTestNS, mysqlTestProject)
	if status.Ready {
		t.Error("a database that does not answer is not ready")
	}
}

// MOCO rolls a changed pod template one partition at a time; instances that
// answer on the old template are not the plan yet (EXC-583, k3d).
func TestMySQLApplyPlan_WaitsForTheRolloutToFinish(t *testing.T) {
	mock, prov := newMySQLTest(t)
	applyTestCluster(t, mock, mysqlFree)
	mock.AutoReconcileClusters = false
	cluster := mock.CRDs[mysqlClusterKey]
	_ = unstructured.SetNestedSlice(cluster.Object, []interface{}{
		map[string]interface{}{"type": "Available", "status": "True"},
		map[string]interface{}{"type": "Healthy", "status": "True"},
		map[string]interface{}{"type": "StatefulSetReady", "status": "False"},
	}, "status", "conditions")
	standard := mysqlStandard
	standard.StorageSize = "5Gi"
	if err := prov.ApplyPlan(context.Background(), mysqlTestNS, mysqlTestProject, standard, "5Gi"); err == nil {
		t.Fatal("the plan was reported applied while MOCO was still rolling it out")
	}
}

// Statements carrying passwords reach the primary on stdin only: exec
// arguments land in the API server's audit log.
func TestMySQLEnsurePlatformUsers_SendsPasswordsOnStdinToThePrimary(t *testing.T) {
	mock, prov := newMySQLTest(t)
	applyTestCluster(t, mock, mysqlFree)
	_ = unstructured.SetNestedField(mock.CRDs[mysqlClusterKey].Object, int64(0), "status", "currentPrimaryIndex")
	passwords := MySQLPasswords{Admin: "secret-admin", App: "secret-app", AuthAdmin: "secret-auth", Watcher: "secret-cdc"}
	if err := prov.EnsurePlatformUsers(context.Background(), mysqlTestNS, mysqlTestProject, "app", passwords); err != nil {
		t.Fatalf("EnsurePlatformUsers: %v", err)
	}
	if len(mock.ExecStdin) != 1 || !strings.Contains(mock.ExecStdin[0], "IDENTIFIED BY 'secret-admin'") {
		t.Fatalf("stdin %v", mock.ExecStdin)
	}
	for _, cmd := range mock.ExecCommands {
		if strings.Contains(cmd, "secret-") {
			t.Errorf("a password reached an exec argument: %s", cmd)
		}
	}
	if err := prov.SetUserPassword(context.Background(), mysqlTestNS, mysqlTestProject, MySQLUserApp, "rotated-secret"); err != nil {
		t.Fatal(err)
	}
	if last := mock.ExecStdin[len(mock.ExecStdin)-1]; !strings.Contains(last, "ALTER USER 'excalibase_app'@'%' IDENTIFIED BY 'rotated-secret'") {
		t.Errorf("stdin %s", last)
	}
}

func TestMySQLEnsurePlatformUsers_ErrorsNeverCarryThePassword(t *testing.T) {
	mock, prov := newMySQLTest(t)
	applyTestCluster(t, mock, mysqlFree)
	mock.WildcardExecError = errors.New("ERROR 1064 near 'IDENTIFIED BY 'secret-admin''")
	err := prov.EnsurePlatformUsers(context.Background(), mysqlTestNS, mysqlTestProject, "app",
		MySQLPasswords{Admin: "secret-admin", App: "a", AuthAdmin: "b", Watcher: "c"})
	if err == nil || strings.Contains(err.Error(), "secret-admin") {
		t.Errorf("err = %v", err)
	}
}
