//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"
)

// EXC-588: each sweep reads the previous reading and stores the new one.
func TestDatabaseWork_StoresTheLastReading(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	saveActivityProject(t, store, "work-1")
	at := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	if _, known, err := store.DatabaseWork(ctx, "work-1"); err != nil || known {
		t.Fatalf("before any reading: known=%v err=%v", known, err)
	}
	for _, work := range []int64{120, 125} {
		if err := store.SaveDatabaseWork(ctx, "work-1", work, at); err != nil {
			t.Fatalf("save %d: %v", work, err)
		}
	}
	if work, known, err := store.DatabaseWork(ctx, "work-1"); err != nil || !known || work != 125 {
		t.Fatalf("last reading: %d known=%v err=%v", work, known, err)
	}
}

// The reading goes with its project.
func TestDatabaseWork_CascadesWithTheProject(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	saveActivityProject(t, store, "work-2")
	if err := store.SaveDatabaseWork(ctx, "work-2", 7, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM database_instances WHERE project_id = 'work-2'`); err != nil {
		t.Fatal(err)
	}
	var rows int
	_ = store.db.QueryRowContext(ctx, `SELECT count(*) FROM project_database_work WHERE project_id = 'work-2'`).Scan(&rows)
	if rows != 0 {
		t.Errorf("%d readings outlived the project", rows)
	}
}
