package main

import (
	"context"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
)

// mysqlBackupWaitSweepInterval is how often MOCO's backup CronJobs are checked
// for their egress wait (EXC-586). Attaching, relinking and resuming add it at
// once; the sweep heals CronJobs made before the wait existed or remade by MOCO.
const mysqlBackupWaitSweepInterval = 5 * time.Minute

// mysqlBackupWaitLockID leads the sweep across replicas.
const mysqlBackupWaitLockID int64 = 0x6168_0acb_0586_ea17

// startMySQLBackupWaitSweep starts the sweep where MOCO serves MySQL and
// reports whether it did.
func startMySQLBackupWaitSweep(lock storage.LeaderLock, mysql *provisioner.MySQLProvisioner, store storage.InstanceStore) bool {
	if mysql == nil {
		return false
	}
	go service.NewMySQLBackupWaitSweep(mysql, store, service.NewLeadership(lock)).Run(context.Background(), mysqlBackupWaitSweepInterval)
	log.Printf("MySQL backups: runs wait for their egress (checked every %s)", mysqlBackupWaitSweepInterval)
	return true
}

// mysqlBackupWaitLock is a Postgres advisory lock in cloud, where replicas
// run side by side, and no lock on a single process.
func mysqlBackupWaitLock(cfg config.AppConfig, sqlStore storage.PlatformStore) storage.LeaderLock {
	if cfg.IsCloud() && sqlStore != nil {
		return pgstore.NewAdvisoryLock(sqlStore.DB(), mysqlBackupWaitLockID)
	}
	return service.AlwaysLeader{}
}
