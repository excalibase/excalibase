package edgefn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

const (
	testProjectA = "proj_a"
	testBootA    = "boot-a"
	testBootB    = "boot-b"
)

// fakeReplayRuntime stands in for a per-project Deno runtime. Tests drive the
// reported status by hand; every Deploy is recorded so assertions can count
// exactly how many replays happened.
type fakeReplayRuntime struct {
	mu        sync.Mutex
	status    RuntimeStatus
	statusErr error
	failIDs   map[string]bool
	deploys   []string
}

func (f *fakeReplayRuntime) Status(context.Context) (RuntimeStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status, f.statusErr
}

func (f *fakeReplayRuntime) Deploy(_ context.Context, req DeployRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deploys = append(f.deploys, req.ID)
	if f.failIDs[req.ID] {
		return errors.New("deploy failed")
	}
	return nil
}

func (f *fakeReplayRuntime) setStatus(bootID string, scripts int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = RuntimeStatus{Healthy: true, BootID: bootID, Scripts: scripts}
	f.statusErr = nil
}

func (f *fakeReplayRuntime) deployCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.deploys)
}

// replayFixture wires a Replayer against one project with `n` stored functions.
type replayFixture struct {
	runtime  *fakeReplayRuntime
	replayer *Replayer
	now      time.Time
	listErr  error
}

