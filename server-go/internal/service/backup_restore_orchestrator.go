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
// Phase 3 fires-and-forgets — RestoreFromBackup returns RUNNING
// immediately and the goroutine drives the steps. Resumability after
// platform restart: ListRunning runs at startup; jobs older than
// staleAfter are marked FAILED.
type RestoreOrchestrator struct {
	jobs       storage.RestoreJobStore
	logger     *log.Logger
	staleAfter time.Duration

	mu    sync.Mutex
	steps []RestoreStep
}

// RestoreOrchestratorConfig wires the collaborators.
type RestoreOrchestratorConfig struct {
	Jobs       storage.RestoreJobStore
	Logger     *log.Logger
	StaleAfter time.Duration // default 30m
}

func NewRestoreOrchestrator(c RestoreOrchestratorConfig) *RestoreOrchestrator {
	logger := c.Logger
	if logger == nil {
		logger = log.Default()
	}
	stale := c.StaleAfter
	if stale == 0 {
		stale = 30 * time.Minute
	}
	return &RestoreOrchestrator{jobs: c.Jobs, logger: logger, staleAfter: stale}
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
	kind, value := req.RestoreTargetKind()
	job := domain.RestoreJob{
		ID:              newRestoreJobID(),
		SourceProjectID: source.ProjectID,
		NewProjectID:    req.GetNewProject(),
		Status:          domain.RestoreStatusRunning,
		TargetKind:      kind,
		TargetValue:     value,
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

// SweepStale marks any RUNNING job older than staleAfter as FAILED.
// Called at platform start so a previous-process crash mid-restore
// surfaces as a failed row instead of an orphan stuck "RUNNING"
// indefinitely. The user can then re-trigger.
func (o *RestoreOrchestrator) SweepStale(ctx context.Context) error {
	running, err := o.jobs.ListRunningRestoreJobs(ctx)
	if err != nil {
		return fmt.Errorf("list running: %w", err)
	}
	cutoff := time.Now().Add(-o.staleAfter)
	for i := range running {
		j := running[i]
		t, err := time.Parse(time.RFC3339, j.UpdatedAt)
		if err != nil {
			continue
		}
		if t.Before(cutoff) {
			j.Status = domain.RestoreStatusFailed
			j.FailureReason = "platform restart while RUNNING; orphan swept"
			_ = o.jobs.UpsertRestoreJob(ctx, &j)
		}
	}
	return nil
}

// run executes the registered steps in order. First failure marks
// the job FAILED with the step name + error in failure_reason.
func (o *RestoreOrchestrator) run(ctx context.Context, j *domain.RestoreJob) {
	o.mu.Lock()
	steps := append([]RestoreStep{}, o.steps...)
	o.mu.Unlock()

	for _, step := range steps {
		j.CurrentStep = step.Name
		if err := o.jobs.UpsertRestoreJob(ctx, j); err != nil {
			o.logger.Printf("restore %s: persist current_step %q: %v", j.ID, step.Name, err)
		}
		if err := step.Run(ctx, j); err != nil {
			j.Status = domain.RestoreStatusFailed
			j.FailureReason = fmt.Sprintf("step %q: %v", step.Name, err)
			_ = o.jobs.UpsertRestoreJob(ctx, j)
			return
		}
	}
	j.Status = domain.RestoreStatusCompleted
	j.CurrentStep = ""
	_ = o.jobs.UpsertRestoreJob(ctx, j)
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
