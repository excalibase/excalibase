package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
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
	after     func(time.Duration) <-chan time.Time
	instances storage.InstanceStore

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
	// Instances is where a swept job's target project is told its restore
	// was interrupted. Optional: without it the job is still failed.
	Instances storage.InstanceStore
	Heartbeat  time.Duration
	Stale      time.Duration
	Now        func() time.Time
	// After is the orchestrator's only sleep, used to space retries of a
	// job write the store could not accept. Injected so tests never wait.
	After func(time.Duration) <-chan time.Time
}

// defaultRestoreHeartbeat / defaultRestoreHeartbeatStale: a driver proves it
// is alive every 15s, and a job is only judged abandoned after 90s of
// silence — six missed heartbeats. The gap is deliberately wide: failing a
// restore that is actually running is worse than failing one late, because
// the caller retries and ends up building the project twice.
const (
	defaultRestoreHeartbeat      = 15 * time.Second
	defaultRestoreHeartbeatStale = 90 * time.Second
	// defaultRestoreWriteAttempts / Backoff bound the retry of a job write
	// the store could not accept. Short: the write is bookkeeping, and the
	// heartbeat is the thing that decides ownership.
	defaultRestoreWriteAttempts = 3
	defaultRestoreWriteBackoff  = time.Second
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
	after := c.After
	if after == nil {
		after = time.After
	}
	return &RestoreOrchestrator{
		jobs: c.Jobs, logger: logger, instance: instance, instances: c.Instances,
		heartbeat: beat, stale: stale, now: now, after: after,
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
	go o.drive(context.Background(), &worker)
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

// restoreInterruptedStep and restoreInterruptedReason are what the target
// project carries after its restore was abandoned. The project is left in
// RESTORING on purpose: that is what stops it being served, and it is also
// what lets it be deleted. Deleting it here instead would destroy resources
// on the strength of a missed heartbeat.
const (
	restoreInterruptedStep   = "RESTORE_INTERRUPTED"
	restoreInterruptedReason = "the restore that was building this project was interrupted and cannot be resumed; delete this project and start the restore again"
)

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
	for _, job := range failed {
		o.logger.Printf("restore %s: owner stopped responding for %s; marked FAILED", job.ID, o.stale)
		o.markTargetInterrupted(job)
	}
	return nil
}

// markTargetInterrupted leaves the reason on the project the restore was
// building, so the user reading GET /api/provision/{id} is told what
// happened and what to do — rather than finding a project stuck in RESTORING
// with nothing to explain it.
func (o *RestoreOrchestrator) markTargetInterrupted(job domain.RestoreJob) {
	if o.instances == nil || job.NewProjectID == "" {
		return
	}
	err := o.instances.RecordRestoreInterrupted(job.NewProjectID, restoreInterruptedStep, restoreInterruptedReason)
	switch {
	case err == nil:
	case errors.Is(err, storage.ErrProjectNotRestoring), errors.Is(err, storage.ErrProjectNotFound):
		// The target finished, was already deleted, or was never created.
		// Nothing to say on it.
	default:
		o.logger.Printf("restore %s: mark target %s interrupted: %v", job.ID, job.NewProjectID, err)
	}
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

// writeOutcome is what came of a conditional job write. The distinction
// between lost and unknown is the whole point: only the first is evidence.
type writeOutcome int

const (
	// writeLanded: the row was updated, so this process still owns the job.
	writeLanded writeOutcome = iota
	// writeLost: the update matched no row. Authoritative — the job is
	// terminal or belongs to someone else now.
	writeLost
	// writeUnknown: the store could not be reached. This says nothing about
	// ownership, so nothing may be destroyed on the strength of it.
	writeUnknown
)

// panicFailureReason is what a caller sees when a restore step panicked. The
// stack goes to the log; the caller gets a sentence and a way forward.
const panicFailureReason = "the restore stopped on an unexpected internal error; delete the target project and start it again"

// drive runs the job and guarantees the process survives it. A panic in any
// step would otherwise take down the whole control plane — every other
// project's provisioning, pausing and teardown runs in here too.
//
// The panicking step's own compensations cannot be reached from here: they
// live in a context inside the frame that unwound. What this can do is stop
// the lie — the job is recorded FAILED, and the target project stays
// RESTORING, which means it is not served and can be deleted.
func (o *RestoreOrchestrator) drive(ctx context.Context, j *domain.RestoreJob) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		o.logger.Printf("restore %s: step %q panicked: %v\n%s", j.ID, j.CurrentStep, r, debug.Stack())
		cancel()
		j.Status = domain.RestoreStatusFailed
		j.FailureReason = panicFailureReason
		o.write(ctx, j, "record panic")
	}()
	o.run(ctx, cancel, j)
}

// run executes the registered steps in order. Every write is conditional on
// this process still owning a RUNNING job.
//
// A refused write (writeLost) is evidence the job was taken, so the step's
// context is cancelled — which is what makes the adapter stop and run its
// compensations. An unreachable store (writeUnknown) is not evidence of
// anything: the run carries on where it safely can, and stops without
// compensating where it cannot, leaving the job RUNNING for the sweep to
// judge once the heartbeats stop. Nothing is ever torn down because a
// bookkeeping write failed.
func (o *RestoreOrchestrator) run(ctx context.Context, cancel context.CancelFunc, j *domain.RestoreJob) {
	o.mu.Lock()
	steps := append([]RestoreStep{}, o.steps...)
	o.mu.Unlock()

	stopBeating := o.beat(ctx, j.ID, cancel)
	defer stopBeating()

	for _, step := range steps {
		j.CurrentStep = step.Name
		switch o.write(ctx, j, "claim step "+step.Name) {
		case writeLost:
			cancel()
			return
		case writeUnknown:
			// Bookkeeping only. Ownership is unchanged as far as anyone
			// knows, and the heartbeat is what would tell us otherwise, so
			// the restore carries on rather than being abandoned.
			o.logger.Printf("restore %s: could not record step %q; continuing", j.ID, step.Name)
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

// write persists the job, retrying a store that cannot be reached. It never
// reports a loss it did not observe: an error that outlasts the retries is
// writeUnknown, not writeLost.
func (o *RestoreOrchestrator) write(ctx context.Context, j *domain.RestoreJob, what string) writeOutcome {
	var lastErr error
	for attempt := 1; attempt <= defaultRestoreWriteAttempts; attempt++ {
		ok, err := o.jobs.UpdateRunningRestoreJob(ctx, j, o.instance)
		switch {
		case err == nil && ok:
			return writeLanded
		case err == nil:
			o.logger.Printf("restore %s: %s refused; the job is no longer ours", j.ID, what)
			return writeLost
		}
		lastErr = err
		o.logger.Printf("restore %s: %s: attempt %d/%d: %v", j.ID, what, attempt, defaultRestoreWriteAttempts, err)
		if attempt == defaultRestoreWriteAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return writeUnknown
		case <-o.after(time.Duration(attempt) * defaultRestoreWriteBackoff):
		}
	}
	o.logger.Printf("restore %s: %s: giving up after %d attempts (%v); leaving the job for the sweep",
		j.ID, what, defaultRestoreWriteAttempts, lastErr)
	return writeUnknown
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
