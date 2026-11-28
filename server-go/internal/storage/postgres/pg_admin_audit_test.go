//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/adminconsole"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

func logAudit(t *testing.T, store *Store, entry domain.AuditEntry) {
	t.Helper()
	if err := store.LogAudit(context.Background(), &entry); err != nil {
		t.Fatal(err)
	}
}

func TestAdminAuditPagesNewestFirstByKeysetAndHidesToolCalls(t *testing.T) {
	store, console := consoleFixture(t)
	for i := 0; i < 7; i++ {
		logAudit(t, store, domain.AuditEntry{UserID: "u-admin", Action: "user.suspend", Resource: "user", ResourceID: "u-cust", Details: "why"})
	}
	logAudit(t, store, domain.AuditEntry{UserID: "u-cust", Action: "mcp.call", Resource: "project", ResourceID: "p-cust", ProjectID: "p-cust", Via: "mcp", Details: "select * from secrets"})
	var seen []string
	var before int64
	for pages := 0; pages < 10; pages++ {
		page, err := console.ListAudit(context.Background(), adminconsole.AuditQuery{BeforeID: before, Limit: 3})
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range page.Items {
			seen = append(seen, row.ID)
			if row.Action == "mcp.call" {
				t.Fatal("a tenant's tool call must not appear in the platform log")
			}
		}
		if !page.More {
			break
		}
		last := page.Items[len(page.Items)-1].ID
		before = mustInt(t, last)
	}
	if len(seen) != 7 {
		t.Fatalf("saw %d rows, want 7 (each once): %v", len(seen), seen)
	}
	for i := 1; i < len(seen); i++ {
		if mustInt(t, seen[i]) >= mustInt(t, seen[i-1]) {
			t.Fatalf("not newest first: %v", seen)
		}
	}
}

