// Package bootstrap contains process-startup wiring extracted from
// cmd/server/main.go so it can be unit-tested. Phase 8.5 introduces the
// scheduler boot path here so tests can assert the worker + cron runner
// actually fire (and stay disabled when the operator turns them off).
//
// This file is the RED skeleton — symbols exist so tests compile, but
// the behaviour is empty. The matching tests in scheduler_test.go must
// fail until the wiring is implemented in the GREEN commit.
package bootstrap

import (
	"context"
	"database/sql"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/scheduler"
)

// SchedulerRunner is the subset of *scheduler.Worker / *scheduler.CronRunner
// we need to start a long-running poll loop.
type SchedulerRunner interface {
	Run(ctx context.Context) error
}

// SchedulerBootConfig wires the scheduler startup. See scheduler_test.go
// for the contract the GREEN implementation must honour.
type SchedulerBootConfig struct {
	Enabled          bool
	PollInterval     time.Duration
	CronPollInterval time.Duration
	Invoker          scheduler.Invoker
	DB               *sql.DB
	Logger           *log.Logger
	NewWorker        func(scheduler.WorkerConfig) SchedulerRunner
	NewCronRunner    func(scheduler.CronRunnerConfig) SchedulerRunner
}

// SchedulerHandles is the boot lifecycle handle.
type SchedulerHandles struct{}

// Stop must cancel the scheduler context. Not yet implemented.
func (h *SchedulerHandles) Stop() {}

// Started must report whether the scheduler runners launched. Not yet
// implemented.
func (h *SchedulerHandles) Started() bool { return false }

// StartScheduler must launch the scheduler worker + cron runner when
// cfg.Enabled. RED skeleton: returns an empty handle so tests fail.
func StartScheduler(_ context.Context, _ SchedulerBootConfig) *SchedulerHandles {
	return &SchedulerHandles{}
}

// SchedulerConfigFromEnv must read EXCALIBASE_SCHEDULER_* knobs. RED
// skeleton: returns the zero config so tests fail.
func SchedulerConfigFromEnv() SchedulerBootConfig {
	return SchedulerBootConfig{}
}
