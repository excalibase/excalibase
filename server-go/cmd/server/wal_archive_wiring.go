package main

import (
	"context"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/email"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// walArchiveSampler is the check that alerts when a database cluster has failed
// to archive WAL for too long (EXC-784), or nil when the WALArchiveAlert guard
// is off or the platform has no CloudNativePG clusters to read.
func walArchiveSampler(cfg config.AppConfig, kube any, instances storage.InstanceStore, alerts *service.AlertingService, notifier service.AlertNotifier) service.LoopSampler {
	clusters, ok := kube.(service.ClusterLister)
	if !ok || instances == nil || cfg.ProvisionerMode == "docker" || !features.Enabled(context.Background(), cfg.Features(), features.WALArchiveAlert) {
		return nil
	}
	monitor := service.NewWALArchiveMonitor(clusters, service.ProjectNamespaces(instances), alerts, cfg.WALArchiveAlertAfter, time.Now)
	if notifier != nil {
		monitor.SetNotifier(notifier)
	}
	return monitor
}

// wireRenewalAlerts makes the credential renewer raise an alert for a project
// whose renewal keeps failing (EXC-784), and reports whether it did.
func wireRenewalAlerts(cfg config.AppConfig, renewer *service.BackupCredentialRenewer, alerts *service.AlertingService, notifier service.AlertNotifier) bool {
	if alerts == nil || !features.Enabled(context.Background(), cfg.Features(), features.WALArchiveAlert) {
		return false
	}
	renewer.SetAlerts(alerts, notifier, cfg.WALArchiveAlertAfter)
	return true
}

// wireRoleCertAlerts makes the role certificate renewer raise an alert for a
// project whose renewal or watcher restart keeps failing (EXC-887).
func wireRoleCertAlerts(cfg config.AppConfig, renewer *service.RoleCertificateRenewer, alerts *service.AlertingService, notifier service.AlertNotifier) bool {
	if alerts == nil || !features.Enabled(context.Background(), cfg.Features(), features.WALArchiveAlert) {
		return false
	}
	renewer.SetAlerts(alerts, notifier, cfg.RoleCertAlertAfter)
	return true
}

// alertMailBatch gathers the alerts one root cause raises in the same pass into one mail.
const alertMailBatch = 30 * time.Second

// adminAlertNotifier mails the platform admins when an alert is raised, or is
// nil when there is no mailer or user table to find them in.
func adminAlertNotifier(cfg config.AppConfig, sqlStore storage.PlatformStore, mail email.Sender, alerts *service.AlertingService) service.AlertNotifier {
	users, ok := sqlStore.(storage.UserStore)
	if !ok || !email.Configured(mail) {
		return nil
	}
	config := service.AdminAlertMailConfig{
		Admins: service.PlatformAdminEmails{Users: users}, Sender: mail,
		From: cfg.EmailFromAddress, FromName: cfg.EmailFromName, DashboardURL: cfg.StudioURL, Batch: alertMailBatch}
	if alerts != nil {
		config.OpenAlerts = alerts.OpenPlatformMetrics
	}
	return service.NewAdminAlertMail(config)
}

// hostWALAlertsOf is the single-host sweep's alert wiring: on while the
// WALArchiveAlert guard is on.
func hostWALAlertsOf(cfg config.AppConfig, sqlStore storage.PlatformStore, alerts *service.AlertingService, mail email.Sender) hostWALAlerts {
	return hostWALAlerts{
		on:       alerts != nil && features.Enabled(context.Background(), cfg.Features(), features.WALArchiveAlert),
		alerts:   alerts,
		notifier: adminAlertNotifier(cfg, sqlStore, mail, alerts),
		after:    cfg.WALArchiveAlertAfter,
	}
}
