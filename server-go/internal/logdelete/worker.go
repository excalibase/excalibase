package logdelete

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// State is where one project's log deletion is.
type State string

const (
	// StatePending waits to be sent to Loki.
	StatePending State = "pending"
	// StateRequested is held by Loki, which removes the lines after its cancel period.
	StateRequested State = "requested"
	// StateDone: Loki processed the request.
	StateDone State = "done"
	// StateRefused: the stored selector is not one project's; never sent.
	StateRefused State = "refused"
)

// Audit actions, one per change of a project's log deletion.
const (
	ActionQueued    = "project.logs.delete_queued"
	ActionSkipped   = "project.logs.delete_skipped"
	ActionRequested = "project.logs.delete_requested"
	ActionDeleted   = "project.logs.deleted"
	ActionRefused   = "project.logs.delete_refused"
)

const (
	defaultEvery = time.Minute
	// claimLease keeps a claimed row from every other replica while one works it.
	claimLease = 5 * time.Minute
	claimLimit = 50
	// verifyAfter is the first check of a sent request; verifyEvery the next ones.
	verifyAfter = 10 * time.Minute
	verifyEvery = 30 * time.Minute
	maxBackoff  = time.Hour
)

// Deletion is one deleted project's queued log deletion.
type Deletion struct {
	ProjectID string
	Selector  string
	// Start is the window's start in whole seconds; the end is when it is sent.
	Start         time.Time
	State         State
	Attempts      int
	NextAttemptAt time.Time
	RequestedAt   time.Time
	DoneAt        time.Time
	// LastError is the last failure, never Loki's answer body.
	LastError string
}

// Store keeps the queue in the platform database.
type Store interface {
	// EnqueueProjectLogDeletion adds the row; a retried teardown keeps the first.
	EnqueueProjectLogDeletion(ctx context.Context, d Deletion) error
	// ClaimDueProjectLogDeletions leases up to limit due rows to the caller.
	ClaimDueProjectLogDeletions(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]Deletion, error)
	// SaveProjectLogDeletion writes the row's progress and ends its lease.
	SaveProjectLogDeletion(ctx context.Context, d Deletion) error
}

// Loki is the deletion API.
type Loki interface {
	Request(ctx context.Context, selector string, start, end time.Time) error
	Status(ctx context.Context, selector string, start time.Time) (Status, error)
}

// Auditor records each change in the platform audit log.
type Auditor interface {
	LogAudit(ctx context.Context, entry *domain.AuditEntry) error
}

// Worker sends queued deletions and confirms them.
type Worker struct {
	store Store
	loki  Loki
	audit Auditor
	now   func() time.Time
	every time.Duration
}

// NewWorker builds a worker; audit may be nil.
func NewWorker(store Store, loki Loki, audit Auditor) *Worker {
	return &Worker{store: store, loki: loki, audit: audit, now: time.Now, every: defaultEvery}
}

// Run works due rows now and every minute until ctx ends.
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.every)
	defer ticker.Stop()
	for {
		w.RunOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// RunOnce works every due row once and answers how many it worked.
func (w *Worker) RunOnce(ctx context.Context) int {
	due, err := w.store.ClaimDueProjectLogDeletions(ctx, w.now(), claimLease, claimLimit)
	if err != nil {
		log.Printf("log deletion: claim: %v", err)
		return 0
	}
	for _, row := range due {
		next := w.advance(ctx, row)
		if err := w.store.SaveProjectLogDeletion(ctx, next); err != nil {
			// The lease runs out and the row is worked again.
			log.Printf("log deletion of %s: save: %v", row.ProjectID, err)
		}
	}
	return len(due)
}

func (w *Worker) advance(ctx context.Context, row Deletion) Deletion {
	if err := CheckSelector(row.Selector); err != nil {
		row.State, row.LastError = StateRefused, err.Error()
		w.record(ctx, ActionRefused, row)
		return row
	}
	if row.State == StateRequested {
		return w.verify(ctx, row)
	}
	return w.request(ctx, row)
}

func (w *Worker) request(ctx context.Context, row Deletion) Deletion {
	now := w.now()
	if err := w.loki.Request(ctx, row.Selector, row.Start, now); err != nil {
		return w.failed(row, err)
	}
	row.State, row.RequestedAt, row.Attempts, row.LastError = StateRequested, now, 0, ""
	row.NextAttemptAt = now.Add(verifyAfter)
	w.record(ctx, ActionRequested, row)
	return row
}

func (w *Worker) verify(ctx context.Context, row Deletion) Deletion {
	now := w.now()
	status, err := w.loki.Status(ctx, row.Selector, row.Start)
	if err != nil {
		return w.failed(row, err)
	}
	row.Attempts, row.LastError = 0, ""
	switch status {
	case StatusProcessed:
		row.State, row.DoneAt = StateDone, now
		w.record(ctx, ActionDeleted, row)
	case StatusReceived:
		row.NextAttemptAt = now.Add(verifyEvery)
	default:
		// Loki lost it (a wiped volume, a restore): send it again.
		log.Printf("log deletion of %s: Loki no longer holds the request; sending it again", row.ProjectID)
		row.State, row.NextAttemptAt = StatePending, now
	}
	return row
}

func (w *Worker) failed(row Deletion, err error) Deletion {
	row.Attempts++
	row.LastError = err.Error()
	row.NextAttemptAt = w.now().Add(backoff(row.Attempts))
	log.Printf("log deletion of %s: attempt %d: %v", row.ProjectID, row.Attempts, err)
	return row
}

// backoff is a minute doubling to an hour; a deletion is never given up.
func backoff(attempts int) time.Duration {
	delay := time.Minute
	for i := 1; i < attempts && delay < maxBackoff; i++ {
		delay *= 2
	}
	return min(delay, maxBackoff)
}

func (w *Worker) record(ctx context.Context, action string, row Deletion) {
	if w.audit == nil {
		return
	}
	if err := w.audit.LogAudit(ctx, AuditEntry(action, row, w.now())); err != nil {
		log.Printf("log deletion of %s: audit %s: %v", row.ProjectID, action, err)
	}
}

// AuditEntry is the platform audit row of one change.
func AuditEntry(action string, row Deletion, at time.Time) *domain.AuditEntry {
	details := fmt.Sprintf("selector=%s start=%s", row.Selector, row.Start.UTC().Format(time.RFC3339))
	if row.LastError != "" && action == ActionRefused {
		details += " reason=" + row.LastError
	}
	return &domain.AuditEntry{Action: action, Resource: "project", ResourceID: row.ProjectID, ProjectID: row.ProjectID, Details: details, Timestamp: &at}
}
