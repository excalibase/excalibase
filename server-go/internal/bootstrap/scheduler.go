// Package bootstrap contains process-startup wiring extracted from
// cmd/server/main.go so it can be unit-tested. Phase 8.5 introduces the
// scheduler boot path here so tests can assert the worker + cron runner
// actually fire (and stay disabled when the operator turns them off).
package bootstrap

import (
	"context"
	"database/sql"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/scheduler"
)

// SchedulerRunner is the subset of *scheduler.Worker / *scheduler.CronRunner
// we need to start a long-running poll loop. Defined as an interface so
// tests can substitute lightweight stubs that don't open Postgres
// connections.
type SchedulerRunner interface {
	Run(ctx context.Context) error
}

// SchedulerBootConfig wires the scheduler startup with the collaborators
// it needs. The DB is required when at least one runner is enabled in
// production; tests inject stubs to avoid that.
type SchedulerBootConfig struct {
	// Enabled mirrors EXCALIBASE_SCHEDULER_ENABLED. Default true.
	Enabled bool
	// PollInterval mirrors EXCALIBASE_SCHEDULER_POLL_MS.
	PollInterval time.Duration
	// CronPollInterval mirrors EXCALIBASE_CRON_POLL_MS. CronRunner has a
	// hard-coded 60s tick today; this field is reserved for the day we
	// make it configurable without churn at the call site.
	CronPollInterval time.Duration
	// Invoker is the runtime adapter the worker uses to dispatch each
	// due task. Required in production; stub in tests.
	Invoker scheduler.Invoker
	// DB is the project-DB pool (or the platform DB pool when the platform
	// owns the scheduler tables). Required in production.
	DB *sql.DB
	// Logger overrides the default std logger; useful in tests.
	Logger *log.Logger
	// NewWorker / NewCronRunner — overridable factories so tests can plug
	// in stub runners without touching a real DB. Nil in production
	// (defaults to scheduler.NewWorker / scheduler.NewCronRunner).
	NewWorker     func(scheduler.WorkerConfig) SchedulerRunner
	NewCronRunner func(scheduler.CronRunnerConfig) SchedulerRunner
}

// SchedulerHandles is returned from Start so the caller can wait for
// graceful shutdown. Stop cancels the goroutines and blocks until both
// runners have returned.
type SchedulerHandles struct {
	cancel  context.CancelFunc
	wg      *sync.WaitGroup
	started bool
}

// Stop cancels the scheduler context and waits for both runners to exit.
// Safe to call multiple times — repeat calls are no-ops.
func (h *SchedulerHandles) Stop() {
	if h == nil || !h.started || h.cancel == nil {
		return
	}
	h.cancel()
	h.wg.Wait()
}

// Started reports whether the scheduler runners were launched. Tests use
// this to assert env-gating without poking goroutine state.
func (h *SchedulerHandles) Started() bool {
	if h == nil {
		return false
	}
	return h.started
}

// StartScheduler launches the scheduler worker and cron runner in their
// own goroutines if cfg.Enabled is true. Returns a handle whose Stop()
// blocks until both runners exit (used by the SIGTERM path in main.go).
//
// When Enabled is false, returns an empty handle whose Started() is false
// and Stop() is a no-op — the only path tests rely on for the off-state.
func StartScheduler(ctx context.Context, cfg SchedulerBootConfig) *SchedulerHandles {
	if !cfg.Enabled {
		return &SchedulerHandles{}
	}
	if cfg.DB == nil {
		// No DB means the scheduler can't function — log and bail rather
		// than panicking inside the goroutines.
		logger := cfg.Logger
		if logger == nil {
			logger = log.Default()
		}
		logger.Printf("scheduler boot skipped: no DB wired")
		return &SchedulerHandles{}
	}
	if cfg.Invoker == nil && cfg.NewWorker == nil {
		// Same reasoning as the nil DB above: the default worker dereferences
		// the invoker for every due task, so starting without one turns the
		// first scheduled task into a panic instead of a clear failure. A
		// caller supplying its own worker factory owns that contract itself.
		logger := cfg.Logger
		if logger == nil {
			logger = log.Default()
		}
		logger.Printf("scheduler boot skipped: no invoker wired")
		return &SchedulerHandles{}
	}

	newWorker := cfg.NewWorker
	if newWorker == nil {
		newWorker = func(c scheduler.WorkerConfig) SchedulerRunner {
			return scheduler.NewWorker(c)
		}
	}
	newCronRunner := cfg.NewCronRunner
	if newCronRunner == nil {
		newCronRunner = func(c scheduler.CronRunnerConfig) SchedulerRunner {
			return scheduler.NewCronRunner(c)
		}
	}

	worker := newWorker(scheduler.WorkerConfig{
		DB:           cfg.DB,
		Invoker:      cfg.Invoker,
		PollInterval: cfg.PollInterval,
		Logger:       cfg.Logger,
	})
	cron := newCronRunner(scheduler.CronRunnerConfig{
		DB:     cfg.DB,
		Logger: cfg.Logger,
	})

	runCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := worker.Run(runCtx); err != nil && cfg.Logger != nil {
			cfg.Logger.Printf("scheduler worker exited: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		if err := cron.Run(runCtx); err != nil && cfg.Logger != nil {
			cfg.Logger.Printf("scheduler cron runner exited: %v", err)
		}
	}()
	return &SchedulerHandles{cancel: cancel, wg: &wg, started: true}
}

// SchedulerConfigFromEnv reads the EXCALIBASE_SCHEDULER_* knobs into a
// partially-filled SchedulerBootConfig. DB + Invoker are still the
// caller's responsibility. Defaults: Enabled=true, PollInterval=5s,
// CronPollInterval=60s.
func SchedulerConfigFromEnv() SchedulerBootConfig {
	c := SchedulerBootConfig{
		Enabled:          true,
		PollInterval:     5 * time.Second,
		CronPollInterval: 60 * time.Second,
	}
	if v := os.Getenv("EXCALIBASE_SCHEDULER_ENABLED"); v != "" {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "0", "false", "no", "off":
			c.Enabled = false
		}
	}
	if v := os.Getenv("EXCALIBASE_SCHEDULER_POLL_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			c.PollInterval = time.Duration(ms) * time.Millisecond
		}
	}
	if v := os.Getenv("EXCALIBASE_CRON_POLL_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			c.CronPollInterval = time.Duration(ms) * time.Millisecond
		}
	}
	return c
}
