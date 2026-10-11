package k8s

import (
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/excalibase/provisioning-poc/internal/config"
)

const testMySQLImage = "ghcr.io/cybozu-go/moco/mysql:8.4.10@sha256:6a8b7362a5b15dd5343c69f316adfc5d1798924470b1d38ce9cad85febb23120"

var (
	freePlan     = config.TierConfig{Instances: 1, StorageSize: "5Gi", CPU: "0.5", Memory: "512Mi", StatementTimeout: "15s"}
	standardPlan = config.TierConfig{Instances: 3, StorageSize: "50Gi", CPU: "2", Memory: "4Gi", StatementTimeout: "30s"}
)

func mysqlOpts(plan config.TierConfig) MySQLClusterOpts {
	return MySQLClusterOpts{ProjectID: "proj-abc", Namespace: "org-proj-abc", Tier: plan, StorageClass: "excalibase-tenant", Image: testMySQLImage}
}

func mysqldContainer(t *testing.T, cluster *unstructured.Unstructured) map[string]interface{} {
	t.Helper()
	containers, _, _ := unstructured.NestedSlice(cluster.Object, "spec", "podTemplate", "spec", "containers")
	for _, raw := range containers {
		c := raw.(map[string]interface{})
		if c["name"] == "mysqld" {
			return c
		}
	}
	t.Fatalf("no mysqld container in %v", containers)
	return nil
}

// ADR 0041 decision 1 and 4: Free is one instance without the slow-log
// sidecar, on MySQL's 1Gi floor; the cluster runs the catalogue's pinned image.
func TestBuildMySQLCluster_Free(t *testing.T) {
	cluster, err := BuildMySQLCluster(mysqlOpts(freePlan))
	if err != nil {
		t.Fatalf("BuildMySQLCluster: %v", err)
	}
	if cluster.GetName() != "proj-abc-mysql" || cluster.GetNamespace() != "org-proj-abc" {
		t.Errorf("name %s/%s", cluster.GetNamespace(), cluster.GetName())
	}
	if cluster.GetAPIVersion() != "moco.cybozu.com/v1beta2" || cluster.GetKind() != "MySQLCluster" {
		t.Errorf("kind %s %s", cluster.GetAPIVersion(), cluster.GetKind())
	}
	replicas, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "replicas")
	if replicas != 1 {
		t.Errorf("replicas %d", replicas)
	}
	off, _, _ := unstructured.NestedBool(cluster.Object, "spec", "disableSlowQueryLogContainer")
	if !off {
		t.Error("the slow-log sidecar must be off on a small plan")
	}
	mysqld := mysqldContainer(t, cluster)
	if mysqld["image"] != testMySQLImage {
		t.Errorf("image %v", mysqld["image"])
	}
	for _, bound := range []string{"requests", "limits"} {
		got, _, _ := unstructured.NestedStringMap(mysqld, "resources", bound)
		if got["cpu"] != "0.5" || got["memory"] != "1Gi" {
			t.Errorf("%s %v", bound, got)
		}
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(cluster.Object, "spec", "podTemplate", "spec", "affinity"); found {
		t.Error("a single instance needs no anti-affinity")
	}
	configName, _, _ := unstructured.NestedString(cluster.Object, "spec", "mysqlConfigMapName")
	if configName != MySQLConfigName("proj-abc") {
		t.Errorf("config map %q", configName)
	}
	claims, _, _ := unstructured.NestedSlice(cluster.Object, "spec", "volumeClaimTemplates")
	if len(claims) != 1 {
		t.Fatalf("claims %v", claims)
	}
	claim := claims[0].(map[string]interface{})
	name, _, _ := unstructured.NestedString(claim, "metadata", "name")
	size, _, _ := unstructured.NestedString(claim, "spec", "resources", "requests", "storage")
	class, _, _ := unstructured.NestedString(claim, "spec", "storageClassName")
	if name != "mysql-data" || size != "5Gi" || class != "excalibase-tenant" {
		t.Errorf("claim %s %s %s", name, size, class)
	}
}

// Standard: three instances, one per node, slow log kept.
func TestBuildMySQLCluster_Standard(t *testing.T) {
	cluster, err := BuildMySQLCluster(mysqlOpts(standardPlan))
	if err != nil {
		t.Fatalf("BuildMySQLCluster: %v", err)
	}
	replicas, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "replicas")
	if replicas != 3 {
		t.Errorf("replicas %d", replicas)
	}
	if off, _, _ := unstructured.NestedBool(cluster.Object, "spec", "disableSlowQueryLogContainer"); off {
		t.Error("Standard keeps the slow-log sidecar")
	}
	terms, _, _ := unstructured.NestedSlice(cluster.Object, "spec", "podTemplate", "spec", "affinity",
		"podAntiAffinity", "requiredDuringSchedulingIgnoredDuringExecution")
	if len(terms) != 1 {
		t.Fatalf("anti-affinity %v", terms)
	}
	term := terms[0].(map[string]interface{})
	labels, _, _ := unstructured.NestedStringMap(term, "labelSelector", "matchLabels")
	if term["topologyKey"] != "kubernetes.io/hostname" || labels["app.kubernetes.io/instance"] != "proj-abc-mysql" {
		t.Errorf("term %v", term)
	}
}

