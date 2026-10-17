package main

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
)

// EXC-586: the sweep that keeps MOCO's backup runs waiting for their egress
// starts wherever MOCO serves MySQL, and nowhere else.
func TestMySQLBackupWaitSweepStartsWhereMOCOServesMySQL(t *testing.T) {
	if startMySQLBackupWaitSweep(service.AlwaysLeader{}, nil, bootInstanceStore(t)) {
		t.Error("started without a MOCO provisioner")
	}
	mysql := provisioner.NewMySQLProvisioner(k8s.NewMockClient())
	if !startMySQLBackupWaitSweep(service.AlwaysLeader{}, mysql, bootInstanceStore(t)) {
		t.Error("not started on Kubernetes")
	}
}

func TestMySQLBackupWaitLockLeadsOnlyWhereReplicasRun(t *testing.T) {
	if _, ok := mysqlBackupWaitLock(config.AppConfig{DeploymentMode: "selfhosted"}, nil).(service.AlwaysLeader); !ok {
		t.Error("a single process waits for a lock")
	}
}
