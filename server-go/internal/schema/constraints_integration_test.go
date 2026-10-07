//go:build integration

package schema

import (
	"context"
	"testing"
)

func TestGetCheckConstraintsReportsEachCheckOfTheTable(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()
	ctx := context.Background()
	if _, err := appDB.ExecContext(ctx, `
		CREATE TABLE cards (
			id serial PRIMARY KEY,
			status text NOT NULL CHECK (status IN ('todo', 'doing', 'done')),
			title text NOT NULL,
			CONSTRAINT title_length CHECK (char_length(title) BETWEEN 1 AND 200)
		);
		CREATE TABLE other (n int CHECK (n > 0));`); err != nil {
		t.Fatalf("create tables: %v", err)
	}
	checks, err := NewIntrospector().GetCheckConstraints(ctx, appDB, "public", "cards")
	if err != nil {
		t.Fatalf("GetCheckConstraints: %v", err)
	}
	if len(checks) != 2 {
		t.Fatalf("checks = %+v", checks)
	}
	byName := map[string]CheckConstraint{}
	for _, check := range checks {
		byName[check.Name] = check
	}
	length, ok := byName["title_length"]
	if !ok || length.Definition != "CHECK (((char_length(title) >= 1) AND (char_length(title) <= 200)))" ||
		len(length.Columns) != 1 || length.Columns[0] != "title" {
		t.Errorf("title_length = %+v", length)
	}
	status, ok := byName["cards_status_check"]
	if !ok || len(status.Columns) != 1 || status.Columns[0] != "status" {
		t.Errorf("status check = %+v", status)
	}
	none, err := NewIntrospector().GetCheckConstraints(ctx, appDB, "public", "missing")
	if err != nil || none == nil || len(none) != 0 {
		t.Errorf("a table with no checks answers an empty list: %v %v", none, err)
	}
}

func TestExecuteQueryReturnsTheRowsOfAWriteWithReturning(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()
	ctx := context.Background()
	if _, err := appDB.ExecContext(ctx, `CREATE TABLE notes (id serial PRIMARY KEY, body text)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	introspector := NewIntrospector()
	inserted := introspector.ExecuteQuery(ctx, appDB, "INSERT INTO notes (body) VALUES ('a'), ('b') RETURNING id, body")
	if inserted.Error != "" || len(inserted.Rows) != 2 || len(inserted.Columns) != 2 || inserted.Columns[1].Name != "body" {
		t.Fatalf("insert returning = %+v", inserted)
	}
	updated := introspector.ExecuteQuery(ctx, appDB, "  update notes set body = 'c' where body = 'a'\n returning *")
	if updated.Error != "" || len(updated.Rows) != 1 || updated.Rows[0][1] != "c" {
		t.Fatalf("update returning = %+v", updated)
	}
	deleted := introspector.ExecuteQuery(ctx, appDB, "DELETE FROM notes RETURNING id")
	if deleted.Error != "" || len(deleted.Rows) != 2 {
		t.Fatalf("delete returning = %+v", deleted)
	}
	plain := introspector.ExecuteQuery(ctx, appDB, "INSERT INTO notes (body) VALUES ('returning is just a word here')")
	if plain.Error != "" || plain.Command != "EXEC" || plain.AffectedRows != 1 {
		t.Fatalf("a write without RETURNING reports its count: %+v", plain)
	}
	var count int
	if err := appDB.QueryRowContext(ctx, "SELECT count(*) FROM notes").Scan(&count); err != nil || count != 1 {
		t.Fatalf("the writes must be committed: count=%d err=%v", count, err)
	}
}
