package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
