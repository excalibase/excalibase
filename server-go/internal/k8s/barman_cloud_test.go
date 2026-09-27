package k8s

import (
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

func projectStore() ObjectStoreOpts {
	return ObjectStoreOpts{EndpointURL: testR2Endpoint, Bucket: "excalibase-backups", SecretName: "backup-s3-creds"}
}

func backedUpCluster() PostgreSQLClusterOpts {
	return PostgreSQLClusterOpts{
		ProjectID: "proj-bkp",
		Namespace: "org-proj-bkp",
		Tier:      config.TierConfig{Instances: 1, StorageSize: "5Gi", Memory: "512Mi", CPU: "0.5"},
		Backup:    &BackupOpts{Schedule: testCronSchedule, RetentionDays: 7, EndpointURL: testR2Endpoint, Bucket: "excalibase-backups", SecretName: "backup-s3-creds"},
	}
}

func pluginsOf(t *testing.T, obj *unstructured.Unstructured) []map[string]interface{} {
	t.Helper()
	raw, _, _ := unstructured.NestedSlice(obj.Object, "spec", "plugins")
	plugins := make([]map[string]interface{}, 0, len(raw))
	for _, entry := range raw {
		plugins = append(plugins, entry.(map[string]interface{}))
	}
	return plugins
}

func barmanPlugin(t *testing.T, obj *unstructured.Unstructured) map[string]interface{} {
	t.Helper()
	var found map[string]interface{}
	for _, plugin := range pluginsOf(t, obj) {
		if plugin["name"] == BarmanCloudPluginName {
			if found != nil {
				t.Fatal("the Barman Cloud plugin is named twice")
			}
			found = plugin
		}
	}
	return found
}

func mentionsInTreeBarman(obj *unstructured.Unstructured) bool {
	encoded, _ := yaml.Marshal(obj.Object)
	return strings.Contains(string(encoded), "barmanObjectStore")
}

func TestClusterWithBackupsArchivesThroughThePlugin(t *testing.T) {
	obj := BuildPostgreSQLCluster(backedUpCluster())

	plugin := barmanPlugin(t, obj)
	if plugin == nil {
		t.Fatal("a cluster with backups must name the Barman Cloud plugin")
	}
	if plugin["isWALArchiver"] != true || plugin["enabled"] != true {
		t.Errorf("the plugin must be the enabled WAL archiver, got %v", plugin)
	}
	parameters := plugin["parameters"].(map[string]interface{})
	if parameters["barmanObjectName"] != BackupObjectStoreName("proj-bkp") {
		t.Errorf("barmanObjectName: got %v", parameters["barmanObjectName"])
	}
	if parameters["serverName"] != "cloud" {
		t.Errorf("serverName must keep the object layout the purge deletes, got %v", parameters["serverName"])
	}
	if _, inTree := obj.Object["spec"].(map[string]interface{})["backup"]; inTree || mentionsInTreeBarman(obj) {
		t.Error("the in-tree barmanObjectStore needs barman-cloud in the Postgres image and must not be rendered")
	}
}

func TestClusterWithoutBackupsNamesNoBackupPlugin(t *testing.T) {
	opts := backedUpCluster()
	opts.Backup = nil
	if plugin := barmanPlugin(t, BuildPostgreSQLCluster(opts)); plugin != nil {
		t.Errorf("a cluster without backups must not archive anywhere, got %v", plugin)
	}
}

func TestDocumentDBClusterWithBackupsNamesBothPlugins(t *testing.T) {
	opts := backedUpCluster()
	opts.DocumentDB = true
	opts.DocumentDBGatewayImage = "gateway@sha256:" + strings.Repeat("0", 64)
	names := []string{}
	for _, plugin := range pluginsOf(t, BuildPostgreSQLCluster(opts)) {
		names = append(names, plugin["name"].(string))
	}
	if len(names) != 2 || !strings.Contains(strings.Join(names, ","), BarmanCloudPluginName) ||
		!strings.Contains(strings.Join(names, ","), config.DocumentDBPluginName) {
		t.Errorf("both plugins must be named, got %v", names)
	}
}

func TestBackupObjectStoreGolden(t *testing.T) {
	store, err := BuildBackupObjectStore("proj-bkp", "org-proj-bkp", projectStore(), 7)
	if err != nil {
		t.Fatalf("BuildBackupObjectStore: %v", err)
	}
	encoded, err := yaml.Marshal(store.Object)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	assertGoldenAt(t, "testdata/barman_cloud/backup-object-store.yaml", string(encoded))
}

// A tier's statement timeout is set cluster-wide; barman-cloud-backup's
// pg_backup_start waits for a checkpoint far longer than any tier allows.
func TestBackupSessionsAreExemptFromTheTierQueryGuard(t *testing.T) {
	store, err := BuildBackupObjectStore("proj-bkp", "org-proj-bkp", projectStore(), 7)
	if err != nil {
		t.Fatalf("BuildBackupObjectStore: %v", err)
	}
	env, _, _ := unstructured.NestedSlice(store.Object, "spec", "instanceSidecarConfiguration", "env")
	if len(env) != 1 {
		t.Fatalf("sidecar env: got %v", env)
	}
	entry := env[0].(map[string]interface{})
	if entry["name"] != "PGOPTIONS" || entry["value"] != "-c statement_timeout=0 -c idle_in_transaction_session_timeout=0" {
		t.Errorf("the backup's own sessions must run without the tier's timeouts, got %v", entry)
	}
}

func TestBackupObjectStoreLeavesServerNameToTheCluster(t *testing.T) {
	store, err := BuildBackupObjectStore("proj-bkp", "org-proj-bkp", projectStore(), 7)
	if err != nil {
		t.Fatalf("BuildBackupObjectStore: %v", err)
	}
	if _, set, _ := unstructured.NestedString(store.Object, "spec", "configuration", "serverName"); set {
		t.Error("the plugin requires the ObjectStore's serverName to be empty")
	}
}

func TestBackupObjectStoreIsWherePurgeDeletes(t *testing.T) {
	store, err := BuildBackupObjectStore("proj-abc", "ns", ObjectStoreOpts{Bucket: "excalibase-backups", SecretName: "s"}, 7)
	if err != nil {
		t.Fatalf("BuildBackupObjectStore: %v", err)
	}
	destination, _, _ := unstructured.NestedString(store.Object, "spec", "configuration", "destinationPath")
	serverName, _, _ := unstructured.NestedString(barmanPlugin(t, BuildPostgreSQLCluster(backedUpCluster())), "parameters", "serverName")
	prefix := strings.TrimPrefix(destination, "s3://excalibase-backups/") + "/" + serverName + "/"
	if got := BarmanObjectPrefix("proj-abc"); got != prefix || got != "proj-abc/cloud/" {
		t.Fatalf("BarmanObjectPrefix = %q, the plugin writes under %q", got, prefix)
	}
}

func TestObjectStoresRefuseAnIncompleteStore(t *testing.T) {
	cases := map[string]ObjectStoreOpts{
		"no bucket": {EndpointURL: testR2Endpoint, SecretName: "s"},
		"no secret": {EndpointURL: testR2Endpoint, Bucket: "b"},
	}
	for name, store := range cases {
		t.Run(name, func(t *testing.T) {
			if obj, err := BuildBackupObjectStore("p", "ns", store, 7); !errors.Is(err, ErrObjectStoreIncomplete) || obj != nil {
				t.Errorf("backup store: got %v, %v", obj, err)
			}
			restore := restoreOf(clusterOpts("dst", "org-dst"))
			restore.Store = store
			if obj, err := BuildRecoverySourceObjectStore(restore); !errors.Is(err, ErrObjectStoreIncomplete) || obj != nil {
				t.Errorf("recovery source: got %v, %v", obj, err)
			}
			if obj, err := BuildRestoreCluster(restore); !errors.Is(err, ErrObjectStoreIncomplete) || obj != nil {
				t.Errorf("restore cluster: got %v, %v", obj, err)
			}
		})
	}
}

func TestRecoverySourceReadsTheSourcePrefixAndPrunesNothing(t *testing.T) {
	store, err := BuildRecoverySourceObjectStore(restoreOf(clusterOpts("dst", "org-dst")))
	if err != nil {
		t.Fatalf("BuildRecoverySourceObjectStore: %v", err)
	}
	if store.GetName() != RecoverySourceObjectStoreName("dst") || store.GetNamespace() != "org-dst" {
		t.Errorf("the recovery source lives beside the restored cluster, got %s/%s", store.GetNamespace(), store.GetName())
	}
	destination, _, _ := unstructured.NestedString(store.Object, "spec", "configuration", "destinationPath")
	if destination != "s3://excalibase-backups/src" {
		t.Errorf("destinationPath: got %q", destination)
	}
	if _, set, _ := unstructured.NestedString(store.Object, "spec", "retentionPolicy"); set {
		t.Error("the restored project must never prune the source project's backups")
	}
}

func TestRestoreClusterRecoversThroughThePlugin(t *testing.T) {
	restored := mustBuildRestore(t, restoreOf(fullProjectCluster()))
	external, _, _ := unstructured.NestedSlice(restored.Object, "spec", "externalClusters")
	if len(external) != 1 {
		t.Fatalf("externalClusters: got %v", external)
	}
	source := external[0].(map[string]interface{})
	plugin, _, _ := unstructured.NestedMap(source, "plugin")
	parameters, _, _ := unstructured.NestedStringMap(plugin, "parameters")
	if plugin["name"] != BarmanCloudPluginName || parameters["barmanObjectName"] != RecoverySourceObjectStoreName("dst") ||
		parameters["serverName"] != "cloud" {
		t.Errorf("recovery must read the source through the plugin, got %v", source)
	}
	recoverySource, _, _ := unstructured.NestedString(restored.Object, "spec", "bootstrap", "recovery", "source")
	if recoverySource != source["name"] {
		t.Errorf("bootstrap recovery source %q is not the external cluster %v", recoverySource, source["name"])
	}
	if mentionsInTreeBarman(restored) {
		t.Error("a restore must not use the in-tree barmanObjectStore")
	}
	own := barmanPlugin(t, restored)
	if own == nil || own["parameters"].(map[string]interface{})["barmanObjectName"] != BackupObjectStoreName("dst") {
		t.Errorf("the restored project archives to its own store, got %v", own)
	}
}

func TestBackupsAreTakenByThePlugin(t *testing.T) {
	scheduled, err := BuildScheduledBackup("duke-db", "ns", testCronSchedule)
	if err != nil {
		t.Fatalf("scheduled backup: %v", err)
	}
	first, err := BuildFirstScheduledBackup("duke-db", "ns", testCronSchedule)
	if err != nil {
		t.Fatalf("first scheduled backup: %v", err)
	}
	for _, obj := range []*unstructured.Unstructured{scheduled, first, BuildManualBackup("duke-db", "ns", "b1")} {
		method, _, _ := unstructured.NestedString(obj.Object, "spec", "method")
		plugin, _, _ := unstructured.NestedString(obj.Object, "spec", "pluginConfiguration", "name")
		if method != "plugin" || plugin != BarmanCloudPluginName {
			t.Errorf("%s must be taken by the Barman Cloud plugin, got method %q plugin %q", obj.GetKind(), method, plugin)
		}
	}
}
