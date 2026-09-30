//go:build integration

package service

import (
	"context"
	"database/sql"
	"testing"
)

// realtimeTestDB provisions a database the way production does: the project
// owner role (CNPG's `app`) owns the database and the user tables, provisioning
// runs the role SQL as the superuser, and the service connects as
// excalibase_app, the role the realtime API dials with.
func realtimeTestDB(t *testing.T) *sql.DB {
	t.Helper()
	pg := startRolePostgres(t)
	pg.psql(t, "CREATE ROLE app LOGIN PASSWORD 'oP'; ALTER DATABASE app OWNER TO app")
	pg.psql(t, BuildProjectRoleSQL("aP", "eP", "wP", "app", "cdc_watcher_pub"))
	pg.psql(t, `
SET ROLE app;
CREATE TABLE public.posts    (id UUID PRIMARY KEY DEFAULT gen_random_uuid(), title TEXT);
CREATE TABLE public.comments (id UUID PRIMARY KEY DEFAULT gen_random_uuid(), body  TEXT);
RESET ROLE;
CREATE SCHEMA nosql AUTHORIZATION excalibase_app;
SET ROLE excalibase_app;
CREATE TABLE nosql.notes (id UUID PRIMARY KEY DEFAULT gen_random_uuid(), data JSONB);
CREATE TABLE excalibase.jobs (id int PRIMARY KEY);
RESET ROLE;
SET ROLE auth_admin;
CREATE TABLE auth.users (id int PRIMARY KEY, password_hash text);
`)
	return pg.connect(t, "excalibase_app", "eP")
}

// TestRealtimeService_ListTables_AllDisabledInitially asserts the bulk-page
// listing reflects an empty-publication start state: every user table
// shows enabled=false because Phase 1 made the publication empty by default.
func TestRealtimeService_ListTables_AllDisabledInitially(t *testing.T) {
	db := realtimeTestDB(t)
	svc := NewRealtimeService(db)

	tables, err := svc.ListTables(context.Background())
	if err != nil {
		t.Fatalf("ListTables: %v", err)
	}
	if len(tables) != 3 {
		t.Fatalf("want 3 user tables, got %d (%v)", len(tables), tables)
	}
	for _, tt := range tables {
		if tt.Enabled {
			t.Errorf("table %s.%s should start disabled", tt.Schema, tt.Table)
		}
	}
}

func TestRealtimeService_EnableTable_AddsToPublication(t *testing.T) {
	db := realtimeTestDB(t)
	svc := NewRealtimeService(db)
	ctx := context.Background()

	if err := svc.EnableTable(ctx, "public", "posts"); err != nil {
		t.Fatalf("EnableTable: %v", err)
	}

	tables, _ := svc.ListTables(ctx)
	enabledMap := map[string]bool{}
	for _, tt := range tables {
		enabledMap[tt.Schema+"."+tt.Table] = tt.Enabled
	}
	if !enabledMap["public.posts"] {
		t.Error("public.posts should be enabled after EnableTable")
	}
	if enabledMap["public.comments"] {
		t.Error("public.comments should still be disabled — only posts was toggled")
	}
}

func TestRealtimeService_EnableTable_IdempotentOnDoubleAdd(t *testing.T) {
	db := realtimeTestDB(t)
	svc := NewRealtimeService(db)
	ctx := context.Background()

	if err := svc.EnableTable(ctx, "public", "posts"); err != nil {
		t.Fatalf("first EnableTable: %v", err)
	}
	if err := svc.EnableTable(ctx, "public", "posts"); err != nil {
		t.Errorf("second EnableTable should be idempotent, got: %v", err)
	}
}

func TestRealtimeService_DisableTable_RemovesFromPublication(t *testing.T) {
	db := realtimeTestDB(t)
	svc := NewRealtimeService(db)
	ctx := context.Background()

	_ = svc.EnableTable(ctx, "public", "posts")
	if err := svc.DisableTable(ctx, "public", "posts"); err != nil {
		t.Fatalf("DisableTable: %v", err)
	}
	tables, _ := svc.ListTables(ctx)
	for _, tt := range tables {
		if tt.Schema == "public" && tt.Table == "posts" && tt.Enabled {
			t.Error("posts should be disabled after DisableTable")
		}
	}
}

func TestRealtimeService_DisableTable_IdempotentOnMissing(t *testing.T) {
	db := realtimeTestDB(t)
	svc := NewRealtimeService(db)

	// Drop a table that was never added — must not error.
	if err := svc.DisableTable(context.Background(), "public", "comments"); err != nil {
		t.Errorf("DisableTable on never-added table should be idempotent, got: %v", err)
	}
}

func TestRealtimeService_EnableAll_AddsEveryUserTable(t *testing.T) {
	db := realtimeTestDB(t)
	svc := NewRealtimeService(db)
	ctx := context.Background()

	count, err := svc.EnableAll(ctx)
	if err != nil {
		t.Fatalf("EnableAll: %v", err)
	}
	if count != 3 {
		t.Errorf("want 3 added, got %d", count)
	}
	tables, _ := svc.ListTables(ctx)
	for _, tt := range tables {
		if !tt.Enabled {
			t.Errorf("table %s.%s should be enabled after EnableAll", tt.Schema, tt.Table)
		}
	}
}

func TestRealtimeService_DisableAll_RemovesEveryTable(t *testing.T) {
	db := realtimeTestDB(t)
	svc := NewRealtimeService(db)
	ctx := context.Background()

	_, _ = svc.EnableAll(ctx)
	count, err := svc.DisableAll(ctx)
	if err != nil {
		t.Fatalf("DisableAll: %v", err)
	}
	if count != 3 {
		t.Errorf("want 3 dropped, got %d", count)
	}
	tables, _ := svc.ListTables(ctx)
	for _, tt := range tables {
		if tt.Enabled {
			t.Errorf("table %s.%s should be disabled after DisableAll", tt.Schema, tt.Table)
		}
	}
}

