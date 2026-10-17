package main

import (
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// registerMySQLBackups serves MySQL projects' backups on Kubernetes through
// MOCO (ADR 0041 decision 6). The bucket is listed with the platform key,
// addressed as the platform addresses it.
func registerMySQLBackups(backupSvc *service.BackupService, mysql *provisioner.MySQLProvisioner,
	k8sClient k8s.KubeClient, store storage.InstanceStore, backupStorage service.BackupStorageSource,
	records storage.BackupRecordStore) *service.MySQLBackupAdapter {
	if mysql == nil {
		return nil
	}
	adapter := service.NewMySQLBackupAdapter(service.MySQLBackupAdapterConfig{
		Kube:      k8sClient,
		MySQL:     mysql,
		Storage:   backupStorage,
		OpenStore: service.AWSObjectDeleterFactory(backupUsePathStyle()),
		Instances: store,
		Records:   records,
	})
	// Shipping binlogs and replaying them stream through pod exec (EXC-587).
	if streamer, ok := k8sClient.(service.PodStreamer); ok {
		adapter.SetPodStreamer(streamer)
	}
	backupSvc.RegisterEngineAdapter(domain.ModeK8s, domain.MySQL, adapter)
	return adapter
}
