//go:build integration

package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"
)

const migrationBeforeAdminConsole = 110

// prodAbuseSuspensions is the table as it stands on prod: created by hand,
// before any migration knew about it.
const prodAbuseSuspensions = `
	CREATE TABLE abuse_suspensions (
		user_id text NOT NULL PRIMARY KEY,
		email text,
		reason text,
		suspended_at timestamptz DEFAULT now()
	)`

func columnShape(t *testing.T, store *Store, table string) map[string]string {
	t.Helper()
	rows, err := store.DB().Query(`
		SELECT column_name, data_type || '/' || is_nullable || '/' || COALESCE(column_default, '')
		FROM information_schema.columns WHERE table_name = $1 AND table_schema = current_schema()`, table)
	if err != nil {
		t.Fatalf("read columns: %v", err)
	}
	defer rows.Close()
	shape := map[string]string{}
	for rows.Next() {
		var name, detail string
		if err := rows.Scan(&name, &detail); err != nil {
			t.Fatal(err)
		}
		shape[name] = detail
	}
	return shape
}

// The migration meets prod's hand-made table: it keeps every row, changes no
// column, and the console works over it (EXC-757).
func TestAdminConsoleMigrationConvergesOnProdsHandMadeTable(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if err := store.migrateTo(migrationBeforeAdminConsole); err != nil {
		t.Fatalf("migrate down to %d: %v", migrationBeforeAdminConsole, err)
	}
	if _, err := store.DB().Exec(`DROP TABLE IF EXISTS abuse_suspensions`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(prodAbuseSuspensions); err != nil {
		t.Fatalf("create prod's table: %v", err)
	}
	for i := 0; i < 85; i++ {
		if _, err := store.DB().Exec(`INSERT INTO abuse_suspensions (user_id, email, reason) VALUES ($1, $2, $3)`,
			fmt.Sprintf("u-%03d", i), fmt.Sprintf("bad%03d@mailinator.test", i), "proxy abuse"); err != nil {
			t.Fatalf("seed row %d: %v", i, err)
		}
	}
	// A row prod could hold with no email or reason: the columns are nullable.
	if _, err := store.DB().Exec(`INSERT INTO abuse_suspensions (user_id) VALUES ('u-null')`); err != nil {
		t.Fatal(err)
	}
	before := columnShape(t, store, "abuse_suspensions")

	if err := store.migrateTo(migrationBeforeAdminConsole + 2); err != nil {
		t.Fatalf("the migration failed over prod's table: %v", err)
	}

	if after := columnShape(t, store, "abuse_suspensions"); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatalf("the migration changed prod's columns:\nbefore %v\nafter  %v", before, after)
	}
	var rowsLeft, distinct int
	if err := store.DB().QueryRow(`SELECT count(*), count(DISTINCT user_id) FROM abuse_suspensions`).Scan(&rowsLeft, &distinct); err != nil {
		t.Fatal(err)
	}
	if rowsLeft != 86 || distinct != 86 {
		t.Fatalf("rows = %d, distinct user_ids = %d, want 86 and 86", rowsLeft, distinct)
	}
	if _, err := store.DB().Exec(`INSERT INTO abuse_suspensions (user_id) VALUES ('u-000')`); err == nil {
		t.Fatal("user_id is no longer unique")
	}
	var tables int
	if err := store.DB().QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_name = 'abuse_suspension_apps' AND table_schema = current_schema()`).Scan(&tables); err != nil || tables != 1 {
		t.Fatalf("abuse_suspension_apps missing after the migration (err %v)", err)
	}

	// The console reads a row with no email or reason without failing.
	console := NewAdminConsole(store.DB())
	overview, err := console.Overview(ctx, time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("overview over prod's rows: %v", err)
	}
	_ = overview
}

// A fresh install gets the same shape as prod.
func TestAdminConsoleMigrationOnAFreshInstallMatchesProd(t *testing.T) {
	store := testStore(t)
	got := columnShape(t, store, "abuse_suspensions")
	for column, want := range map[string]string{
		"user_id":      "text/NO/",
		"email":        "text/YES/",
		"reason":       "text/YES/",
		"suspended_at": "timestamp with time zone/YES/now()",
	} {
		if got[column] != want {
			t.Errorf("%s = %q, want %q", column, got[column], want)
		}
	}
}
