// Package bootstrap contains process-startup wiring extracted from
// cmd/server/main.go so it can be unit-tested. The scheduler boot path
// lives here so tests can assert the sweep actually fires — and that an
// enabled-but-unwired scheduler stops the process rather than running
// nothing in silence.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/scheduler"
)

// ErrSchedulerUnwired is returned when the scheduler is enabled but a
// collaborator it cannot work without is missing.
var ErrSchedulerUnwired = errors.New("function scheduler is enabled but not wired")

// SchedulerRunner is the subset of *scheduler.Fanout we need to start a
// long-running poll loop. Defined as an interface so tests can substitute
// lightweight stubs that don't open Postgres connections.
type SchedulerRunner interface {
	Run(ctx context.Context) error
}

// SchedulerBootConfig wires the scheduler startup with the collaborators
// it needs. Every field but Logger and NewRunner is required whenever
// Enabled is true.
type SchedulerBootConfig struct {
	// Enabled mirrors EXCALIBASE_SCHEDULER_ENABLED.
	Enabled bool
	// PollInterval is the task-queue cadence; CronPollInterval the cron
	// registry cadence.
	PollInterval     time.Duration
	CronPollInterval time.Duration
	// Invoker is the runtime adapter each due task is dispatched through.
	Invoker scheduler.Invoker
	// Functions is the platform's registry of deployed functions: a claimed
	// row may only name a module the platform itself deployed.
	Functions scheduler.FunctionRegistry
	// Limits bound what one tenant's rows can cost the platform.
	Limits scheduler.Limits
	// Projects lists the projects whose databases may be swept; ProjectDB
	// resolves one project's pool. Scheduled tasks and cron registries live
	// in the tenant's own database, so the sweep needs both.
	Projects  scheduler.ProjectsFn
	ProjectDB scheduler.ProjectDBFn
	// CronLeader decides whether this replica enqueues cron jobs. Cron
	// enqueues are not claim-protected, so exactly one replica may walk the
	// registry.
	CronLeader scheduler.Leader
	// Logger overrides the default std logger; useful in tests.
	Logger *log.Logger
	// NewRunner is an overridable factory so tests can plug in a stub sweep
	// without touching a real DB. Nil in production.
	NewRunner func(scheduler.FanoutConfig) SchedulerRunner
}

// missing names every collaborator an enabled scheduler lacks.
func (c SchedulerBootConfig) missing() []string {
	var gaps []string
	if c.Invoker == nil {
		gaps = append(gaps, "invoker")
	}
	if c.Functions == nil {
		gaps = append(gaps, "function registry")
	}
	if c.Projects == nil {
		gaps = append(gaps, "projects")
	}
	if c.ProjectDB == nil {
		gaps = append(gaps, "project db resolver")
	}
	if c.CronLeader == nil {
		gaps = append(gaps, "cron leader")
	}
	return gaps
}

// SchedulerHandles is returned from Start so the caller can wait for
// graceful shutdown. Stop cancels the sweep and blocks until it returns.
type SchedulerHandles struct {
	cancel  context.CancelFunc
	wg      *sync.WaitGroup
	started bool
}

// Stop cancels the scheduler context and waits for the sweep to exit.
// Safe to call multiple times — repeat calls are no-ops.
func (h *SchedulerHandles) Stop() {
	if h == nil || !h.started || h.cancel == nil {
		return
	}
	h.cancel()
	h.wg.Wait()
}

// Started reports whether the sweep was launched. Tests use this to assert
// the enabled/disabled gate without poking goroutine state.
func (h *SchedulerHandles) Started() bool {
	return h != nil && h.started
}

// StartScheduler launches the per-project sweep in its own goroutine when
// cfg.Enabled is true. It returns an error naming every missing
// collaborator instead of starting a scheduler that would run nothing.
func StartScheduler(ctx context.Context, cfg SchedulerBootConfig) (*SchedulerHandles, error) {
	if !cfg.Enabled {
		return &SchedulerHandles{}, nil
	}
	if gaps := cfg.missing(); len(gaps) > 0 {
		return &SchedulerHandles{}, fmt.Errorf("%w: missing %s", ErrSchedulerUnwired, strings.Join(gaps, ", "))
	}

	newRunner := cfg.NewRunner
	if newRunner == nil {
		newRunner = func(c scheduler.FanoutConfig) SchedulerRunner { return scheduler.NewFanout(c) }
	}
	runner := newRunner(scheduler.FanoutConfig{
		Projects:     cfg.Projects,
		DB:           cfg.ProjectDB,
		Invoker:      cfg.Invoker,
		Functions:    cfg.Functions,
		Limits:       cfg.Limits,
		CronLeader:   cfg.CronLeader,
		PollInterval: cfg.PollInterval,
		CronInterval: cfg.CronPollInterval,
		Logger:       cfg.Logger,
	})

	runCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := runner.Run(runCtx); err != nil && cfg.Logger != nil {
			cfg.Logger.Printf("function scheduler exited: %v", err)
		}
	}()
	return &SchedulerHandles{cancel: cancel, wg: &wg, started: true}, nil
}
