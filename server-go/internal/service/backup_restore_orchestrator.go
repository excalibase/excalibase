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
	jobs   storage.RestoreJobStore
	logger *log.Logger

	mu    sync.Mutex
	steps []RestoreStep
}

// RestoreOrchestratorConfig wires the collaborators.
type RestoreOrchestratorConfig struct {
	Jobs   storage.RestoreJobStore
	Logger *log.Logger
}

func NewRestoreOrchestrator(c RestoreOrchestratorConfig) *RestoreOrchestrator {
	logger := c.Logger
	if logger == nil {
		logger = log.Default()
	}
	return &RestoreOrchestrator{jobs: c.Jobs, logger: logger}
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

// abandonedRestoreReason is what a caller sees on a job whose process died.
// A restore cannot be resumed by observation: the only place that knows how
// far it got is the goroutine that is gone, and the target project it was
// building was never activated, so nothing usable was left behind. Marking
// the job FAILED lets the user re-trigger, which is a clean start.
const abandonedRestoreReason = "the platform restarted while this restore was running; start it again"

// SweepStale marks every RUNNING job as FAILED. Called at platform start:
// restores are driven by an in-process goroutine, so after a restart no job
// still has one, regardless of how recently it was updated. Leaving a young
// job RUNNING would strand it forever — no later sweep runs to catch it.
func (o *RestoreOrchestrator) SweepStale(ctx context.Context) error {
	running, err := o.jobs.ListRunningRestoreJobs(ctx)
	if err != nil {
		return fmt.Errorf("list running: %w", err)
	}
	for i := range running {
		j := running[i]
		j.Status = domain.RestoreStatusFailed
		j.FailureReason = abandonedRestoreReason
		if err := o.jobs.UpsertRestoreJob(ctx, &j); err != nil {
			o.logger.Printf("restore %s: mark abandoned: %v", j.ID, err)
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
