//go:build integration

package postgres

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// ADR 0041: a MySQL project records the version it runs, and a minor upgrade
// moves the recorded version with the cluster.
func TestInstances_MySQLVersionRoundTripsAndUpdates(t *testing.T) {
	store := testStore(t)
	inst := instanceRow("proj-mysqlver1", "org-mysql")
	inst.DBType = domain.MySQL
	inst.PostgresVersion = ""
	inst.MySQLVersion = "8.4.9"
	if err := store.Create(inst); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := store.FindByProjectID("proj-mysqlver1")
	if err != nil || got == nil {
		t.Fatalf("FindByProjectID: %v %v", got, err)
	}
	if got.MySQLVersion != "8.4.9" || got.DBType != domain.MySQL {
		t.Fatalf("stored %q %q", got.DBType, got.MySQLVersion)
	}
	got.MySQLVersion = "8.4.10"
	if err := store.Update(got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	again, _ := store.FindByProjectID("proj-mysqlver1")
	if again.MySQLVersion != "8.4.10" {
		t.Fatalf("after upgrade %q", again.MySQLVersion)
	}
}
