//go:build integration

package postgres

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/adminconsole"
)

func userIDs(t *testing.T, console *AdminConsole, query adminconsole.UserQuery) ([]string, int) {
	t.Helper()
	if query.Limit == 0 {
		query.Limit = 50
	}
	page, err := console.ListUsers(context.Background(), query)
	if err != nil {
		t.Fatalf("%+v: %v", query, err)
	}
	ids := []string{}
	for _, row := range page.Items {
		ids = append(ids, row.ID)
	}
	return ids, page.Total
}

func orgIDs(t *testing.T, console *AdminConsole, query adminconsole.OrgQuery) ([]string, int) {
	t.Helper()
	if query.Limit == 0 {
		query.Limit = 50
	}
	page, err := console.ListOrgs(context.Background(), query)
	if err != nil {
		t.Fatalf("%+v: %v", query, err)
	}
	ids := []string{}
	for _, row := range page.Items {
		ids = append(ids, row.ID)
	}
	return ids, page.Total
}

func TestAdminConsoleUsersSortByEveryAllowedColumn(t *testing.T) {
	_, console := consoleFixture(t)
	cases := []struct {
		query adminconsole.UserQuery
		want  []string
	}{
		{adminconsole.UserQuery{Sort: "email"}, []string{"u-admin", "u-cust", "u-other"}},
		{adminconsole.UserQuery{Sort: "email", Desc: true}, []string{"u-other", "u-cust", "u-admin"}},
		{adminconsole.UserQuery{Sort: "apps", Desc: true}, []string{"u-cust", "u-admin", "u-other"}},
		{adminconsole.UserQuery{Sort: "projects", Desc: true}, []string{"u-cust", "u-other", "u-admin"}},
		{adminconsole.UserQuery{Sort: "orgs", Desc: true}, []string{"u-cust", "u-other", "u-admin"}},
	}
	for _, tc := range cases {
		if got, _ := userIDs(t, console, tc.query); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%+v: got %v, want %v", tc.query, got, tc.want)
		}
	}
	for _, sort := range []string{"created", "lastLogin"} {
		if got, total := userIDs(t, console, adminconsole.UserQuery{Sort: sort, Desc: true}); len(got) != 3 || total != 3 {
			t.Errorf("sort %s: %v total=%d", sort, got, total)
		}
	}
}

func TestAdminConsoleUsersPageKeepsTheTotalOfTheWholeFilter(t *testing.T) {
	_, console := consoleFixture(t)
	got, total := userIDs(t, console, adminconsole.UserQuery{Sort: "email", Limit: 1, Offset: 1})
	if !reflect.DeepEqual(got, []string{"u-cust"}) || total != 3 {
		t.Fatalf("got %v total=%d", got, total)
	}
}

