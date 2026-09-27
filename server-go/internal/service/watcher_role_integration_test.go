//go:build integration

package service

import (
	"testing"
)

// The statements the CDC watcher runs for its slot owner registry, verbatim:
// unqualified, so the role's search_path decides where the table lives.
const (
	watcherRegistryDDL = `CREATE TABLE IF NOT EXISTS excalibase_cdc_slot_owners (
	slot_name    text PRIMARY KEY,
	owner_id     text NOT NULL,
	heartbeat_at timestamptz NOT NULL DEFAULT now(),
	released_at  timestamptz
)`
	watcherRegistryUpsert = `INSERT INTO excalibase_cdc_slot_owners (slot_name, owner_id, heartbeat_at, released_at)
VALUES ('cdc_watcher', 'pod-1', now(), NULL)
ON CONFLICT (slot_name) DO UPDATE
	SET owner_id = EXCLUDED.owner_id, heartbeat_at = now(), released_at = NULL`
)

func TestProjectRoleSQL_WatcherKeepsItsSlotRegistryOutsidePublic(t *testing.T) {
	pg := startRolePostgres(t)
	pg.psql(t, BuildProjectRoleSQL("aP", "eP", "wP", "app", "cdc_watcher_pub"))
	watcher := pg.connect(t, roleWatcher, "wP")

	for _, stmt := range []string{watcherRegistryDDL, watcherRegistryUpsert, watcherRegistryUpsert} {
		if _, err := watcher.Exec(stmt); err != nil {
			t.Fatalf("watcher registry statement failed: %v\n%s", err, stmt)
		}
	}

	admin := pg.connect(t, "postgres", "p")
	var schemaName string
	if err := admin.QueryRow(`SELECT n.nspname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relname = 'excalibase_cdc_slot_owners'`).Scan(&schemaName); err != nil {
		t.Fatalf("locate registry: %v", err)
	}
	if schemaName != "excalibase_cdc" {
		t.Errorf("registry lives in %s, want excalibase_cdc", schemaName)
	}
}

func TestProjectRoleSQL_WatcherHasNoWriteAccessBeyondItsSchema(t *testing.T) {
	pg := startRolePostgres(t)
	pg.psql(t, BuildProjectRoleSQL("aP", "eP", "wP", "app", "cdc_watcher_pub"))
	admin := pg.connect(t, "postgres", "p")

	privileges := []struct {
		check string
		want  bool
	}{
		{`has_schema_privilege('cdc_watcher', 'public', 'CREATE')`, false},
		{`has_schema_privilege('cdc_watcher', 'auth', 'USAGE')`, false},
		{`has_schema_privilege('cdc_watcher', 'excalibase', 'USAGE')`, false},
		{`has_schema_privilege('cdc_watcher', 'excalibase_cdc', 'CREATE')`, true},
		{`has_schema_privilege('excalibase_app', 'excalibase_cdc', 'USAGE')`, false},
		{`has_schema_privilege('auth_admin', 'excalibase_cdc', 'USAGE')`, false},
	}
	for _, p := range privileges {
		var got bool
		if err := admin.QueryRow("SELECT " + p.check).Scan(&got); err != nil {
			t.Fatalf("%s: %v", p.check, err)
		}
		if got != p.want {
			t.Errorf("%s = %v, want %v", p.check, got, p.want)
		}
	}
}
