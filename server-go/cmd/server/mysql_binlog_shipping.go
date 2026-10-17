package main

import (
	"context"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
)

// mysqlBinlogShipInterval is how often MySQL binlogs are shipped between
// backups (EXC-587); it bounds how far behind now a restore can reach.
const mysqlBinlogShipInterval = time.Minute

// mysqlBinlogShipLockID leads the Kubernetes ship sweep across replicas.
const mysqlBinlogShipLockID int64 = 0x6168_0acb_0587_b16e

// startMySQLBinlogShipping starts the ship sweep of each mode that serves
// MySQL, only with its feature on, and returns how many it started.
func startMySQLBinlogShipping(flags features.Flags, lock storage.LeaderLock, kube *service.MySQLBackupAdapter,
	host *service.HostMySQLBackupAdapter, store storage.InstanceStore) int {
	if !features.Enabled(context.Background(), flags, features.MySQLPITR) {
		return 0
	}
	started := 0
	if kube != nil {
		go service.NewMySQLBinlogSweep(kube, store, service.NewLeadership(lock)).Run(context.Background(), mysqlBinlogShipInterval)
		started++
	}
	if host != nil {
		go service.NewHostMySQLBinlogSweep(host, store).Run(context.Background(), mysqlBinlogShipInterval)
		started++
	}
	if started > 0 {
		log.Printf("MySQL binlogs: shipped every %s between backups", mysqlBinlogShipInterval)
	}
	return started
}

// mysqlBinlogShipLock is a Postgres advisory lock in cloud, where replicas
// run side by side, and no lock on a single process.
func mysqlBinlogShipLock(cfg config.AppConfig, sqlStore storage.PlatformStore) storage.LeaderLock {
	if cfg.IsCloud() && sqlStore != nil {
		return pgstore.NewAdvisoryLock(sqlStore.DB(), mysqlBinlogShipLockID)
	}
	return service.AlwaysLeader{}
}
