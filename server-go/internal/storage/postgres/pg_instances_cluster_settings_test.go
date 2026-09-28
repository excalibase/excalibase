//go:build integration

package postgres

import (
	"errors"
	"maps"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/storage"
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

// A tuning (EXC-492) is the one write that changes the recorded parameters,
// and only while the project still holds the status the tuning read.
func TestInstances_UpdateParametersIfStatusRewritesOnlyTheParameters(t *testing.T) {
	store := testStore(t)
	inst := instanceRow("proj-settings3", "org-set")
	inst.StorageClass = "fast-ssd"
	inst.Parameters = map[string]string{"work_mem": "64MB"}
	if err := store.Create(inst); err != nil {
		t.Fatalf("Create: %v", err)
	}

	tuned := map[string]string{"work_mem": "8MB", "jit": "off"}
	if err := store.UpdateParametersIfStatus(inst.ProjectID, tuned, inst.Status); err != nil {
		t.Fatalf("UpdateParametersIfStatus: %v", err)
	}
	got, err := store.FindByProjectID(inst.ProjectID)
	if err != nil || got == nil {
		t.Fatalf("read back: %v", err)
	}
	if !maps.Equal(got.Parameters, tuned) || got.StorageClass != "fast-ssd" {
		t.Errorf("read back %v %q, want %v fast-ssd", got.Parameters, got.StorageClass, tuned)
	}

	err = store.UpdateParametersIfStatus(inst.ProjectID, map[string]string{"jit": "on"}, "PAUSED")
	if !errors.Is(err, storage.ErrProjectStatusChanged) {
		t.Fatalf("err = %v, want ErrProjectStatusChanged", err)
	}
	if err := store.UpdateParametersIfStatus("proj-missing", tuned, "ACTIVE"); !errors.Is(err, storage.ErrProjectNotFound) {
		t.Fatalf("err = %v, want ErrProjectNotFound", err)
	}
}
