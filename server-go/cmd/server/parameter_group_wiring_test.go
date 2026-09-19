package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
)

// TestBuildParameterGroupStore_UnwritablePathFailsBoot reproduces EXC-398
// finding 13: the constructor error used to be discarded, so a nil store
// survived boot and every parameter-group request panicked. Boot must fail
// instead.
func TestBuildParameterGroupStore_UnwritablePathFailsBoot(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses file permission checks")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0700) })

	cfg := config.AppConfig{StoragePath: filepath.Join(parent, "storage")}
	store, err := buildParameterGroupStore(cfg)
	if err == nil {
		t.Fatal("unwritable STORAGE_PATH: got nil error, want a boot failure")
	}
	if store != nil {
		t.Error("a nil-safe store must not survive a constructor failure")
	}
}

// TestBuildParameterGroupStore_WritablePath keeps the normal boot path green.
func TestBuildParameterGroupStore_WritablePath(t *testing.T) {
	cfg := config.AppConfig{StoragePath: t.TempDir()}
	store, err := buildParameterGroupStore(cfg)
	if err != nil {
		t.Fatalf("buildParameterGroupStore: %v", err)
	}
	if store == nil {
		t.Fatal("writable STORAGE_PATH returned a nil store")
	}
}
