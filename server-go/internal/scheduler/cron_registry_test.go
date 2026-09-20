package scheduler

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// cronRow programs one registry row with an every-minute interval schedule.
func cronRow(mock sqlmock.Sqlmock, module, export string) {
	mock.ExpectQuery("FROM excalibase.excalibase_cron_jobs").
		WillReturnRows(sqlmock.NewRows(
			[]string{"name", "project_id", "module_name", "export_name", "args", "schedule", "last_enqueued_at"}).
			AddRow("nightly", "proj_a", module, export, []byte(`{}`),
				[]byte(`{"kind":"interval","minutes":1}`), nil))
}

// The registry is tenant-written, so a row can name a module the platform
// never deployed. Enqueueing it would put a task in the queue the worker
// then has to refuse — and the cadence is the tenant's, so it would do that
// every minute forever.
func TestCronRunner_DoesNotEnqueueAnUndeployedModule(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	cronRow(mock, "not-deployed", "sweep")

	cr := NewCronRunner(CronRunnerConfig{
		DB: db, ProjectID: "proj_a", Logger: quietLogger(),
		Functions: knownFunctions{modules: map[string]bool{"proj_a/jobs": true}},
	})
	if err := cr.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// A runner with no registry wired cannot answer "did the platform deploy
// this?", so it enqueues nothing rather than guessing yes.
func TestCronRunner_WithoutARegistryEnqueuesNothing(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	cronRow(mock, "jobs", "sweep")

	cr := NewCronRunner(CronRunnerConfig{DB: db, ProjectID: "proj_a", Logger: quietLogger()})
	if err := cr.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// The sweep is what holds the registry, so the cron half has to be handed it
// the same way the task half is.
func TestFanout_CronTickPassesTheFunctionRegistry(t *testing.T) {
	p := newProjectMock(t)
	p.expectSchedulerTables(true)
	cronRow(p.mock, "not-deployed", "sweep")

	f := fanoutOver(map[string]*projectMock{"proj_a": p}, &recordingInvoker{}, alwaysLeader{})
	if err := f.CronTick(context.Background()); err != nil {
		t.Fatalf("CronTick: %v", err)
	}
	if err := p.mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}
