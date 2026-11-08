package main

import (
	"context"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
)

const rollbackRetryLockID int64 = 0x6168_0acb_b011_ba4c

// startRollbackRetrySweeper finishes rollbacks a failed cleanup left short.
// Cloud replicas elect a leader on their own advisory key so only one sweeps.
func startRollbackRetrySweeper(cfg config.AppConfig, sqlStore storage.PlatformStore, provSvc *service.ProvisioningService) func() {
	var lock storage.LeaderLock = service.AlwaysLeader{}
	if cfg.IsCloud() && sqlStore != nil {
		lock = pgstore.NewAdvisoryLock(sqlStore.DB(), rollbackRetryLockID)
	}
	return provSvc.StartRollbackRetrySweeper(context.Background(), service.NewLeadership(lock), service.RollbackRetryInterval)
}
