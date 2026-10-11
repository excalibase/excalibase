package main

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// EXC-592: a single host backs its MySQL projects up itself.
func TestHostMySQLBackupsAreWiredOnlyWhereMySQLIsServed(t *testing.T) {
	if hostMySQLBackupAdapter(nil, nil, nil, nil, nil, "b", nil) != nil {
		t.Fatal("an adapter without a MySQL provisioner")
	}
	startHostMySQLBackups(nil, nil)
	adapter := hostMySQLBackupAdapter(provisioner.NewDockerMySQLProvisioner(nil), nil, nil, nil, nil, "b", nil)
	if adapter == nil {
		t.Fatal("no adapter for a host that serves MySQL")
	}
}

func TestSingleHostPortsNeedThePlatformStoresAllocator(t *testing.T) {
	if _, err := singleHostPorts(config.AppConfig{}, nil); err == nil {
		t.Fatal("ports without an allocator")
	}
}

// EXC-593: a single host's Postgres backups restore onto held ports and ship
// WAL only where the host has them.
func TestHostPostgresBackupsAreWiredWithTheirPorts(t *testing.T) {
	startHostPostgresWAL(nil, nil)
	adapter := hostPostgresBackupAdapter(nil, nil, nil, nil, nil, "b", nil)
	if adapter == nil || adapter.BackupsConfigured() {
		t.Fatal("an adapter with no store must not claim it can back up")
	}
}
