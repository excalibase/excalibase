package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// RestoreStep is one named stage in a restore. Each step runs at most
// once per RestoreJob and writes its name into restore_jobs.current_step
// before running so platform restarts mid-flight are observable.
type RestoreStep struct {
	Name string
	Run  func(ctx context.Context, j *domain.RestoreJob) error
}

// StepFn is sugar for use sites that want to inline a step.
type StepFn func(ctx context.Context, j *domain.RestoreJob) error

// RestoreOrchestrator runs the multi-step restore pipeline against
// a registered set of RestoreSteps. Steps are run sequentially; the
// first failure marks the RestoreJob FAILED and stops further steps.
//
// RestoreFromBackup returns RUNNING immediately and the goroutine drives
// the steps. That goroutine is the only thing driving a job, so a platform
// restart abandons every RUNNING job — SweepStale fails them at boot.
type RestoreOrchestrator struct {
	jobs     storage.RestoreJobStore
	logger   *log.Logger
	instance string
	// heartbeat is how often a driving job proves it is alive; stale is how
	// long a job may go unheard before another replica may fail it. stale is
	// several heartbeats so a slow write or a brief pause is not mistaken
	// for a dead process.
	heartbeat time.Duration
	stale     time.Duration
	now       func() time.Time

	mu    sync.Mutex
	steps []RestoreStep
}

// RestoreOrchestratorConfig wires the collaborators. InstanceID, Heartbeat,
// Stale and Now have working defaults; tests override them.
type RestoreOrchestratorConfig struct {
	Jobs   storage.RestoreJobStore
	Logger *log.Logger
	// InstanceID names this platform process in restore_jobs.owner. It must
	// differ between replicas; the default is generated per process.
	InstanceID string
	Heartbeat  time.Duration
	Stale      time.Duration
	Now        func() time.Time
}

// defaultRestoreHeartbeat / defaultRestoreHeartbeatStale: a driver proves it
// is alive every 15s, and a job is only judged abandoned after 90s of
// silence — six missed heartbeats. The gap is deliberately wide: failing a
// restore that is actually running is worse than failing one late, because
// the caller retries and ends up building the project twice.
const (
	defaultRestoreHeartbeat      = 15 * time.Second
	defaultRestoreHeartbeatStale = 90 * time.Second
)

func NewRestoreOrchestrator(c RestoreOrchestratorConfig) *RestoreOrchestrator {
	logger := c.Logger
	if logger == nil {
		logger = log.Default()
	}
	instance := c.InstanceID
	if instance == "" {
		instance = newInstanceID()
	}
	beat := c.Heartbeat
	if beat == 0 {
		beat = defaultRestoreHeartbeat
	}
	stale := c.Stale
	if stale == 0 {
		stale = defaultRestoreHeartbeatStale
	}
	now := c.Now
	if now == nil {
		now = time.Now
	}
	return &RestoreOrchestrator{
		jobs: c.Jobs, logger: logger, instance: instance,
		heartbeat: beat, stale: stale, now: now,
	}
}

// newInstanceID names this process in the jobs it owns. It only has to be
// unique among the replicas running at one time.
func newInstanceID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("instance-%d", time.Now().UnixNano())
	}
	return "instance-" + hex.EncodeToString(b)
}

// SetSteps replaces the step pipeline. Order matters — steps run
// in slice order. Tests use this to inject deterministic fakes.
func (o *RestoreOrchestrator) SetSteps(steps []RestoreStep) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.steps = steps
}

// Start submits a new RestoreJob and kicks off the async run. Returns
// a snapshot of the job row immediately — caller polls via Get to
// track progress. The async goroutine works on its own copy of the
// job so the snapshot returned to the caller is safe to read without
// synchronisation.
func (o *RestoreOrchestrator) Start(ctx context.Context, source *domain.DatabaseInstance, req domain.RestoreRequest) (*domain.RestoreJob, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if req.TargetProjectID == "" {
		return nil, ErrTargetProjectIDMissing
	}
	kind, value := req.RestoreTargetKind()
	job := domain.RestoreJob{
		ID:              newRestoreJobID(),
		SourceProjectID: source.ProjectID,
		NewProjectID:    req.TargetProjectID,
		NewProjectName:  req.NewProjectName,
		Status:          domain.RestoreStatusRunning,
		TargetKind:      kind,
		TargetValue:     value,
		Owner:           o.instance,
	}
	if err := o.jobs.UpsertRestoreJob(ctx, &job); err != nil {
		return nil, fmt.Errorf("persist restore job: %w", err)
	}

	// Hand the goroutine a separate copy so the caller's snapshot
	// stays immutable. UpsertRestoreJob mutates the struct (sets
	// CreatedAt / UpdatedAt) so we re-copy after persisting.
	worker := job
	go o.run(context.Background(), &worker)
	snapshot := job
	return &snapshot, nil
}

// Get retrieves a restore job by id, within the project polling for it.
func (o *RestoreOrchestrator) Get(ctx context.Context, projectID, id string) (*domain.RestoreJob, error) {
	return o.jobs.FindRestoreJob(ctx, projectID, id)
}

