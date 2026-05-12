package bootstrap

import (
	"context"
	"database/sql"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/scheduler"
)

// stubRunner is a SchedulerRunner that records whether Run() was called
// and exits when the context is cancelled. Tests use it to assert that
// StartScheduler actually launches both runners (or doesn't, when
// disabled) without spinning up real Postgres connections.
type stubRunner struct {
	ran     atomic.Bool
	exited  atomic.Bool
}

func (s *stubRunner) Run(ctx context.Context) error {
	s.ran.Store(true)
	<-ctx.Done()
	s.exited.Store(true)
	return nil
}

// TestStartScheduler_EnabledLaunchesBothRunners — when cfg.Enabled is true
// and a DB is wired, both Worker.Run and CronRunner.Run goroutines must
// fire. The stubs let us assert this without touching Postgres.
func TestStartScheduler_EnabledLaunchesBothRunners(t *testing.T) {
	worker := &stubRunner{}
	cron := &stubRunner{}
	// non-nil sentinel DB so the boot guard doesn't bail. The stub
	// runners never deref it.
	db := &sql.DB{}

	handles := StartScheduler(context.Background(), SchedulerBootConfig{
		Enabled:      true,
		PollInterval: 100 * time.Millisecond,
		DB:           db,
		NewWorker: func(_ scheduler.WorkerConfig) SchedulerRunner {
			return worker
		},
		NewCronRunner: func(_ scheduler.CronRunnerConfig) SchedulerRunner {
			return cron
		},
	})

	if !handles.Started() {
		t.Fatal("Started() = false, want true when Enabled and DB are set")
	}
	// Give the goroutines a moment to call Run().
	for i := 0; i < 50; i++ {
		if worker.ran.Load() && cron.ran.Load() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !worker.ran.Load() {
		t.Errorf("worker.Run was not invoked")
	}
	if !cron.ran.Load() {
		t.Errorf("cron.Run was not invoked")
	}

	// Stop must cancel the context and wait for both runners to exit.
	handles.Stop()
	if !worker.exited.Load() {
		t.Errorf("worker did not observe ctx.Done on Stop")
	}
	if !cron.exited.Load() {
		t.Errorf("cron did not observe ctx.Done on Stop")
	}
}

// TestStartScheduler_DisabledDoesNotLaunch — EXCALIBASE_SCHEDULER_ENABLED=0
// (modelled here as Enabled:false) must skip the goroutines entirely. Stop
// is a no-op.
func TestStartScheduler_DisabledDoesNotLaunch(t *testing.T) {
	worker := &stubRunner{}
	cron := &stubRunner{}
	db := &sql.DB{}

	handles := StartScheduler(context.Background(), SchedulerBootConfig{
		Enabled: false,
		DB:      db,
		NewWorker: func(_ scheduler.WorkerConfig) SchedulerRunner {
			return worker
		},
		NewCronRunner: func(_ scheduler.CronRunnerConfig) SchedulerRunner {
			return cron
		},
	})

	if handles.Started() {
		t.Fatalf("Started() = true when Enabled=false, want false")
	}
	// Stop() must be safe to call even when nothing was started.
	handles.Stop()

	// Give the goroutines a hypothetical chance to run before asserting.
	time.Sleep(50 * time.Millisecond)
	if worker.ran.Load() {
		t.Errorf("worker.Run unexpectedly invoked when Enabled=false")
	}
	if cron.ran.Load() {
		t.Errorf("cron.Run unexpectedly invoked when Enabled=false")
	}
}

// TestStartScheduler_GracefulShutdown — the parent context being cancelled
// must propagate through to the scheduler runners.
func TestStartScheduler_GracefulShutdown(t *testing.T) {
	worker := &stubRunner{}
	cron := &stubRunner{}
	db := &sql.DB{}

	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	handles := StartScheduler(parent, SchedulerBootConfig{
		Enabled: true,
		DB:      db,
		NewWorker: func(_ scheduler.WorkerConfig) SchedulerRunner {
			return worker
		},
		NewCronRunner: func(_ scheduler.CronRunnerConfig) SchedulerRunner {
			return cron
		},
	})
	// Wait for both runners to enter Run before cancelling.
	for i := 0; i < 50 && (!worker.ran.Load() || !cron.ran.Load()); i++ {
		time.Sleep(10 * time.Millisecond)
	}

	cancelParent()
	// Stop blocks until both goroutines return.
	handles.Stop()
	if !worker.exited.Load() || !cron.exited.Load() {
		t.Fatalf("graceful shutdown failed: worker.exited=%v cron.exited=%v",
			worker.exited.Load(), cron.exited.Load())
	}
}

// TestStartScheduler_NoDBSkips — calling StartScheduler with cfg.Enabled=true
// but a nil DB must not panic and must report Started()=false.
func TestStartScheduler_NoDBSkips(t *testing.T) {
	handles := StartScheduler(context.Background(), SchedulerBootConfig{
		Enabled: true,
	})
	if handles.Started() {
		t.Errorf("Started()=true with no DB; should skip boot")
	}
	handles.Stop()
}

// TestSchedulerConfigFromEnv_Defaults — with no env vars set, defaults
// should be Enabled=true, PollInterval=5s, CronPollInterval=60s.
func TestSchedulerConfigFromEnv_Defaults(t *testing.T) {
	t.Setenv("EXCALIBASE_SCHEDULER_ENABLED", "")
	t.Setenv("EXCALIBASE_SCHEDULER_POLL_MS", "")
	t.Setenv("EXCALIBASE_CRON_POLL_MS", "")
	c := SchedulerConfigFromEnv()
	if !c.Enabled {
		t.Errorf("Enabled default: got false, want true")
	}
	if c.PollInterval != 5*time.Second {
		t.Errorf("PollInterval default: got %v, want 5s", c.PollInterval)
	}
	if c.CronPollInterval != 60*time.Second {
		t.Errorf("CronPollInterval default: got %v, want 60s", c.CronPollInterval)
	}
}

// TestSchedulerConfigFromEnv_DisabledByEnv — common falsey values for the
// SCHEDULER_ENABLED flag must turn the scheduler off.
func TestSchedulerConfigFromEnv_DisabledByEnv(t *testing.T) {
	cases := []string{"0", "false", "FALSE", "no", "off"}
	for _, v := range cases {
		t.Run(v, func(t *testing.T) {
			t.Setenv("EXCALIBASE_SCHEDULER_ENABLED", v)
			c := SchedulerConfigFromEnv()
			if c.Enabled {
				t.Errorf("Enabled with env=%q: got true, want false", v)
			}
		})
	}
}

// TestSchedulerConfigFromEnv_OverridesPoll — explicit ms values override
// the defaults; non-numeric values are ignored.
func TestSchedulerConfigFromEnv_OverridesPoll(t *testing.T) {
	t.Setenv("EXCALIBASE_SCHEDULER_POLL_MS", "1500")
	t.Setenv("EXCALIBASE_CRON_POLL_MS", "120000")
	c := SchedulerConfigFromEnv()
	if c.PollInterval != 1500*time.Millisecond {
		t.Errorf("PollInterval: got %v, want 1.5s", c.PollInterval)
	}
	if c.CronPollInterval != 120000*time.Millisecond {
		t.Errorf("CronPollInterval: got %v, want 120s", c.CronPollInterval)
	}
	// Bad values stay at defaults.
	t.Setenv("EXCALIBASE_SCHEDULER_POLL_MS", "nope")
	c = SchedulerConfigFromEnv()
	if c.PollInterval != 5*time.Second {
		t.Errorf("PollInterval bad-value fallback: got %v, want 5s", c.PollInterval)
	}
}

// silenceLogger keeps test output clean when the production path's
// fallback to log.Default() would otherwise dump messages.
func init() {
	_ = os.Stdout
}