func TestAdminConsoleUsersFilterByRoleAndStatus(t *testing.T) {
	_, console := consoleFixture(t)
	ctx := context.Background()
	if got, total := userIDs(t, console, adminconsole.UserQuery{Role: "platform_admin"}); !reflect.DeepEqual(got, []string{"u-admin"}) || total != 1 {
		t.Fatalf("role filter: %v total=%d", got, total)
	}
	if err := console.Suspend(ctx, adminconsole.Suspension{UserID: "u-other", Reason: "x", At: time.Now()}, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := userIDs(t, console, adminconsole.UserQuery{Status: "suspended", IncludeSuspended: true}); !reflect.DeepEqual(got, []string{"u-other"}) {
		t.Fatalf("suspended filter: %v", got)
	}
	if got, _ := userIDs(t, console, adminconsole.UserQuery{Status: "active", IncludeSuspended: true, Sort: "email"}); !reflect.DeepEqual(got, []string{"u-admin", "u-cust"}) {
		t.Fatalf("active filter: %v", got)
	}
}

func TestAdminConsoleUsersRefuseASortTheStoreDoesNotKnow(t *testing.T) {
	_, console := consoleFixture(t)
	for _, sort := range []string{"password_hash", "email; DROP TABLE users", "u.email"} {
		if _, err := console.ListUsers(context.Background(), adminconsole.UserQuery{Sort: sort, Limit: 5}); err == nil {
			t.Errorf("sort %q must be refused", sort)
		}
	}
	if got, _ := userIDs(t, console, adminconsole.UserQuery{}); len(got) != 3 {
		t.Fatal("the users table must be intact")
	}
}

func TestAdminConsoleOrgsSortAndFilter(t *testing.T) {
	store, console := consoleFixture(t)
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `UPDATE orgs SET tier = 'STANDARD' WHERE id = 'o-other'`); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		query adminconsole.OrgQuery
		want  []string
	}{
		{adminconsole.OrgQuery{Sort: "name"}, []string{"o-cust", "o-other"}},
		{adminconsole.OrgQuery{Sort: "name", Desc: true}, []string{"o-other", "o-cust"}},
		{adminconsole.OrgQuery{Sort: "apps", Desc: true}, []string{"o-cust", "o-other"}},
		{adminconsole.OrgQuery{Sort: "owner", Desc: true}, []string{"o-other", "o-cust"}},
		{adminconsole.OrgQuery{Sort: "plan", Desc: true}, []string{"o-other", "o-cust"}},
		{adminconsole.OrgQuery{Tier: "STANDARD"}, []string{"o-other"}},
		{adminconsole.OrgQuery{Tier: "FREE"}, []string{"o-cust"}},
	}
	for _, tc := range cases {
		if got, _ := orgIDs(t, console, tc.query); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%+v: got %v, want %v", tc.query, got, tc.want)
		}
	}
	for _, sort := range []string{"created", "projects"} {
		if got, total := orgIDs(t, console, adminconsole.OrgQuery{Sort: sort}); len(got) != 2 || total != 2 {
			t.Errorf("sort %s: %v total=%d", sort, got, total)
		}
	}
	if err := console.Suspend(ctx, adminconsole.Suspension{UserID: "u-cust", Reason: "x", At: time.Now()}, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := orgIDs(t, console, adminconsole.OrgQuery{OwnerStatus: "suspended", IncludeSuspended: true}); !reflect.DeepEqual(got, []string{"o-cust"}) {
		t.Fatalf("owner status filter: %v", got)
	}
	if _, err := console.ListOrgs(ctx, adminconsole.OrgQuery{Sort: "owner_id; --", Limit: 5}); err == nil {
		t.Fatal("an unknown org sort must be refused")
	}
}

func TestAdminConsoleOrgPlanFilterIgnoresTheCaseOfTheStoredTier(t *testing.T) {
	store, console := consoleFixture(t)
	if _, err := store.db.ExecContext(context.Background(), `UPDATE orgs SET tier = 'standard' WHERE id = 'o-other'`); err != nil {
		t.Fatal(err)
	}
	if got, _ := orgIDs(t, console, adminconsole.OrgQuery{Tier: "STANDARD"}); !reflect.DeepEqual(got, []string{"o-other"}) {
		t.Fatalf("a lowercase stored tier must match the plan filter: %v", got)
	}
}

func TestAdminConsoleSearchTreatsPercentAndUnderscoreAsText(t *testing.T) {
	store, console := consoleFixture(t)
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `UPDATE orgs SET name = '100% Real_Co' WHERE id = 'o-cust'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE users SET email = 'a_b@example.test' WHERE id = 'u-other'`); err != nil {
		t.Fatal(err)
	}
	for search, want := range map[string][]string{"%": {"o-cust"}, "100%": {"o-cust"}, "l_c": {"o-cust"}, "r_a": {}, "othe_": {}} {
		if got, _ := orgIDs(t, console, adminconsole.OrgQuery{Search: search}); !reflect.DeepEqual(got, want) {
			t.Errorf("org search %q: got %v, want %v", search, got, want)
		}
	}
	for search, want := range map[string][]string{"a_b": {"u-other"}, "a%b": {}, "_": {"u-other"}} {
		if got, _ := userIDs(t, console, adminconsole.UserQuery{Search: search}); !reflect.DeepEqual(got, want) {
			t.Errorf("user search %q: got %v, want %v", search, got, want)
		}
	}
}

func TestAdminConsolePastTheEndStillReportsTheTotal(t *testing.T) {
	_, console := consoleFixture(t)
	if got, total := userIDs(t, console, adminconsole.UserQuery{Limit: 1, Offset: 50}); len(got) != 0 || total != 3 {
		t.Errorf("users: %v total=%d", got, total)
	}
	if got, total := orgIDs(t, console, adminconsole.OrgQuery{Limit: 1, Offset: 50}); len(got) != 0 || total != 2 {
		t.Errorf("orgs: %v total=%d", got, total)
	}
}
