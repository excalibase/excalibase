package storage

import (
	"errors"
	"maps"
	"testing"
)

func TestFileSystemUpdateParametersIfStatus(t *testing.T) {
	store, err := NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	live := restoringProject()
	live.Status = "ACTIVE"
	live.Parameters = map[string]string{"work_mem": "64MB"}
	if err := store.Create(live); err != nil {
		t.Fatalf("create: %v", err)
	}

	tuned := map[string]string{"jit": "off"}
	if err := store.UpdateParametersIfStatus("target-x", tuned, "ACTIVE"); err != nil {
		t.Fatalf("UpdateParametersIfStatus: %v", err)
	}
	stored, _ := store.FindByProjectID("target-x")
	if !maps.Equal(stored.Parameters, tuned) {
		t.Errorf("parameters = %v, want %v", stored.Parameters, tuned)
	}
	tuned["jit"] = "on"
	if again, _ := store.FindByProjectID("target-x"); again.Parameters["jit"] != "off" {
		t.Error("the store kept the caller's map instead of a copy")
	}

	if err := store.UpdateParametersIfStatus("target-x", nil, "PAUSED"); !errors.Is(err, ErrProjectStatusChanged) {
		t.Errorf("a moved row: got %v, want ErrProjectStatusChanged", err)
	}
	if err := store.UpdateParametersIfStatus("missing", nil, "ACTIVE"); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("missing project: got %v, want ErrProjectNotFound", err)
	}
}
