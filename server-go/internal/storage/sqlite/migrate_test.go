package sqlite

import (
	"path/filepath"
	"testing"
)

func TestMigrateCreatesAllTables(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	store, err := New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()

	// All tables should exist after migration
	tables := []string{
		"users", "access_tokens", "database_instances",
		"database_metrics", "alerts", "migration_records",
		"backup_records", "parameter_groups", "audit_log",
	}
	for _, table := range tables {
		var count int
		err := store.db.QueryRow(
			"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&count)
		if err != nil {
			t.Fatalf("query sqlite_master for %s: %v", table, err)
		}
		if count != 1 {
			t.Errorf("table %s not found", table)
		}
	}
}

func TestMigrateVersion(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	store, err := New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()

	// Should report current migration version
	version, dirty, err := store.MigrationVersion()
	if err != nil {
		t.Fatalf("MigrationVersion: %v", err)
	}
	if version < 1 {
		t.Errorf("version: got %d, want >= 1", version)
	}
	if dirty {
		t.Error("migration should not be dirty")
	}
}

func TestMigrateIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// First open — runs migration
	store1, err := New(dbPath)
	if err != nil {
		t.Fatalf("New (first): %v", err)
	}
	store1.Close()

	// Second open — migration should be idempotent (no error)
	store2, err := New(dbPath)
	if err != nil {
		t.Fatalf("New (second): %v", err)
	}
	store2.Close()
}
