//go:build integration

package service

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// realtimeTestDB spins up a Postgres container, applies the role-creation
// SQL, and returns a *sql.DB plus cleanup. Tests run as the postgres
// superuser because that mirrors how the studio backend connects when
// performing privileged ops; in production it'd be excalibase_app instead,
// but the service layer is role-agnostic — it just runs ALTER PUBLICATION.
func realtimeTestDB(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()

	pg, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("app"),
		postgres.WithUsername("postgres"),
		postgres.WithPassword("p"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(45*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { pg.Terminate(ctx) })

	host, _ := pg.Host(ctx)
	port, _ := pg.MappedPort(ctx, "5432/tcp")
	dsn := fmt.Sprintf("postgres://postgres:p@%s:%s/app?sslmode=disable", host, port.Port())

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// Apply the same role + publication setup that production provisioning runs.
	if _, err := db.ExecContext(ctx, BuildProjectRoleSQL("aP", "eP", "wP", "app", "cdc_watcher_pub")); err != nil {
		t.Fatalf("apply role SQL: %v", err)
	}
	// Seed user-data tables in public + nosql schemas to mimic post-provision state.
	seedSQL := `
CREATE SCHEMA IF NOT EXISTS nosql;
CREATE TABLE IF NOT EXISTS public.posts    (id UUID PRIMARY KEY DEFAULT gen_random_uuid(), title TEXT);
CREATE TABLE IF NOT EXISTS public.comments (id UUID PRIMARY KEY DEFAULT gen_random_uuid(), body  TEXT);
CREATE TABLE IF NOT EXISTS nosql.notes     (id UUID PRIMARY KEY DEFAULT gen_random_uuid(), data  JSONB);
ALTER TABLE public.posts    OWNER TO excalibase_app;
ALTER TABLE public.comments OWNER TO excalibase_app;
ALTER TABLE nosql.notes     OWNER TO excalibase_app;
`
	if _, err := db.ExecContext(ctx, seedSQL); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return db
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