func newReplayFixture(t *testing.T, functionCount int, tune func(*ReplayConfig)) *replayFixture {
	t.Helper()
	fx := &replayFixture{
		runtime: &fakeReplayRuntime{failIDs: map[string]bool{}},
		now:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	cfg := ReplayConfig{
		Projects: func() ([]string, error) { return []string{testProjectA}, fx.listErr },
		Runtime: func(context.Context, string) (ReplayRuntime, error) {
			return fx.runtime, nil
		},
		Deploys: func(_ context.Context, projectID string) ([]DeployRequest, error) {
			out := make([]DeployRequest, 0, functionCount)
			for i := 0; i < functionCount; i++ {
				out = append(out, DeployRequest{ID: fmt.Sprintf("%s__fn%d", projectID, i), Code: "x"})
			}
			return out, nil
		},
		Logger: log.New(io.Discard, "", 0),
		Now:    func() time.Time { return fx.now },
	}
	if tune != nil {
		tune(&cfg)
	}
	fx.replayer = NewReplayer(cfg)
	return fx
}

func (fx *replayFixture) tick(t *testing.T) {
	t.Helper()
	fx.replayer.Tick(context.Background())
}

func (fx *replayFixture) advance(d time.Duration) { fx.now = fx.now.Add(d) }

func assertDeploys(t *testing.T, fx *replayFixture, want int) {
	t.Helper()
	if got := fx.runtime.deployCount(); got != want {
		t.Fatalf("deploys: got %d, want %d (%v)", got, want, fx.runtime.deploys)
	}
}

func TestReplayer_ColdRuntimeIsReplayedExactlyOnce(t *testing.T) {
	fx := newReplayFixture(t, 3, nil)
	fx.runtime.setStatus(testBootA, 0)

	fx.tick(t)
	assertDeploys(t, fx, 3)

	fx.runtime.setStatus(testBootA, 3)
	fx.tick(t)
	fx.tick(t)
	assertDeploys(t, fx, 3)
}

func TestReplayer_WarmRuntimeOnFirstSightIsLeftAlone(t *testing.T) {
	fx := newReplayFixture(t, 3, nil)
	fx.runtime.setStatus(testBootA, 3)

	fx.tick(t)
	assertDeploys(t, fx, 0)
}

func TestReplayer_BootIDChangeTriggersExactlyOneReplay(t *testing.T) {
	fx := newReplayFixture(t, 2, nil)
	fx.runtime.setStatus(testBootA, 2)
	fx.tick(t)
	assertDeploys(t, fx, 0)

	fx.advance(time.Hour)
	fx.runtime.setStatus(testBootB, 0)
	fx.tick(t)
	assertDeploys(t, fx, 2)

	fx.runtime.setStatus(testBootB, 2)
	fx.tick(t)
	fx.tick(t)
	assertDeploys(t, fx, 2)
}

func TestReplayer_UnchangedBootIDNeverReplays(t *testing.T) {
	fx := newReplayFixture(t, 2, nil)
	fx.runtime.setStatus(testBootA, 2)
	for i := 0; i < 10; i++ {
		fx.advance(time.Minute)
		fx.tick(t)
	}
	assertDeploys(t, fx, 0)
}

// Runtimes that predate the bootId field still get replayed: a runtime we
// previously filled that now reports zero loaded scripts has restarted.
func TestReplayer_ScriptsDroppingToZeroWithoutBootIDIsARestart(t *testing.T) {
	fx := newReplayFixture(t, 2, nil)
	fx.runtime.setStatus("", 0)
	fx.tick(t)
	assertDeploys(t, fx, 2)

	fx.runtime.setStatus("", 2)
	fx.advance(time.Hour)
	fx.tick(t)
	assertDeploys(t, fx, 2)

	fx.runtime.setStatus("", 0)
	fx.advance(time.Hour)
	fx.tick(t)
	assertDeploys(t, fx, 4)
}

// A legacy runtime seen warm first (provisioning restarted, runtime did not)
// must still be repaired when it later restarts and reports zero scripts.
func TestReplayer_WarmLegacyRuntimeThatLaterEmptiesIsReplayed(t *testing.T) {
	fx := newReplayFixture(t, 2, nil)
	fx.runtime.setStatus("", 2)
	fx.tick(t)
	assertDeploys(t, fx, 0)

	fx.runtime.setStatus("", 0)
	fx.advance(time.Hour)
	fx.tick(t)
	assertDeploys(t, fx, 2)
}

func TestReplayer_FlappingRuntimeIsRateLimited(t *testing.T) {
	fx := newReplayFixture(t, 1, func(c *ReplayConfig) { c.MinGap = time.Minute })
	fx.runtime.setStatus(testBootA, 0)
	fx.tick(t)
	assertDeploys(t, fx, 1)

	// Restart immediately after: inside MinGap, so the replay is deferred…
	fx.runtime.setStatus(testBootB, 0)
	fx.advance(10 * time.Second)
	fx.tick(t)
	assertDeploys(t, fx, 1)

	// …and a third restart inside the same window still yields ONE replay
	// once the gap has elapsed.
	fx.runtime.setStatus("boot-c", 0)
	fx.advance(10 * time.Second)
	fx.tick(t)
	assertDeploys(t, fx, 1)

	fx.advance(time.Minute)
	fx.tick(t)
	assertDeploys(t, fx, 2)
	fx.runtime.setStatus("boot-c", 1)
	fx.tick(t)
	assertDeploys(t, fx, 2)
}

func TestReplayer_DeployFailureIsRetriedWithBackoffNotLost(t *testing.T) {
	fx := newReplayFixture(t, 3, nil)
	fx.runtime.failIDs[testProjectA+"__fn1"] = true
	fx.runtime.setStatus(testBootA, 0)

	fx.tick(t)
	assertDeploys(t, fx, 3)

	// Inside the backoff window nothing is attempted.
	fx.advance(500 * time.Millisecond)
	fx.tick(t)
	assertDeploys(t, fx, 3)

	// After the backoff the whole project is replayed again (deploys are
	// idempotent) and now succeeds.
	fx.runtime.failIDs = map[string]bool{}
	fx.advance(5 * time.Second)
	fx.tick(t)
	assertDeploys(t, fx, 6)

	fx.runtime.setStatus(testBootA, 3)
	fx.advance(time.Hour)
	fx.tick(t)
	assertDeploys(t, fx, 6)
}

func TestReplayer_BackoffGrowsAndIsCapped(t *testing.T) {
	fx := newReplayFixture(t, 1, func(c *ReplayConfig) { c.MaxBackoff = 8 * time.Second })
	fx.runtime.failIDs[testProjectA+"__fn0"] = true
	fx.runtime.setStatus(testBootA, 0)

	// failures: 1 → 2s, 2 → 4s, 3 → 8s, 4 → 8s (capped)
	waits := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second}
	fx.tick(t)
	attempts := 1
	for _, wait := range waits {
		fx.advance(wait - time.Millisecond)
		fx.tick(t)
		assertDeploys(t, fx, attempts)
		fx.advance(time.Millisecond)
		fx.tick(t)
		attempts++
		assertDeploys(t, fx, attempts)
	}
}

func TestReplayer_UnreachableRuntimeIsSkippedUntilItAnswers(t *testing.T) {
	fx := newReplayFixture(t, 2, nil)
	fx.runtime.statusErr = errors.New("connection refused")

	fx.tick(t)
	assertDeploys(t, fx, 0)

	fx.runtime.setStatus(testBootA, 0)
	fx.tick(t)
	assertDeploys(t, fx, 2)
}

func TestReplayer_RuntimeLookupErrorIsSkipped(t *testing.T) {
	fx := newReplayFixture(t, 2, func(c *ReplayConfig) {
		c.Runtime = func(context.Context, string) (ReplayRuntime, error) {
			return nil, errors.New("no namespace")
		}
	})
	fx.tick(t)
	assertDeploys(t, fx, 0)
}

