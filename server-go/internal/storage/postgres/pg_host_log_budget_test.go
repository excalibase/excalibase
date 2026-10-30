//go:build integration

package postgres

import (
	"context"
	"testing"
)

// EXC-678: a project's day of stored log text survives a restart, adds up
// across batches, keeps its notice, and old days are pruned.
func TestHostLogBudget_AddReadAndPrune(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if bytes, noticed, err := store.HostLogBudgetUse(ctx, "proj-a", "2026-10-30"); err != nil || bytes != 0 || noticed {
		t.Fatalf("empty = %d %v %v", bytes, noticed, err)
	}
	for _, add := range []struct {
		bytes   int64
		noticed bool
	}{{100, false}, {50, false}, {0, true}, {0, false}} {
		if err := store.AddHostLogBudgetUse(ctx, "proj-a", "2026-10-30", add.bytes, add.noticed); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.AddHostLogBudgetUse(ctx, "proj-a", "2026-10-27", 7, false); err != nil {
		t.Fatal(err)
	}
	if err := store.AddHostLogBudgetUse(ctx, "proj-b", "2026-10-30", 9, false); err != nil {
		t.Fatal(err)
	}
	if bytes, noticed, err := store.HostLogBudgetUse(ctx, "proj-a", "2026-10-30"); err != nil || bytes != 150 || !noticed {
		t.Fatalf("proj-a = %d %v %v", bytes, noticed, err)
	}
	if bytes, noticed, _ := store.HostLogBudgetUse(ctx, "proj-b", "2026-10-30"); bytes != 9 || noticed {
		t.Fatalf("proj-b = %d %v", bytes, noticed)
	}
	if err := store.PruneHostLogBudget(ctx, "2026-10-28"); err != nil {
		t.Fatal(err)
	}
	if bytes, _, _ := store.HostLogBudgetUse(ctx, "proj-a", "2026-10-27"); bytes != 0 {
		t.Errorf("an old day survived the prune: %d", bytes)
	}
	if bytes, _, _ := store.HostLogBudgetUse(ctx, "proj-a", "2026-10-30"); bytes != 150 {
		t.Errorf("today was pruned: %d", bytes)
	}
	if _, _, err := store.HostLogBudgetUse(ctx, "proj-a", "not a day"); err == nil {
		t.Error("a malformed day was read")
	}
}
