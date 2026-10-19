package dockerapps

import (
	"context"
	"testing"
	"time"
)

// EXC-651: an app killed for memory and restarted by the engine read ACTIVE;
// its restarts and the engine's reason are reported.
func TestAppRestartsReportsTheEnginesRestartsAndOOMKill(t *testing.T) {
	h := newHarness(t, nil)
	h.rollout(freeApp(), "dep-1")
	c := h.engine.appContainers("app-01")[0]
	stopped := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	c.state, c.restarts, c.oomKilled, c.exitCode, c.finishedAt = "restarting", 2, true, 137, stopped.Format(time.RFC3339Nano)

	restarts, err := h.rt.AppRestarts(context.Background(), testDB, "app-01")
	if err != nil {
		t.Fatal(err)
	}
	if restarts.Count != 2 || restarts.LastReason != "OOMKilled" || restarts.LastExitCode == nil || *restarts.LastExitCode != 137 {
		t.Fatalf("restarts = %+v, want 2 and OOMKilled (137)", restarts)
	}
	if restarts.LastAt == nil || !restarts.LastAt.Equal(stopped) {
		t.Errorf("lastAt = %v, want %v", restarts.LastAt, stopped)
	}
}

// A container the engine started again keeps its count and stop time; the
// engine clears the reason on start, so none is invented.
func TestAppRestartsOfARunningContainerKeepsTheCountAndStopTime(t *testing.T) {
	h := newHarness(t, nil)
	h.rollout(freeApp(), "dep-1")
	c := h.engine.appContainers("app-01")[0]
	stopped := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	c.restarts, c.finishedAt = 1, stopped.Format(time.RFC3339Nano)

	restarts, err := h.rt.AppRestarts(context.Background(), testDB, "app-01")
	if err != nil {
		t.Fatal(err)
	}
	if restarts.Count != 1 || restarts.LastReason != "" || restarts.LastAt == nil || !restarts.LastAt.Equal(stopped) {
		t.Fatalf("restarts = %+v, want 1 at %v with no reason", restarts, stopped)
	}
}

func TestAppRestartsOfACleanAppIsNone(t *testing.T) {
	h := newHarness(t, nil)
	h.rollout(freeApp(), "dep-1")
	restarts, err := h.rt.AppRestarts(context.Background(), testDB, "app-01")
	if err != nil {
		t.Fatal(err)
	}
	if restarts.Count != 0 || restarts.LastAt != nil {
		t.Fatalf("restarts = %+v, want none", restarts)
	}
}
