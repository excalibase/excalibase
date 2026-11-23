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

// startCrashGuard stops customer apps that keep crashing, and alerts on
// databases that do, while FEATURE_CRASHLOOPGUARD is on (EXC-839).
func startCrashGuard(cfg config.AppConfig, sqlStore storage.PlatformStore, store storage.InstanceStore, kube any, deps *handlerDeps) func() {
	return newCrashGuardStarter(cfg, cfg.Features(), sqlStore, store, kube, deps.appDeploySvc, deps.alerts, deps.emailSender, sqlStore)
}

// newCrashGuardStarter holds the decisions startCrashGuard makes, apart from the dependencies it is handed.
func newCrashGuardStarter(cfg config.AppConfig, flags features.Flags, sqlStore storage.PlatformStore, store storage.InstanceStore,
	kube any, deploys *service.AppDeployService, alerts *service.AlertingService, mail email.Sender, members ...service.OrgMemberLister) func() {
	idle := func() {
		// guard off, or nothing here to read pods with
	}
	pods, canRead := kube.(service.ContainerCrashSource)
	if !crashGuardWanted(context.Background(), cfg, flags) || sqlStore == nil || store == nil || deploys == nil || alerts == nil || !canRead {
		return idle
	}
	var lock storage.LeaderLock = service.AlwaysLeader{}
	if cfg.IsCloud() {
		lock = standingLock(sqlStore, crashGuardLockID)
	}
	guard := service.NewCrashGuard(service.CrashGuardConfig{
		Apps: apphost.NewPostgresAppStore(sqlStore.DB()), Instances: store, Pods: pods, Stopper: deploys, Alerts: alerts,
		Notifier:  crashStopNotifier(cfg, mail, members),
		Threshold: cfg.CrashLoop.MaxPerHour, ExemptProjects: cfg.CrashLoop.ExemptProjects,
		ProtectedNamespaces: crashGuardProtectedNamespaces(cfg),
	})
	log.Printf("crash guard started: apps stop at %d crashes an hour, databases only alert", cfg.CrashLoop.MaxPerHour)
	return guard.Start(context.Background(), service.NewLeadership(lock), crashGuardInterval)
}

// crashStopNotifier mails the project's owners when a mail sender is
// configured; without one the stop is still recorded and shown in Studio.
func crashStopNotifier(cfg config.AppConfig, mail email.Sender, members []service.OrgMemberLister) service.CrashNotifier {
	if !email.Configured(mail) || len(members) == 0 || members[0] == nil {
		log.Printf("crash guard: no mail sender configured, owners are not emailed when an app is stopped")
		return nil
	}
	return service.NewCrashStopMail(members[0], mail, cfg.EmailFromAddress, cfg.EmailFromName, cfg.StudioURL)
}
