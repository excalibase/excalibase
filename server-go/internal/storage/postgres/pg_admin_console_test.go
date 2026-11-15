//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/adminconsole"
	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

// consoleFixture holds a customer who owns a project with two apps, a second
// customer, and a platform admin.
func consoleFixture(t *testing.T) (*Store, *AdminConsole) {
	t.Helper()
	store := testStore(t)
	ctx := context.Background()
	verified := time.Now()
	for _, user := range []domain.User{
		{ID: "u-cust", Username: "cust", Email: "cust@mailinator.test", Role: "user", Active: true, EmailVerifiedAt: &verified},
		{ID: "u-other", Username: "other", Email: "other@example.test", Role: "user", Active: true},
		{ID: "u-admin", Username: "admin", Email: "admin@example.test", Role: "platform_admin", Active: true},
	} {
		user.PasswordHash = testutil.FixturePasswordHash()
		if err := store.CreateUser(ctx, &user); err != nil {
			t.Fatalf("create %s: %v", user.ID, err)
		}
	}
	for _, org := range []*domain.Org{
		{ID: "o-cust", Name: "Cust Co", Slug: "cust-co", Tier: domain.Free, OwnerID: "u-cust"},
		{ID: "o-other", Name: "Other Co", Slug: "other-co", Tier: domain.Free, OwnerID: "u-other"},
	} {
		if err := store.CreateOrg(ctx, org); err != nil {
			t.Fatalf("create org: %v", err)
		}
	}
	for project, org := range map[string]string{"p-cust": "o-cust", "p-other": "o-other"} {
		if _, err := store.db.ExecContext(ctx,
			`INSERT INTO database_instances (project_id, org_id, owner_id, status, project_name, namespace, storage_size, no_database, created_at)
			 VALUES ($1, $2, '', 'ACTIVE', $1, 'ns-'||$1, '1Gi', FALSE, NOW())`, project, org); err != nil {
			t.Fatalf("create project: %v", err)
		}
	}
	for _, app := range []struct{ id, status string }{{"a-run", apphost.StatusRunning}, {"a-stopped", apphost.StatusStopped}} {
		if _, err := store.db.ExecContext(ctx,
			`INSERT INTO apps (project_id, id, name, status, doc) VALUES ('p-cust', $1, $1, $2, '{"replicas":2}'::jsonb)`, app.id, app.status); err != nil {
			t.Fatalf("create app: %v", err)
		}
	}
	return store, store.AdminConsole()
}

