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
	cursors, ok := d.sqlStore.(hostlogs.Cursors)
	if !ok {
		log.Printf("host logs: the platform store keeps no cursor table; off")
		return nil
	}
	engine, err := enginestats.New(d.cfg.DockerHost)
	if err != nil {
		log.Printf("host logs: %v; off", err)
		return nil
	}
	shipper := hostlogs.New(hostlogs.Deps{
		Engine: hostlogs.EngineFrom(engine), Cursors: cursors, Instances: d.instances,
		Pusher: hostlogs.NewLokiPusher(d.cfg.HostLogs.LokiURL, hostlogs.NewPushClient()),
		Flags:  flags, Retention: d.cfg.LogRetention, DailyBytes: d.cfg.HostLogs.DailyBytesPerProject,
	})
	if features.Enabled(context.Background(), flags, features.LogRetention) {
		d.provisioning.SetLogRetention(flags, d.cfg.LogRetention, noNamespacePlans{})
	}
	d.provisioning.SetHostLogStore(d.cfg.HostLogs.LokiURL)
	d.provisioning.SetSingleHostLogs(engine)
	log.Printf("host logs: on; %d MiB per project per day", d.cfg.HostLogs.DailyBytesPerProject>>20)
	return shipper
}
