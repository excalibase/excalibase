//go:build integration

package scheduler

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// TestCronRunner_EnqueuesNextDueForCronJob — a row in excalibase_cron_jobs
// with a `cron`-kind schedule must produce an excalibase_scheduled_functions
// row for its next due time after a single Tick.
func TestCronRunner_EnqueuesNextDueForCronJob(t *testing.T) {
	db, teardown := setupSchedulerPG(t)
	defer teardown()
	ctx := context.Background()

	// Seed a daily cron — fires at 00:00 UTC every day. We don't care which
	// concrete time it picks; only that it picks SOMETHING in the future.
	_, err := db.ExecContext(ctx, `
		INSERT INTO excalibase.excalibase_cron_jobs
		  (name, project_id, module_name, export_name, args, schedule, last_enqueued_at)
		VALUES
		  ('daily-digest', 'proj_a', 'jobs', 'sendDigest', '{}',
		   '{"kind":"cron","expression":"0 0 * * *"}'::jsonb, NULL)
	`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	cr := NewCronRunner(CronRunnerConfig{DB: db, ProjectID: cronProject(t, db), Functions: allModules{}})
	if err := cr.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM excalibase.excalibase_scheduled_functions
		 WHERE project_id = $1 AND module_name = $2 AND export_name = $3`,
		"proj_a", "jobs", "sendDigest",
	).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("scheduled rows: got %d, want 1", count)
	}
	var lastEnqueued *time.Time
	_ = db.QueryRowContext(ctx,
		`SELECT last_enqueued_at FROM excalibase.excalibase_cron_jobs WHERE name = $1 AND project_id = $2`,
		"daily-digest", "proj_a",
	).Scan(&lastEnqueued)
	if lastEnqueued == nil {
		t.Errorf("last_enqueued_at: not updated after Tick")
	}
}

// TestCronRunner_IsIdempotentWithinPeriod — calling Tick twice in quick
// succession must not double-enqueue. The runner advances `last_enqueued_at`
// so the second call sees no fresh "next due time" before the period elapses.
func TestCronRunner_IsIdempotentWithinPeriod(t *testing.T) {
	db, teardown := setupSchedulerPG(t)
	defer teardown()
	ctx := context.Background()

	_, err := db.ExecContext(ctx, `
		INSERT INTO excalibase.excalibase_cron_jobs
		  (name, project_id, module_name, export_name, args, schedule, last_enqueued_at)
		VALUES
		  ('hourly-purge', 'proj_a', 'jobs', 'purge', '{}',
		   '{"kind":"hourly","minuteUTC":15}'::jsonb, NULL)
	`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	cr := NewCronRunner(CronRunnerConfig{DB: db, ProjectID: cronProject(t, db), Functions: allModules{}})
	for i := 0; i < 3; i++ {
		if err := cr.Tick(ctx); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM excalibase.excalibase_scheduled_functions
		 WHERE project_id = $1 AND module_name = $2`,
		"proj_a", "jobs",
	).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("scheduled rows after 3 ticks: got %d, want 1", count)
	}
}

