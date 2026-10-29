//go:build integration

package postgres

import (
	"context"
	"testing"
)

// EXC-672: each followed container's newest stored line survives a restart.
func TestHostLogCursors_SaveReadAndPrune(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if got, err := store.HostLogCursors(ctx); err != nil || len(got) != 0 {
		t.Fatalf("empty = %v %v", got, err)
	}
	if err := store.SaveHostLogCursor(ctx, "db-a", "proj-a", 100); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveHostLogCursor(ctx, "db-b", "proj-b", 200); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveHostLogCursor(ctx, "db-a", "proj-a", 150); err != nil {
		t.Fatal(err)
	}
	// An older save (a reopened follow racing a newer one) never moves a cursor back.
	if err := store.SaveHostLogCursor(ctx, "db-a", "proj-a", 120); err != nil {
		t.Fatal(err)
	}
	got, err := store.HostLogCursors(ctx)
	if err != nil || len(got) != 2 || got["db-a"] != 150 || got["db-b"] != 200 {
		t.Fatalf("cursors = %v %v", got, err)
	}
	if err := store.PruneHostLogCursors(ctx, []string{"db-b", "db-new"}); err != nil {
		t.Fatal(err)
	}
	got, _ = store.HostLogCursors(ctx)
	if len(got) != 1 || got["db-b"] != 200 {
		t.Errorf("after prune = %v", got)
	}
	if err := store.PruneHostLogCursors(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ = store.HostLogCursors(ctx); len(got) != 0 {
		t.Errorf("an engine with no containers keeps no cursors: %v", got)
	}
}
