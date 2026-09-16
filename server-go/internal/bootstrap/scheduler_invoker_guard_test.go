package bootstrap

import (
	"context"
	"database/sql"
	"log"
	"strings"
	"testing"
)

// The worker dereferences the invoker for every due task. Booting without one
// used to start the goroutines and panic on the first task instead of failing
// visibly at startup.
func TestStartScheduler_nilInvoker_doesNotStart(t *testing.T) {
	var captured strings.Builder
	cfg := SchedulerBootConfig{
		Enabled: true,
		DB:      &sql.DB{},
		Logger:  log.New(&captured, "", 0),
	}

	handles := StartScheduler(context.Background(), cfg)

	if handles.Started() {
		t.Fatal("scheduler started without an invoker wired")
	}
	if !strings.Contains(captured.String(), "no invoker wired") {
		t.Errorf("expected a boot-skipped log naming the invoker, got %q", captured.String())
	}
}
