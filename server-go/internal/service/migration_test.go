package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

func TestListMigrationsReadsZonelessRecordsAsUTCAndSkipsUnreadableOnes(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "projects", "proj-a", "migrations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"001.json":  `{"id":"001","sql":"select 1","status":"APPLIED","appliedAt":"2026-10-07T02:30:00.000000000"}`,
		"002.json":  `{"id":"002","sql":"select 2","status":"APPLIED","appliedAt":"not a time"}`,
		"notes.txt": "ignored",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	records, err := NewMigrationService(nil, nil, root).ListMigrations("proj-a")
	if err != nil || len(records) != 1 || records[0].ID != "001" {
		t.Fatalf("records = %+v err = %v", records, err)
	}
	encoded, _ := json.Marshal(records[0])
	if !strings.Contains(string(encoded), `"appliedAt":"2026-10-07T02:30:00Z"`) {
		t.Fatalf("appliedAt is not written with its zone: %s", encoded)
	}
}

// TestMigration_TenantDSNUsesAppRole pins SEC-C2: migrations connect to the
// tenant database as the non-superuser excalibase_app role (which has CREATE
// on the database for DDL), never as the postgres superuser. Running as
// postgres allowed COPY ... TO PROGRAM = OS command execution.
func TestMigration_TenantDSNUsesAppRole(t *testing.T) {
	creds := map[string]string{
		"host": "db.internal", "port": "5432",
		"username": "excalibase_app", "password": "s3cr3t",
		"database": "app",
		"sslcert":  "CERT-PEM", "sslkey": "KEY-PEM", "sslrootcert": "CA-PEM",
	}
	dsn, err := buildTenantDSN(creds, dsnOverrides{})
	if err != nil {
		t.Fatalf("buildTenantDSN: %v", err)
	}

	if !strings.Contains(dsn, "user='excalibase_app'") {
		t.Errorf("migration must connect as excalibase_app; got %q", dsn)
	}
	if strings.Contains(dsn, "user='postgres'") {
		t.Errorf("migration must NOT connect as the postgres superuser (SEC-C2); got %q", dsn)
	}
	if !strings.Contains(dsn, "sslmode='verify-full'") {
		t.Errorf("default sslmode should be verify-full with the role's certificate; got %q", dsn)
	}
}

// TestMigration_TenantDSNOverrides covers local-dev overrides (port-forward),
// mirroring the schema handler. The mode is stated by the operator: an
// override that names none is refused rather than silently downgraded.
func TestMigration_TenantDSNOverrides(t *testing.T) {
	creds := map[string]string{"host": "vaulthost", "port": "5432", "username": "excalibase_app", "password": "p", "database": "app"}
	if _, err := buildTenantDSN(creds, dsnOverrides{host: "127.0.0.1", port: "15432"}); err == nil {
		t.Error("an override naming no ssl mode must be refused, not downgraded")
	}

	dsn, err := buildTenantDSN(creds, dsnOverrides{host: "127.0.0.1", port: "15432", sslmode: "disable"})
	if err != nil {
		t.Fatalf("buildTenantDSN: %v", err)
	}
	for _, want := range []string{"host='127.0.0.1'", "port='15432'", "sslmode='disable'"} {
		if !strings.Contains(dsn, want) {
			t.Errorf("expected %q in dsn, got %q", want, dsn)
		}
	}
}

// TestApplyMigration_TwoInTheSameSecondAreBothKeptInOrder pins EXC-605: ids were
// mig-<yyyymmdd-hhmmss>, so a second migration within the same second overwrote the first's record.
func TestApplyMigration_TwoInTheSameSecondAreBothKeptInOrder(t *testing.T) {
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	store.Create(&domain.DatabaseInstance{ProjectID: "same-second", Namespace: "ns", Status: "ACTIVE"})
	vault := newFakeVault()
	vault.Put("projects/same-second/credentials/excalibase_app", map[string]string{
		"host": "127.0.0.1", "port": "1", "username": "excalibase_app", "password": "x", "database": "app",
		"sslcert": "CERT-PEM", "sslkey": "KEY-PEM", "sslrootcert": "CA-PEM",
	})
	svc := NewMigrationService(store, vault, dir)
	instant := time.Date(2026, 10, 13, 3, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return instant }

	for _, name := range []string{"first", "second", "third"} {
		if _, err := svc.ApplyMigration(context.Background(), "same-second", domain.MigrationRequest{Name: name, SQL: "select 1"}); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}

	records, err := svc.ListMigrations("same-second")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(records))
	ids := map[string]bool{}
	for _, rec := range records {
		names = append(names, rec.Name)
		ids[rec.ID] = true
	}
	if strings.Join(names, ",") != "first,second,third" || len(ids) != 3 {
		t.Fatalf("history = %v (ids %v), want first,second,third with distinct ids", names, ids)
	}
}

// TestListMigrations_OldSecondPrecisionIdsStillReadInOrder: records written before EXC-605 keep reading,
// ordered with the new ones by time.
func TestListMigrations_OldSecondPrecisionIdsStillReadInOrder(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "projects", "proj-old", "migrations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"mig-20261001-120000.json":           `{"id":"mig-20261001-120000","name":"old","status":"APPLIED"}`,
		"mig-20261013-030000-000000000.json": `{"id":"mig-20261013-030000-000000000","name":"new","status":"APPLIED"}`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	records, err := NewMigrationService(nil, nil, root).ListMigrations("proj-old")
	if err != nil || len(records) != 2 || records[0].Name != "old" || records[1].Name != "new" {
		t.Fatalf("records = %+v err = %v", records, err)
	}
}
