package main

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
)

// ADR 0041 decision 6: on Kubernetes a MySQL project's backups go through
// the MOCO adapter, which also takes the restore probe at boot.
func TestMySQLBackupsAreServedOnKubernetes(t *testing.T) {
	backupSvc := bootBackupService(t, domain.ModeK8s)
	mysql := provisioner.NewMySQLProvisioner(k8s.NewMockClient())
	storageSource := service.StaticBackupStorage(&domain.S3Credentials{AccessKeyID: "k", SecretAccessKey: "s", Endpoint: "https://r2", Bucket: "b"})
	registerMySQLBackups(backupSvc, mysql, k8s.NewMockClient(), nil, storageSource)

	inst := &domain.DatabaseInstance{ProjectID: "p", DBType: domain.MySQL, DeploymentMode: domain.ModeK8s}
	if !backupSvc.SupportsInPlaceRestoreFor(inst) {
		t.Error("a MySQL project cannot be restored in place")
	}
	if err := wireRestoreVerification(backupSvc, bootVault{}, bootReadyTimeout); err != nil {
		t.Fatalf("the MySQL adapter refused the probe: %v", err)
	}
}

// Without a MySQL provisioner (single host) nothing is registered.
func TestMySQLBackupsNeedTheProvisioner(t *testing.T) {
	backupSvc := bootBackupService(t, domain.ModeK8s)
	registerMySQLBackups(backupSvc, nil, k8s.NewMockClient(), nil, nil)
	if backupSvc.SupportsInPlaceRestoreFor(&domain.DatabaseInstance{ProjectID: "p", DBType: domain.MySQL, DeploymentMode: domain.ModeK8s}) {
		t.Error("an adapter was registered with no MySQL provisioner")
	}
}
