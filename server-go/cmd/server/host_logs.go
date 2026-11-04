package main

import (
	"context"
	"log"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/enginestats"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/hostlogs"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
)

// hostLogDeps is what a single host's stored log search is built from.
type hostLogDeps struct {
	cfg          config.AppConfig
	sqlStore     storage.PlatformStore
	instances    storage.InstanceStore
	provisioning *service.ProvisioningService
}

// noNamespacePlans: a single host's plan rides on each line's labels, not on a namespace.
type noNamespacePlans struct{}

func (noNamespacePlans) ProjectNamespacePlans(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}
func (noNamespacePlans) SetNamespacePlan(context.Context, string, string) error { return nil }

// wireHostLogs points the project log search at the bundled Loki and answers
// the shipper that fills it (EXC-672); nil while the flag is off.
func wireHostLogs(d hostLogDeps) *hostlogs.Shipper {
	flags := d.cfg.Features()
	if !features.Enabled(context.Background(), flags, features.HostLogs) {
		return nil
	}
	if d.cfg.HostLogs.LokiURL == "" {
		log.Printf("host logs: HOST_LOGS_LOKI_URL is unset; off")
		return nil
	}
	tables, ok := d.sqlStore.(hostLogTables)
	if !ok {
		log.Printf("host logs: the platform store keeps no cursor or budget table; off")
		return nil
	}
	engine, err := enginestats.New(d.cfg.DockerHost)
	if err != nil {
		log.Printf("host logs: %v; off", err)
		return nil
	}
	disk := hostlogs.NewDiskGuard(d.cfg.HostLogs.DiskPath, d.cfg.HostLogs.MinFreePercent)
	if err := disk.Check(); err != nil {
		// A disk that cannot be read would never pause shipping: refuse to ship blind.
		log.Printf("host logs: %v; off (set HOST_LOGS_DISK_PATH)", err)
		return nil
	}
	shipper := hostlogs.New(hostlogs.Deps{
		Engine: hostlogs.EngineFrom(engine), Cursors: tables, Budgets: tables, Instances: d.instances,
		Pusher: hostlogs.NewLokiPusher(d.cfg.HostLogs.LokiURL, hostlogs.NewPushClient()),
		Disk:   disk,
		Flags:  flags, Retention: d.cfg.LogRetention,
		DailyBytes: d.cfg.HostLogs.DailyBytesPerProject, DailyBytesByPlan: d.cfg.HostLogs.DailyBytesByPlan,
	})
	if features.Enabled(context.Background(), flags, features.LogRetention) {
		d.provisioning.SetLogRetention(flags, d.cfg.LogRetention, noNamespacePlans{})
	}
	d.provisioning.SetHostLogStore(d.cfg.HostLogs.LokiURL)
	d.provisioning.SetSingleHostLogs(engine)
	log.Printf("host logs: on; %d MiB per project per day unless its plan says otherwise; paused under %d%% disk free",
		d.cfg.HostLogs.DailyBytesPerProject>>20, d.cfg.HostLogs.MinFreePercent)
	return shipper
}

// hostLogTables are the platform database's cursor and budget tables.
type hostLogTables interface {
	hostlogs.Cursors
	hostlogs.BudgetStore
}

// A missing method would turn host logs off silently.
var _ hostLogTables = (*pgstore.Store)(nil)

// logDeletionLokiURL is the Loki a deleted project's lines are removed from
// (EXC-678): the chart's, or a single host's bundled one while hostlogs is on.
func logDeletionLokiURL(cfg config.AppConfig) string {
	if cfg.LokiURL != "" {
		return cfg.LokiURL
	}
	if features.Enabled(context.Background(), cfg.Features(), features.HostLogs) {
		return cfg.HostLogs.LokiURL
	}
	return ""
}
