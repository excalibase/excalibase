package scheduler

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"
)

// Deferred tasks and cron registries live in each tenant's own database —
// the runtime's `ctx.scheduler.*` RPCs write them through the tenant
// connection, and the deploy-time cron sync writes them through the project
// pool. A sweep pointed at a single database therefore sees one tenant at
// most, which is why the fanout walks every servable project instead.

// ProjectsFn lists the projects whose databases may be swept now.
type ProjectsFn func(ctx context.Context) ([]string, error)

// ProjectDBFn resolves a project's own database pool.
type ProjectDBFn func(ctx context.Context, projectID string) (*sql.DB, error)

// Leader reports whether this replica may fire schedules.
type Leader interface {
	IsLeader(ctx context.Context) (bool, error)
}

// FanoutConfig wires the sweep's collaborators.
type FanoutConfig struct {
	Projects   ProjectsFn
	DB         ProjectDBFn
	Invoker    Invoker
	CronLeader Leader
	// Functions is the platform's registry of deployed functions; a claimed
	// row may only name a module it knows for that project.
	Functions FunctionRegistry
	// Limits bound what one tenant's rows can cost the platform.
	Limits Limits
	// PollInterval is the task-queue cadence (default 5s); CronInterval the
	// registry cadence (default 60s, the finest a 5-field cron expresses).
	PollInterval time.Duration
	CronInterval time.Duration
	// Now is the clock the per-project backoff reads; defaults to time.Now.
	Now    func() time.Time
	Logger *log.Logger
}

// Limits are the platform's bounds on tenant-written work. Zero values take
// the package defaults.
type Limits struct {
	// Batch caps rows claimed per project per task tick.
	Batch int
	// ProjectConcurrency caps a project's in-flight invocations;
	// GlobalConcurrency caps them across every project on this replica.
	ProjectConcurrency int
	GlobalConcurrency  int
	// MaxArgsBytes caps one task's args.
	MaxArgsBytes int
	// MaxAttempts is the platform's retry budget per task.
	MaxAttempts int
	// CronMinInterval is the finest cron cadence honoured;
	// CronMaxJobs caps registry rows read per project per cron tick.
	CronMinInterval time.Duration
	CronMaxJobs     int
	// ProjectTimeout bounds one project's whole sweep.
	ProjectTimeout time.Duration
	// ClaimLease is how long a claimed task may stay 'running' before the
	// sweep takes it back.
	ClaimLease time.Duration
}

// DefaultGlobalConcurrency caps invocations in flight on one replica.
const DefaultGlobalConcurrency = 32

// DefaultProjectTimeout bounds one project's sweep. A tenant owns its
// database and can make any statement there hang — a view over the scheduler
// table calling pg_sleep, a lock held open — and without a deadline that one
// tenant stops every other tenant's tasks for good.
const DefaultProjectTimeout = 30 * time.Second

// Fanout sweeps the task queue and the cron registry of every servable
// project. Task claims are safe on every replica — FOR UPDATE SKIP LOCKED
// keeps two workers off the same row — while cron enqueues are decided from
// `last_enqueued_at` and so run on the leader alone.
type Fanout struct {
	projects   ProjectsFn
	db         ProjectDBFn
	invoker    Invoker
	cronLeader Leader
	functions  FunctionRegistry
	limits     Limits
	global     *Semaphore
	poll       time.Duration
	cronPoll   time.Duration
	// projectTimeout is how long one project's sweep may take before the
	// sweep abandons it and moves on to the next tenant.
	projectTimeout time.Duration
	logger         *log.Logger

	now func() time.Time

	// ready remembers what the presence check found. A positive is kept for
	// the life of the process; a negative only for a window, because a
	// project deploys its first scheduled function whenever it likes — and
	// re-asking every tick would hold a warm connection on every tenant
	// database for nothing.
	readyMu sync.Mutex
	ready   map[string]readyState

	// quiet holds, per project, when the sweep may next try a database that
	// did not answer. Without it an unreachable tenant is retried on every
	// tick forever, at the cost of a connection timeout each time.
	quietMu sync.Mutex
	quiet   map[string]backoffState
}

