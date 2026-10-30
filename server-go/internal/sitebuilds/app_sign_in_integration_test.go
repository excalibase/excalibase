//go:build integration

package sitebuilds_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/lib/pq"

	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitebuildstest"
)

// A sign-in returns to the site or the app it started from, never both.
func TestSignIn_RemembersTheAppItStartedFrom(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{AppsOn: true})
	ctx, expires := context.Background(), time.Now().Add(time.Minute)
	appID := h.AddApp(h.Project)
	app := sitebuilds.SignIn{UserID: h.User, ProjectID: h.Project, AppID: appID, BrowserHash: "b-app"}
	site := sitebuilds.SignIn{UserID: h.User, ProjectID: h.Project, SiteID: h.Site.ID, BrowserHash: "b-site"}
	for state, signIn := range map[string]sitebuilds.SignIn{"state-app-" + appID: app, "state-site-" + appID: site} {
		if err := h.Store.SaveSignIn(ctx, state, signIn, expires); err != nil {
			t.Fatal(err)
		}
		taken, err := h.Store.TakeSignIn(ctx, state, time.Now())
		if err != nil || taken != signIn {
			t.Errorf("%s: took %+v, want %+v: %v", state, taken, signIn, err)
		}
	}
	const insert = `INSERT INTO site_git_states (state_hash, user_id, project_id, site_id, app_id, browser_hash, expires_at)
		VALUES ($1, 'u', 'p', $2, $3, 'b', NOW() + INTERVAL '1 minute')`
	for name, target := range map[string][2]any{"both": {"s", "a"}, "neither": {nil, nil}} {
		_, err := h.SQL().Exec(insert, "state-"+name+"-"+appID, target[0], target[1])
		var refused *pq.Error
		if !errors.As(err, &refused) || refused.Code != checkViolation {
			t.Errorf("a sign-in naming %s: %v", name, err)
		}
	}
	checkMigrationDownAndUp(t, h, "000111_app_git_sign_ins", `SELECT COUNT(*) FROM information_schema.columns
		WHERE table_name = 'site_git_states' AND column_name = 'app_id'`)
}

// checkMigrationDownAndUp runs a migration's down then up in a transaction it rolls back; probe counts what down must remove.
func checkMigrationDownAndUp(t *testing.T, h *sitebuildstest.Harness, name, probe string) {
	t.Helper()
	read := func(direction string) string {
		raw, err := os.ReadFile("../storage/postgres/migrations/" + name + "." + direction + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	tx, err := h.SQL().Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(read("down")); err != nil {
		t.Fatalf("down: %v", err)
	}
	var left int
	if err := tx.QueryRow(probe).Scan(&left); err != nil || left != 0 {
		t.Errorf("down left %d: %v", left, err)
	}
	if _, err := tx.Exec(read("up")); err != nil {
		t.Fatalf("up again: %v", err)
	}
}
