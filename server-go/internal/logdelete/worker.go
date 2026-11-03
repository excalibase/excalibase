package logdelete

import (
	"context"
	"errors"
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
	// StateLost: Loki kept losing the request; an operator looks before it is sent again.
	StateLost State = "lost"
)

// Audit actions, one per change of a project's log deletion.
const (
	ActionQueued    = "project.logs.delete_queued"
	ActionSkipped   = "project.logs.delete_skipped"
	ActionRequested = "project.logs.delete_requested"
	ActionDeleted   = "project.logs.deleted"
	ActionRefused   = "project.logs.delete_refused"
	ActionLost      = "project.logs.delete_lost"
)

const (
	defaultEvery = time.Minute
	// claimLease keeps a claimed row from every other replica while one works it.
	claimLease = 5 * time.Minute
	claimLimit = 50
	// leaseMargin stops a pass while its lease still has time for one more row.
	leaseMargin = time.Minute
	// endSlack covers the collector's last lines after the teardown.
	endSlack = 5 * time.Minute
	// maxResends bounds sending again a request Loki does not hold.
	maxResends = 3
	// verifyAfter is the first check of a sent request; verifyEvery the next ones.
	verifyAfter = 10 * time.Minute
	verifyEvery = 30 * time.Minute
	maxBackoff  = time.Hour
)

// Deletion is one deleted project's queued log deletion.
type Deletion struct {
	ProjectID string
	Selector  string
	// Start and End bound the window: from before the project existed to just
	// after its teardown, so a project id used again later keeps its own lines.
	Start         time.Time
	End           time.Time
	State         State
	Attempts      int
	NextAttemptAt time.Time
	RequestedAt   time.Time
	DoneAt        time.Time
	// LastError is the last failure, never Loki's answer body.
	LastError string
	// Resends counts requests sent again because Loki no longer held them.
	Resends int
	// LeaseUntil is the claim this row was handed out under; a save under an
	// older claim is refused, so two replicas never both work a row.
	LeaseUntil time.Time
}

// Store keeps the queue in the platform database.
type Store interface {
	// EnqueueProjectLogDeletion adds the row; a retried teardown keeps the first.
	EnqueueProjectLogDeletion(ctx context.Context, d Deletion) error
	// ClaimDueProjectLogDeletions leases up to limit due rows to the caller.
	ClaimDueProjectLogDeletions(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]Deletion, error)
	// SaveProjectLogDeletion writes the row's progress and ends its lease;
	// ErrLeaseLost when another claim took the row meanwhile.
	SaveProjectLogDeletion(ctx context.Context, d Deletion) error
}

// ErrLeaseLost is a save from a worker whose claim on the row ran out.
var ErrLeaseLost = errors.New("log deletion: the row's lease ran out")

// Loki is the deletion API.
type Loki interface {
	Request(ctx context.Context, selector string, start, end time.Time) error
	// Statuses is every delete request Loki holds, read once per pass.
	Statuses(ctx context.Context) (Statuses, error)
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

// RunOnce works due rows once, stopping before their lease runs out, and
// answers how many it worked.
func (w *Worker) RunOnce(ctx context.Context) int {
	claimedAt := w.now()
	due, err := w.store.ClaimDueProjectLogDeletions(ctx, claimedAt, claimLease, claimLimit)
	if err != nil {
		log.Printf("log deletion: claim: %v", err)
		return 0
	}
	run := &pass{worker: w}
	worked := 0
	for _, row := range due {
		if w.now().Sub(claimedAt) > claimLease-leaseMargin {
			// The rest come due again when their lease ends.
			log.Printf("log deletion: %d rows left for the next pass", len(due)-worked)
			break
		}
		next := run.advance(ctx, row)
		if err := w.store.SaveProjectLogDeletion(ctx, next); err != nil {
			// The lease runs out and the row is worked again.
			log.Printf("log deletion of %s: save: %v", row.ProjectID, err)
		}
		worked++
	}
	return worked
}

// pass is one RunOnce: Loki's list of requests is read at most once in it.
type pass struct {
	worker    *Worker
	statuses  Statuses
	statusErr error
	read      bool
}

func (p *pass) advance(ctx context.Context, row Deletion) Deletion {
	w := p.worker
	if err := CheckSelector(row.Selector); err != nil {
		row.State, row.LastError = StateRefused, err.Error()
		w.record(ctx, ActionRefused, row)
		return row
	}
	if row.State != StateRequested {
		return w.request(ctx, row)
	}
	if !p.read {
		p.statuses, p.statusErr = w.loki.Statuses(ctx)
		p.read = true
	}
	if p.statusErr != nil {
		return w.failed(row, p.statusErr)
	}
	return w.verify(ctx, row, p.statuses.Of(row.Selector, row.Start))
}

func (w *Worker) request(ctx context.Context, row Deletion) Deletion {
	now := w.now()
	end := now
	if !row.End.IsZero() && row.End.Before(now) {
		end = row.End
	}
	if err := w.loki.Request(ctx, row.Selector, row.Start, end); err != nil {
		return w.failed(row, err)
	}
	row.State, row.RequestedAt, row.Attempts, row.LastError = StateRequested, now, 0, ""
	row.NextAttemptAt = now.Add(verifyAfter)
	w.record(ctx, ActionRequested, row)
	return row
}

func (w *Worker) verify(ctx context.Context, row Deletion, status Status) Deletion {
	now := w.now()
	row.Attempts, row.LastError = 0, ""
	switch status {
	case StatusProcessed:
		row.State, row.DoneAt = StateDone, now
		w.record(ctx, ActionDeleted, row)
	case StatusReceived:
		row.NextAttemptAt = now.Add(verifyEvery)
	default:
		return w.missing(ctx, row, now)
	}
	return row
}

// missing is a sent request Loki no longer holds (a wiped volume, a restore):
// sent again a few times, then left for an operator, so a Loki that keeps
// dropping it or reports it differently does not grow its store forever.
func (w *Worker) missing(ctx context.Context, row Deletion, now time.Time) Deletion {
	if row.Resends >= maxResends {
		row.State, row.LastError = StateLost, "Loki does not hold the request after resending it"
		log.Printf("log deletion of %s: Loki lost the request %d times; stopped", row.ProjectID, row.Resends+1)
		w.record(ctx, ActionLost, row)
		return row
	}
	log.Printf("log deletion of %s: Loki no longer holds the request; sending it again", row.ProjectID)
	row.State, row.NextAttemptAt = StatePending, now
	row.Resends++
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
