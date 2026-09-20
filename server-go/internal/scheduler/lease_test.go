package scheduler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// A claim commits status='running'. If the replica then dies — or the
// invoker never returns — nothing ever moves that row again: the claim only
// re-reads 'pending'. The lease is what makes the claim recoverable.
func TestWorker_ReclaimsRowsAbandonedPastTheLease(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectExec("status = 'running'").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("SET status = 'failed'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT id, project_id").WillReturnRows(sqlmock.NewRows(
		[]string{"id", "project_id", "module_name", "export_name", "args", "attempts"}))
	mock.ExpectCommit()

	w := NewWorker(WorkerConfig{DB: db, ProjectID: "proj_a", Logger: quietLogger()})
	if _, err := w.claimDue(context.Background()); err != nil {
		t.Fatalf("claimDue: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// Without a cap, a row that kills whatever picks it up would be re-claimed
// forever. Each reap spends an attempt, and the last one ends the row.
func TestReapSQL_SpendsAnAttemptAndCapsAPoisonRow(t *testing.T) {
	reap := strings.Join(strings.Fields(reapAbandonedSQL), " ")
	for _, fragment := range []string{
		"attempts = attempts + 1",
		"'failed'",
		"'pending'",
		"claimed_at",
	} {
		if !strings.Contains(reap, fragment) {
			t.Errorf("the reap does not use %s", fragment)
		}
	}
	// A row claimed before the lease column existed has no claim time; it is
	// abandoned by definition, not immortal.
	if !strings.Contains(reap, "claimed_at IS NULL") {
		t.Error("a row with no claim time is never reaped")
	}
}

// The lease can only be read off a row if the claim records when it took it.
func TestClaimSQL_RecordsTheClaimTime(t *testing.T) {
	if !strings.Contains(markRunningSQL, "claimed_at = now()") {
		t.Error("the claim does not record when it took the row")
	}
}

func TestNewWorker_ClaimLeaseDefaultsAboveTheInvokeTimeout(t *testing.T) {
	w := NewWorker(WorkerConfig{ProjectID: "proj_a"})
	if w.claimLease != DefaultClaimLease {
		t.Errorf("claimLease: got %v, want %v", w.claimLease, DefaultClaimLease)
	}
	if DefaultClaimLease < time.Minute {
		t.Errorf("the lease must outlast an invocation, got %v", DefaultClaimLease)
	}
	set := NewWorker(WorkerConfig{ProjectID: "proj_a", ClaimLease: 90 * time.Second})
	if set.claimLease != 90*time.Second {
		t.Errorf("claimLease: got %v, want 90s", set.claimLease)
	}
}