func TestBuildMySQLCluster_RefusesIncompleteInput(t *testing.T) {
	opts := mysqlOpts(freePlan)
	opts.Image = ""
	if _, err := BuildMySQLCluster(opts); err == nil {
		t.Error("an empty image was accepted")
	}
	opts = mysqlOpts(config.TierConfig{Instances: 2, StorageSize: "5Gi", CPU: "1", Memory: "1Gi"})
	if _, err := BuildMySQLCluster(opts); err == nil {
		t.Error("MOCO runs an odd number of instances; 2 was accepted")
	}
	opts = mysqlOpts(config.TierConfig{Instances: 1})
	if _, err := BuildMySQLCluster(opts); !errors.Is(err, ErrTierSizingIncomplete) {
		t.Errorf("incomplete plan: %v", err)
	}
}

// Free's buffer pool is 128MiB (decision 4); larger plans take MOCO's 70 %.
func TestBuildMySQLConfig(t *testing.T) {
	free, err := BuildMySQLConfig(mysqlOpts(freePlan))
	if err != nil {
		t.Fatal(err)
	}
	data, _, _ := unstructured.NestedStringMap(free.Object, "data")
	if free.GetName() != MySQLConfigName("proj-abc") || free.GetKind() != "ConfigMap" {
		t.Errorf("config %s %s", free.GetKind(), free.GetName())
	}
	want := map[string]string{
		"innodb_buffer_pool_size": "134217728", "max_connections": "100", "max_execution_time": "15000",
		"innodb_log_buffer_size": "16777216", "temptable_max_ram": "67108864", "table_open_cache": "1000",
	}
	for k, v := range want {
		if data[k] != v {
			t.Errorf("free %s=%q, want %q", k, data[k], v)
		}
	}
	standard, _ := BuildMySQLConfig(mysqlOpts(standardPlan))
	data, _, _ = unstructured.NestedStringMap(standard.Object, "data")
	if _, set := data["innodb_buffer_pool_size"]; set {
		t.Error("a large plan must leave the buffer pool to MOCO's ratio")
	}
	if data["max_execution_time"] != "30000" {
		t.Errorf("standard max_execution_time %q", data["max_execution_time"])
	}
	if data["temptable_max_ram"] != "268435456" {
		t.Errorf("standard temptable_max_ram %q: in-memory temp tables are capped at a sixteenth of memory", data["temptable_max_ram"])
	}
	if _, set := data["innodb_log_buffer_size"]; set {
		t.Error("a large plan keeps the default log buffer")
	}
}

func TestMySQLClusterStorageBytes(t *testing.T) {
	cluster, _ := BuildMySQLCluster(mysqlOpts(standardPlan))
	got, err := MySQLClusterStorageBytes(cluster)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(3 * 50 << 30); got != want {
		t.Errorf("bytes %d, want %d", got, want)
	}
}

