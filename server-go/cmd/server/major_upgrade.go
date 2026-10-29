package main

import (
	"context"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/handler"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
	"github.com/excalibase/provisioning-poc/internal/vaultclient"
)

// majorUpgradeLockID is the advisory lock the major upgrade recovery leads
// on; distinct from every other lock id the platform takes.
const majorUpgradeLockID int64 = 0x6168_0acb_6d75_0668

// majorUpgradeRecoveryInterval is how often the leader looks for runs whose
// driver died with the replica that held them.
const majorUpgradeRecoveryInterval = time.Minute

type majorUpgradeDeps struct {
	store     storage.InstanceStore
	provSvc   *service.ProvisioningService
	backupSvc *service.BackupService
	kube      k8s.KubeClient
	vault     vaultclient.VaultClient
	observers []service.StatusObserver
}

// wireMajorUpgrades serves major version upgrades (EXC-668). Restores read
// each major's archive whether or not the feature is on, since a project
// upgraded once keeps that layout. The routes and the recovery sweep follow
// the feature. Returns the sweep's stop.
func wireMajorUpgrades(cfg config.AppConfig, sqlStore storage.PlatformStore, d majorUpgradeDeps, deps *handlerDeps) func() {
	runs, ok := sqlStore.(storage.MajorUpgradeStore)
	if !ok || d.backupSvc == nil {
		return func() { /* nothing was started */ }
	}
	archives := service.NewMajorUpgradeArchives(runs)
	d.backupSvc.SetMajorUpgradeArchives(archives)
	svc := service.NewMajorUpgradeService(service.MajorUpgradeConfig{
		Runs: runs, Instances: d.store, Projects: d.provSvc, Probe: service.NewVaultDatabaseProbe(d.vault),
		Engines:   majorUpgradeEngines(d, archives),
		Backups:   func(inst *domain.DatabaseInstance) bool { return backupStoreFor(d.backupSvc, inst) },
		Observers: d.observers,
	})
	deps.majorUpgradeHandler = handler.NewMajorUpgradeHandler(svc, sqlStore)
	if !features.Enabled(context.Background(), cfg.Features(), features.MajorUpgrade) {
		return func() { /* no sweep while the feature is off */ }
	}
	var lock storage.LeaderLock = service.AlwaysLeader{}
	if cfg.IsCloud() {
		lock = pgstore.NewAdvisoryLock(sqlStore.DB(), majorUpgradeLockID)
	}
	return svc.StartRecoverySweeper(context.Background(), service.NewLeadership(lock), majorUpgradeRecoveryInterval)
}

func majorUpgradeEngines(d majorUpgradeDeps, archives *service.MajorUpgradeArchives) map[domain.DeploymentMode]service.MajorUpgrader {
	engines := map[domain.DeploymentMode]service.MajorUpgrader{}
	if adapter := d.backupSvc.K8sPostgres(); adapter != nil && d.kube != nil {
		engines[domain.ModeK8s] = service.NewK8sMajorUpgrader(service.K8sMajorUpgraderConfig{
			Client: d.kube, Backups: adapter, Archives: archives,
		})
	}
	if adapter := d.backupSvc.DockerPostgres(); adapter != nil {
		engines[domain.ModeDocker] = service.NewDockerMajorUpgrader(service.DockerMajorUpgraderConfig{Backups: adapter})
	}
	return engines
}

// backupStoreFor: the project's deployment has a store to write backups to.
func backupStoreFor(backups *service.BackupService, inst *domain.DatabaseInstance) bool {
	configured, err := backups.BackupsConfigured(inst.ProjectID)
	return err == nil && configured
}
