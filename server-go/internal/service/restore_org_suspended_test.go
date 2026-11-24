package service

import (
	"context"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// A restore into a new project starts a new database cluster. Wired as in
// production (the provisioning service registers the copy), it runs for an
// organisation whose owner is suspended: nothing on the restore path asks the
// org guard, so any remaining member can spin up fresh compute.
func TestRestoreCopy_RefusedWhileTheOrgOwnerIsSuspended(t *testing.T) {
	prov, store, mock := setupProvisioningTest(t)
	suspendedOwnerGuard(prov, "org1")
	setOrgTier(prov, "org1", domain.Enterprise)
	backups := NewBackupService(store, mock, t.TempDir(), StaticBackupStorage(r2Storage()))
	backups.SetProjectRegistrar(prov)
	backups.SetOrgGuard(prov.orgGuard) // cmd/server hands every service the same guard
	armRestore(backups, mock, store)
	store.Create(&domain.DatabaseInstance{
		ProjectID: "bk-db", OrgID: "org1", Namespace: "org1-bk-db", Status: "ACTIVE",
		DBType: domain.PostgreSQL, PostgresVersion: "17",
	})

	_, err := backups.RestoreFromBackup(context.Background(), "bk-db", domain.RestoreRequest{
		NewProjectName: "bk-db-restored", TargetProjectID: "bk-db-restored",
	})
	t.Logf("restore err = %v", err)
	for _, call := range mock.Calls {
		if strings.Contains(call, "org1-bk-db-restored") {
			t.Fatalf("the restore acted on the cluster for a suspended org: %s (err %v)", call, err)
		}
	}
}
