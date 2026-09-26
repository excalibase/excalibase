//go:build integration

package postgres

import (
	"maps"
	"testing"
)

// A restore rebuilds the project's cluster from these, so they must survive
// the round trip exactly and no update may rewrite them.
func TestInstances_ClusterSettingsAreStoredAtCreation(t *testing.T) {
	store := testStore(t)
	inst := instanceRow("proj-settings1", "org-set")
	inst.StorageClass = "fast-ssd"
	inst.Parameters = map[string]string{"work_mem": "64MB", "shared_preload_libraries": "pg_stat_statements"}
	if err := store.Create(inst); err != nil {
		t.Fatalf("Create: %v", err)
	}

	inst.StorageClass = "other"
	inst.Parameters = map[string]string{"work_mem": "1MB"}
	if err := store.Update(inst); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := store.FindByProjectID(inst.ProjectID)
	if err != nil || got == nil {
		t.Fatalf("read back: %v", err)
	}
	want := map[string]string{"work_mem": "64MB", "shared_preload_libraries": "pg_stat_statements"}
	if got.StorageClass != "fast-ssd" || !maps.Equal(got.Parameters, want) {
		t.Errorf("read back storage class %q parameters %v, want fast-ssd %v", got.StorageClass, got.Parameters, want)
	}
}

func TestInstances_NoClusterSettingsReadBackEmpty(t *testing.T) {
	store := testStore(t)
	if err := store.Create(instanceRow("proj-settings2", "org-set")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := store.FindByProjectID("proj-settings2")
	if err != nil || got == nil {
		t.Fatalf("read back: %v", err)
	}
	if got.StorageClass != "" || len(got.Parameters) != 0 {
		t.Errorf("a project created with neither reads back %q %v", got.StorageClass, got.Parameters)
	}
}
