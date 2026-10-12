//go:build integration

package service

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// TestApplyMigration_BackToBackOnRealPostgresBothRunAndBothAreListed applies two migrations back to back
// through the service as the project's app role, the way two apply_migration calls arrive, and pins
// EXC-605: both run and both appear in the history, in the order they were applied.
func TestApplyMigration_BackToBackOnRealPostgresBothRunAndBothAreListed(t *testing.T) {
	pg := startRolePostgres(t)
	pg.psql(t, "CREATE ROLE excalibase_app LOGIN PASSWORD 'app-pass'; GRANT CREATE ON DATABASE app TO excalibase_app; "+
		"GRANT CREATE, USAGE ON SCHEMA public TO excalibase_app;")

	dir := t.TempDir()
	store, err := storage.NewFileSystemStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(&domain.DatabaseInstance{ProjectID: "back-to-back", Namespace: "ns", Status: "ACTIVE"}); err != nil {
		t.Fatal(err)
	}
	vault := newFakeVault()
	vault.Put("projects/back-to-back/credentials/excalibase_app", map[string]string{
		"host": pg.host, "port": pg.port, "username": "excalibase_app", "password": "app-pass", "database": "app",
	})
	svc := NewMigrationService(store, vault, dir)
	svc.overrides = dsnOverrides{host: pg.host, port: pg.port, sslmode: "disable"}
	instant := time.Now()
	svc.now = func() time.Time { return instant }

	for _, step := range []struct{ name, sql string }{
		{"courses", "CREATE TABLE courses (id int primary key)"},
		{"videos", "CREATE TABLE videos (id int primary key, course_id int references courses(id))"},
	} {
		rec, err := svc.ApplyMigration(context.Background(), "back-to-back", domain.MigrationRequest{Name: step.name, SQL: step.sql})
		if err != nil || rec.Status != "APPLIED" {
			t.Fatalf("apply %s: rec=%+v err=%v", step.name, rec, err)
		}
	}

	records, err := svc.ListMigrations("back-to-back")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].Name != "courses" || records[1].Name != "videos" || records[0].ID == records[1].ID {
		t.Fatalf("history = %+v, want courses then videos with distinct ids", records)
	}
	var tables int
	if err := pg.connect(t, "excalibase_app", "app-pass").QueryRow(
		"SELECT count(*) FROM pg_tables WHERE schemaname = 'public' AND tablename IN ('courses','videos')").Scan(&tables); err != nil || tables != 2 {
		t.Fatalf("tables = %d err = %v, want both migrations to have run", tables, err)
	}
}
