package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/excalibase/provisioning-poc/internal/adminconsole"
)

// Over a database that cannot be reached every read and write answers an
// error: the console never shows an empty page for a store that is down.
func TestAdminConsoleSurfacesAnUnreachableDatabase(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectClose()
	if err := mockDB.Close(); err != nil {
		t.Fatal(err)
	}
	db := mockDB
	ctx := context.Background()
	console := NewAdminConsole(db)
	calls := map[string]func() error{
		"ListUsers": func() error { _, e := console.ListUsers(ctx, adminconsole.UserQuery{}); return e },
		"ListOrgs":  func() error { _, e := console.ListOrgs(ctx, adminconsole.OrgQuery{}); return e },
		"Overview":  func() error { _, e := console.Overview(ctx, time.Now()); return e },
		"ProjectCore": func() error {
			_, e := console.ProjectCore(ctx, "p1")
			return e
		},
		"ProjectSites":     func() error { _, e := console.ProjectSites(ctx, "p1"); return e },
		"ProjectFunctions": func() error { _, e := console.ProjectFunctions(ctx, "p1"); return e },
		"ProjectAudit":     func() error { _, e := console.ProjectAudit(ctx, "p1", 5); return e },
		"ProjectFacts":     func() error { _, e := console.ProjectFacts(ctx); return e },
		"FindUser":         func() error { _, e := console.FindUser(ctx, "u1"); return e },
		"OwnedApps":        func() error { _, e := console.OwnedApps(ctx, "u1"); return e },
		"Suspend": func() error {
			return console.Suspend(ctx, adminconsole.Suspension{UserID: "u1"}, []adminconsole.SuspendedApp{{ProjectID: "p", AppID: "a"}})
		},
		"Unsuspend":          func() error { return console.Unsuspend(ctx, "u1") },
		"SuspendedApps":      func() error { _, e := console.SuspendedApps(ctx, "u1"); return e },
		"ForgetSuspended":    func() error { return console.ForgetSuspendedApp(ctx, "u1", "p", "a") },
		"OwnerSuspendedOrgs": func() error { _, e := console.OwnerSuspendedOrgs(ctx); return e },
		"RecordUserLogin":    func() error { return (&Store{db: db}).RecordUserLogin(ctx, "u1", "1.2.3.4", time.Now()) },
	}
	for name, call := range calls {
		if err := call(); err == nil {
			t.Errorf("%s answered without an error over a closed database", name)
		}
	}
}
