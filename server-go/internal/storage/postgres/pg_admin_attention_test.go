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

// pageAllSuspended reads every page of the window, 20 at a time, until the cursor runs out.
func pageAllSuspended(t *testing.T, console *AdminConsole, query adminconsole.AttentionQuery) (rows []adminconsole.SuspendedBrief, pages int) {
	t.Helper()
	for {
		page, err := console.ListSuspended(context.Background(), query)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		if page.Total != 60 {
			t.Fatalf("total = %d, want 60 (the 72-hour-old suspension is outside the window)", page.Total)
		}
		rows = append(rows, page.Items...)
		if page.NextCursor == "" {
			return rows, pages
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
}

func requireEachOnce(t *testing.T, ids []string, want int) {
	t.Helper()
	unique := map[string]bool{}
	for _, id := range ids {
		if unique[id] {
			t.Fatalf("%s appeared on two pages", id)
		}
		unique[id] = true
	}
	if len(ids) != want {
		t.Fatalf("got %d rows, want %d: %v", len(ids), want, ids)
	}
}

func requireNewestFirst(t *testing.T, rows []adminconsole.SuspendedBrief) {
	t.Helper()
	for i := 1; i < len(rows); i++ {
		prev, cur := rows[i-1], rows[i]
		if cur.SuspendedAt.After(prev.SuspendedAt) || (cur.SuspendedAt.Equal(prev.SuspendedAt) && cur.UserID >= prev.UserID) {
			t.Fatalf("rows %d and %d are out of order: %+v then %+v", i-1, i, prev, cur)
		}
	}
}

func TestSuspendedPagesHaveStableOrderNoGapsNoDuplicates(t *testing.T) {
	store := testStore(t)
	seedSuspensions(t, store, 60)
	rows, pages := pageAllSuspended(t, store.AdminConsole(), adminconsole.AttentionQuery{Since: time.Now().Add(-24 * time.Hour), Limit: 20})
	ids := make([]string, len(rows))
	for i, row := range rows {
		ids[i] = row.UserID
	}
	requireEachOnce(t, ids, 60)
	if pages != 3 {
		t.Fatalf("got %d pages, want 3", pages)
	}
	requireNewestFirst(t, rows)
}

// seedFailedProjects adds 7 failed projects; the last three have no change time.
func seedFailedProjects(t *testing.T, console *AdminConsole) {
	t.Helper()
	for i := 0; i < 7; i++ {
		updated := any(time.Now().Add(-time.Duration(i/2) * time.Minute))
		if i >= 4 {
			updated = nil
		}
		if _, err := console.db.ExecContext(context.Background(),
			`INSERT INTO database_instances (project_id, org_id, owner_id, status, project_name, namespace, storage_size, no_database, created_at, updated_at)
			 VALUES ($1, 'o-cust', '', 'FAILED', $1, 'ns-'||$1, '1Gi', FALSE, NOW(), $2)`, fmt.Sprintf("f-%d", i), updated); err != nil {
			t.Fatal(err)
		}
	}
}

func pageAllFailed(t *testing.T, console *AdminConsole) []string {
	t.Helper()
	query := adminconsole.AttentionQuery{Limit: 3}
	var ids []string
	for pages := 0; pages < 5; pages++ {
		page, err := console.ListFailedProjects(context.Background(), query)
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
			return ids
		}
		cursor, err := adminconsole.DecodeCursor(page.NextCursor)
		if err != nil {
			t.Fatal(err)
		}
		query.After = &cursor
	}
	return ids
}

func TestFailedProjectsPageAcrossRowsWithAndWithoutAChangeTime(t *testing.T) {
	_, console := consoleFixture(t)
	seedFailedProjects(t, console)
	ids := pageAllFailed(t, console)
	requireEachOnce(t, ids, 7)
	if ids[5] != "f-5" || ids[6] != "f-4" {
		t.Fatalf("the projects with no change time come last, by id descending: %v", ids)
	}
}

func TestSuspendedCursorOrderIsServedByAnIndex(t *testing.T) {
	store := testStore(t)
	var found string
	if err := store.db.QueryRowContext(context.Background(), `SELECT indexname FROM pg_indexes WHERE tablename = 'abuse_suspensions' AND indexdef LIKE '%suspended_at DESC, user_id DESC%'`).Scan(&found); err != nil {
		t.Fatalf("no index serves the (suspended_at, user_id) order: %v", err)
	}
}
