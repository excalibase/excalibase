package main

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/hostlogs"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// cursorTables stands in for the platform store's cursor table.
type cursorTables struct{ storage.PlatformStore }

func (cursorTables) HostLogCursors(context.Context) (map[string]int64, error) { return nil, nil }
func (cursorTables) SaveHostLogCursor(context.Context, string, string, int64) error {
	return nil
}
func (cursorTables) PruneHostLogCursors(context.Context, []string) error { return nil }

var _ hostlogs.Cursors = cursorTables{}

// EXC-672: the shipper exists only with hostlogs on, on a single host, with its Loki named.
func TestHostLogShippingStartsOnlyWithItsFeature(t *testing.T) {
	on := []features.Feature{features.HostLogs}
	docker := func(dark []features.Feature, loki string) config.AppConfig {
		return config.AppConfig{DarkFeatures: dark, ProvisionerMode: "docker", DockerHost: "tcp://engine-proxy:2375",
			HostLogs: config.HostLogs{LokiURL: loki, DailyBytesPerProject: 1 << 20}}
	}
	for name, tc := range map[string]struct {
		cfg    config.AppConfig
		tables storage.PlatformStore
		want   bool
	}{
		"off":            {docker(nil, "http://loki:3100"), cursorTables{}, false},
		"on, Kubernetes": {config.AppConfig{DarkFeatures: on, HostLogs: config.HostLogs{LokiURL: "http://loki:3100"}}, cursorTables{}, false},
		"on, no Loki":    {docker(on, ""), cursorTables{}, false},
		"on, no table":   {docker(on, "http://loki:3100"), nil, false},
		"on, bad engine": {config.AppConfig{DarkFeatures: on, ProvisionerMode: "docker", DockerHost: "unix:///x", HostLogs: config.HostLogs{LokiURL: "http://loki:3100"}}, cursorTables{}, false},
		"on":             {docker(on, "http://loki:3100"), cursorTables{}, true},
	} {
		store, _ := storage.NewFileSystemStore(t.TempDir())
		svc := service.NewProvisioningService(store, provisioner.NewFactory(), nil)
		shipper := wireHostLogs(hostLogDeps{cfg: tc.cfg, sqlStore: tc.tables, instances: store, provisioning: svc})
		if (shipper != nil) != tc.want {
			t.Errorf("%s: shipper built = %v", name, shipper != nil)
		}
	}
}
