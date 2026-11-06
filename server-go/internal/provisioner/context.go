package provisioner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// CleanupTimeout bounds one cleanup. The slowest, removing a database from a
// namespace it shares, waits up to 2 minutes on Helm and 5 on the cluster.
const CleanupTimeout = 10 * time.Minute

// CleanupContext is what cleanup runs on: it outlives whatever ended the work
// being undone (a hang-up, a deadline, a claim) and still cannot run forever.
func CleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), CleanupTimeout)
}

// CleanupFunc is a compensation action for a successful provisioning step.
// It's called if a later stage fails, to roll back side effects.
type CleanupFunc func(ctx context.Context) error

// CleanupResult records what happened when a cleanup ran.
type CleanupResult struct {
	Name  string `json:"name"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// ErrCleanupSkipped is why a last cleanup did not run.
var ErrCleanupSkipped = errors.New("skipped: an earlier cleanup failed")

type cleanupEntry struct {
	name  string
	stage domain.ProvisioningStage
	fn    CleanupFunc
	last  bool
}

// ProvisionContext tracks current stage/step and cleanup actions for rollback.
// Passed into provisioners as a replacement for the old StageCallback.
type ProvisionContext struct {
	mu       sync.Mutex
	stage    domain.ProvisioningStage
	step     string
	cleanups []cleanupEntry
	// cleanupTimeout overrides how long one cleanup may run; zero is CleanupTimeout.
	cleanupTimeout time.Duration

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

// RegisterLastCleanup adds an action that runs after every other cleanup, and
// only if they all worked: removing the record of what they were removing.
func (pc *ProvisionContext) RegisterLastCleanup(name string, fn CleanupFunc) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.cleanups = append(pc.cleanups, cleanupEntry{name: name, stage: pc.stage, fn: fn, last: true})
}

// Rollback runs all registered cleanups in reverse order. Returns a result for each.
// Failures in individual cleanups are captured but do not stop subsequent cleanups.
// Each runs detached from ctx on its own deadline, so a finished ctx cannot
// strand the cluster and one hung cleanup cannot starve the rest.
func (pc *ProvisionContext) Rollback(ctx context.Context) []CleanupResult {
	pc.mu.Lock()
	cleanups := make([]cleanupEntry, len(pc.cleanups))
	copy(cleanups, pc.cleanups)
	pc.mu.Unlock()

	results := make([]CleanupResult, 0, len(cleanups))
	failed := false
	for _, wantLast := range []bool{false, true} {
		for i := len(cleanups) - 1; i >= 0; i-- {
			c := cleanups[i]
			if c.last != wantLast {
				continue
			}
			var err error = ErrCleanupSkipped
			if !c.last || !failed {
				err = pc.runCleanup(ctx, c)
			}
			failed = failed || err != nil
			results = append(results, cleanupResult(c.name, err))
		}
	}
	return results
}

func cleanupResult(name string, err error) CleanupResult {
	if err != nil {
		return CleanupResult{Name: name, Error: err.Error()}
	}
	return CleanupResult{Name: name, OK: true}
}

func (pc *ProvisionContext) runCleanup(ctx context.Context, c cleanupEntry) (err error) {
	// A panic is this cleanup's failure; the rest still have to run. The result
	// reaches the row and API, so the panic itself stays in the log.
	defer func() {
		if r := recover(); r != nil {
			log.Printf("ERROR: cleanup %q panicked: %v\n%s", c.name, r, debug.Stack())
			err = errors.New("the cleanup stopped on an internal error")
		}
	}()
	cleanupCtx, cancel := CleanupContext(ctx)
	defer cancel()
	if pc.cleanupTimeout > 0 {
		var shorten context.CancelFunc
		cleanupCtx, shorten = context.WithTimeout(cleanupCtx, pc.cleanupTimeout)
		defer shorten()
	}
	return c.fn(cleanupCtx)
}

// CleanupCount reports how many cleanup actions are currently registered.
func (pc *ProvisionContext) CleanupCount() int {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	return len(pc.cleanups)
}

// EncodeCleanupResults is the form results take in a project's rollback log.
func EncodeCleanupResults(results []CleanupResult) (string, error) {
	encoded, err := json.Marshal(results)
	return string(encoded), err
}

// ParseCleanupResults reads a rollback log; an empty one holds no results.
func ParseCleanupResults(rollbackLog string) ([]CleanupResult, error) {
	if rollbackLog == "" {
		return nil, nil
	}
	var results []CleanupResult
	err := json.Unmarshal([]byte(rollbackLog), &results)
	return results, err
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
