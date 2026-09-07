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
	dsn := buildTenantDSN(creds, dsnOverrides{})

	if !strings.Contains(dsn, "user=excalibase_app") {
		t.Errorf("migration must connect as excalibase_app; got %q", dsn)
	}
	if strings.Contains(dsn, "user=postgres") {
		t.Errorf("migration must NOT connect as the postgres superuser (SEC-C2); got %q", dsn)
	}
	if !strings.Contains(dsn, "sslmode=require") {
		t.Errorf("default sslmode should be require; got %q", dsn)
	}
}

// TestMigration_TenantDSNOverrides covers local-dev overrides (port-forward),
// mirroring the schema handler: an explicit host flips sslmode to disable.
func TestMigration_TenantDSNOverrides(t *testing.T) {
	creds := map[string]string{"host": "vaulthost", "port": "5432", "username": "excalibase_app", "password": "p", "database": "app"}
	dsn := buildTenantDSN(creds, dsnOverrides{host: "127.0.0.1", port: "15432"})
	for _, want := range []string{"host=127.0.0.1", "port=15432", "sslmode=disable"} {
		if !strings.Contains(dsn, want) {
			t.Errorf("expected %q in dsn, got %q", want, dsn)
		}
	}
}
