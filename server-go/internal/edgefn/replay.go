package edgefn

import (
	"context"
	"log"
	"sync"
	"time"
)

// Replay defaults. The poll is deliberately relaxed: a restarted runtime is
// unreachable for a few seconds anyway (image start + readiness), and every
// tick costs one /health round-trip per project with functions.
const (
	DefaultReplayInterval   = 15 * time.Second
	DefaultReplayMinGap     = 30 * time.Second
	DefaultReplayMaxBackoff = 5 * time.Minute
	replayBaseBackoff       = 2 * time.Second
	replayStatusTimeout     = 5 * time.Second
)

// ReplayRuntime is the slice of RuntimeClient the replayer needs.
type ReplayRuntime interface {
	Status(ctx context.Context) (RuntimeStatus, error)
	Deploy(ctx context.Context, req DeployRequest) error
}

// ProjectLister is implemented by stores that can enumerate every project
// holding at least one function — the set the replayer watches.
type ProjectLister interface {
	ProjectIDs() ([]string, error)
}

// ReplayConfig wires the Replayer to the control plane. Projects, Runtime and
// Deploys are required; the rest default.
type ReplayConfig struct {
	// Projects enumerates the projects that own functions.
	Projects func() ([]string, error)
	// Runtime resolves the project's runtime WITHOUT provisioning one — a
	// project whose runtime does not exist must yield an error or an
	// unreachable client, never a freshly created pod.
	Runtime func(ctx context.Context, projectID string) (ReplayRuntime, error)
	// Deploys builds the full deploy set for the project from the store.
	Deploys func(ctx context.Context, projectID string) ([]DeployRequest, error)
	// Interval is the poll period of Run.
	Interval time.Duration
	// MinGap is the minimum time between two replays of the same project —
	// a flapping runtime is replayed at most once per MinGap.
	MinGap time.Duration
	// MaxBackoff caps the retry delay after failed replays.
	MaxBackoff time.Duration
	Logger     *log.Logger
	// Now is injectable for tests.
	Now func() time.Time
}

// replayState is what the replayer remembers per project between ticks.
type replayState struct {
	bootID      string
	deployed    int
	lastReplay  time.Time
	failures    int
	nextAttempt time.Time
	pending     bool
}

// Replayer re-deploys a project's functions from the store whenever its Deno
// runtime comes up cold (EXC-337). The runtime keeps deployed code in memory
// only, so a pod restart forgets every function; the control plane is the
// source of truth and pushes them back.
//
// A restart is detected from the runtime's /health: a changed bootId, or a
// runtime we previously filled reporting zero loaded scripts (runtimes that
// predate the bootId field). Replays are idempotent, rate-limited per project
// (MinGap) and retried with capped exponential backoff on failure.
type Replayer struct {
	cfg   ReplayConfig
	mu    sync.Mutex
	state map[string]*replayState
}

func NewReplayer(cfg ReplayConfig) *Replayer {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultReplayInterval
	}
	if cfg.MinGap <= 0 {
		cfg.MinGap = DefaultReplayMinGap
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = DefaultReplayMaxBackoff
	}
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Replayer{cfg: cfg, state: make(map[string]*replayState)}
}

