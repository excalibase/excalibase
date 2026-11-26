//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/abuse"
	"github.com/excalibase/provisioning-poc/internal/adminconsole"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

type analyticsPerson struct {
	id, email, role, kind string
	verified, suspended   bool
	project               bool
	signedUp              time.Duration
}

func analyticsFixture(t *testing.T, people []analyticsPerson) (*Store, *AdminConsole) {
	t.Helper()
	store := testStore(t)
	ctx := context.Background()
	for _, person := range people {
		user := domain.User{ID: person.id, Username: person.id, Email: person.email, Role: person.role, Active: true, Kind: person.kind}
		if person.verified {
			verifiedAt := time.Now()
			user.EmailVerifiedAt = &verifiedAt
		}
		user.PasswordHash = testutil.FixturePasswordHash()
		if err := store.CreateUser(ctx, &user); err != nil {
			t.Fatalf("create %s: %v", person.id, err)
		}
		if _, err := store.db.ExecContext(ctx, `UPDATE users SET created_at = $2 WHERE id = $1`, person.id, time.Now().Add(-person.signedUp)); err != nil {
			t.Fatal(err)
		}
		if person.suspended {
			if _, err := store.db.ExecContext(ctx, `INSERT INTO abuse_suspensions (user_id, email, reason) VALUES ($1, $2, 'proxy')`, person.id, person.email); err != nil {
				t.Fatal(err)
			}
		}
		if person.project {
			if _, err := store.db.ExecContext(ctx, `INSERT INTO orgs (id, name, slug, owner_id) VALUES ($1, $1, $1, $1)`, person.id); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(ctx,
				`INSERT INTO database_instances (project_id, org_id, owner_id, status, project_name, namespace, storage_size, no_database, created_at)
				 VALUES ('p-'||$1, $1, '', 'ACTIVE', $1, 'ns-'||$1, '1Gi', FALSE, NOW())`, person.id); err != nil {
				t.Fatal(err)
			}
		}
	}
	return store, store.AdminConsole()
}

