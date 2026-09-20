package service

import (
	"strings"
	"testing"
)

// TestMigration_TenantDSNUsesAppRole pins SEC-C2: migrations connect to the
// tenant database as the non-superuser excalibase_app role (which has CREATE
// on the database for DDL), never as the postgres superuser. Running as
// postgres allowed COPY ... TO PROGRAM = OS command execution.
func TestMigration_TenantDSNUsesAppRole(t *testing.T) {
	creds := map[string]string{
		"host": "db.internal", "port": "5432",
		"username": "excalibase_app", "password": "s3cr3t",
		"database": "app",
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
	if !strings.Contains(dsn, "sslmode='require'") {
		t.Errorf("default sslmode should be require; got %q", dsn)
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
