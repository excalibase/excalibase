package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/byoc"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

func migrationDialFixture(t *testing.T, mode domain.DeploymentMode) *MigrationService {
	t.Helper()
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	store.Create(&domain.DatabaseInstance{
		ProjectID: "proj-dial", Namespace: "ns", Status: "ACTIVE", DeploymentMode: mode,
	})
	vault := newFakeVault()
	vault.Put("projects/proj-dial/credentials/excalibase_app", map[string]string{
		"host": "127.0.0.1", "port": "1", "username": "u", "password": "p", "database": "d",
	})
	return NewMigrationService(store, vault, dir)
}

func TestMigrationService_OpenTenantDB_BYOCDialsThroughGuard(t *testing.T) {
	svc := migrationDialFixture(t, domain.ModeBYOC)
	db, err := svc.openTenantDB("proj-dial")
	if err != nil {
		t.Fatalf("openTenantDB = %v", err)
	}
	defer db.Close()
	if err := db.PingContext(context.Background()); !errors.Is(err, byoc.ErrInternalAddress) {
		t.Fatalf("BYOC migration ping = %v, want ErrInternalAddress", err)
	}
}

func TestMigrationService_OpenTenantDB_ManagedIsNotGuarded(t *testing.T) {
	svc := migrationDialFixture(t, domain.ModeK8s)
	db, err := svc.openTenantDB("proj-dial")
	if err != nil {
		t.Fatalf("openTenantDB = %v", err)
	}
	defer db.Close()
	if err := db.PingContext(context.Background()); err == nil || errors.Is(err, byoc.ErrInternalAddress) {
		t.Fatalf("managed migration ping = %v, want a plain connection error", err)
	}
}
