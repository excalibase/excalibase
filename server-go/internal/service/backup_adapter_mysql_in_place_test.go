package service

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

var sourceClusterKey = mysqlSourceNS + "/" + k8s.MySQLClusterName(mysqlSourceProject)

// mocoPoller stands in for MOCO and the Job controller: each tick finishes
// every manual backup Job with outcome and, when it succeeds, puts its run
// in the bucket at 13:00.
func (lab *mysqlAdapterLab) mocoPoller(outcome string) provisioner.Poller {
	now := time.Unix(0, 0)
	return provisioner.Poller{
		Interval: time.Second, Timeout: time.Minute,
		Now: func() time.Time { return now },
		After: func(d time.Duration) <-chan time.Time {
			now = now.Add(d)
			lab.finishManualJobs(outcome)
			tick := make(chan time.Time, 1)
			tick <- now
			return tick
		},
	}
}

func (lab *mysqlAdapterLab) finishManualJobs(outcome string) {
	for key, obj := range lab.mock.CRDs {
		if obj.GetKind() != "Job" || !strings.Contains(key, "-manual-") {
			continue
		}
		if outcome == k8s.MySQLJobFailed {
			_ = unstructured.SetNestedSlice(obj.Object, []interface{}{
				map[string]interface{}{"type": "Failed", "status": "True", "message": "dump failed"}}, "status", "conditions")
			continue
		}
		_ = unstructured.SetNestedField(obj.Object, int64(1), "status", "succeeded")
		_ = unstructured.SetNestedField(obj.Object, "2026-10-11T12:59:30Z", "status", "startTime")
		_ = unstructured.SetNestedField(obj.Object, "2026-10-11T13:02:00Z", "status", "completionTime")
		run := k8s.MOCOBackupPrefix(mysqlSourceNS, mysqlSourceProject) + "20261011-130000/dump.tar"
		if !containsKey(lab.objects.keys, run) {
			lab.objects.keys = append(lab.objects.keys, run)
			sort.Strings(lab.objects.keys)
		}
	}
}

func containsKey(keys []string, key string) bool {
	for _, k := range keys {
		if k == key {
			return true
		}
	}
	return false
}

// The running database is backed up first and the run that backup made is
// what a failed replace rolls back to.
func TestMySQLSafetyBackupReturnsTheRunItMade(t *testing.T) {
	lab := newMySQLAdapterLab(t, sourceKeys()...)
	lab.cronJob()
	lab.adapter.SetReadyPoller(lab.mocoPoller(k8s.MySQLJobSucceeded))
	id, err := lab.adapter.TakeSafetyBackup(context.Background(), lab.source)
	if err != nil {
		t.Fatalf("TakeSafetyBackup: %v", err)
	}
	if id != "20261011-130000" {
		t.Errorf("safety backup %q", id)
	}
	target, err := lab.adapter.InPlaceRecoveryTarget(context.Background(), lab.source, domain.RestoreRequest{BackupID: id})
	if err != nil || target["restorePoint"] != "2026-10-11T13:00:00Z" {
		t.Errorf("rollback target %v %v", target, err)
	}
}

func TestMySQLSafetyBackupThatFailsChangesNothing(t *testing.T) {
	lab := newMySQLAdapterLab(t, sourceKeys()...)
	lab.cronJob()
	lab.adapter.SetReadyPoller(lab.mocoPoller(k8s.MySQLJobFailed))
	if _, err := lab.adapter.TakeSafetyBackup(context.Background(), lab.source); err == nil {
		t.Fatal("a failed safety backup was reported taken")
	}
}

// liveSourceCluster is the project's running cluster, linked to its policy.
func (lab *mysqlAdapterLab) liveSourceCluster(t *testing.T) {
	t.Helper()
	image, _ := config.MySQLImage("8.4.9")
	cluster, err := k8s.BuildMySQLCluster(k8s.MySQLClusterOpts{
		ProjectID: mysqlSourceProject, Namespace: mysqlSourceNS, Image: image, StorageClass: "fast",
		Tier: config.TierConfig{Instances: 1, StorageSize: "5Gi", CPU: "0.5", Memory: "1Gi"},
	})
	if err != nil {
		t.Fatal(err)
	}
	lab.mock.CRDs[sourceClusterKey] = k8s.WithMySQLBackupPolicy(cluster, mysqlSourceProject)
	policy, err := k8s.BuildMySQLBackupPolicy(k8s.MySQLBackupOpts{
		ProjectID: mysqlSourceProject, Namespace: mysqlSourceNS, Schedule: "0 */6 * * *",
		Plan: config.TierConfig{Instances: 1, CPU: "0.5", Memory: "1Gi"}, Disk: "5Gi",
		Store: k8s.MySQLBackupStore{Bucket: "backups", Endpoint: testR2Endpoint},
	})
	if err != nil {
		t.Fatal(err)
	}
	lab.mock.CRDs[mysqlSourceNS+"/"+policy.GetName()] = policy
}

