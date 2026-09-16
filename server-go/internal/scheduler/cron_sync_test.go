package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// TestSyncCronJobs_ValidatesProjectID — empty project id is rejected
// before any SQL hits the DB.
func TestSyncCronJobs_ValidatesProjectID(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	tx, _ := db.Begin()
	if err := SyncCronJobs(context.Background(), tx, "", "fn", nil); err == nil {
		t.Error("expected error on empty projectID")
	}
}

// TestSyncCronJobs_ValidatesFunctionID — empty function id is rejected.
func TestSyncCronJobs_ValidatesFunctionID(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	tx, _ := db.Begin()
	if err := SyncCronJobs(context.Background(), tx, "proj", "", nil); err == nil {
		t.Error("expected error on empty functionID")
	}
}

// TestSyncCronJobs_RejectsRowMissingName — defence-in-depth on top of the
// bundler validator.
func TestSyncCronJobs_RejectsRowMissingName(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.ExpectBegin()
	tx, _ := db.Begin()
	jobs := []CronJobRow{{Name: "", Schedule: json.RawMessage(`{"kind":"hourly"}`)}}
	err = SyncCronJobs(context.Background(), tx, "proj", "fn", jobs)
	if err == nil {
		t.Error("expected error on row with empty name")
	}
}

// TestSyncCronJobs_RejectsRowMissingSchedule — a job declared without a
// schedule cannot be enqueued; refuse the sync.
func TestSyncCronJobs_RejectsRowMissingSchedule(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM excalibase.excalibase_cron_jobs").WillReturnResult(sqlmock.NewResult(0, 0))
	tx, _ := db.Begin()
	jobs := []CronJobRow{{Name: "j1"}}
	jobs[0].FnRef.ModuleName = "m"
	jobs[0].FnRef.ExportName = "x"
	err = SyncCronJobs(context.Background(), tx, "proj", "fn", jobs)
	if err == nil {
		t.Error("expected error on row missing schedule")
	}
}

// TestSyncCronJobs_RejectsRowMissingFnRef — both moduleName and exportName
// must be non-empty.
func TestSyncCronJobs_RejectsRowMissingFnRef(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM excalibase.excalibase_cron_jobs").WillReturnResult(sqlmock.NewResult(0, 0))
	tx, _ := db.Begin()
	jobs := []CronJobRow{{Name: "j1", Schedule: json.RawMessage(`{"kind":"hourly"}`)}}
	err = SyncCronJobs(context.Background(), tx, "proj", "fn", jobs)
	if err == nil {
		t.Error("expected error on row missing fnRef")
	}
}

// TestSyncCronJobs_PropagatesDeleteError — an SQL error on the bulk delete
// must bubble up.
func TestSyncCronJobs_PropagatesDeleteError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM excalibase.excalibase_cron_jobs").
		WillReturnError(errors.New("boom"))
	tx, _ := db.Begin()
	err = SyncCronJobs(context.Background(), tx, "proj", "fn", nil)
	if err == nil || !contains(err.Error(), "clear function rows") {
		t.Errorf("expected 'clear function rows' wrapping, got: %v", err)
	}
}

// TestSyncCronJobs_PropagatesUpsertError — an SQL error on the per-row
// upsert must bubble up with the offending row name.
func TestSyncCronJobs_PropagatesUpsertError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM excalibase.excalibase_cron_jobs").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("INSERT INTO excalibase.excalibase_cron_jobs").
		WillReturnError(errors.New("boom"))
	tx, _ := db.Begin()
	row := CronJobRow{
		Name:     "j1",
		Schedule: json.RawMessage(`{"kind":"hourly"}`),
		Args:     json.RawMessage(`{}`),
	}
	row.FnRef.ModuleName = "m"
	row.FnRef.ExportName = "x"
	err = SyncCronJobs(context.Background(), tx, "proj", "fn", []CronJobRow{row})
	if err == nil || !contains(err.Error(), `upsert row "j1"`) {
		t.Errorf("expected 'upsert row \"j1\"' wrapping, got: %v", err)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || (len(s) > 0 && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}())))
}
