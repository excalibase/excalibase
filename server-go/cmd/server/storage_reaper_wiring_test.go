package main

import (
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/storagesvc"
)

// An upload that is never confirmed leaves bytes no catalogue row names, so
// nothing charges for them and nothing else would ever remove them. The sweep
// that collects them only exists if it is actually started at boot.

func TestStorageReaper_StartedWhenStorageIsConfigured(t *testing.T) {
	svc := storagesvc.NewService(nil, nil, nil)
	reaper, stop := startStorageReaper(config.AppConfig{}, nil, svc)
	if reaper == nil {
		t.Fatal("storage configured: the reaper must be started")
	}
	stop()
}

// With no storage service there is no blob plane to sweep; the sweep is
// absent by construction rather than started and failing every tick.
func TestStorageReaper_AbsentWhenStorageIsNotConfigured(t *testing.T) {
	reaper, stop := startStorageReaper(config.AppConfig{}, nil, nil)
	if reaper != nil {
		t.Fatal("no storage service: the reaper must not be started")
	}
	stop() // must be safe to call
}

// The grace is how long an unconfirmed object is left alone. An operator can
// raise it; an unusable value is refused rather than quietly replaced.
func TestStorageReaperGrace(t *testing.T) {
	t.Setenv("STORAGE_REAP_GRACE", "")
	if got := storageReapGrace(); got != storagesvc.DefaultUnconfirmedGrace {
		t.Errorf("unset: got %s, want %s", got, storagesvc.DefaultUnconfirmedGrace)
	}
	t.Setenv("STORAGE_REAP_GRACE", "6h")
	if got := storageReapGrace(); got != 6*time.Hour {
		t.Errorf("set: got %s, want 6h", got)
	}
}

// The advisory key each scheduler claims must be its own: two schedulers
// sharing one key would mean only one of them ever runs.
func TestStorageReaperAdvisoryKeyIsDistinct(t *testing.T) {
	keys := map[int64]string{
		backupSchedulerLockID: "backup scheduler",
		idlePauseLockID:       "idle pause",
		storageReapLockID:     "storage reaper",
	}
	if len(keys) != 3 {
		t.Fatalf("advisory keys collide: %v", keys)
	}
	if storageReapLockID <= 0 {
		t.Errorf("advisory key must be positive, got %d", storageReapLockID)
	}
}
