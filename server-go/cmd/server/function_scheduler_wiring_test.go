package main

import (
	"os"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/projectdb"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

func bootInstanceStore(t *testing.T) storage.InstanceStore {
	t.Helper()
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	return store
}

// The finding: the production builder never called SetProjectDBFn, so a
// deploy declaring a table applied nothing and schema/apply answered 503.
func TestBuildFunctionHandler_WiresTheProjectDatabaseResolver(t *testing.T) {
	cfg := config.AppConfig{StoragePath: t.TempDir(), ProvisionerMode: "docker", AutoMigrate: true}
	store := bootInstanceStore(t)
	opener := projectdb.NewOpener(store, bootVault{}, projectdb.Overrides{}, projectdb.PoolLimits{})
	defer opener.Close()

	h := buildFunctionHandler(cfg, bootVault{}, store, nil, nil, opener)

	if !h.HasProjectDB() {
		t.Fatal("the production function handler has no project database resolver")
	}
	if !h.AutoMigrates() {
		t.Error("EXCALIBASE_AUTO_MIGRATE defaults on; the handler must be told so")
	}
}

// The scheduler's cron half leads on its own advisory key: sharing one with
// another sweep would mean only one of them ever runs.
func TestFunctionCronAdvisoryKeyIsDistinct(t *testing.T) {
	keys := map[int64]string{
		backupSchedulerLockID: "backup scheduler",
		idlePauseLockID:       "idle pause",
		storageReapLockID:     "storage reaper",
		restoreSweepLockID:    "restore sweeper",
		functionCronLockID:    "function cron",
	}
	if len(keys) != 5 {
		t.Fatalf("advisory keys collide: %v", keys)
	}
	if functionCronLockID <= 0 {
		t.Errorf("advisory key must be positive, got %d", functionCronLockID)
	}
}

// Pause is the transition that costs most: the database is down for as long
// as the project stays paused, so the pool cache has to hear about it.
func TestPauseServiceEvictsTheProjectPool(t *testing.T) {
	body, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if !strings.Contains(string(body), "pauseSvc.AddStatusObserver(projectDB)") {
		t.Error("a pause leaves the project's database pool open")
	}
}
