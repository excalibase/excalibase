package projectdb

import (
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func mysqlStatsCreds() map[string]map[string]string {
	creds := mysqlCreds()
	app := maps.Clone(creds["projects/proj_my/credentials/admin"])
	app["username"] = "excalibase_app"
	creds["projects/proj_my/credentials/excalibase_app"] = app
	return creds
}

// EXC-654: the statement digests are read as the platform login, which alone
// is granted them; the customer's admin is never handed that pool.
func TestOpenMySQLStats_OpensThePlatformLoginInAPoolOfItsOwn(t *testing.T) {
	o := NewOpener(testStore(t, &domain.DatabaseInstance{ProjectID: "proj_my", Status: "ACTIVE", DBType: domain.MySQL}),
		fakeVault{data: mysqlStatsCreds()}, Overrides{}, PoolLimits{})
	defer o.Close()
	stats, err := o.OpenMySQLStats(context.Background(), "proj_my")
	if err != nil {
		t.Fatal(err)
	}
	single, err := o.OpenMySQL(context.Background(), "proj_my", false)
	if err != nil {
		t.Fatal(err)
	}
	if stats == single || o.CachedPools() != 2 {
		t.Fatalf("stats pool shared with admin's: cached %d", o.CachedPools())
	}
	o.Evict("proj_my")
	if o.CachedPools() != 0 {
		t.Fatalf("evict left %d pools", o.CachedPools())
	}
}

func TestOpenMySQLStats_RefusesAnythingButThePlatformLogin(t *testing.T) {
	creds := mysqlStatsCreds()
	creds["projects/proj_my/credentials/excalibase_app"]["username"] = "admin"
	o := NewOpener(testStore(t, &domain.DatabaseInstance{ProjectID: "proj_my", Status: "ACTIVE", DBType: domain.MySQL}),
		fakeVault{data: creds}, Overrides{}, PoolLimits{})
	defer o.Close()
	if _, err := o.OpenMySQLStats(context.Background(), "proj_my"); err == nil {
		t.Fatal("a record that is not excalibase_app opened the stats pool")
	}
}

func TestOpenMySQLStats_RefusesAPostgresProject(t *testing.T) {
	o := NewOpener(testStore(t, &domain.DatabaseInstance{ProjectID: "proj_a", Status: "ACTIVE"}),
		fakeVault{data: appCreds()}, Overrides{}, PoolLimits{})
	defer o.Close()
	if _, err := o.OpenMySQLStats(context.Background(), "proj_a"); !errors.Is(err, ErrNotMySQL) {
		t.Fatalf("err = %v", err)
	}
}
