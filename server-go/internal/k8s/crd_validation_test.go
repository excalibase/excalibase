package k8s

import (
	"encoding/json"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	sigsyaml "sigs.k8s.io/yaml"
)

// TestCRDProducesValidYAML verifies the generated CRD can be serialized to valid YAML
// that matches what CNPG operator expects. This catches field name typos and schema issues.
func TestCRDProducesValidYAML(t *testing.T) {
	obj := BuildPostgreSQLCluster(PostgreSQLClusterOpts{
		ProjectID: "test-db",
		Namespace: "org-test-db",
		Tier:      config.TierConfig{Instances: 3, StorageSize: "50Gi", Memory: "4Gi", CPU: "2"},
		Backup:    &BackupOpts{Schedule: "0 2 * * *", RetentionDays: 30},
		Parameters: map[string]string{
			"shared_preload_libraries": "pg_stat_statements",
			"pg_stat_statements.max":   "10000",
		},
		Tags:            map[string]string{"owner": "duke"},
		PostgresVersion: "16",
		DatabaseName:    "mydb",
		MasterUsername:  "admin",
		StorageClass:    "standard",
	})

	// Must serialize to valid JSON
	jsonBytes, err := json.Marshal(obj.Object)
	if err != nil {
		t.Fatalf("JSON marshal: %v", err)
	}

	// Must convert to valid YAML (what kubectl apply sees)
	yamlBytes, err := sigsyaml.JSONToYAML(jsonBytes)
	if err != nil {
		t.Fatalf("YAML conversion: %v", err)
	}

	yamlStr := string(yamlBytes)

	// Verify required CNPG fields exist
	requiredFields := []string{
		"apiVersion: postgresql.cnpg.io/v1",
		"kind: Cluster",
		"name: test-db-postgres",
		"namespace: org-test-db",
		"instances: 3",
		"size: 50Gi",
		"storageClass: standard",
		"memory: 4Gi",
		"enablePodMonitor: false",
		"shared_preload_libraries:",
		"- pg_stat_statements",
		"pg_stat_statements.max:",
		"max_connections:",
		"retentionPolicy:",
		"barmanObjectStore:",
		"s3Credentials:",
		"destinationPath: s3://postgres-backups/test-db",
		"imageName: ghcr.io/cloudnative-pg/postgresql:16",
		"database: mydb",
		"owner: admin",
	}

	for _, field := range requiredFields {
		if !containsString(yamlStr, field) {
			t.Errorf("YAML missing required field: %s\n\nFull YAML:\n%s", field, yamlStr)
		}
	}
}

// TestCRDSharedPreloadLibrariesNotInParameters ensures shared_preload_libraries
// is NOT inside spec.postgresql.parameters (CNPG rejects this).
func TestCRDSharedPreloadLibrariesNotInParameters(t *testing.T) {
	obj := BuildPostgreSQLCluster(PostgreSQLClusterOpts{
		ProjectID:  "test",
		Namespace:  "ns",
		Tier:       config.TierConfig{Instances: 1, StorageSize: "5Gi", Memory: "512Mi", CPU: "0.5"},
		Parameters: map[string]string{"shared_preload_libraries": "pg_stat_statements,pgaudit"},
	})

	spec := obj.Object["spec"].(map[string]interface{})
	pg := spec["postgresql"].(map[string]interface{})
	params := pg["parameters"].(map[string]interface{})

	// shared_preload_libraries must NOT be in parameters
	if _, exists := params["shared_preload_libraries"]; exists {
		t.Error("shared_preload_libraries should NOT be in spec.postgresql.parameters — CNPG rejects it there")
	}

	// It should be in the dedicated array field
	libs := pg["shared_preload_libraries"].([]interface{})
	if len(libs) != 2 {
		t.Errorf("expected 2 libraries, got %d: %v", len(libs), libs)
	}
	if libs[0] != "pg_stat_statements" || libs[1] != "pgaudit" {
		t.Errorf("libraries: got %v", libs)
	}
}

// TestCRDFreeTierDisablesPodMonitor verifies FREE tier doesn't create PodMonitor.
func TestCRDFreeTierDisablesPodMonitor(t *testing.T) {
	obj := BuildPostgreSQLCluster(PostgreSQLClusterOpts{
		ProjectID: "free-db",
		Namespace: "ns",
		Tier:      config.TierConfig{Instances: 1, StorageSize: "5Gi", Memory: "512Mi", CPU: "0.5"},
	})
	monitoring := obj.Object["spec"].(map[string]interface{})["monitoring"].(map[string]interface{})
	if monitoring["enablePodMonitor"] != false {
		t.Error("FREE tier must have enablePodMonitor=false")
	}
}

// TestCRDAllTiersDisablePodMonitor verifies enablePodMonitor stays off for
// every tier. CNPG's PodMonitor reconciler crashes when prometheus-operator
// CRDs are installed but Prometheus isn't actually scraping the project
// namespace — so we never set this true at the CRD level.
func TestCRDAllTiersDisablePodMonitor(t *testing.T) {
	obj := BuildPostgreSQLCluster(PostgreSQLClusterOpts{
		ProjectID: "ent-db",
		Namespace: "ns",
		Tier:      config.TierConfig{Instances: 5, StorageSize: "500Gi", Memory: "16Gi", CPU: "4"},
	})
	monitoring := obj.Object["spec"].(map[string]interface{})["monitoring"].(map[string]interface{})
	if monitoring["enablePodMonitor"] != false {
		t.Error("enablePodMonitor must stay false to avoid CNPG operator panic")
	}
}

// TestCRDBackupNotIncludedWhenNil verifies no backup section when not configured.
func TestCRDBackupNotIncludedWhenNil(t *testing.T) {
	obj := BuildPostgreSQLCluster(PostgreSQLClusterOpts{
		ProjectID: "no-backup",
		Namespace: "ns",
		Tier:      config.TierConfig{Instances: 1, StorageSize: "5Gi", Memory: "512Mi", CPU: "0.5"},
	})
	spec := obj.Object["spec"].(map[string]interface{})
	if _, exists := spec["backup"]; exists {
		t.Error("backup should not exist when not configured")
	}
}

// TestCRDDefaultDatabaseName verifies default db/user when not specified.
func TestCRDDefaultDatabaseName(t *testing.T) {
	obj := BuildPostgreSQLCluster(PostgreSQLClusterOpts{
		ProjectID: "default-db",
		Namespace: "ns",
		Tier:      config.TierConfig{Instances: 1, StorageSize: "5Gi", Memory: "512Mi", CPU: "0.5"},
	})
	spec := obj.Object["spec"].(map[string]interface{})
	// No bootstrap section when using defaults "app"/"app"
	if _, exists := spec["bootstrap"]; exists {
		t.Error("bootstrap should not exist with default app/app")
	}
}

// TestScheduledBackupTargetsCorrectCluster verifies the backup CRD references the right cluster.
func TestScheduledBackupTargetsCorrectCluster(t *testing.T) {
	obj := BuildScheduledBackup("my-db", "my-ns", "0 3 * * *")
	spec := obj.Object["spec"].(map[string]interface{})

	if spec["schedule"] != "0 3 * * *" {
		t.Errorf("schedule: got %v", spec["schedule"])
	}
	cluster := spec["cluster"].(map[string]interface{})
	if cluster["name"] != "my-db-postgres" {
		t.Errorf("cluster name should follow convention: got %v", cluster["name"])
	}
	if spec["target"] != "prefer-standby" {
		t.Error("backup should prefer-standby for multi-instance clusters")
	}
}

func containsString(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && contains(s, substr)
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
