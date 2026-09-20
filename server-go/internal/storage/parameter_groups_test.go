package storage

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

const testHighPerf = "high-perf"

func TestParameterGroupStoreCRUD(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileSystemParameterGroupStore(dir)
	if err != nil {
		t.Fatalf("init: %v", err)
	}

	pg := &domain.ParameterGroup{
		Name:        testHighPerf,
		Description: "High performance settings",
		Parameters:  map[string]string{"max_connections": "200", "shared_buffers": "4GB"},
	}

	// Save
	if err := store.Save(pg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// FindByName
	got, _ := store.FindByName(testHighPerf)
	if got == nil {
		t.Fatal("FindByName returned nil")
	}
	if got.Parameters["max_connections"] != "200" {
		t.Errorf("max_connections: got %s", got.Parameters["max_connections"])
	}

	// FindAll
	all, _ := store.FindAll()
	if len(all) != 1 {
		t.Errorf("FindAll: got %d", len(all))
	}

	// Delete
	store.Delete(testHighPerf)
	got, _ = store.FindByName(testHighPerf)
	if got != nil {
		t.Error("should be nil after delete")
	}
}

func TestParameterGroupStoreReload(t *testing.T) {
	dir := t.TempDir()
	store1, _ := NewFileSystemParameterGroupStore(dir)
	store1.Save(&domain.ParameterGroup{
		Name:       "persist-pg",
		Parameters: map[string]string{"key": "val"},
	})

	store2, _ := NewFileSystemParameterGroupStore(dir)
	got, _ := store2.FindByName("persist-pg")
	if got == nil {
		t.Fatal("not found after reload")
	}
	if got.Parameters["key"] != "val" {
		t.Errorf("key: got %s", got.Parameters["key"])
	}
}

func TestParameterGroupStoreNotFound(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewFileSystemParameterGroupStore(dir)
	got, _ := store.FindByName("nope")
	if got != nil {
		t.Error("expected nil")
	}
}
