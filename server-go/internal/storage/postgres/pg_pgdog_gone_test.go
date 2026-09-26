//go:build integration

package postgres

import (
	"context"
	"testing"
)

// PgDog is not part of the platform; its route and credential tables must not survive a migration.
func TestPgDogTablesAreGone(t *testing.T) {
	store := testStore(t)
	for _, table := range []string{"pgdog_users", "pgdog_databases"} {
		var exists bool
		err := store.db.QueryRowContext(context.Background(),
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)`, table).Scan(&exists)
		if err != nil {
			t.Fatalf("probe for %s: %v", table, err)
		}
		if exists {
			t.Errorf("%s still exists", table)
		}
	}
}
