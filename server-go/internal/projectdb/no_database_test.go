package projectdb

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// A project without a database has nothing to open; the caller is told so
// rather than handed a pool built from credentials that do not exist.
func TestOpen_RefusesAProjectWithoutADatabase(t *testing.T) {
	o := NewOpener(testStore(t, &domain.DatabaseInstance{ProjectID: "proj_a", Status: "ACTIVE", NoDatabase: true}),
		fakeVault{data: appCreds()}, Overrides{}, PoolLimits{})
	defer o.Close()

	_, err := o.Open(context.Background(), "proj_a")
	if !errors.Is(err, domain.ErrNoDatabase) {
		t.Fatalf("err: got %v, want domain.ErrNoDatabase", err)
	}
	if o.CachedPools() != 0 {
		t.Fatal("a pool was cached for a project with no database")
	}
}

// The function scheduler sweeps every servable project's database; a project
// without one is not swept.
func TestServableProjectIDs_SkipsProjectsWithoutADatabase(t *testing.T) {
	o := NewOpener(testStore(t,
		&domain.DatabaseInstance{ProjectID: "proj_a", Status: "ACTIVE"},
		&domain.DatabaseInstance{ProjectID: "proj_apps", Status: "ACTIVE", NoDatabase: true},
	), fakeVault{data: appCreds()}, Overrides{}, PoolLimits{})
	defer o.Close()

	ids, err := o.ServableProjectIDs(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(ids) != 1 || ids[0] != "proj_a" {
		t.Errorf("servable projects: got %v, want [proj_a]", ids)
	}
}
