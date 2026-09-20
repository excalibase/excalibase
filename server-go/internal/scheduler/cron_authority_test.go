package scheduler

import (
	"context"
	"database/sql/driver"
	"io"
	"log"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// cronRows programs the registry walk with the given rows.
func cronRows(mock sqlmock.Sqlmock, rows *sqlmock.Rows) {
	mock.ExpectQuery("FROM excalibase.excalibase_cron_jobs").WillReturnRows(rows)
}

func cronRegistryRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"name", "project_id", "module_name", "export_name", "args", "schedule", "last_enqueued_at",
	})
}

func sweptCron(cfg CronRunnerConfig) CronRunnerConfig {
	cfg.ProjectID = "proj_a"
	cfg.Logger = log.New(io.Discard, "", 0)
	cfg.IDGen = func() string { return "generated" }
	return cfg
}

// The registry is tenant-written too: a job row naming another project must
// not enqueue work against that project.
func TestCronRunner_OnlyReadsTheSweptProjectsJobs(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery("FROM excalibase.excalibase_cron_jobs").
		WithArgs("proj_a", sqlmock.AnyArg()).
		WillReturnRows(cronRegistryRows())

	cr := NewCronRunner(sweptCron(CronRunnerConfig{DB: db}))
	if err := cr.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("the registry walk is not scoped to the swept project: %v", err)
	}
}

// Even inside the swept project's own registry, a row whose project_id was
// tampered with is not enqueued.
func TestCronRunner_SkipsARowNamingAnotherProject(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	cronRows(mock, cronRegistryRows().AddRow(
		"nightly", "proj_victim", "jobs", "sweep", []byte(`{}`), []byte(`{"kind":"hourly"}`), nil))

	cr := NewCronRunner(sweptCron(CronRunnerConfig{DB: db}))
	if err := cr.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("a foreign-project cron row was acted on: %v", err)
	}
}

// A schedule finer than the platform minimum is clipped, not honoured: a
// per-second cron would otherwise be a tenant-controlled load multiplier.
func TestCronRunner_ClipsSchedulesFinerThanTheMinimum(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	cronRows(mock, cronRegistryRows().AddRow(
		"fast", "proj_a", "jobs", "sweep", []byte(`{}`), []byte(`{"kind":"interval","seconds":1}`), nil))
	mock.ExpectBegin()
	var scheduledFor time.Time
	mock.ExpectExec("INSERT INTO excalibase.excalibase_scheduled_functions").
		WithArgs("generated", "proj_a", "jobs", "sweep", sqlmock.AnyArg(),
			matchTime{&scheduledFor}).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE excalibase.excalibase_cron_jobs").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	cr := NewCronRunner(sweptCron(CronRunnerConfig{DB: db, MinInterval: time.Minute}))
	if err := cr.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
	if delay := time.Until(scheduledFor); delay < 50*time.Second {
		t.Errorf("a 1s cron was enqueued %s out; the platform minimum is a minute", delay)
	}
}

// matchTime captures the timestamp bound to an argument.
type matchTime struct{ into *time.Time }

func (m matchTime) Match(v driver.Value) bool {
	if ts, ok := v.(time.Time); ok {
		*m.into = ts
		return true
	}
	return false
}

// A cron row naming a module that is not a plain identifier is not enqueued.
func TestCronRunner_SkipsBadIdentifiers(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	cronRows(mock, cronRegistryRows().AddRow(
		"bad", "proj_a", "../escape", "sweep", []byte(`{}`), []byte(`{"kind":"hourly"}`), nil))

	cr := NewCronRunner(sweptCron(CronRunnerConfig{DB: db}))
	if err := cr.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("a cron row with a hostile module name was enqueued: %v", err)
	}
}

// The registry walk is capped, so a tenant cannot make one tick walk a
// million rows.
func TestCronRunner_CapsTheNumberOfJobsRead(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery("FROM excalibase.excalibase_cron_jobs").
		WithArgs("proj_a", 7).
		WillReturnRows(cronRegistryRows())

	cr := NewCronRunner(sweptCron(CronRunnerConfig{DB: db, MaxJobs: 7}))
	if err := cr.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("the registry walk is not capped: %v", err)
	}
}
