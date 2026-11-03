package k8s

import (
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func storeFixture(t *testing.T) *unstructured.Unstructured {
	t.Helper()
	store, err := BuildBackupObjectStore("proj", "org-proj", ObjectStoreOpts{Bucket: "bkt", SecretName: BackupCredentialsSecretName, EndpointURL: "https://s3.example"}, 7)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestArchiveObjectStoreName(t *testing.T) {
	if got := ArchiveObjectStoreName("proj", ""); got != "proj-backups" {
		t.Fatalf("original archive store = %q", got)
	}
	if got := ArchiveObjectStoreName("proj", "pg17"); got != "proj-backups-pg17" {
		t.Fatalf("pg17 store = %q", got)
	}
}

func TestArchiveServerName(t *testing.T) {
	if ArchiveServerName("") != "cloud" || ArchiveServerName("pg17") != "pg17" {
		t.Fatalf("server names: %q %q", ArchiveServerName(""), ArchiveServerName("pg17"))
	}
}

// Every archive stays under the prefix the scoped credentials, the purge and
// deletion already cover.
func TestArchiveKeysStayUnderTheProjectPrefix(t *testing.T) {
	for _, archive := range []string{"", "pg16", "pg17"} {
		destination := ArchiveDestinationPath("bkt", "proj", archive)
		key := destination[len("s3://bkt/"):] + "/" + ArchiveServerName(archive) + "/"
		if len(key) < len(BarmanObjectPrefix("proj")) || key[:len(BarmanObjectPrefix("proj"))] != BarmanObjectPrefix("proj") {
			t.Errorf("archive %q keys %q are outside %q", archive, key, BarmanObjectPrefix("proj"))
		}
	}
	if ArchiveDestinationPath("bkt", "proj", "") != "s3://bkt/proj" {
		t.Fatal("the original archive moved")
	}
}

func TestBuildArchiveObjectStoreCopiesTheStoreAtTheNewPath(t *testing.T) {
	current := storeFixture(t)
	next, err := BuildArchiveObjectStore(current, "proj", "pg17")
	if err != nil {
		t.Fatal(err)
	}
	if next.GetName() != "proj-backups-pg17" || next.GetNamespace() != "org-proj" {
		t.Fatalf("store = %s/%s", next.GetNamespace(), next.GetName())
	}
	path, _, _ := unstructured.NestedString(next.Object, "spec", "configuration", "destinationPath")
	if path != "s3://bkt/proj/cloud" {
		t.Fatalf("destinationPath = %q", path)
	}
	retention, _, _ := unstructured.NestedString(next.Object, "spec", "retentionPolicy")
	secret, _, _ := unstructured.NestedString(next.Object, "spec", "configuration", "s3Credentials", "accessKeyId", "name")
	endpoint, _, _ := unstructured.NestedString(next.Object, "spec", "configuration", "endpointURL")
	if retention != "7d" || secret != BackupCredentialsSecretName || endpoint != "https://s3.example" {
		t.Fatalf("copied spec lost settings: retention %q secret %q endpoint %q", retention, secret, endpoint)
	}
	original, _, _ := unstructured.NestedString(current.Object, "spec", "configuration", "destinationPath")
	if original != "s3://bkt/proj" {
		t.Fatalf("the source store was modified: %q", original)
	}
}

func TestBuildArchiveObjectStoreRefusesAStoreItCannotRead(t *testing.T) {
	broken := &unstructured.Unstructured{Object: map[string]interface{}{"metadata": map[string]interface{}{"name": "x", "namespace": "ns"}}}
	if _, err := BuildArchiveObjectStore(broken, "proj", "pg17"); err == nil {
		t.Fatal("a store without a destination was copied")
	}
	odd := storeFixture(t)
	_ = unstructured.SetNestedField(odd.Object, "gs://elsewhere/proj", "spec", "configuration", "destinationPath")
	if _, err := BuildArchiveObjectStore(odd, "proj", "pg17"); err == nil {
		t.Fatal("a store outside s3 was copied")
	}
}

func clusterFixture() *unstructured.Unstructured {
	cluster := BuildPostgreSQLCluster(PostgreSQLClusterOpts{
		ProjectID: "proj", Namespace: "org-proj", ImageName: "excalibase/postgresql:16@sha256:aa",
		Backup: &BackupOpts{Schedule: "0 0 * * *", RetentionDays: 7, Bucket: "bkt", SecretName: BackupCredentialsSecretName},
	})
	return cluster
}

func TestSetClusterArchiveChangesImageAndArchiverTogether(t *testing.T) {
	cluster := clusterFixture()
	if err := SetClusterArchive(cluster, "excalibase/postgresql:17@sha256:bb", "proj-backups-pg17", "pg17"); err != nil {
		t.Fatal(err)
	}
	image, _, _ := unstructured.NestedString(cluster.Object, "spec", "imageName")
	store, server, ok := ClusterArchive(cluster)
	if image != "excalibase/postgresql:17@sha256:bb" || !ok || store != "proj-backups-pg17" || server != "pg17" {
		t.Fatalf("image %q store %q server %q", image, store, server)
	}
}

func TestSetClusterArchiveRefusesAClusterThatArchivesNowhere(t *testing.T) {
	cluster := BuildPostgreSQLCluster(PostgreSQLClusterOpts{ProjectID: "proj", Namespace: "org-proj", ImageName: "img:16@sha256:aa"})
	if err := SetClusterArchive(cluster, "img:17@sha256:bb", "s", "pg17"); !errors.Is(err, ErrClusterHasNoArchive) {
		t.Fatalf("err = %v", err)
	}
}

func TestClusterDataMajorAndHealth(t *testing.T) {
	cluster := clusterFixture()
	if _, ok := ClusterDataMajor(cluster); ok {
		t.Fatal("a cluster with no status reported a data major")
	}
	_ = unstructured.SetNestedField(cluster.Object, int64(17), "status", "pgDataImageInfo", "majorVersion")
	_ = unstructured.SetNestedField(cluster.Object, "Cluster in healthy state", "status", "phase")
	_ = unstructured.SetNestedField(cluster.Object, int64(3), "status", "instances")
	_ = unstructured.SetNestedField(cluster.Object, int64(2), "status", "readyInstances")
	if major, ok := ClusterDataMajor(cluster); !ok || major != 17 {
		t.Fatalf("major = %d %v", major, ok)
	}
	if ClusterFullyServing(cluster) {
		t.Fatal("2 of 3 ready counted as serving")
	}
	if !ClusterPrimaryServing(cluster) {
		t.Fatal("a healthy phase with ready instances is a serving primary")
	}
	_ = unstructured.SetNestedField(cluster.Object, int64(3), "status", "readyInstances")
	if !ClusterFullyServing(cluster) {
		t.Fatal("3 of 3 ready in a healthy phase is serving")
	}
}

func TestMajorUpgradeJobState(t *testing.T) {
	job := func(cluster string, conditions ...map[string]interface{}) *unstructured.Unstructured {
		items := []interface{}{}
		for _, c := range conditions {
			items = append(items, c)
		}
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"kind": "Job",
			"metadata": map[string]interface{}{
				"name":   cluster + "-1-major-upgrade",
				"labels": map[string]interface{}{"cnpg.io/jobRole": "major-upgrade", "cnpg.io/cluster": cluster},
			},
			"status": map[string]interface{}{"conditions": items},
		}}
	}
	other := &unstructured.Unstructured{Object: map[string]interface{}{"kind": "Job", "metadata": map[string]interface{}{
		"name": "x", "labels": map[string]interface{}{"cnpg.io/jobRole": "initdb", "cnpg.io/cluster": "proj-postgres"}}}}
	failed := map[string]interface{}{"type": "Failed", "status": "True"}

	if state := MajorUpgradeJobState([]*unstructured.Unstructured{other}, "proj-postgres", time.Time{}); state != UpgradeJobAbsent {
		t.Fatalf("state = %v", state)
	}
	if state := MajorUpgradeJobState([]*unstructured.Unstructured{job("proj-postgres")}, "proj-postgres", time.Time{}); state != UpgradeJobRunning {
		t.Fatalf("state = %v", state)
	}
	if state := MajorUpgradeJobState([]*unstructured.Unstructured{job("proj-postgres", failed)}, "proj-postgres", time.Time{}); state != UpgradeJobFailed {
		t.Fatalf("state = %v", state)
	}
	if state := MajorUpgradeJobState([]*unstructured.Unstructured{job("other-postgres", failed)}, "proj-postgres", time.Time{}); state != UpgradeJobAbsent {
		t.Fatalf("another cluster's job counted: %v", state)
	}
}

