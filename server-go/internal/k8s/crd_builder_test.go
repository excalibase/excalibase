package k8s

import (
	"encoding/json"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
)

const (
	testCronSchedule = "0 2 * * *"
	testPGStatParam  = "pg_stat_statements.max"
)


func TestBuildPostgreSQLClusterFree(t *testing.T) {
	obj := BuildPostgreSQLCluster(PostgreSQLClusterOpts{
		ProjectID: "test-db",
		Namespace: "org-test-db",
		Tier:      config.TierConfig{Instances: 1, StorageSize: "5Gi", Memory: "512Mi", CPU: "0.5"},
	})

	spec := obj.Object["spec"].(map[string]interface{})

	if spec["instances"] != int64(1) {
		t.Errorf("instances: got %v", spec["instances"])
	}

	monitoring := spec["monitoring"].(map[string]interface{})
	if monitoring["enablePodMonitor"] != false {
		t.Error("FREE tier should have enablePodMonitor=false")
	}

	meta := obj.Object["metadata"].(map[string]interface{})
	if meta["name"] != "test-db-postgres" {
		t.Errorf("name: got %v", meta["name"])
	}
}

func TestBuildPostgreSQLClusterStandard(t *testing.T) {
	obj := BuildPostgreSQLCluster(PostgreSQLClusterOpts{
		ProjectID: "duke-db",
		Namespace: "exca-duke-db",
		Tier:      config.TierConfig{Instances: 3, StorageSize: "50Gi", Memory: "4Gi", CPU: "2"},
		Backup:    &BackupOpts{Schedule: testCronSchedule, RetentionDays: 30},
		Parameters: map[string]string{
			"shared_preload_libraries": "pg_stat_statements",
			testPGStatParam:   "10000",
			"pg_stat_statements.track": "all",
		},
		Tags: map[string]string{"owner": "duke", "env": "demo"},
	})

	spec := obj.Object["spec"].(map[string]interface{})

	// STANDARD tier: 3 instances. enablePodMonitor stays false at the CRD
	// level because the operator's PodMonitor reconciler crashes when the
	// Prometheus operator's CRDs exist but Prometheus isn't actually
	// scraping the project namespace.
	if spec["instances"] != int64(3) {
		t.Errorf("instances: got %v", spec["instances"])
	}

	monitoring := spec["monitoring"].(map[string]interface{})
	if monitoring["enablePodMonitor"] != false {
		t.Error("enablePodMonitor must be false to avoid CNPG operator panic")
	}

	// Backup configured
	backup := spec["backup"]
	if backup == nil {
		t.Fatal("backup should be configured")
	}

	// shared_preload_libraries in postgresql section
	pg := spec["postgresql"].(map[string]interface{})
	libs := pg["shared_preload_libraries"].([]interface{})
	if len(libs) != 1 || libs[0] != "pg_stat_statements" {
		t.Errorf("shared_preload_libraries: got %v", libs)
	}

	// Custom params should be in parameters (not shared_preload_libraries)
	params := pg["parameters"].(map[string]interface{})
	if params[testPGStatParam] != "10000" {
		t.Errorf("pg_stat_statements.max: got %v", params[testPGStatParam])
	}

	// Tags as labels
	meta := obj.Object["metadata"].(map[string]interface{})
	labels := meta["labels"].(map[string]interface{})
	if labels["owner"] != "duke" {
		t.Errorf("label owner: got %v", labels["owner"])
	}
}

func TestBuildPostgreSQLClusterJSON(t *testing.T) {
	obj := BuildPostgreSQLCluster(PostgreSQLClusterOpts{
		ProjectID: "json-test",
		Namespace: "ns",
		Tier:      config.TierConfig{Instances: 1, StorageSize: "5Gi", Memory: "512Mi", CPU: "0.5"},
	})

	// Must be valid JSON (serializable)
	_, err := json.Marshal(obj.Object)
	if err != nil {
		t.Fatalf("CRD not serializable to JSON: %v", err)
	}
}

func TestBuildScheduledBackup(t *testing.T) {
	obj := BuildScheduledBackup("duke-db", "exca-duke-db", testCronSchedule)

	meta := obj.Object["metadata"].(map[string]interface{})
	if meta["name"] != "duke-db-postgres-backup" {
		t.Errorf("name: got %v", meta["name"])
	}

	spec := obj.Object["spec"].(map[string]interface{})
	if spec["schedule"] != testCronSchedule {
		t.Errorf("schedule: got %v", spec["schedule"])
	}

	cluster := spec["cluster"].(map[string]interface{})
	if cluster["name"] != "duke-db-postgres" {
		t.Errorf("cluster name: got %v", cluster["name"])
	}
}
