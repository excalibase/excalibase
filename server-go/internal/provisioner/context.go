package provisioner

import (
	"context"
	"fmt"
	"sync"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// CleanupFunc is a compensation action for a successful provisioning step.
// It's called if a later stage fails, to roll back side effects.
type CleanupFunc func(ctx context.Context) error

// CleanupResult records what happened when a cleanup ran.
type CleanupResult struct {
	Name  string `json:"name"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type cleanupEntry struct {
	name  string
	stage domain.ProvisioningStage
	fn    CleanupFunc
}

// ProvisionContext tracks current stage/step and cleanup actions for rollback.
// Passed into provisioners as a replacement for the old StageCallback.
type ProvisionContext struct {
	mu       sync.Mutex
	stage    domain.ProvisioningStage
	step     string
	cleanups []cleanupEntry

	onStage func(stage domain.ProvisioningStage)
	onStep  func(step string)
}

func NewProvisionContext(onStage func(domain.ProvisioningStage), onStep func(string)) *ProvisionContext {
	return &ProvisionContext{
		onStage: onStage,
		onStep:  onStep,
	}
}

// SetStage marks the pipeline as entering a new stage and notifies observers.
func (pc *ProvisionContext) SetStage(stage domain.ProvisioningStage) {
	pc.mu.Lock()
	pc.stage = stage
	pc.step = ""
	pc.mu.Unlock()
	if pc.onStage != nil {
		pc.onStage(stage)
	}
}

// SetStep records a sub-step within the current stage (e.g. "waiting for replica 2").
func (pc *ProvisionContext) SetStep(step string) {
	pc.mu.Lock()
	pc.step = step
	pc.mu.Unlock()
	if pc.onStep != nil {
		pc.onStep(step)
	}
}

// Stage returns the current stage (set via SetStage).
func (pc *ProvisionContext) Stage() domain.ProvisioningStage {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	return pc.stage
}

// Step returns the current step (set via SetStep).
func (pc *ProvisionContext) Step() string {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	return pc.step
}

// RegisterCleanup adds a compensation action to run on rollback.
// Cleanups run in LIFO order — most recent registration first.
func (pc *ProvisionContext) RegisterCleanup(name string, fn CleanupFunc) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.cleanups = append(pc.cleanups, cleanupEntry{
		name:  name,
		stage: pc.stage,
		fn:    fn,
	})
}

// Rollback runs all registered cleanups in reverse order. Returns a result for each.
// Failures in individual cleanups are captured but do not stop subsequent cleanups.
func (pc *ProvisionContext) Rollback(ctx context.Context) []CleanupResult {
	pc.mu.Lock()
	cleanups := make([]cleanupEntry, len(pc.cleanups))
	copy(cleanups, pc.cleanups)
	pc.mu.Unlock()

	results := make([]CleanupResult, 0, len(cleanups))
	for i := len(cleanups) - 1; i >= 0; i-- {
		c := cleanups[i]
		err := c.fn(ctx)
		if err != nil {
			results = append(results, CleanupResult{
				Name:  c.name,
				OK:    false,
				Error: err.Error(),
			})
		} else {
			results = append(results, CleanupResult{
				Name: c.name,
				OK:   true,
			})
		}
	}
	return results
}

// CleanupCount reports how many cleanup actions are currently registered.
func (pc *ProvisionContext) CleanupCount() int {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	return len(pc.cleanups)
}

// StageError wraps an error with the stage and step where it occurred.
type StageError struct {
	Stage domain.ProvisioningStage
	Step  string
	Err   error
}

func (e *StageError) Error() string {
	if e.Step != "" {
		return fmt.Sprintf("stage %s (%s): %v", e.Stage, e.Step, e.Err)
	}
	return fmt.Sprintf("stage %s: %v", e.Stage, e.Err)
}

func (e *StageError) Unwrap() error { return e.Err }

// Fail returns a StageError wrapping the current stage/step.
func (pc *ProvisionContext) Fail(err error) error {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	return &StageError{
		Stage: pc.stage,
		Step:  pc.step,
		Err:   err,
	}
}