// TestRealtimeService_ListTables_ExcludesSystemSchemas asserts internal
// schemas (auth, pg_*, information_schema) never appear in the bulk list.
// Toggling auth.users on by mistake would expose password hashes via CDC.
func TestRealtimeService_ListTables_ExcludesSystemSchemas(t *testing.T) {
	db := realtimeTestDB(t)
	svc := NewRealtimeService(db)

	tables, _ := svc.ListTables(context.Background())
	for _, tt := range tables {
		switch tt.Schema {
		case "auth", "information_schema":
			t.Errorf("system schema %s.%s leaked into Realtime listing", tt.Schema, tt.Table)
		}
		if len(tt.Schema) >= 3 && tt.Schema[:3] == "pg_" {
			t.Errorf("pg_ system schema %s.%s leaked into Realtime listing", tt.Schema, tt.Table)
		}
	}
}

func TestRealtimeService_ListTables_ExcludesPlatformSchemas(t *testing.T) {
	db := realtimeTestDB(t)
	tables, err := NewRealtimeService(db).ListTables(context.Background())
	if err != nil {
		t.Fatalf("ListTables: %v", err)
	}
	for _, tt := range tables {
		if tt.Schema == "excalibase" {
			t.Errorf("platform table %s.%s offered for Realtime", tt.Schema, tt.Table)
		}
	}
}

func TestRealtimeService_EnableTable_RefusesPlatformAndAuthTables(t *testing.T) {
	db := realtimeTestDB(t)
	svc := NewRealtimeService(db)
	ctx := context.Background()

	for _, ref := range [][2]string{{"auth", "users"}, {"excalibase", "jobs"}, {"public", "missing"}, {"pg_catalog", "pg_authid"}} {
		if err := svc.EnableTable(ctx, ref[0], ref[1]); err == nil {
			t.Errorf("%s.%s must not be publishable", ref[0], ref[1])
		}
	}
	var published int
	if err := db.QueryRow(`SELECT count(*) FROM pg_publication_tables WHERE pubname = 'cdc_watcher_pub'`).Scan(&published); err != nil {
		t.Fatalf("count published: %v", err)
	}
	if published != 0 {
		t.Errorf("published %d tables, want 0", published)
	}
}

func TestRealtimeService_AppRoleCannotAlterThePublicationDirectly(t *testing.T) {
	db := realtimeTestDB(t)
	for _, stmt := range []string{
		`ALTER PUBLICATION cdc_watcher_pub ADD TABLE nosql.notes`,
		`ALTER PUBLICATION cdc_watcher_pub RENAME TO other_pub`,
		`DROP PUBLICATION cdc_watcher_pub`,
	} {
		if _, err := db.Exec(stmt); err == nil {
			t.Errorf("excalibase_app ran %q; only the realtime functions may change the publication", stmt)
		}
	}
}

func replicaIdentity(t *testing.T, db *sql.DB, schemaName, table string) string {
	t.Helper()
	var identity string
	err := db.QueryRow(`SELECT c.relreplident::text FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname = $2`, schemaName, table).Scan(&identity)
	if err != nil {
		t.Fatalf("replica identity of %s.%s: %v", schemaName, table, err)
	}
	return identity
}

// TestRealtimeService_EnabledTablesPublishCompleteOldRows asserts enabling a
// table sets REPLICA IDENTITY FULL (f), so a delete carries the whole old row,
// and disabling it restores DEFAULT (d); other tables are left as they were.
func TestRealtimeService_EnabledTablesPublishCompleteOldRows(t *testing.T) {
	db := realtimeTestDB(t)
	svc := NewRealtimeService(db)
	ctx := context.Background()
	if _, err := db.Exec(`CREATE TABLE public."MixedCase" (id int PRIMARY KEY)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	for _, table := range []string{"posts", "MixedCase"} {
		if err := svc.EnableTable(ctx, "public", table); err != nil {
			t.Fatalf("EnableTable %s: %v", table, err)
		}
		if err := svc.EnableTable(ctx, "public", table); err != nil {
			t.Fatalf("second EnableTable %s: %v", table, err)
		}
		if got := replicaIdentity(t, db, "public", table); got != "f" {
			t.Errorf("public.%s replica identity = %q after enable, want f", table, got)
		}
	}
	if got := replicaIdentity(t, db, "public", "comments"); got != "d" {
		t.Errorf("public.comments replica identity = %q, want d (never enabled)", got)
	}

	if err := svc.DisableTable(ctx, "public", "posts"); err != nil {
		t.Fatalf("DisableTable: %v", err)
	}
	if got := replicaIdentity(t, db, "public", "posts"); got != "d" {
		t.Errorf("public.posts replica identity = %q after disable, want d", got)
	}
}

// TestRealtimeService_RefusedTablesKeepTheirReplicaIdentity asserts the
// function still touches only user tables: a refused auth or platform table
// is neither published nor altered.
func TestRealtimeService_RefusedTablesKeepTheirReplicaIdentity(t *testing.T) {
	db := realtimeTestDB(t)
	svc := NewRealtimeService(db)

	for _, ref := range [][2]string{{"auth", "users"}, {"excalibase", "jobs"}} {
		if err := svc.EnableTable(context.Background(), ref[0], ref[1]); err == nil {
			t.Errorf("%s.%s must not be publishable", ref[0], ref[1])
		}
		if got := replicaIdentity(t, db, ref[0], ref[1]); got != "d" {
			t.Errorf("%s.%s replica identity = %q, want d (untouched)", ref[0], ref[1], got)
		}
	}
}
