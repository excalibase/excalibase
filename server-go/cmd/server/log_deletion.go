package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/logdelete"
	"github.com/excalibase/provisioning-poc/internal/service"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
)

// logDeletionStore is the platform database's queue and audit log.
type logDeletionStore interface {
	logdelete.Store
	service.AuditWriter
}

// The platform database is the queue; a missing method would turn deletion off silently.
var _ logDeletionStore = (*pgstore.Store)(nil)

// wireLogDeletion makes every project teardown queue the removal of the
// project's stored lines from the Loki at lokiURL (EXC-678); nil, and no step,
// while the flag is off or there is no Loki or queue.
func wireLogDeletion(cfg config.AppConfig, store logDeletionStore, provSvc *service.ProvisioningService, lokiURL string) *logdelete.Worker {
	if !features.Enabled(context.Background(), cfg.Features(), features.LogDeletion) {
		return nil
	}
	if lokiURL == "" || store == nil {
		log.Printf("log deletion: no Loki or no platform database; off")
		return nil
	}
	provSvc.SetLogDeletion(store, store, longestLogRetention(cfg.LogRetention))
	log.Printf("log deletion: on")
	return logdelete.NewWorker(store, logdelete.NewLokiClient(lokiURL, &http.Client{}), store)
}

// longestLogRetention is the longest any project's lines are kept.
func longestLogRetention(retention config.LogRetention) time.Duration {
	longest := retention.Default
	for _, period := range retention.ByPlan {
		longest = max(longest, period)
	}
	return longest
}
