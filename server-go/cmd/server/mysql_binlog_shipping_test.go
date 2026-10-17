package main

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
)

// EXC-587: shipping MySQL binlogs between backups ships dark; with the
// feature off neither sweep starts, on it starts where MySQL is served.
func TestMySQLBinlogShippingStartsOnlyWithItsFeature(t *testing.T) {
	kube := service.NewMySQLBackupAdapter(service.MySQLBackupAdapterConfig{Kube: k8s.NewMockClient()})
	host := hostMySQLBackupAdapter(provisioner.NewDockerMySQLProvisioner(nil), nil, nil, nil, nil, "b", nil)
	if started := startMySQLBinlogShipping(features.NewStatic(), service.AlwaysLeader{}, kube, host, bootInstanceStore(t)); started != 0 {
		t.Fatalf("started %d sweeps with the feature off", started)
	}
	on := features.NewStatic(features.MySQLPITR)
	if started := startMySQLBinlogShipping(on, service.AlwaysLeader{}, nil, nil, bootInstanceStore(t)); started != 0 {
		t.Fatalf("started %d sweeps where no MySQL is served", started)
	}
	if started := startMySQLBinlogShipping(on, service.AlwaysLeader{}, kube, nil, bootInstanceStore(t)); started != 1 {
		t.Fatalf("started %d sweeps on Kubernetes, want 1", started)
	}
	if started := startMySQLBinlogShipping(on, service.AlwaysLeader{}, nil, host, bootInstanceStore(t)); started != 1 {
		t.Fatalf("started %d sweeps on a single host, want 1", started)
	}
}

// Replicas side by side in cloud lead the ship by an advisory lock; a single
// process needs none.
func TestMySQLBinlogShipLockLeadsOnlyWhereReplicasRun(t *testing.T) {
	if _, ok := mysqlBinlogShipLock(config.AppConfig{DeploymentMode: "selfhosted"}, nil).(service.AlwaysLeader); !ok {
		t.Error("a single process waits for a lock")
	}
	if _, ok := mysqlBinlogShipLock(config.AppConfig{DeploymentMode: "cloud"}, nil).(service.AlwaysLeader); !ok {
		t.Error("no store, no lock to take")
	}
}
