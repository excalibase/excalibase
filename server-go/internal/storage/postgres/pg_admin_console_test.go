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
	if _, listed := userRow(hidden, "u-cust"); listed {
		t.Fatal("a suspended account must be hidden by default")
	}
	shown, _ := console.ListUsers(ctx, adminconsole.UserQuery{Limit: 50, IncludeSuspended: true})
	row, listed := userRow(shown, "u-cust")
	if !listed {
		t.Fatal("includeSuspended must show the account")
	}
	if row.Status != adminconsole.StatusSuspended || row.SuspendedReason != "proxy" {
		t.Fatalf("row = %+v", row)
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

func TestAdminConsoleProjectFactsNameOrgOwnerAndUsage(t *testing.T) {
	store, console := consoleFixture(t)
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `INSERT INTO database_metrics (project_id, timestamp, cpu_usage_cores, memory_usage_mb) VALUES ('p-cust', NOW(), 0.4, 310)`); err != nil {
		t.Fatal(err)
	}
	if err := console.Suspend(ctx, adminconsole.Suspension{UserID: "u-other", Reason: "x", At: time.Now()}, nil); err != nil {
		t.Fatal(err)
	}
	facts, err := console.ProjectFacts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := facts["p-cust"]; got.OrgName != "Cust Co" || got.OwnerEmail != "cust@mailinator.test" || !got.HasUsage || got.CPUCores != 0.4 || got.MemoryMB != 310 {
		t.Fatalf("p-cust = %+v", got)
	}
	if got := facts["p-other"]; got.OwnerStatus != adminconsole.StatusSuspended || got.HasUsage {
		t.Fatalf("p-other = %+v", got)
	}
}

func TestRecordUserLoginStoresTimeAndAddress(t *testing.T) {
	store, _ := consoleFixture(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Second)
	if err := store.RecordUserLogin(ctx, "u-other", "198.51.100.7", at); err != nil {
		t.Fatal(err)
	}
	var ip string
	var seen time.Time
	if err := store.db.QueryRowContext(ctx, `SELECT last_login_ip, last_login_at FROM users WHERE id = 'u-other'`).Scan(&ip, &seen); err != nil {
		t.Fatal(err)
	}
	if ip != "198.51.100.7" || !seen.Equal(at) {
		t.Fatalf("stored %q at %v", ip, seen)
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

func TestSuspensionTakesDownOnlyWhatTheAccountsOrgsHold(t *testing.T) {
	store, console := consoleFixture(t)
	ctx := context.Background()
	// A project the account made in someone else's org is that org's, not the account's.
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO database_instances (project_id, org_id, owner_id, status, project_name, namespace, storage_size, no_database, created_at)
		 VALUES ('p-made', 'o-other', 'u-cust', 'ACTIVE', 'p-made', 'ns-made', '1Gi', FALSE, NOW())`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO apps (project_id, id, name, status, doc) VALUES ('p-made', 'a-made', 'a-made', 'ACTIVE', '{"replicas":1}'::jsonb)`); err != nil {
		t.Fatal(err)
	}
	projects, err := console.OwnedProjects(ctx, "u-cust")
	if err != nil || len(projects) != 1 || projects[0] != "p-cust" {
		t.Fatalf("projects = %v, %v; want only the org the account owns", projects, err)
	}
	apps, err := console.OwnedApps(ctx, "u-cust")
	if err != nil {
		t.Fatal(err)
	}
	for _, app := range apps {
		if app.ProjectID == "p-made" {
			t.Fatalf("app %s of a good-standing org's project is listed for the suspension", app.AppID)
		}
	}
}

func TestSuspendedUsersAndRecordSuspendedApps(t *testing.T) {
	_, console := consoleFixture(t)
	ctx := context.Background()
	if err := console.Suspend(ctx, adminconsole.Suspension{UserID: "u-cust", Reason: "x", At: time.Now()}, nil); err != nil {
		t.Fatal(err)
	}
	users, err := console.SuspendedUsers(ctx)
	if err != nil || !users["u-cust"] || users["u-other"] {
		t.Fatalf("users = %v, %v", users, err)
	}
	stopped := []adminconsole.SuspendedApp{{ProjectID: "p", AppID: "a", AppName: "web", PreviousReplicas: 2}}
	if err := console.RecordSuspendedApps(ctx, "u-cust", stopped); err != nil {
		t.Fatal(err)
	}
	got, err := console.SuspendedApps(ctx, "u-cust")
	if err != nil || len(got) != 1 || got[0].PreviousReplicas != 2 {
		t.Fatalf("recorded = %+v, %v", got, err)
	}
}

func store(t *testing.T, console *AdminConsole) *Store {
	t.Helper()
	return &Store{db: console.db}
}

// userRow finds one account in a page of the console's users.
func userRow(page adminconsole.Page[adminconsole.UserRow], id string) (adminconsole.UserRow, bool) {
	for _, row := range page.Items {
		if row.ID == id {
			return row, true
		}
	}
	return adminconsole.UserRow{}, false
}