// readyState is the remembered answer to "does this database carry the
// scheduler tables", with when it was asked.
type readyState struct {
	present   bool
	checkedAt time.Time
}

// negativeReadyTTL is how long "no scheduler tables" is believed.
const negativeReadyTTL = 5 * time.Minute

// backoffState is how long a project stays out of the sweep and how many
// consecutive failures it has had.
type backoffState struct {
	until   time.Time
	strikes int
}

// Bounds on the negative-result backoff: the first failure costs a tick,
// a persistent one settles at five minutes.
const (
	minProjectBackoff = 5 * time.Second
	maxProjectBackoff = 5 * time.Minute
)

func NewFanout(c FanoutConfig) *Fanout {
	logger := c.Logger
	if logger == nil {
		logger = log.Default()
	}
	poll := c.PollInterval
	if poll <= 0 {
		poll = 5 * time.Second
	}
	cronPoll := c.CronInterval
	if cronPoll <= 0 {
		cronPoll = 60 * time.Second
	}
	limits := c.Limits
	if limits.GlobalConcurrency <= 0 {
		limits.GlobalConcurrency = DefaultGlobalConcurrency
	}
	if limits.ProjectTimeout <= 0 {
		limits.ProjectTimeout = DefaultProjectTimeout
	}
	return &Fanout{
		projects:       c.Projects,
		db:             c.DB,
		invoker:        c.Invoker,
		cronLeader:     c.CronLeader,
		functions:      c.Functions,
		limits:         limits,
		global:         NewSemaphore(limits.GlobalConcurrency),
		poll:           poll,
		cronPoll:       cronPoll,
		projectTimeout: limits.ProjectTimeout,
		logger:         logger,
		now:            nowOr(c.Now),
		ready:          make(map[string]readyState),
		quiet:          make(map[string]backoffState),
	}
}

func nowOr(fn func() time.Time) func() time.Time {
	if fn == nil {
		return time.Now
	}
	return fn
}

// backOff puts a project out of the sweep for a while after a failure, with
// the wait doubling up to the cap.
func (f *Fanout) backOff(projectID string) {
	f.quietMu.Lock()
	defer f.quietMu.Unlock()
	state := f.quiet[projectID]
	state.strikes++
	wait := minProjectBackoff << min(state.strikes-1, 16)
	if wait > maxProjectBackoff || wait <= 0 {
		wait = maxProjectBackoff
	}
	state.until = f.now().Add(wait)
	f.quiet[projectID] = state
}

// clearBackoff forgets a project's failures once it answers again.
func (f *Fanout) clearBackoff(projectID string) {
	f.quietMu.Lock()
	defer f.quietMu.Unlock()
	delete(f.quiet, projectID)
}

// backedOff reports whether a project is still inside its quiet window.
func (f *Fanout) backedOff(projectID string) bool {
	f.quietMu.Lock()
	defer f.quietMu.Unlock()
	state, ok := f.quiet[projectID]
	return ok && f.now().Before(state.until)
}

// Run sweeps until the context is cancelled. A failing tick is logged and
// the loop continues: a tenant database being briefly unreachable must not
// take the scheduler out for every other tenant.
func (f *Fanout) Run(ctx context.Context) error {
	tasks := time.NewTicker(f.poll)
	defer tasks.Stop()
	crons := time.NewTicker(f.cronPoll)
	defer crons.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tasks.C:
			if err := f.TaskTick(ctx); err != nil {
				f.logger.Printf("scheduler.Fanout task tick: %v", err)
			}
		case <-crons.C:
			if err := f.CronTick(ctx); err != nil {
				f.logger.Printf("scheduler.Fanout cron tick: %v", err)
			}
		}
	}
}

// TaskTick drains each project's due tasks once. Exported so tests and
// operators can drive a deterministic cycle.
func (f *Fanout) TaskTick(ctx context.Context) error {
	return f.forEachProject(ctx, "task", func(ctx context.Context, projectID string, db *sql.DB) error {
		worker := NewWorker(WorkerConfig{
			DB:            db,
			ProjectID:     projectID,
			Invoker:       f.invoker,
			Functions:     f.functions,
			PollInterval:  f.poll,
			Batch:         f.limits.Batch,
			MaxAttempts:   f.limits.MaxAttempts,
			MaxArgsBytes:  f.limits.MaxArgsBytes,
			MaxConcurrent: f.limits.ProjectConcurrency,
			ClaimLease:    f.limits.ClaimLease,
			Global:        f.global,
			Logger:        f.logger,
		})
		return worker.Tick(ctx)
	})
}

