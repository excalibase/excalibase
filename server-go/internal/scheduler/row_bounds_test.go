package scheduler

import (
	"context"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// A tenant can write a row whose args are gigabytes. Checking the size after
// the row is in memory is checking it too late, so the claim only ever
// selects rows already within the platform's bounds.
func TestWorker_ClaimSelectsOnlyRowsWithinTheSizeBounds(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectExec("SET status = 'failed'").WillReturnResult(sqlmock.NewResult(0, 0))
	claim := mock.ExpectQuery("SELECT id, project_id")
	claim.WillReturnRows(sqlmock.NewRows(
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

// The oversize close must not read the payload it is refusing — that is the
// whole point of doing it in SQL.
func TestClaimSQL_BoundsEveryTenantWrittenColumn(t *testing.T) {
	for _, fragment := range []string{
		"octet_length(args::text)",
		"length(id)",
		"length(module_name)",
		"length(export_name)",
	} {
		if !strings.Contains(claimDueSQL, fragment) {
			t.Errorf("the claim select does not bound %s", fragment)
		}
		if !strings.Contains(closeOversizedSQL, fragment) {
			t.Errorf("the oversize close does not bound %s", fragment)
		}
	}
	if strings.Contains(closeOversizedSQL, "SELECT args") {
		t.Error("the oversize close reads the payload it is refusing")
	}
}

// The cron registry is tenant-written too, and its rows are read in full on
// every leader tick.
func TestCronSQL_BoundsEveryTenantWrittenColumn(t *testing.T) {
	for _, fragment := range []string{
		"octet_length(args::text)",
		"octet_length(schedule::text)",
		"length(name)",
		"length(module_name)",
		"length(export_name)",
	} {
		if !strings.Contains(cronListSQL, fragment) {
			t.Errorf("the cron list does not bound %s", fragment)
		}
	}
}
