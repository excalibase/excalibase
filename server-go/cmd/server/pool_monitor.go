package main

import (
	"context"
	"database/sql"
	"time"

	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// startPoolPressureMonitor alerts when the platform database pool stays full
// (EXC-746). Each replica watches its own pool.
func startPoolPressureMonitor(sqlStore storage.PlatformStore, alerts *service.AlertingService) *service.PoolPressureMonitor {
	pool, ok := sqlStore.(interface{ DB() *sql.DB })
	if !ok || pool.DB() == nil {
		return nil
	}
	monitor := service.NewPoolPressureMonitor(pool.DB().Stats, alerts, time.Now)
	go monitor.Run(context.Background())
	return monitor
}