// abandonedRestoreReason is what a caller sees on a job whose driver died.
// A restore cannot be resumed by observation: the only place that knew how
// far it got is the goroutine that is gone, and the target project it was
// building was never activated, so nothing usable was left behind. Marking
// the job FAILED lets the user re-trigger, which is a clean start.
const abandonedRestoreReason = "the process running this restore stopped responding; start it again"

// SweepAbandoned fails every RUNNING job whose owner has gone silent. It runs
// at boot and on a timer under leadership, so a job orphaned by a crashed
// replica is failed without waiting for the next restart.
//
// It never touches a job this process owns, nor one whose heartbeat is still
// fresh. That is what makes it safe during a rolling deploy: a restarting
// replica sees its peers' restores as live and leaves them running.
func (o *RestoreOrchestrator) SweepAbandoned(ctx context.Context) error {
	failed, err := o.jobs.FailAbandonedRestoreJobs(ctx, o.instance, o.stale, abandonedRestoreReason)
	if err != nil {
		return fmt.Errorf("fail abandoned restore jobs: %w", err)
	}
	for _, id := range failed {
		o.logger.Printf("restore %s: owner stopped responding for %s; marked FAILED", id, o.stale)
	}
	return nil
}

// SweepStale is the boot-time name for SweepAbandoned.
func (o *RestoreOrchestrator) SweepStale(ctx context.Context) error {
	return o.SweepAbandoned(ctx)
}

// StartSweeper runs SweepAbandoned on a timer for as long as this process is
// the leader, so an orphaned job is failed while the platform is up rather
// than at the next restart. Returns a function that stops the sweeper.
func (o *RestoreOrchestrator) StartSweeper(ctx context.Context, leader LeaderChecker, every time.Duration) func() {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				o.sweepIfLeader(ctx, leader)
			}
		}
	}()
	return cancel
}

// LeaderChecker is the slice of Leadership the sweeper needs: only one
// replica should be failing other replicas' jobs at a time.
type LeaderChecker interface {
	IsLeader(ctx context.Context) (bool, error)
}

func (o *RestoreOrchestrator) sweepIfLeader(ctx context.Context, leader LeaderChecker) {
	if leader != nil {
		ok, err := leader.IsLeader(ctx)
		if err != nil {
			o.logger.Printf("restore sweep: leadership check: %v", err)
			return
		}
		if !ok {
			return
		}
	}
	if err := o.SweepAbandoned(ctx); err != nil {
		o.logger.Printf("restore sweep: %v", err)
	}
}

// run executes the registered steps in order. Every write is conditional on
// this process still owning a RUNNING job; the moment one is refused the job
// has been taken (its owner was judged dead), so the step's context is
// cancelled — which is what makes the adapter stop and run its compensations
// — and nothing further is written over the recorded outcome.
func (o *RestoreOrchestrator) run(ctx context.Context, j *domain.RestoreJob) {
	o.mu.Lock()
	steps := append([]RestoreStep{}, o.steps...)
	o.mu.Unlock()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopBeating := o.beat(ctx, j.ID, cancel)
	defer stopBeating()

	for _, step := range steps {
		j.CurrentStep = step.Name
		if !o.write(ctx, j, "claim step "+step.Name) {
			cancel()
			return
		}
		if err := step.Run(ctx, j); err != nil {
			j.Status = domain.RestoreStatusFailed
			j.FailureReason = fmt.Sprintf("step %q: %v", step.Name, err)
			o.write(ctx, j, "record failure")
			return
		}
	}
	j.Status = domain.RestoreStatusCompleted
	j.CurrentStep = ""
	o.write(ctx, j, "record completion")
}

// write persists the job, reporting whether this process still owns it.
func (o *RestoreOrchestrator) write(ctx context.Context, j *domain.RestoreJob, what string) bool {
	ok, err := o.jobs.UpdateRunningRestoreJob(ctx, j, o.instance)
	if err != nil {
		o.logger.Printf("restore %s: %s: %v", j.ID, what, err)
		return false
	}
	if !ok {
		o.logger.Printf("restore %s: %s refused; the job is no longer ours", j.ID, what)
	}
	return ok
}

// beat refreshes the job's liveness marker while a step runs, so a long wait
// is not mistaken for a dead process. Losing the job cancels the run.
func (o *RestoreOrchestrator) beat(ctx context.Context, id string, lost context.CancelFunc) func() {
	ctx, stop := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(o.heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				held, err := o.jobs.HeartbeatRestoreJob(ctx, id, o.instance)
				if err != nil {
					o.logger.Printf("restore %s: heartbeat: %v", id, err)
					continue
				}
				if !held {
					o.logger.Printf("restore %s: job taken; stopping so its resources are cleaned up", id)
					lost()
					return
				}
			}
		}
	}()
	return stop
}

// newRestoreJobID is a 16-byte hex token. Collision-resistant enough
// for the restore-job space; doesn't need to be cryptographic.
func newRestoreJobID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// Fall back to ns timestamp — uniqueness still holds within
		// a single platform process, and the DB has UNIQUE on id.
		return fmt.Sprintf("restore-%d", time.Now().UnixNano())
	}
	return "restore-" + hex.EncodeToString(b)
}

// ErrRestoreJobNotFound is returned by handlers polling on an unknown id.
var ErrRestoreJobNotFound = errors.New("restore job not found")
