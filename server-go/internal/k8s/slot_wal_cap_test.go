package k8s

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
)

func clusterParameters(t *testing.T, opts PostgreSQLClusterOpts) map[string]interface{} {
	t.Helper()
	return postgresqlSection(t, opts)["parameters"].(map[string]interface{})
}

func TestClusterCapsTheWALASlotMayRetainByTierStorage(t *testing.T) {
	for storage, want := range map[string]string{"5Gi": "1024MB", "50Gi": "10240MB", "500Gi": "102400MB"} {
		params := clusterParameters(t, PostgreSQLClusterOpts{
			ProjectID: "proj-cap", Namespace: "ns-cap",
			Tier: config.TierConfig{CPU: "0.5", Memory: "512Mi", StorageSize: storage, Instances: 1},
		})
		if params["max_slot_wal_keep_size"] != want {
			t.Errorf("storage %s: max_slot_wal_keep_size = %v, want %s", storage, params["max_slot_wal_keep_size"], want)
		}
	}
}

// A stalled change stream would otherwise be free to fill the disk again.
func TestATenantParameterCannotLiftTheSlotWALCap(t *testing.T) {
	params := clusterParameters(t, PostgreSQLClusterOpts{
		ProjectID: "proj-cap", Namespace: "ns-cap",
		Tier:       config.TierConfig{CPU: "0.5", Memory: "512Mi", StorageSize: "5Gi", Instances: 1},
		Parameters: map[string]string{"max_slot_wal_keep_size": "-1", "work_mem": "8MB"},
	})
	if params["max_slot_wal_keep_size"] != "1024MB" {
		t.Errorf("max_slot_wal_keep_size = %v, want the platform's 1024MB", params["max_slot_wal_keep_size"])
	}
	if params["work_mem"] != "8MB" {
		t.Errorf("the tenant's other parameters must survive: work_mem = %v", params["work_mem"])
	}
}
