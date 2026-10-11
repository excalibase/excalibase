package main

import (
	"testing"

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
