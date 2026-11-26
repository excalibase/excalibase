//go:build integration

package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/adminconsole"
)

func seedSuspensions(t *testing.T, store *Store, count int) {
	t.Helper()
	base := time.Now().Add(-time.Hour)
	for i := 0; i < count; i++ {
		// Every fifth row shares its timestamp with the one before, so the id has to break ties.
		at := base.Add(time.Duration(i-i%5) * time.Second)
		if _, err := store.db.ExecContext(context.Background(),
			`INSERT INTO abuse_suspensions (user_id, email, reason, suspended_at) VALUES ($1, $2, 'proxy', $3)`,
			fmt.Sprintf("u-%03d", i), fmt.Sprintf("u%d@x.test", i), at); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.ExecContext(context.Background(),
		`INSERT INTO abuse_suspensions (user_id, email, reason, suspended_at) VALUES ('u-old', 'old@x.test', 'proxy', $1)`, time.Now().Add(-72*time.Hour)); err != nil {
		t.Fatal(err)
	}
}

func TestSuspendedPagesHaveStableOrderNoGapsNoDuplicates(t *testing.T) {
	store := testStore(t)
	seedSuspensions(t, store, 60)
	console := store.AdminConsole()
	ctx := context.Background()
	query := adminconsole.AttentionQuery{Since: time.Now().Add(-24 * time.Hour), Limit: 20}
	seen := map[string]bool{}
	var order []adminconsole.SuspendedBrief
	pages := 0
	for {
		page, err := console.ListSuspended(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		if page.Total != 60 {
			t.Fatalf("total = %d, want 60 (the 72-hour-old suspension is outside the window)", page.Total)
		}
		for _, item := range page.Items {
			if seen[item.UserID] {
				t.Fatalf("%s appeared on two pages", item.UserID)
			}
			seen[item.UserID] = true
			order = append(order, item)
		}
		if page.NextCursor == "" {
			break
		}
		cursor, err := adminconsole.DecodeCursor(page.NextCursor)
		if err != nil {
			t.Fatal(err)
		}
		query.After = &cursor
		if pages > 10 {
			t.Fatal("paging does not end")
		}
	}
	if len(order) != 60 || pages != 3 {
		t.Fatalf("got %d rows in %d pages, want 60 in 3", len(order), pages)
	}
	for i := 1; i < len(order); i++ {
		prev, cur := order[i-1], order[i]
		if cur.SuspendedAt.After(prev.SuspendedAt) || (cur.SuspendedAt.Equal(prev.SuspendedAt) && cur.UserID >= prev.UserID) {
			t.Fatalf("rows %d and %d are out of order: %+v then %+v", i-1, i, prev, cur)
		}
	}
}

func TestFailedProjectsPageAcrossRowsWithAndWithoutAChangeTime(t *testing.T) {
	_, console := consoleFixture(t)
	store := console.db
	ctx := context.Background()
	for i := 0; i < 7; i++ {
		updated := any(time.Now().Add(-time.Duration(i/2) * time.Minute))
		if i >= 4 {
			updated = nil
		}
		if _, err := store.ExecContext(ctx,
			`INSERT INTO database_instances (project_id, org_id, owner_id, status, project_name, namespace, storage_size, no_database, created_at, updated_at)
			 VALUES ($1, 'o-cust', '', 'FAILED', $1, 'ns-'||$1, '1Gi', FALSE, NOW(), $2)`, fmt.Sprintf("f-%d", i), updated); err != nil {
			t.Fatal(err)
		}
	}
	query := adminconsole.AttentionQuery{Limit: 3}
	var ids []string
	for pages := 0; pages < 5; pages++ {
		page, err := console.ListFailedProjects(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		if page.Total != 7 {
			t.Fatalf("total = %d, want 7", page.Total)
		}
		for _, item := range page.Items {
			ids = append(ids, item.ProjectID)
		}
		if page.NextCursor == "" {
			break
		}
		cursor, err := adminconsole.DecodeCursor(page.NextCursor)
		if err != nil {
			t.Fatal(err)
		}
		query.After = &cursor
	}
	unique := map[string]bool{}
	for _, id := range ids {
		unique[id] = true
	}
	if len(ids) != 7 || len(unique) != 7 {
		t.Fatalf("paged ids = %v, want the 7 failed projects once each", ids)
	}
	if ids[5] != "f-5" && ids[5] != "f-4" || ids[6] != "f-4" && ids[6] != "f-5" {
		t.Fatalf("the two projects with no change time come last, by id descending: %v", ids)
	}
}

func TestSuspendedCursorOrderIsServedByAnIndex(t *testing.T) {
	store := testStore(t)
	var found string
	if err := store.db.QueryRowContext(context.Background(), `SELECT indexname FROM pg_indexes WHERE tablename = 'abuse_suspensions' AND indexdef LIKE '%suspended_at DESC, user_id DESC%'`).Scan(&found); err != nil {
		t.Fatalf("no index serves the (suspended_at, user_id) order: %v", err)
	}
}