// 1 → 3 is a plan change; 3 → 1 is refused (MOCO's webhook refuses it too;
// decision 2: a downgrade is a restore into a new Free project).
func TestWithMySQLTier(t *testing.T) {
	free, _ := BuildMySQLCluster(mysqlOpts(freePlan))
	up, err := WithMySQLTier(free, standardPlan, "50Gi")
	if err != nil {
		t.Fatalf("1 → 3: %v", err)
	}
	replicas, _, _ := unstructured.NestedInt64(up.Object, "spec", "replicas")
	if replicas != 3 {
		t.Errorf("replicas %d", replicas)
	}
	if mysqldContainer(t, up)["image"] != testMySQLImage {
		t.Error("a plan change must keep the image")
	}
	if off, _, _ := unstructured.NestedBool(up.Object, "spec", "disableSlowQueryLogContainer"); off {
		t.Error("Standard keeps the slow-log sidecar")
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(up.Object, "spec", "podTemplate", "spec", "affinity"); !found {
		t.Error("three instances need one node each")
	}
	if got, _ := MySQLClusterStorageBytes(up); got != 3*50<<30 {
		t.Errorf("disk %d", got)
	}
	if replicas, _, _ := unstructured.NestedInt64(free.Object, "spec", "replicas"); replicas != 1 {
		t.Error("WithMySQLTier changed its input")
	}
	if _, err := WithMySQLTier(up, freePlan, "5Gi"); !errors.Is(err, ErrMySQLScaleDown) {
		t.Errorf("3 → 1: %v", err)
	}
}

func TestWithMySQLImageAndOffline(t *testing.T) {
	cluster, _ := BuildMySQLCluster(mysqlOpts(freePlan))
	next := "ghcr.io/cybozu-go/moco/mysql:8.4.11@sha256:1111111111111111111111111111111111111111111111111111111111111111"
	upgraded, err := WithMySQLImage(cluster, next)
	if err != nil {
		t.Fatal(err)
	}
	if mysqldContainer(t, upgraded)["image"] != next || mysqldContainer(t, cluster)["image"] != testMySQLImage {
		t.Error("image not replaced on a copy")
	}
	paused := WithMySQLOffline(cluster, true)
	if offline, _, _ := unstructured.NestedBool(paused.Object, "spec", "offline"); !offline {
		t.Error("offline not set")
	}
	if offline, _, _ := unstructured.NestedBool(cluster.Object, "spec", "offline"); offline {
		t.Error("WithMySQLOffline changed its input")
	}
	// MOCO 0.34-0.37 crash-loops its controller on an offline cluster whose
	// clustering still runs (cybozu-go/moco#946): every MySQL project would go
	// down while one is paused. Clustering stops with the pause.
	if paused.GetAnnotations()["moco.cybozu.com/clustering-stopped"] != "true" {
		t.Errorf("a paused cluster keeps clustering: %v", paused.GetAnnotations())
	}
	resumed := WithMySQLOffline(paused, false)
	if offline, _, _ := unstructured.NestedBool(resumed.Object, "spec", "offline"); offline {
		t.Error("offline not cleared")
	}
	if _, stopped := resumed.GetAnnotations()["moco.cybozu.com/clustering-stopped"]; stopped {
		t.Error("a resumed cluster must cluster again")
	}
}

func TestMySQLClusterReadiness(t *testing.T) {
	cluster, _ := BuildMySQLCluster(mysqlOpts(freePlan))
	if MySQLClusterAvailable(cluster) {
		t.Error("a cluster with no status is not available")
	}
	_ = unstructured.SetNestedSlice(cluster.Object, []interface{}{
		map[string]interface{}{"type": "Available", "status": "True"},
		map[string]interface{}{"type": "Healthy", "status": "False"},
	}, "status", "conditions")
	_ = unstructured.SetNestedField(cluster.Object, int64(2), "status", "currentPrimaryIndex")
	if !MySQLClusterAvailable(cluster) || MySQLClusterHealthy(cluster) {
		t.Error("conditions misread")
	}
	if MySQLClusterRolledOut(cluster) {
		t.Error("no StatefulSetReady is not rolled out")
	}
	conditions, _, _ := unstructured.NestedSlice(cluster.Object, "status", "conditions")
	conditions = append(conditions, map[string]interface{}{"type": "StatefulSetReady", "status": "True"})
	_ = unstructured.SetNestedSlice(cluster.Object, conditions, "status", "conditions")
	if !MySQLClusterRolledOut(cluster) {
		t.Error("StatefulSetReady misread")
	}
	if got := MySQLPrimaryPod(cluster); got != "moco-proj-abc-mysql-2" {
		t.Errorf("primary %s", got)
	}
}

func TestMySQLServiceNames(t *testing.T) {
	if got := MySQLPrimaryHost("proj-abc", "org-proj-abc"); got != "moco-proj-abc-mysql-primary.org-proj-abc.svc.cluster.local" {
		t.Errorf("primary %s", got)
	}
	if got := MySQLReplicaHost("proj-abc", "org-proj-abc"); got != "moco-proj-abc-mysql-replica.org-proj-abc.svc.cluster.local" {
		t.Errorf("replica %s", got)
	}
}

func TestMySQLSettingsAreTheConfigMapsData(t *testing.T) {
	for _, plan := range []config.TierConfig{freePlan, standardPlan} {
		settings, err := MySQLSettings(plan)
		if err != nil {
			t.Fatal(err)
		}
		cm, _ := BuildMySQLConfig(mysqlOpts(plan))
		data, _, _ := unstructured.NestedStringMap(cm.Object, "data")
		if len(settings) != len(data) {
			t.Fatalf("settings %v, config map %v", settings, data)
		}
		for k, v := range data {
			if settings[k] != v {
				t.Errorf("%s=%q, config map has %q", k, settings[k], v)
			}
		}
	}
}