// TestCronRunner_HandlesIntervalSchedule — interval schedules ride the same
// path as cron strings; the runner translates the {hours,minutes,seconds}
// shape into a robfig/cron-friendly "@every Nm" form.
func TestCronRunner_HandlesIntervalSchedule(t *testing.T) {
	db, teardown := setupSchedulerPG(t)
	defer teardown()
	ctx := context.Background()

	_, err := db.ExecContext(ctx, `
		INSERT INTO excalibase.excalibase_cron_jobs
		  (name, project_id, module_name, export_name, args, schedule, last_enqueued_at)
		VALUES
		  ('heartbeat', 'proj_b', 'jobs', 'beat', '{}',
		   '{"kind":"interval","hours":0,"minutes":10,"seconds":0}'::jsonb, NULL)
	`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	cr := NewCronRunner(CronRunnerConfig{DB: db, ProjectID: cronProject(t, db), Functions: allModules{}})
	if err := cr.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM excalibase.excalibase_scheduled_functions
		 WHERE project_id = $1 AND module_name = $2 AND export_name = $3`,
		"proj_b", "jobs", "beat",
	).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("scheduled rows for interval job: got %d, want 1", count)
	}
}

// TestCronRunner_HandlesDailySchedule — daily shape lowers to a 5-field
// cron expression internally. Verifies the schedule kind isn't rejected.
func TestCronRunner_HandlesDailySchedule(t *testing.T) {
	db, teardown := setupSchedulerPG(t)
	defer teardown()
	ctx := context.Background()

	_, err := db.ExecContext(ctx, `
		INSERT INTO excalibase.excalibase_cron_jobs
		  (name, project_id, module_name, export_name, args, schedule, last_enqueued_at)
		VALUES
		  ('daily-x', 'proj_c', 'jobs', 'x', '{}',
		   '{"kind":"daily","hourUTC":9,"minuteUTC":30}'::jsonb, NULL)
	`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	cr := NewCronRunner(CronRunnerConfig{DB: db, ProjectID: cronProject(t, db), Functions: allModules{}})
	if err := cr.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	var count int
	_ = db.QueryRowContext(ctx,
		`SELECT count(*) FROM excalibase.excalibase_scheduled_functions WHERE project_id = $1`,
		"proj_c",
	).Scan(&count)
	if count != 1 {
		t.Errorf("scheduled rows for daily job: got %d, want 1", count)
	}
}

// cronProject reads the project the fixture seeded, so each test drives the
// runner as the sweep would: with the project whose database this is.
func cronProject(t *testing.T, db *sql.DB) string {
	t.Helper()
	var projectID string
	if err := db.QueryRow(
		`SELECT project_id FROM excalibase.excalibase_cron_jobs LIMIT 1`).Scan(&projectID); err != nil {
		t.Fatalf("read seeded project: %v", err)
	}
	return projectID
}

type steppingClock struct{ current time.Time }

func (c *steppingClock) now() time.Time { return c.current }

func (c *steppingClock) advance(d time.Duration) { c.current = c.current.Add(d) }

func seedCronJob(t *testing.T, db *sql.DB, name, schedule string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO excalibase.excalibase_cron_jobs
		  (name, project_id, module_name, export_name, args, schedule, last_enqueued_at)
		VALUES ($1, 'proj_a', 'jobs', $1, '{}', $2::jsonb, NULL)
	`, name, schedule); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func scheduledCount(t *testing.T, db *sql.DB, exportName string) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM excalibase.excalibase_scheduled_functions WHERE export_name = $1`,
		exportName,
	).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	return count
}

func tickEvery(t *testing.T, cr *CronRunner, clock *steppingClock, ticks int, step time.Duration) {
	t.Helper()
	for i := 0; i < ticks; i++ {
		if err := cr.Tick(context.Background()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
		clock.advance(step)
	}
}

func TestCronRunner_IntervalEnqueuesOncePerInterval(t *testing.T) {
	db, teardown := setupSchedulerPG(t)
	defer teardown()
	seedCronJob(t, db, "beat", `{"kind":"interval","minutes":10}`)
	clock := &steppingClock{current: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)}
	cr := NewCronRunner(CronRunnerConfig{DB: db, ProjectID: cronProject(t, db), Functions: allModules{}, Now: clock.now})

	tickEvery(t, cr, clock, 5, time.Minute)
	if got := scheduledCount(t, db, "beat"); got != 1 {
		t.Fatalf("rows within the first interval: got %d, want 1", got)
	}
	clock.advance(6 * time.Minute)
	tickEvery(t, cr, clock, 3, time.Minute)
	if got := scheduledCount(t, db, "beat"); got != 2 {
		t.Errorf("rows after the pending run passed: got %d, want 2", got)
	}
}

func TestCronRunner_FireTimeInsideMinimumWindowEnqueuesOnce(t *testing.T) {
	db, teardown := setupSchedulerPG(t)
	defer teardown()
	seedCronJob(t, db, "purge", `{"kind":"hourly","minuteUTC":15}`)
	clock := &steppingClock{current: time.Date(2026, 9, 26, 15, 14, 30, 0, time.UTC)}
	cr := NewCronRunner(CronRunnerConfig{DB: db, ProjectID: cronProject(t, db), Functions: allModules{}, Now: clock.now})

	tickEvery(t, cr, clock, 3, 20*time.Second)
	if got := scheduledCount(t, db, "purge"); got != 1 {
		t.Errorf("rows for one fire time: got %d, want 1", got)
	}
}