func TestAdminConsoleUsersCountWhatTheyOwnAndRun(t *testing.T) {
	_, console := consoleFixture(t)
	page, err := console.ListUsers(context.Background(), adminconsole.UserQuery{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]adminconsole.UserRow{}
	for _, row := range page.Items {
		byID[row.ID] = row
	}
	cust := byID["u-cust"]
	if cust.OrgCount != 1 || cust.ProjectCount != 1 || cust.RunningApps != 1 || !cust.EmailVerified || cust.Status != adminconsole.StatusActive {
		t.Fatalf("customer row = %+v", cust)
	}
	if page.Total != 3 {
		t.Fatalf("total = %d", page.Total)
	}
}

func TestAdminConsoleSearchMatchesEmailDomainAndIP(t *testing.T) {
	store, console := consoleFixture(t)
	ctx := context.Background()
	if err := store.RecordUserLogin(ctx, "u-other", "198.51.100.7", time.Now()); err != nil {
		t.Fatal(err)
	}
	search := func(q string) []string {
		page, err := console.ListUsers(ctx, adminconsole.UserQuery{Search: q, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		for _, row := range page.Items {
			ids = append(ids, row.ID)
		}
		return ids
	}
	if got := search("@mailinator.test"); len(got) != 1 || got[0] != "u-cust" {
		t.Fatalf("domain search = %v", got)
	}
	if got := search("198.51.100.7"); len(got) != 1 || got[0] != "u-other" {
		t.Fatalf("ip search = %v", got)
	}
	if got := search("%"); len(got) != 0 {
		t.Fatalf("a wildcard must match literally, got %v", got)
	}
}

func TestAdminConsoleSuspendHidesTheAccountUnlessAsked(t *testing.T) {
	_, console := consoleFixture(t)
	ctx := context.Background()
	err := console.Suspend(ctx, adminconsole.Suspension{UserID: "u-cust", Email: "cust@mailinator.test", Reason: "proxy", At: time.Now()},
		[]adminconsole.SuspendedApp{{ProjectID: "p-cust", AppID: "a-run", AppName: "a-run", PreviousReplicas: 2}})
	if err != nil {
		t.Fatal(err)
	}
	hidden, _ := console.ListUsers(ctx, adminconsole.UserQuery{Limit: 50})
	for _, row := range hidden.Items {
		if row.ID == "u-cust" {
			t.Fatal("a suspended account must be hidden by default")
		}
	}
	shown, _ := console.ListUsers(ctx, adminconsole.UserQuery{Limit: 50, IncludeSuspended: true})
	found := false
	for _, row := range shown.Items {
		if row.ID == "u-cust" {
			found = true
			if row.Status != adminconsole.StatusSuspended || row.SuspendedReason != "proxy" {
				t.Fatalf("row = %+v", row)
			}
		}
	}
	if !found {
		t.Fatal("includeSuspended must show the account")
	}
	user, _ := store(t, console).FindUserByID(ctx, "u-cust")
	if user.Active {
		t.Fatal("users.active must be false")
	}
	apps, _ := console.SuspendedApps(ctx, "u-cust")
	if len(apps) != 1 || apps[0].PreviousReplicas != 2 {
		t.Fatalf("recorded apps = %+v", apps)
	}
	if err := console.Unsuspend(ctx, "u-cust"); err != nil {
		t.Fatal(err)
	}
	user, _ = store(t, console).FindUserByID(ctx, "u-cust")
	if !user.Active {
		t.Fatal("unsuspend must reactivate")
	}
	ref, _ := console.FindUser(ctx, "u-cust")
	if ref.Suspended {
		t.Fatal("suspension row must be gone")
	}
}

func TestAdminConsoleOrgsShowOwnerStatusAndUsage(t *testing.T) {
	_, console := consoleFixture(t)
	ctx := context.Background()
	if err := console.Suspend(ctx, adminconsole.Suspension{UserID: "u-cust", Reason: "x", At: time.Now()}, nil); err != nil {
		t.Fatal(err)
	}
	page, err := console.ListOrgs(ctx, adminconsole.OrgQuery{Limit: 50, IncludeSuspended: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, org := range page.Items {
		if org.ID == "o-cust" && (org.OwnerStatus != adminconsole.StatusSuspended || org.ProjectCount != 1 || org.RunningApps != 1 || org.OwnerEmail != "cust@mailinator.test") {
			t.Fatalf("org = %+v", org)
		}
	}
	hidden, _ := console.ListOrgs(ctx, adminconsole.OrgQuery{Limit: 50})
	for _, org := range hidden.Items {
		if org.ID == "o-cust" {
			t.Fatal("an org with a suspended owner must be hidden by default")
		}
	}
}

func TestAdminConsoleProjectDetailNamesTheOwner(t *testing.T) {
	_, console := consoleFixture(t)
	core, err := console.ProjectCore(context.Background(), "p-cust")
	if err != nil || core == nil {
		t.Fatalf("core = %+v, %v", core, err)
	}
	if core.OwnerEmail != "cust@mailinator.test" || core.OrgSlug != "cust-co" || core.DBStatus != "ACTIVE" {
		t.Fatalf("core = %+v", core)
	}
	if missing, _ := console.ProjectCore(context.Background(), "nope"); missing != nil {
		t.Fatal("an unknown project is nil")
	}
}

func TestAdminConsoleOverviewListsFailedProjectsAndSignups(t *testing.T) {
	store, console := consoleFixture(t)
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `UPDATE database_instances SET status = 'FAILED', failure_reason = 'no capacity' WHERE project_id = 'p-other'`); err != nil {
		t.Fatal(err)
	}
	overview, err := console.Overview(ctx, time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.FailedProjects) != 1 || overview.FailedProjects[0].FailureReason != "no capacity" || overview.FailedProjects[0].OwnerEmail != "other@example.test" {
		t.Fatalf("failed = %+v", overview.FailedProjects)
	}
	if overview.Signups24h != 3 || len(overview.SignupDomains) != 2 || overview.SignupDomains[0].Domain != "example.test" {
		t.Fatalf("signups = %d %+v", overview.Signups24h, overview.SignupDomains)
	}
}

func TestOwnerSuspendedOrgsNamesOrgsOfSuspendedOwners(t *testing.T) {
	_, console := consoleFixture(t)
	ctx := context.Background()
	if err := console.Suspend(ctx, adminconsole.Suspension{UserID: "u-cust", Reason: "x", At: time.Now()}, nil); err != nil {
		t.Fatal(err)
	}
	orgs, err := console.OwnerSuspendedOrgs(ctx)
	if err != nil || !orgs["o-cust"] || orgs["o-other"] {
		t.Fatalf("orgs = %v, %v", orgs, err)
	}
}

func store(t *testing.T, console *AdminConsole) *Store {
	t.Helper()
	return &Store{db: console.db}
}
