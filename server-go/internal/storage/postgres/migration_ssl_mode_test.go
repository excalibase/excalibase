package postgres

import (
	"os"
	"strings"
	"testing"
)

// EXC-870: single-host rows created before sslMode was recorded carry an empty
// one. The backfill touches only those and can run twice.
func TestBackfillSSLModeTouchesOnlySingleHostRowsWithNone(t *testing.T) {
	raw, err := os.ReadFile("migrations/000114_single_host_ssl_mode.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	statements := strings.Count(string(raw), ";")
	sql := strings.Join(strings.Fields(string(raw)), " ")
	if statements != 1 || !strings.HasSuffix(sql, "UPDATE database_instances SET ssl_mode = 'disable' WHERE deployment_mode = 'docker' AND (ssl_mode IS NULL OR ssl_mode = '');") {
		t.Fatalf("migration is not the one guarded update: %q", sql)
	}
}
