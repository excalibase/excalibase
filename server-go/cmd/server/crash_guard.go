package main

import (
	"context"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/email"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// The guard looks at restart counts once a minute, on the leading replica.
const (
	crashGuardLockID   int64 = 0x6168_0acb_c4a5_8d39
	crashGuardInterval       = time.Minute
)

// crashGuardWanted: the guard is dark until its flag is on (EXC-839).
func crashGuardWanted(ctx context.Context, cfg config.AppConfig, flags features.Flags) bool {
	return cfg.AppHostingEnabled && features.Enabled(ctx, flags, features.CrashLoopGuard)
}

// crashGuardProtectedNamespaces are never read, whatever the project list says.
func crashGuardProtectedNamespaces(cfg config.AppConfig) []string {
	protected := []string{"kube-system", "kube-public", "kube-node-lease", "default"}
	for _, namespace := range []string{cfg.PodNamespace, cfg.DenoNamespace, cfg.EdgeNamespace} {
		if namespace != "" {
			protected = append(protected, namespace)
		}
	}
	return protected
}

// crashGuardDeps is what the guard is built from; fields left nil keep it off.
type crashGuardDeps struct {
	flags    features.Flags
	sqlStore storage.PlatformStore
	store    storage.InstanceStore
	kube     any
	deploys  *service.AppDeployService
	alerts   *service.AlertingService
	mail     email.Sender
	members  service.OrgMemberLister
}

// startCrashGuard stops customer apps that keep crashing, and alerts on
// databases that do, while FEATURE_CRASHLOOPGUARD is on (EXC-839).
func startCrashGuard(cfg config.AppConfig, sqlStore storage.PlatformStore, store storage.InstanceStore, kube any, deps *handlerDeps) func() {
	return newCrashGuardStarter(cfg, crashGuardDeps{
		flags: cfg.Features(), sqlStore: sqlStore, store: store, kube: kube,
		deploys: deps.appDeploySvc, alerts: deps.alerts, mail: deps.emailSender, members: sqlStore,
	})
}

// newCrashGuardStarter holds the decisions startCrashGuard makes, apart from the dependencies it is handed.
func newCrashGuardStarter(cfg config.AppConfig, deps crashGuardDeps) func() {
	idle := func() {
		// guard off, or nothing here to read pods with
	}
	pods, canRead := deps.kube.(service.ContainerCrashSource)
	if !crashGuardWanted(context.Background(), cfg, deps.flags) || deps.sqlStore == nil || deps.store == nil ||
		deps.deploys == nil || deps.alerts == nil || !canRead {
		return idle
	}
	var lock storage.LeaderLock = service.AlwaysLeader{}
	if cfg.IsCloud() {
		lock = standingLock(deps.sqlStore, crashGuardLockID)
	}
	guard := service.NewCrashGuard(service.CrashGuardConfig{
		Apps: apphost.NewPostgresAppStore(deps.sqlStore.DB()), Instances: deps.store, Pods: pods, Stopper: deps.deploys, Alerts: deps.alerts,
		Notifier:  crashStopNotifier(cfg, deps.mail, deps.members),
		Threshold: cfg.CrashLoop.MaxPerHour, ExemptProjects: cfg.CrashLoop.ExemptProjects,
		ProtectedNamespaces: crashGuardProtectedNamespaces(cfg),
	})
	log.Printf("crash guard started: apps stop at %d crashes an hour, databases only alert", cfg.CrashLoop.MaxPerHour)
	return guard.Start(context.Background(), service.NewLeadership(lock), crashGuardInterval)
}

// crashStopNotifier mails the project's owners when a mail sender is
// configured; without one the stop is still recorded and shown in Studio.
func crashStopNotifier(cfg config.AppConfig, mail email.Sender, members service.OrgMemberLister) service.CrashNotifier {
	if !email.Configured(mail) || members == nil {
		log.Printf("crash guard: no mail sender configured, owners are not emailed when an app is stopped")
		return nil
	}
	return service.NewCrashStopMail(members, mail, cfg.EmailFromAddress, cfg.EmailFromName, cfg.StudioURL)
}
