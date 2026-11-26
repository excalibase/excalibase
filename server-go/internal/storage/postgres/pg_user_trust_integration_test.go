//go:build integration

package postgres

import (
	"context"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/trust"
)

const trustActorSystem = "system"

func trustRecord(userID string, state trust.State, method trust.Method, reason trust.Reason) trust.Record {
	return trust.Record{UserID: userID, State: state, Method: method, Reason: reason, CheckVersion: 1}
}

func auditRows(t *testing.T, store *Store, userID string) [][3]string {
	t.Helper()
	rows, err := store.db.Query(`SELECT from_state, to_state, actor FROM trust_audit WHERE user_id = $1 ORDER BY id`, userID)
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

func TestUserTrustGoesWithTheUser(t *testing.T) {
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
	if rows := auditRows(t, store, "u3"); len(rows) != 0 {
		t.Errorf("audit outlived its user: %v", rows)
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
