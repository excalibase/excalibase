package service

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

const testMigDB = "mig-db"

func setupMigrationTest(t *testing.T) *MigrationService {
	t.Helper()
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	mock.ExecOutput["org-mig-db/mig-db-postgres-1"] = "CREATE TABLE"

	store.Save(&domain.DatabaseInstance{
		ProjectID: testMigDB, Namespace: "org-mig-db", Status: "ACTIVE",
	})

	return NewMigrationService(store, mock, dir)
}

func TestApplyMigration(t *testing.T) {
	svc := setupMigrationTest(t)
	rec, err := svc.ApplyMigration(context.Background(), testMigDB, domain.MigrationRequest{
		SQL:         "CREATE TABLE test (id int)",
		Description: "test migration",
	})
	if err != nil {
		t.Fatalf("ApplyMigration: %v", err)
	}
	if rec.Status != "APPLIED" {
		t.Errorf("status: got %s, want APPLIED", rec.Status)
	}
	if rec.Output != "CREATE TABLE" {
		t.Errorf("output: got %s", rec.Output)
	}
}

func TestListMigrationsAfterApply(t *testing.T) {
	svc := setupMigrationTest(t)
	svc.ApplyMigration(context.Background(), testMigDB, domain.MigrationRequest{SQL: "SELECT 1"})

	list, _ := svc.ListMigrations(testMigDB)
	if len(list) != 1 {
		t.Errorf("expected 1 migration, got %d", len(list))
	}
}

func TestListMigrationsEmpty(t *testing.T) {
	svc := setupMigrationTest(t)
	list, _ := svc.ListMigrations(testMigDB)
	if len(list) != 0 {
		t.Errorf("expected 0 migrations, got %d", len(list))
	}
}
