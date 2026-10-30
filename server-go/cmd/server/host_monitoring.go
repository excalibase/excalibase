package main

import (
	"context"
	"log"
	"net/netip"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/email"
	"github.com/excalibase/provisioning-poc/internal/enginestats"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/handler"
	"github.com/excalibase/provisioning-poc/internal/hostmetrics"
	"github.com/excalibase/provisioning-poc/internal/hostmonitor"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/vaultclient"
)

// hostMonitoringTables are the platform DB's monitoring tables and audit log.
type hostMonitoringTables interface {
	hostmonitor.Store
	hostmonitor.SettingsStore
	handler.MonitoringReads
	LogAudit(ctx context.Context, entry *domain.AuditEntry) error
}

// hostMonitoringOn is the hostmetrics flag, which config keeps off off a single host.
func hostMonitoringOn(cfg config.AppConfig) bool {
	return features.Enabled(context.Background(), cfg.Features(), features.HostMetrics)
}

// hostMonitoringDeps is what single-host monitoring is built from.
type hostMonitoringDeps struct {
	cfg       config.AppConfig
	sqlStore  storage.PlatformStore
	instances storage.InstanceStore
	docker    provisioner.DockerClient
	vault     vaultclient.VaultClient
	mail      email.Sender
	metrics   *service.MetricsService
	alerts    *service.AlertingService
	// provisioning searches the database containers' logs; nil leaves them refused.
	provisioning *service.ProvisioningService
}

// wireHostMonitoring samples the host and its containers on the metrics loop
// (EXC-670) and answers the handler serving them; nil while the flag is off.
func wireHostMonitoring(d hostMonitoringDeps) *handler.HostMonitoringHandler {
	if !hostMonitoringOn(d.cfg) {
		return nil
	}
	tables, ok := d.sqlStore.(hostMonitoringTables)
	if !ok {
		log.Printf("host monitoring: the platform store keeps no monitoring tables; off")
		return nil
	}
	engine, err := enginestats.New(d.cfg.DockerHost)
	if err != nil {
		log.Printf("host monitoring: %v; off", err)
		return nil
	}
	var mail email.Sender
	if email.Configured(d.mail) {
		mail = d.mail
	}
	notifier := hostmonitor.NewNotifier(hostmonitor.NotifierDeps{Mail: mail, From: d.cfg.EmailFromAddress, FromName: d.cfg.EmailFromName,
		Vault: d.vault, Client: hostmonitor.GuardedClient(denyPrefixes(d.cfg.AppEgressExtraDenyCIDRs)), StudioURL: d.cfg.StudioURL})
	monitor := hostmonitor.New(hostmonitor.Deps{
		Engine: engine, Store: tables, Instances: d.instances, Alerts: d.alerts, Notifier: hostmonitor.NewQueue(notifier),
		Paths: hostmetrics.DefaultPaths(d.cfg.HostMetrics.NetDev), Platform: d.cfg.HostMetrics.Containers,
	})
	d.metrics.SetHostSampler(monitor)
	d.metrics.SetSingleHost(hostmonitor.NewDatabases(monitor, d.docker))
	if d.provisioning != nil {
		d.provisioning.SetSingleHostLogs(engine)
	}
	settings := hostmonitor.NewSettings(tables, d.vault).WithNotifier(notifier)
	log.Printf("host monitoring: on; %d platform containers", len(d.cfg.HostMetrics.Containers))
	return handler.NewHostMonitoringHandler(tables, settings, tables)
}

// mountHostMonitoring serves the Host page to platform readers and the
// alert rules to platform admins, and each project its own containers.
func mountHostMonitoring(admin chi.Router, d *handlerDeps) {
	if d.hostMonitoringHandler == nil {
		return
	}
	admin.Route("/monitoring", func(r chi.Router) {
		r.Use(features.Require(d.features, features.HostMetrics))
		r.Group(func(r chi.Router) {
			r.Use(auth.RequirePermission(auth.PermViewAny))
			d.hostMonitoringHandler.ReadRoutes(r)
		})
		r.Group(func(r chi.Router) {
			r.Use(auth.RequirePermission(auth.PermManageSetup))
			r.Use(auth.RequireUnrestrictedCredentialForWrites)
			r.Use(custommw.LimitBody(custommw.DefaultBodyLimit))
			d.hostMonitoringHandler.WriteRoutes(r)
		})
	})
}

func mountProjectContainers(r *chi.Mux, sqlStore storage.OrgStore, store storage.InstanceStore, d *handlerDeps) {
	if d.hostMonitoringHandler == nil {
		return
	}
	r.Route("/api/projects/{projectId}/containers", func(r chi.Router) {
		r.Use(features.Require(d.features, features.HostMetrics))
		r.Use(custommw.TenantContext)
		r.Use(auth.RequireAuth)
		r.Use(custommw.RequireProjectAccess(store, sqlStore))
		r.Get("/metrics", d.hostMonitoringHandler.ProjectContainers)
	})
}

// denyPrefixes are the install's own ranges, which config has already validated.
func denyPrefixes(cidrs []string) []netip.Prefix {
	prefixes := make([]netip.Prefix, 0, len(cidrs))
	for _, cidr := range cidrs {
		if prefix, err := netip.ParsePrefix(cidr); err == nil {
			prefixes = append(prefixes, prefix)
		}
	}
	return prefixes
}
