package bootstrap

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/scheduler"
)

// stubRunner is a SchedulerRunner that records whether Run() was called
// and exits when the context is cancelled. Tests use it to assert that
// StartScheduler actually launches the sweep without touching Postgres.
type stubRunner struct {
	ran    atomic.Bool
	exited atomic.Bool
}

func (s *stubRunner) Run(ctx context.Context) error {
	s.ran.Store(true)
	<-ctx.Done()
	s.exited.Store(true)
	return nil
}

type stubLeader struct{}

func (stubLeader) IsLeader(context.Context) (bool, error) { return true, nil }

// wiredConfig is a boot config with every collaborator present.
func wiredConfig(runner SchedulerRunner) SchedulerBootConfig {
	return SchedulerBootConfig{
		Enabled:      true,
		PollInterval: 100 * time.Millisecond,
		Invoker:      schedulerInvokerStub{},
		Functions:    registryStub{},
		Projects:     func(context.Context) ([]string, error) { return nil, nil },
		ProjectDB:    func(context.Context, string) (*sql.DB, error) { return nil, nil },
		CronLeader:   stubLeader{},
		NewRunner:    func(scheduler.FanoutConfig) SchedulerRunner { return runner },
	}
}

// registryStub stands in for the platform's function registry.
type registryStub struct{}

func (registryStub) HasFunction(string, string) (bool, error) { return true, nil }

// schedulerInvokerStub satisfies scheduler.Invoker with the json.RawMessage
// signature the seam declares.
type schedulerInvokerStub struct{}

func (schedulerInvokerStub) Invoke(context.Context, string, string, string, json.RawMessage) error {
	return nil
}

func TestStartScheduler_EnabledLaunchesTheSweep(t *testing.T) {
	runner := &stubRunner{}
	handles, err := StartScheduler(context.Background(), wiredConfig(runner))
	if err != nil {
		t.Fatalf("StartScheduler: %v", err)
	}
	if !handles.Started() {
		t.Fatal("Started() = false, want true when enabled and fully wired")
	}
	for i := 0; i < 50 && !runner.ran.Load(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if !runner.ran.Load() {
		t.Error("the sweep was never run")
	}
	handles.Stop()
	if !runner.exited.Load() {
		t.Error("the sweep did not observe ctx.Done on Stop")
	}
}

func TestStartScheduler_DisabledDoesNotLaunch(t *testing.T) {
	runner := &stubRunner{}
	cfg := wiredConfig(runner)
	cfg.Enabled = false

	handles, err := StartScheduler(context.Background(), cfg)
	if err != nil {
		t.Fatalf("StartScheduler: %v", err)
	}
	if handles.Started() {
		t.Fatal("Started() = true when disabled")
	}
	handles.Stop()
	time.Sleep(50 * time.Millisecond)
	if runner.ran.Load() {
		t.Error("the sweep ran while the scheduler was disabled")
	}
}

// The finding: with the scheduler enabled and a collaborator missing, the
// platform used to boot, log a line and run nothing. Startup now fails and
// names what is missing.
func TestStartScheduler_EnabledButUnwiredFailsToStart(t *testing.T) {
	cases := map[string]func(*SchedulerBootConfig){
		"invoker":           func(c *SchedulerBootConfig) { c.Invoker = nil },
		"function registry": func(c *SchedulerBootConfig) { c.Functions = nil },
		"projects":          func(c *SchedulerBootConfig) { c.Projects = nil },
		"project db":        func(c *SchedulerBootConfig) { c.ProjectDB = nil },
		"leader":            func(c *SchedulerBootConfig) { c.CronLeader = nil },
	}
	for missing, strip := range cases {
		t.Run(missing, func(t *testing.T) {
			runner := &stubRunner{}
			cfg := wiredConfig(runner)
			strip(&cfg)

			handles, err := StartScheduler(context.Background(), cfg)
			if !errors.Is(err, ErrSchedulerUnwired) {
				t.Fatalf("err: got %v, want ErrSchedulerUnwired", err)
			}
			if !strings.Contains(err.Error(), missing) {
				t.Errorf("err must name the missing collaborator %q: %v", missing, err)
			}
			if handles.Started() {
				t.Error("a half-wired scheduler must not start")
			}
			time.Sleep(20 * time.Millisecond)
			if runner.ran.Load() {
				t.Error("a half-wired scheduler ran anyway")
			}
		})
	}
}

// A disabled scheduler needs no collaborators — an operator who turned it
// off must not be forced to wire it.
func TestStartScheduler_DisabledAndUnwiredIsFine(t *testing.T) {
	handles, err := StartScheduler(context.Background(), SchedulerBootConfig{Enabled: false})
	if err != nil {
		t.Fatalf("StartScheduler: %v", err)
	}
	if handles.Started() {
		t.Error("Started() = true when disabled")
	}
	handles.Stop()
}

func TestStartScheduler_GracefulShutdown(t *testing.T) {
	runner := &stubRunner{}
	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()

	handles, err := StartScheduler(parent, wiredConfig(runner))
	if err != nil {
		t.Fatalf("StartScheduler: %v", err)
	}
	for i := 0; i < 50 && !runner.ran.Load(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	cancelParent()
	handles.Stop()
	if !runner.exited.Load() {
		t.Fatal("graceful shutdown failed: the sweep did not exit")
	}
}

// The production path builds the real sweep — nothing in main supplies a
// factory, so the default one has to work.
func TestStartScheduler_BuildsTheRealSweepByDefault(t *testing.T) {
	cfg := wiredConfig(nil)
	cfg.NewRunner = nil
	cfg.PollInterval = time.Hour
	cfg.CronPollInterval = time.Hour

	handles, err := StartScheduler(context.Background(), cfg)
	if err != nil {
		t.Fatalf("StartScheduler: %v", err)
	}
	if !handles.Started() {
		t.Fatal("the default sweep was not started")
	}
	handles.Stop()
}