// CronTick walks each project's cron registry once, on the leader only.
func (f *Fanout) CronTick(ctx context.Context) error {
	leader, err := f.cronLeader.IsLeader(ctx)
	if err != nil {
		return fmt.Errorf("cron leadership: %w", err)
	}
	if !leader {
		return nil
	}
	return f.forEachProject(ctx, "cron", func(ctx context.Context, projectID string, db *sql.DB) error {
		return NewCronRunner(CronRunnerConfig{
			DB:           db,
			ProjectID:    projectID,
			MinInterval:  f.limits.CronMinInterval,
			MaxJobs:      f.limits.CronMaxJobs,
			MaxArgsBytes: f.limits.MaxArgsBytes,
			Logger:       f.logger,
		}).Tick(ctx)
	})
}

// forEachProject resolves every servable project's database and runs sweep
// against the ones that carry the scheduler tables. Per-project failures are
// logged and the sweep continues; only an unreadable project list is fatal
// to the tick, because then the sweep covered nothing and must say so.
func (f *Fanout) forEachProject(ctx context.Context, kind string, sweep sweepFn) error {
	projects, err := f.projects(ctx)
	if err != nil {
		return fmt.Errorf("list projects: %w", err)
	}
	for _, projectID := range projects {
		if f.backedOff(projectID) {
			continue
		}
		if err := f.sweepProject(ctx, projectID, sweep); err != nil {
			f.logger.Printf("scheduler: %s sweep %s: %v", kind, projectID, err)
			f.backOff(projectID)
			continue
		}
		f.clearBackoff(projectID)
	}
	return nil
}

// sweepFn is one project's work for a tick, run under that project's own
// deadline rather than the process context.
type sweepFn func(context.Context, string, *sql.DB) error

// sweepProject runs one project's sweep under its own deadline, so a tenant
// whose database will not answer costs the platform one timeout instead of
// the sweep. The deadline alone is enough here: the sweep is sequential, so
// the worst a hostile project adds to every other project is that one wait,
// and a second one puts it into the existing backoff.
//
// A deadline that expires is returned as an error precisely so the backoff
// fires — a tenant that simply answers slowly never errors otherwise.
func (f *Fanout) sweepProject(ctx context.Context, projectID string, sweep sweepFn) error {
	ctx, cancel := context.WithTimeout(ctx, f.projectTimeout)
	defer cancel()
	db, err := f.db(ctx, projectID)
	if err != nil {
		return err
	}
	ready, err := f.hasSchedulerTables(ctx, projectID, db)
	if err != nil {
		return err
	}
	if !ready {
		return nil
	}
	if err := sweep(ctx, projectID, db); err != nil {
		return err
	}
	return ctx.Err()
}

// hasSchedulerTables reports whether the project's database carries the
// reserved scheduler tables. A project that never deployed a scheduled
// function has none, and asking is cheaper than failing a query per tick.
func (f *Fanout) hasSchedulerTables(ctx context.Context, projectID string, db *sql.DB) (bool, error) {
	now := f.now()
	f.readyMu.Lock()
	cached, seen := f.ready[projectID]
	f.readyMu.Unlock()
	if seen && (cached.present || now.Sub(cached.checkedAt) < negativeReadyTTL) {
		return cached.present, nil
	}
	var present bool
	if err := db.QueryRowContext(ctx,
		`SELECT to_regclass('excalibase.excalibase_scheduled_functions') IS NOT NULL`,
	).Scan(&present); err != nil {
		return false, fmt.Errorf("check scheduler tables: %w", err)
	}
	f.readyMu.Lock()
	f.ready[projectID] = readyState{present: present, checkedAt: now}
	f.readyMu.Unlock()
	return present, nil
}