func TestAdminAuditFiltersByActorAddressActionPrefixAndTarget(t *testing.T) {
	store, console := consoleFixture(t)
	logAudit(t, store, domain.AuditEntry{UserID: "u-admin", Action: "user.suspend", Resource: "user", ResourceID: "u-cust"})
	logAudit(t, store, domain.AuditEntry{UserID: "u-admin", Action: "admin.project.open", Resource: "project", ResourceID: "p-cust"})
	logAudit(t, store, domain.AuditEntry{UserID: "u-admin", Action: "database.reset", Resource: "database", ResourceID: "db-1", ProjectID: "p-cust"})
	logAudit(t, store, domain.AuditEntry{UserID: "u-other", Action: "vault.read", Resource: "vault", ResourceID: "k"})
	count := func(q adminconsole.AuditQuery) int {
		q.Limit = 50
		page, err := console.ListAudit(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		return len(page.Items)
	}
	cases := []struct {
		name string
		q    adminconsole.AuditQuery
		want int
	}{
		{"by actor id", adminconsole.AuditQuery{Actor: "u-admin"}, 3},
		{"by actor address", adminconsole.AuditQuery{Actor: "ADMIN@example.test"}, 3},
		{"by action prefix", adminconsole.AuditQuery{Action: "user."}, 1},
		{"a percent is not a wildcard", adminconsole.AuditQuery{Action: "%"}, 0},
		{"by target resource or project", adminconsole.AuditQuery{Target: "p-cust"}, 2},
		{"filters combine", adminconsole.AuditQuery{Actor: "u-admin", Action: "vault."}, 0},
	}
	for _, tc := range cases {
		if got := count(tc.q); got != tc.want {
			t.Errorf("%s: %d rows, want %d", tc.name, got, tc.want)
		}
	}
	page, _ := console.ListAudit(context.Background(), adminconsole.AuditQuery{Action: "user.suspend", Limit: 5})
	if row := page.Items[0]; row.ActorEmail != "admin@example.test" || row.TargetEmail != "cust@mailinator.test" {
		t.Fatalf("addresses not joined: %+v", row)
	}
}

func TestAdminUserDetailGathersTheAccountAndLeaksNothingElse(t *testing.T) {
	store, console := consoleFixture(t)
	ctx := context.Background()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := store.db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO org_members (org_id, user_id, role) VALUES ('o-other', 'u-cust', 'developer')`)
	exec(`INSERT INTO studio_identities (provider, subject, user_id, email) VALUES ('github', 's1', 'u-cust', 'cust@mailinator.test')`)
	exec(`INSERT INTO user_trust (user_id, state, method, reason) VALUES ('u-cust', 'limited', 'github', 'young account')`)
	exec(`INSERT INTO trust_audit (user_id, from_state, to_state, method, reason, actor) VALUES ('u-cust', NULL, 'limited', 'github', 'young account', 'system')`)
	exec(`INSERT INTO abuse_suspensions (user_id, email, reason, reason_code, suspended_at) VALUES ('u-cust', 'cust@mailinator.test', 'proxy', 'proxy', NOW())`)
	logAudit(t, store, domain.AuditEntry{UserID: "u-admin", Action: "user.suspend", Resource: "user", ResourceID: "u-cust", Details: "proxy"})
	logAudit(t, store, domain.AuditEntry{UserID: "u-admin", Action: "user.suspend", Resource: "user", ResourceID: "u-other", Details: "someone else"})

	detail, err := console.ReadUserDetail(ctx, "u-cust")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Profile.Email != "cust@mailinator.test" || detail.Profile.Status != "suspended" || !detail.Profile.EmailVerified {
		t.Fatalf("profile = %+v", detail.Profile)
	}
	if detail.Profile.Trust == nil || detail.Profile.Trust.State != "limited" || len(detail.TrustHistory) != 1 {
		t.Fatalf("trust = %+v history %+v", detail.Profile.Trust, detail.TrustHistory)
	}
	if !detail.Suspension.Suspended || detail.Suspension.ReasonCode != "proxy" || len(detail.Suspensions) != 1 || detail.Suspensions[0].ActorEmail != "admin@example.test" {
		t.Fatalf("suspension = %+v history %+v", detail.Suspension, detail.Suspensions)
	}
	roles := map[string]string{}
	for _, org := range detail.Orgs {
		roles[org.ID] = org.Role
	}
	if len(roles) != 2 || roles["o-other"] != "developer" || roles["o-cust"] == "" {
		t.Fatalf("orgs = %+v", detail.Orgs)
	}
	if len(detail.Projects) != 2 {
		t.Fatalf("projects = %+v, want both orgs' projects", detail.Projects)
	}
	if len(detail.SignIn) != 1 || detail.SignIn[0].Provider != "github" {
		t.Fatalf("sign-in = %+v", detail.SignIn)
	}
	for _, row := range detail.Audit {
		if row.ResourceID == "u-other" {
			t.Fatalf("another account's entry leaked: %+v", row)
		}
	}
	if len(detail.Audit) != 1 {
		t.Fatalf("audit = %+v", detail.Audit)
	}
}

func TestAdminUserDetailPasswordAccountAndUnknownUser(t *testing.T) {
	_, console := consoleFixture(t)
	detail, err := console.ReadUserDetail(context.Background(), "u-other")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.SignIn) != 1 || detail.SignIn[0].Kind != "password" {
		t.Fatalf("an account with no linked provider signs in by password: %+v", detail.SignIn)
	}
	if detail.Profile.Trust != nil || detail.Suspension.Suspended {
		t.Fatalf("nothing decided: %+v", detail)
	}
	if _, err := console.ReadUserDetail(context.Background(), "nobody"); err != adminconsole.ErrUserNotFound {
		t.Fatalf("err = %v, want ErrUserNotFound", err)
	}
}

func mustInt(t *testing.T, id string) int64 {
	t.Helper()
	var n int64
	for _, c := range id {
		n = n*10 + int64(c-'0')
	}
	return n
}

func TestAdminUserDetailSuspensionHistoryLeavesOutToolCalls(t *testing.T) {
	store, console := consoleFixture(t)
	logAudit(t, store, domain.AuditEntry{UserID: "u-admin", Action: "user.suspend", Resource: "user", ResourceID: "u-cust", Details: "proxy"})
	logAudit(t, store, domain.AuditEntry{UserID: "u-admin", Action: "user.suspend", Resource: "user", ResourceID: "u-cust", Via: "mcp", ProjectID: "p-cust", Details: "tenant call"})
	detail, err := console.ReadUserDetail(context.Background(), "u-cust")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Suspensions) != 1 || detail.Suspensions[0].Reason != "proxy" {
		t.Fatalf("history = %+v, want only the platform's own entry", detail.Suspensions)
	}
}

func TestAdminAuditFiltersAreIndexed(t *testing.T) {
	store, _ := consoleFixture(t)
	for _, name := range []string{"idx_audit_log_user_id_id", "idx_audit_log_resource_id_id", "idx_audit_log_project_id_id", "idx_audit_log_action_prefix"} {
		var found bool
		if err := store.db.QueryRowContext(context.Background(), `SELECT EXISTS (SELECT 1 FROM pg_class c JOIN pg_index i ON i.indexrelid = c.oid WHERE c.relname = $1 AND i.indisvalid AND i.indisready)`, name).Scan(&found); err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Errorf("index %s is missing or invalid: a filter that matches nothing would scan the whole log", name)
		}
	}
}

func TestAdminAuditActorFilterStillFindsAnAccountThatIsGone(t *testing.T) {
	store, console := consoleFixture(t)
	logAudit(t, store, domain.AuditEntry{UserID: "u-deleted", Action: "token.create", Resource: "token"})
	page, err := console.ListAudit(context.Background(), adminconsole.AuditQuery{Actor: "u-deleted", Limit: 5})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("page = %+v, err = %v; the trail outlives the account", page, err)
	}
}
