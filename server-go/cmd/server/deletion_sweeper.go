package main

import (
	"context"
	"log"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
)

const deletionSweepLockID int64 = 0x6168_0acb_de1e_7e47

// startDeletionSweeper boots the sweep that hard-deletes projects whose grace
// period has ended and purges kept backups whose retention has ended. Cloud
// replicas elect a leader on their own advisory key so only one sweeps.
func startDeletionSweeper(cfg config.AppConfig, sqlStore storage.PlatformStore, work service.DeletionSweepWork) func() {
	var lock storage.LeaderLock = service.AlwaysLeader{}
	if cfg.IsCloud() && sqlStore != nil {
		lock = pgstore.NewAdvisoryLock(sqlStore.DB(), deletionSweepLockID)
	}
	sweeper := service.NewDeletionSweeper(service.DeletionSweeperConfig{Work: work, Lock: lock})
	sweeper.Start(context.Background())
	log.Printf("Deletion sweep started (every %s; projects hard-deleted %s after DELETE, kept backups purged %s after that)",
		service.DefaultDeletionSweepInterval, service.DeletionGracePeriod, service.RetainedBackupPeriod)
	return sweeper.Stop
}