func logRefusal(t *testing.T, store *Store, userID, action string, ago time.Duration) {
	t.Helper()
	at := time.Now().Add(-ago)
	entry := &domain.AuditEntry{UserID: userID, Action: action, Resource: "app", ProjectID: "p-" + userID, Details: "surface=create image=x", Timestamp: &at}
	if err := store.LogAudit(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
}

const day = 24 * time.Hour

var analyticsPeople = []analyticsPerson{
	{id: "kept", email: "kept@gmail.com", role: "user", kind: "human", verified: true, project: true, signedUp: 2 * day},
	{id: "spam1", email: "a@spam.test", role: "user", kind: "human", suspended: true, signedUp: day},
	{id: "spam2", email: "b@spam.test", role: "user", kind: "human", suspended: true, signedUp: day},
	{id: "tried", email: "t@gmail.com", role: "user", kind: "human", verified: true, project: true, signedUp: 3 * day},
	{id: "old", email: "o@gmail.com", role: "user", kind: "human", verified: true, signedUp: 60 * day},
	{id: "staff", email: "s@gmail.com", role: "platform_admin", kind: "human", verified: true, signedUp: day},
	{id: "svc", email: "svc@gmail.com", role: "user", kind: "service", signedUp: day},
}

func TestUserAnalyticsCountsCustomersOfTheWindowOnly(t *testing.T) {
	store, console := analyticsFixture(t, analyticsPeople)
	logRefusal(t, store, "tried", abuse.AuditActionProxyRefused, day)

	got, err := console.UserAnalytics(context.Background(), 30)
	if err != nil {
		t.Fatal(err)
	}
	if got.Signups != 4 || got.Suspended != 2 {
		t.Fatalf("signups %d suspended %d, want 4 and 2 (staff, service and the 60-day-old account are left out)", got.Signups, got.Suspended)
	}
	if len(got.PerDay) != 30 {
		t.Fatalf("per day has %d entries, want 30 with the empty days present", len(got.PerDay))
	}
	var total, flagged int
	for _, entry := range got.PerDay {
		total, flagged = total+entry.Total, flagged+entry.Flagged
	}
	if total != 4 || flagged != 2 {
		t.Fatalf("per day adds up to %d with %d suspended, want 4 and 2", total, flagged)
	}
	want := map[string]int{"Signed up": 4, "Verified email": 2, "Created a project": 2, "Never refused": 3}
	for _, step := range got.Funnel {
		if step.Count != want[step.Label] {
			t.Errorf("funnel %q = %d, want %d", step.Label, step.Count, want[step.Label])
		}
	}
	wantDomains := []adminconsole.DomainStat{{Domain: "gmail.com", Accounts: 2}, {Domain: "spam.test", Accounts: 2, Suspended: 2}}
	if len(got.Domains) != 2 || got.Domains[0] != wantDomains[0] || got.Domains[1] != wantDomains[1] {
		t.Fatalf("domains = %+v, want %+v", got.Domains, wantDomains)
	}
}

func TestAbuseAnalyticsSplitsRefusalsAndFindsTheStillActive(t *testing.T) {
	store, console := analyticsFixture(t, analyticsPeople)
	logRefusal(t, store, "tried", abuse.AuditActionProxyRefused, day)
	logRefusal(t, store, "tried", abuse.AuditActionProxyRefused, day+time.Hour)
	logRefusal(t, store, "kept", abuse.AuditActionRegistryRefused, 2*day)
	logRefusal(t, store, "spam1", abuse.AuditActionProxyRefused, day)
	logRefusal(t, store, "staff", abuse.AuditActionProxyRefused, day)
	logRefusal(t, store, "old", abuse.AuditActionProxyRefused, 45*day)

	got, err := console.AbuseAnalytics(context.Background(), 30)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProxyRefusals != 3 || got.RegistryRefusals != 1 || got.RefusedUsers != 3 || got.RefusedStillActive != 2 || got.Suspended != 2 {
		t.Fatalf("abuse = %+v, want 3 proxy, 1 registry, 3 users of whom 2 still active, 2 suspended", got)
	}
	if len(got.PerDay) != 30 {
		t.Fatalf("per day has %d entries, want 30", len(got.PerDay))
	}
	if len(got.Latest) != 4 || got.Latest[0].Email == "" {
		t.Fatalf("latest = %+v, want the 4 refusals of customers in the window, newest first", got.Latest)
	}
	for _, brief := range got.Latest {
		if brief.UserID == "spam1" && !brief.Suspended {
			t.Fatalf("a refusal by a suspended account must say so: %+v", brief)
		}
	}
}

func TestTrafficAnalyticsSumsEachProjectsDailyPeak(t *testing.T) {
	store, console := analyticsFixture(t, nil)
	ctx := context.Background()
	for _, sample := range []struct {
		project string
		ago     time.Duration
		active  int
	}{{"p1", day, 10}, {"p1", day + time.Hour, 14}, {"p2", day, 6}, {"p2", 10 * day, 99}} {
		if _, err := store.db.ExecContext(ctx, `INSERT INTO database_metrics (project_id, timestamp, active_connections) VALUES ($1, $2, $3)`,
			sample.project, time.Now().Add(-sample.ago), sample.active); err != nil {
			t.Fatal(err)
		}
	}
	got, err := console.TrafficAnalytics(ctx, adminconsole.TrafficWindowDays)
	if err != nil {
		t.Fatal(err)
	}
	if got.PeakConnections != 20 || len(got.PeakConnectionsDay) != adminconsole.TrafficWindowDays {
		t.Fatalf("traffic = %+v, want a peak of 20 (14 + 6) over 7 days; the 10-day-old sample is outside", got)
	}
}

var _ adminconsole.AnalyticsReader = (*AdminConsole)(nil)