// ADR 0041 decision 6: in place = the same-named cluster rebuilt by MOCO from
// the project's own prefix at the point, then backed up again by its policy.
func TestMySQLReplaceDatabaseRebuildsTheClusterFromItsOwnBackups(t *testing.T) {
	lab := newMySQLAdapterLab(t, sourceKeys()...)
	lab.liveSourceCluster(t)
	target, err := lab.adapter.InPlaceRecoveryTarget(context.Background(), lab.source, domain.RestoreRequest{TargetTime: zoned(at(7, 30))})
	if err != nil {
		t.Fatalf("InPlaceRecoveryTarget: %v", err)
	}
	if err := lab.adapter.ReplaceDatabase(context.Background(), lab.source, target); err != nil {
		t.Fatalf("ReplaceDatabase: %v", err)
	}
	if !slicesContain(lab.mock.Calls, "DeleteCRD:"+sourceClusterKey) {
		t.Error("the old cluster was not deleted")
	}
	cluster := lab.mock.CRDs[sourceClusterKey]
	restore, _, _ := unstructured.NestedMap(cluster.Object, "spec", "restore")
	if restore["sourceName"] != "proj1-mysql" || restore["sourceNamespace"] != mysqlSourceNS || restore["restorePoint"] != "2026-10-11T07:30:00Z" {
		t.Errorf("restore %v", restore)
	}
	envFrom, _, _ := unstructured.NestedSlice(restore, "jobConfig", "envFrom")
	if envFrom[0].(map[string]interface{})["secretRef"].(map[string]interface{})["name"] != k8s.MySQLBackupCredentialsSecret {
		t.Errorf("the restore reads with %v", envFrom)
	}
	if name, _, _ := unstructured.NestedString(cluster.Object, "spec", "backupPolicyName"); name != k8s.MySQLBackupPolicyName(mysqlSourceProject) {
		t.Errorf("backups not re-attached: %q", name)
	}
	if replicas, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "replicas"); replicas != 1 {
		t.Errorf("the replacement runs %d instances", replicas)
	}
	if cluster.GetResourceVersion() != "" || cluster.GetUID() != "" {
		t.Error("the replacement carries the old object's identity")
	}
	if _, ok := lab.mock.Secrets[mysqlSourceNS+"/"+inPlaceTemplateSecret(mysqlSourceProject)]; ok {
		t.Error("the saved definition was left behind")
	}
}

// A replace interrupted after the delete rebuilds from the saved definition.
func TestMySQLReplaceDatabaseResumesFromTheSavedDefinition(t *testing.T) {
	lab := newMySQLAdapterLab(t, sourceKeys()...)
	lab.liveSourceCluster(t)
	if _, err := lab.adapter.clusterTemplate(context.Background(), lab.source); err != nil {
		t.Fatalf("save: %v", err)
	}
	delete(lab.mock.CRDs, sourceClusterKey)
	target := map[string]interface{}{"restorePoint": "2026-10-11T06:00:00Z"}
	if err := lab.adapter.ReplaceDatabase(context.Background(), lab.source, target); err != nil {
		t.Fatalf("ReplaceDatabase: %v", err)
	}
	if _, ok := lab.mock.CRDs[sourceClusterKey]; !ok {
		t.Error("the cluster was not rebuilt")
	}
}

// The replacement takes the same names, so it waits for every volume to go.
func TestMySQLReplaceDatabaseWaitsForTheOldVolumes(t *testing.T) {
	lab := newMySQLAdapterLab(t, sourceKeys()...)
	lab.liveSourceCluster(t)
	lab.mock.PVCs[mysqlSourceNS] = []string{"mysql-data-moco-proj1-mysql-0", "other-claim"}
	err := lab.adapter.ReplaceDatabase(context.Background(), lab.source, map[string]interface{}{"restorePoint": "2026-10-11T06:00:00Z"})
	if err == nil || !strings.Contains(err.Error(), "mysql-data-moco-proj1-mysql-0") {
		t.Fatalf("err %v, want the wait to name the volume", err)
	}
}

func slicesContain(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
