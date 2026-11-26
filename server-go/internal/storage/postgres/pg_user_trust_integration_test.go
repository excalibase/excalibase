//go:build integration

package postgres

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/trust"
)

const trustActorSystem = "system"

func trustRecord(userID string, state trust.State, method trust.Method, reason trust.Reason) trust.Record {
	return trust.Record{UserID: userID, State: state, Method: method, Reason: reason, CheckVersion: 1}
}

func auditRows(t *testing.T, store *Store, userID string) [][3]string {
	t.Helper()
	rows, err := store.db.Query(`SELECT COALESCE(from_state, ''), to_state, actor FROM trust_audit WHERE user_id = $1 ORDER BY id`, userID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out [][3]string
	for rows.Next() {
		var row [3]string
		if err := rows.Scan(&row[0], &row[1], &row[2]); err != nil {
			t.Fatal(err)
		}
		out = append(out, row)
	}
	return out
}

func TestUserTrustSaveFindAndAudit(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if err := store.CreateUser(ctx, userNamed("u1", "ulysses")); err != nil {
		t.Fatal(err)
	}
	if got, err := store.FindUserTrust(ctx, "u1"); err != nil || got != nil {
		t.Fatalf("no row yet: %v %v", got, err)
	}
	if err := store.SaveUserTrust(ctx, trustRecord("u1", trust.Limited, trust.MethodGitHub, trust.ReasonUnverified), trustActorSystem); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveUserTrust(ctx, trustRecord("u1", trust.Full, trust.MethodGitHub, trust.ReasonOK), trustActorSystem); err != nil {
		t.Fatal(err)
	}
	got, err := store.FindUserTrust(ctx, "u1")
	if err != nil || got == nil || got.State != trust.Full || got.Method != trust.MethodGitHub || got.Reason != trust.ReasonOK || got.CheckVersion != 1 || got.DecidedAt.IsZero() {
		t.Fatalf("stored record: %+v %v", got, err)
	}
	want := [][3]string{{"", "limited", trustActorSystem}, {"limited", "full", trustActorSystem}}
	audit := auditRows(t, store, "u1")
	if len(audit) != 2 || audit[0] != want[0] || audit[1] != want[1] {
		t.Errorf("audit trail %v, want %v", audit, want)
	}
}

func TestUserTrustRefusesWhatIsNotARecord(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if err := store.CreateUser(ctx, userNamed("u2", "uma")); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveUserTrust(ctx, trustRecord("u2", "maybe", trust.MethodGitHub, trust.ReasonOK), trustActorSystem); err == nil {
		t.Error("an unknown state was stored")
	}
	if err := store.SaveUserTrust(ctx, trustRecord("u2", trust.Full, trust.MethodAdmin, trust.ReasonAdminOverride), ""); err == nil {
		t.Error("a decision with no actor was stored")
	}
	if err := store.SaveUserTrust(ctx, trustRecord("nobody", trust.Full, trust.MethodAdmin, trust.ReasonAdminOverride), "admin"); err == nil {
		t.Error("a decision for a user that does not exist was stored")
	}
	if _, err := store.db.Exec(`INSERT INTO user_trust (user_id, state, method, reason, check_version) VALUES ('u2','bogus','github','ok',1)`); err == nil {
		t.Error("the database accepted an unknown state")
	}
	if got, _ := store.FindUserTrust(ctx, "u2"); got != nil {
		t.Errorf("a refused save left a row: %+v", got)
	}
	if rows := auditRows(t, store, "u2"); len(rows) != 0 {
		t.Errorf("a refused save left audit rows: %v", rows)
	}
}

// The decision goes with the account, the audit trail does not: it is the
// evidence of who was limited or verified, and of who changed it.
func TestUserTrustGoesWithTheUserButTheAuditStays(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if err := store.CreateUser(ctx, userNamed("u3", "ursula")); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveUserTrust(ctx, trustRecord("u3", trust.Limited, trust.MethodGitHub, trust.ReasonUnverified), trustActorSystem); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteUser(ctx, "u3"); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.FindUserTrust(ctx, "u3"); got != nil {
		t.Errorf("trust outlived its user: %+v", got)
	}
	if rows := auditRows(t, store, "u3"); len(rows) != 1 {
		t.Errorf("audit rows after the user was deleted: %v, want the one entry kept", rows)
	}
}

func TestTrustAuditRefusesRowsThatAreNotDecisions(t *testing.T) {
	store := testStore(t)
	insert := `INSERT INTO trust_audit (user_id, from_state, to_state, method, reason, actor) VALUES ($1, NULLIF($2, ''), $3, $4, 'ok', $5)`
	cases := map[string][]any{
		"bad to_state":   {"x", "", "maybe", "github", "system"},
		"bad from_state": {"x", "maybe", "full", "github", "system"},
		"bad method":     {"x", "", "full", "bribe", "system"},
		"empty actor":    {"x", "", "full", "github", ""},
	}
	for name, args := range cases {
		if _, err := store.db.Exec(insert, args...); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if _, err := store.db.Exec(insert, "gone-user", "", "full", "github", "system"); err != nil {
		t.Errorf("a decision for a user with no row any more was refused: %v", err)
	}
}

func TestTrustBackfillMarker(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if got, err := store.LatestBackfill(ctx); err != nil || got != nil {
		t.Fatalf("before any backfill: %v %v", got, err)
	}
	cutoff := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if err := store.RecordBackfill(ctx, trust.BackfillRun{FinishedAt: time.Now(), Cutoff: cutoff}); err != nil {
		t.Fatal(err)
	}
	got, err := store.LatestBackfill(ctx)
	if err != nil || got == nil || !got.Cutoff.Equal(cutoff) || got.FinishedAt.IsZero() {
		t.Errorf("after: %+v %v", got, err)
	}
	if err := store.RecordBackfill(ctx, trust.BackfillRun{}); err == nil {
		t.Error("a marker with no times was recorded")
	}
}

// Two decisions at once must leave one row and an audit trail that chains.
func TestUserTrustConcurrentSavesChain(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if err := store.CreateUser(ctx, userNamed("u4", "ugo")); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			state, reason := trust.Limited, trust.ReasonUnverified
			if i%2 == 0 {
				state, reason = trust.Full, trust.ReasonOK
			}
			if err := store.SaveUserTrust(ctx, trustRecord("u4", state, trust.MethodGitHub, reason), trustActorSystem); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	audit := auditRows(t, store, "u4")
	if len(audit) != 8 {
		t.Fatalf("audit rows: %d", len(audit))
	}
	for i := 1; i < len(audit); i++ {
		if audit[i][0] != audit[i-1][1] {
			t.Errorf("audit row %d starts at %q but the one before ended at %q", i, audit[i][0], audit[i-1][1])
		}
	}
}

func backdateUser(t *testing.T, store *Store, userID string, created time.Time) {
	t.Helper()
	if _, err := store.db.Exec(`UPDATE users SET created_at = $2 WHERE id = $1`, userID, created); err != nil {
		t.Fatal(err)
	}
}

// Owner decision 2026-11-26: accounts created before the cutoff are full;
// the cutoff day itself and later are not grandfathered.
func TestGrandfatherUsersBeforeTheCutoffOnly(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	cutoff := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for id, created := range map[string]time.Time{
		"gf-old":      cutoff.Add(-24 * time.Hour),
		"gf-boundary": cutoff,
		"gf-new":      cutoff.Add(24 * time.Hour),
		"gf-decided":  cutoff.Add(-48 * time.Hour),
	} {
		if err := store.CreateUser(ctx, userNamed(id, id)); err != nil {
			t.Fatal(err)
		}
		backdateUser(t, store, id, created)
	}
	if err := store.SaveUserTrust(ctx, trustRecord("gf-decided", trust.Limited, trust.MethodAdmin, trust.ReasonAdminOverride), "admin-1"); err != nil {
		t.Fatal(err)
	}

	granted, err := store.GrandfatherUsersBefore(ctx, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	old, _ := store.FindUserTrust(ctx, "gf-old")
	if old == nil || old.State != trust.Full || old.Method != trust.MethodGrandfathered || old.Reason != trust.ReasonGrandfathered {
		t.Errorf("an account before the cutoff: %+v", old)
	}
	for _, id := range []string{"gf-boundary", "gf-new"} {
		if got, _ := store.FindUserTrust(ctx, id); got != nil {
			t.Errorf("%s was grandfathered: %+v", id, got)
		}
	}
	if decided, _ := store.FindUserTrust(ctx, "gf-decided"); decided == nil || decided.State != trust.Limited {
		t.Errorf("an earlier decision was overwritten: %+v", decided)
	}
	if audit := auditRows(t, store, "gf-old"); len(audit) != 1 || audit[0] != [3]string{"", "full", "backfill"} {
		t.Errorf("audit trail %v", audit)
	}
	if granted < 1 {
		t.Errorf("granted %d, want at least the one account", granted)
	}
	again, err := store.GrandfatherUsersBefore(ctx, cutoff)
	if err != nil || again != 0 {
		t.Errorf("a second run granted %d %v, want 0", again, err)
	}
}