// Run polls until ctx is cancelled. One tick runs before the first wait so a
// runtime that restarted while provisioning was down is repaired promptly.
func (r *Replayer) Run(ctx context.Context) {
	ticker := time.NewTicker(r.cfg.Interval)
	defer ticker.Stop()
	for {
		r.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Tick inspects every project once. Exported so tests (and Run) drive it
// synchronously.
func (r *Replayer) Tick(ctx context.Context) {
	projects, err := r.cfg.Projects()
	if err != nil {
		r.cfg.Logger.Printf("WARN: function replay: list projects: %v", err)
		return
	}
	for _, projectID := range projects {
		if ctx.Err() != nil {
			return
		}
		r.inspect(ctx, projectID)
	}
}

// inspect probes one project's runtime and replays it when it looks cold.
func (r *Replayer) inspect(ctx context.Context, projectID string) {
	state := r.stateFor(projectID)
	now := r.cfg.Now()
	if now.Before(state.nextAttempt) {
		return
	}
	target, err := r.cfg.Runtime(ctx, projectID)
	if err != nil {
		return
	}
	status, err := r.probe(ctx, target)
	if err != nil || !status.Healthy {
		return
	}
	if !r.needsReplay(state, status) {
		state.bootID = status.BootID
		state.deployed = status.Scripts
		return
	}
	if r.throttled(state, now) {
		state.pending = true
		state.nextAttempt = state.lastReplay.Add(r.cfg.MinGap)
		return
	}
	r.replay(ctx, projectID, target, state, status)
}

// throttled applies MinGap after a successful replay only; retries of a
// failed replay are paced by the backoff in nextAttempt instead.
func (r *Replayer) throttled(state *replayState, now time.Time) bool {
	if state.failures > 0 || state.lastReplay.IsZero() {
		return false
	}
	return now.Sub(state.lastReplay) < r.cfg.MinGap
}

func (r *Replayer) probe(ctx context.Context, target ReplayRuntime) (RuntimeStatus, error) {
	probeCtx, cancel := context.WithTimeout(ctx, replayStatusTimeout)
	defer cancel()
	return target.Status(probeCtx)
}

// needsReplay decides whether the reported status means the runtime lost
// its functions since we last looked.
func (r *Replayer) needsReplay(state *replayState, status RuntimeStatus) bool {
	if state.pending {
		return true
	}
	first := state.lastReplay.IsZero() && state.bootID == "" && state.deployed == 0
	if first {
		return status.Scripts == 0
	}
	if status.BootID != "" && status.BootID != state.bootID {
		return true
	}
	return status.Scripts == 0 && state.deployed > 0
}

// replay pushes every stored function to the runtime and records the outcome.
func (r *Replayer) replay(ctx context.Context, projectID string, target ReplayRuntime, state *replayState, status RuntimeStatus) {
	now := r.cfg.Now()
	state.lastReplay = now
	state.bootID = status.BootID
	deploys, err := r.cfg.Deploys(ctx, projectID)
	if err != nil {
		r.fail(state, now, projectID, 0, err)
		return
	}
	if len(deploys) == 0 {
		r.succeed(state, 0)
		return
	}
	failed := 0
	for _, req := range deploys {
		if err := target.Deploy(ctx, req); err != nil {
			failed++
			r.cfg.Logger.Printf("WARN: function replay %s: %v", req.ID, err)
		}
	}
	if failed > 0 {
		r.fail(state, now, projectID, len(deploys), nil)
		return
	}
	r.succeed(state, len(deploys))
	r.cfg.Logger.Printf("function replay: project=%s bootId=%s functions=%d", projectID, status.BootID, len(deploys))
}

func (r *Replayer) succeed(state *replayState, deployed int) {
	state.deployed = deployed
	state.failures = 0
	state.pending = false
	state.nextAttempt = time.Time{}
}

func (r *Replayer) fail(state *replayState, now time.Time, projectID string, total int, err error) {
	state.failures++
	state.pending = true
	backoff := r.backoff(state.failures)
	state.nextAttempt = now.Add(backoff)
	if err != nil {
		r.cfg.Logger.Printf("WARN: function replay project=%s: %v (retry in %s)", projectID, err, backoff)
		return
	}
	r.cfg.Logger.Printf("WARN: function replay project=%s: some of %d deploys failed (retry in %s)", projectID, total, backoff)
}

// backoff is replayBaseBackoff doubled per consecutive failure, capped.
func (r *Replayer) backoff(failures int) time.Duration {
	d := replayBaseBackoff
	for i := 1; i < failures && d < r.cfg.MaxBackoff; i++ {
		d *= 2
	}
	if d > r.cfg.MaxBackoff {
		d = r.cfg.MaxBackoff
	}
	return d
}

func (r *Replayer) stateFor(projectID string) *replayState {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.state[projectID]
	if !ok {
		state = &replayState{}
		r.state[projectID] = state
	}
	return state
}