func TestRecoverySourceReadsTheArchiveItIsGiven(t *testing.T) {
	opts := RestoreClusterOpts{
		Cluster: PostgreSQLClusterOpts{ProjectID: "copy", Namespace: "org-copy", ImageName: "img:16@sha256:aa",
			Tier: config.TierConfig{Instances: 1, StorageSize: "1Gi", CPU: "1", Memory: "1Gi"}},
		SourceProjectID: "proj",
		SourceArchive:   "pg16",
		Store:           ObjectStoreOpts{Bucket: "bkt", SecretName: RecoverySourceCredentialsSecretName},
	}
	store, err := BuildRecoverySourceObjectStore(opts)
	if err != nil {
		t.Fatal(err)
	}
	path, _, _ := unstructured.NestedString(store.Object, "spec", "configuration", "destinationPath")
	if path != "s3://bkt/proj/cloud" {
		t.Fatalf("destinationPath = %q", path)
	}
	cluster, err := BuildRestoreCluster(opts)
	if err != nil {
		t.Fatal(err)
	}
	external, _, _ := unstructured.NestedSlice(cluster.Object, "spec", "externalClusters")
	server, _, _ := unstructured.NestedString(external[0].(map[string]interface{}), "plugin", "parameters", "serverName")
	if server != "pg16" {
		t.Fatalf("serverName = %q", server)
	}

	opts.SourceArchive = ""
	store, _ = BuildRecoverySourceObjectStore(opts)
	path, _, _ = unstructured.NestedString(store.Object, "spec", "configuration", "destinationPath")
	cluster, _ = BuildRestoreCluster(opts)
	external, _, _ = unstructured.NestedSlice(cluster.Object, "spec", "externalClusters")
	server, _, _ = unstructured.NestedString(external[0].(map[string]interface{}), "plugin", "parameters", "serverName")
	if path != "s3://bkt/proj" || server != "cloud" {
		t.Fatalf("original archive: path %q server %q", path, server)
	}
}

func TestMajorUpgradeJobStateIgnoresAnEarlierAttempt(t *testing.T) {
	started := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	old := &unstructured.Unstructured{Object: map[string]interface{}{"metadata": map[string]interface{}{
		"name": "old", "labels": map[string]interface{}{"cnpg.io/jobRole": "major-upgrade", "cnpg.io/cluster": "proj-postgres"}}}}
	old.SetCreationTimestamp(metav1.NewTime(started.Add(-time.Hour)))
	_ = unstructured.SetNestedField(old.Object, int64(1), "status", "failed")
	if state := MajorUpgradeJobState([]*unstructured.Unstructured{old}, "proj-postgres", started); state != UpgradeJobAbsent {
		t.Fatalf("an earlier failed attempt counted: %v", state)
	}
}
