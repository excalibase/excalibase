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
func (cursorTables) HostLogBudgetUse(context.Context, string, string) (int64, bool, error) {
	return 0, false, nil
}
func (cursorTables) AddHostLogBudgetUse(context.Context, string, string, int64, bool) error {
	return nil
}
func (cursorTables) PruneHostLogBudget(context.Context, string) error { return nil }

var (
	_ hostlogs.Cursors     = cursorTables{}
	_ hostlogs.BudgetStore = cursorTables{}
)

// onlyCursors has the cursor table and not the budget one.
type onlyCursors struct{ storage.PlatformStore }

func (onlyCursors) HostLogCursors(context.Context) (map[string]int64, error) { return nil, nil }
func (onlyCursors) SaveHostLogCursor(context.Context, string, string, int64) error {
	return nil
}
func (onlyCursors) PruneHostLogCursors(context.Context, []string) error { return nil }

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
		"on, no budgets": {docker(on, "http://loki:3100"), onlyCursors{}, false},
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

// EXC-678: deletion goes to the chart's Loki on Kubernetes and to the bundled
// one on a single host with hostlogs on; nowhere otherwise.
func TestLogDeletionLokiURL(t *testing.T) {
	hostLogs := config.HostLogs{LokiURL: "http://loki:3100"}
	for name, tc := range map[string]struct {
		cfg  config.AppConfig
		want string
	}{
		"kubernetes":            {config.AppConfig{LokiURL: "http://loki.ns.svc:3100"}, "http://loki.ns.svc:3100"},
		"single host, logs on":  {config.AppConfig{ProvisionerMode: "docker", DarkFeatures: []features.Feature{features.HostLogs}, HostLogs: hostLogs}, "http://loki:3100"},
		"single host, logs off": {config.AppConfig{ProvisionerMode: "docker", HostLogs: hostLogs}, ""},
		"kubernetes, host flag": {config.AppConfig{DarkFeatures: []features.Feature{features.HostLogs}, HostLogs: hostLogs}, ""},
	} {
		if got := logDeletionLokiURL(tc.cfg); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}