func TestReplayer_ProjectListErrorDoesNotPanic(t *testing.T) {
	fx := newReplayFixture(t, 2, nil)
	fx.listErr = errors.New("db down")
	fx.runtime.setStatus(testBootA, 0)
	fx.tick(t)
	assertDeploys(t, fx, 0)
}

func TestReplayer_EmptyProjectIsNotReplayed(t *testing.T) {
	fx := newReplayFixture(t, 0, nil)
	fx.runtime.setStatus(testBootA, 0)
	fx.tick(t)
	fx.runtime.setStatus(testBootB, 0)
	fx.advance(time.Hour)
	fx.tick(t)
	assertDeploys(t, fx, 0)
}

func TestReplayer_RunStopsWhenContextIsCancelled(t *testing.T) {
	fx := newReplayFixture(t, 1, func(c *ReplayConfig) { c.Interval = time.Millisecond })
	fx.runtime.setStatus(testBootA, 0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		fx.replayer.Run(ctx)
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for fx.runtime.deployCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	assertDeploys(t, fx, 1)
}

func TestReplayConfig_Defaults(t *testing.T) {
	r := NewReplayer(ReplayConfig{})
	if r.cfg.Interval != DefaultReplayInterval || r.cfg.MinGap != DefaultReplayMinGap ||
		r.cfg.MaxBackoff != DefaultReplayMaxBackoff || r.cfg.Logger == nil || r.cfg.Now == nil {
		t.Fatalf("defaults not applied: %+v", r.cfg)
	}
}

func TestReplayConfigFromEnv(t *testing.T) {
	t.Setenv("EXCALIBASE_FN_REPLAY_ENABLED", "false")
	t.Setenv("EXCALIBASE_FN_REPLAY_POLL_MS", "2500")
	enabled, cfg := ReplayConfigFromEnv()
	if enabled {
		t.Fatal("expected replay disabled")
	}
	if cfg.Interval != 2500*time.Millisecond {
		t.Fatalf("interval: got %v", cfg.Interval)
	}
	t.Setenv("EXCALIBASE_FN_REPLAY_ENABLED", "")
	t.Setenv("EXCALIBASE_FN_REPLAY_POLL_MS", "garbage")
	enabled, cfg = ReplayConfigFromEnv()
	if !enabled || cfg.Interval != DefaultReplayInterval {
		t.Fatalf("expected enabled with default interval, got %v %v", enabled, cfg.Interval)
	}
}

// --- RuntimeClient.Status ---

func TestRuntimeClient_Status_ReadsBootIDAndScripts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"status": "healthy", "scripts": 4, "bootId": "8f1c", "uptime": 12.5,
		})
	}))
	defer srv.Close()

	status, err := NewRuntimeClient(srv.URL, "").Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Healthy || status.BootID != "8f1c" || status.Scripts != 4 {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestRuntimeClient_Status_ToleratesLegacyHealthWithoutBootID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"status": "healthy", "scripts": 0})
	}))
	defer srv.Close()

	status, err := NewRuntimeClient(srv.URL, "").Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Healthy || status.BootID != "" || status.Scripts != 0 {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestRuntimeClient_Status_UnhealthyOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(503)
	}))
	defer srv.Close()

	status, err := NewRuntimeClient(srv.URL, "").Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Healthy {
		t.Fatalf("expected unhealthy, got %+v", status)
	}
}

func TestRuntimeClient_Status_ErrorWhenUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close()
	if _, err := NewRuntimeClient(srv.URL, "").Status(context.Background()); err == nil {
		t.Fatal("expected error for closed server")
	}
}

// --- ProjectIDs on the filesystem store ---

func TestFunctionStore_ProjectIDs_ListsEveryProjectWithFunctionsOnce(t *testing.T) {
	store := NewFunctionStore(t.TempDir())
	for _, pair := range [][2]string{{"proj_x", "a"}, {"proj_x", "b"}, {"proj_y", "c"}} {
		fn := &Function{ID: pair[1], ProjectID: pair[0], Name: pair[1], Active: true,
			Files: []File{{Path: "index.ts", Content: "export default () => new Response('ok')"}}}
		if err := store.Save(fn); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := store.ProjectIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != "proj_x" || ids[1] != "proj_y" {
		t.Fatalf("project ids: got %v", ids)
	}

	if err := store.Delete("proj_y", "c"); err != nil {
		t.Fatal(err)
	}
	ids, _ = store.ProjectIDs()
	if len(ids) != 1 || ids[0] != "proj_x" {
		t.Fatalf("after delete: got %v", ids)
	}
}

func TestProjectLister_ImplementedByBothStores(t *testing.T) {
	var _ ProjectLister = (*FunctionStore)(nil)
	var _ ProjectLister = (*PostgresFunctionStore)(nil)
}
