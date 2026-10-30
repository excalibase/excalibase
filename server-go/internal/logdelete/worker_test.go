package logdelete

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

type memStore struct {
	mu        sync.Mutex
	rows      map[string]Deletion
	claimErr  error
	saveErr   error
	claimedAt []time.Time
}

func newMemStore(rows ...Deletion) *memStore {
	store := &memStore{rows: map[string]Deletion{}}
	for _, row := range rows {
		store.rows[row.ProjectID] = row
	}
	return store
}

func (m *memStore) EnqueueProjectLogDeletion(_ context.Context, d Deletion) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[d.ProjectID] = d
	return nil
}

func (m *memStore) ClaimDueProjectLogDeletions(_ context.Context, now time.Time, _ time.Duration, limit int) ([]Deletion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.claimedAt = append(m.claimedAt, now)
	if m.claimErr != nil {
		return nil, m.claimErr
	}
	var due []Deletion
	for _, row := range m.rows {
		if (row.State == StatePending || row.State == StateRequested) && !row.NextAttemptAt.After(now) && len(due) < limit {
			due = append(due, row)
		}
	}
	return due, nil
}

func (m *memStore) SaveProjectLogDeletion(_ context.Context, d Deletion) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.saveErr != nil {
		return m.saveErr
	}
	m.rows[d.ProjectID] = d
	return nil
}

func (m *memStore) row(id string) Deletion {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rows[id]
}

type fakeLoki struct {
	requests  []string
	requestFn func(selector string) error
	status    Status
	statusErr error
}

func (f *fakeLoki) Request(_ context.Context, selector string, _, _ time.Time) error {
	f.requests = append(f.requests, selector)
	if f.requestFn != nil {
		return f.requestFn(selector)
	}
	return nil
}

func (f *fakeLoki) Status(context.Context, string, time.Time) (Status, error) {
	return f.status, f.statusErr
}

type memAudit struct {
	entries []domain.AuditEntry
	err     error
}

func (m *memAudit) LogAudit(_ context.Context, entry *domain.AuditEntry) error {
	m.entries = append(m.entries, *entry)
	return m.err
}

func (m *memAudit) actions() []string {
	out := make([]string, 0, len(m.entries))
	for _, entry := range m.entries {
		out = append(out, entry.Action+":"+entry.ResourceID)
	}
	return out
}

var workerNow = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func newTestWorker(store Store, loki Loki, audit Auditor) (*Worker, *time.Time) {
	now := workerNow
	worker := NewWorker(store, loki, audit)
	worker.now = func() time.Time { return now }
	return worker, &now
}

func pending(id, selector string) Deletion {
	return Deletion{ProjectID: id, Selector: selector, Start: workerNow.Add(-72 * time.Hour), State: StatePending, NextAttemptAt: workerNow}
}

func TestWorker_RequestsThenConfirmsTheDeletion(t *testing.T) {
	store := newMemStore(pending("proj-a", selA))
	loki := &fakeLoki{status: StatusReceived}
	audit := &memAudit{}
	worker, now := newTestWorker(store, loki, audit)

	worker.RunOnce(context.Background())
	row := store.row("proj-a")
	if row.State != StateRequested || !row.RequestedAt.Equal(workerNow) || len(loki.requests) != 1 || loki.requests[0] != selA {
		t.Fatalf("after request: %+v, requests %v", row, loki.requests)
	}
	if !row.NextAttemptAt.Equal(workerNow.Add(verifyAfter)) {
		t.Errorf("first check at %v", row.NextAttemptAt)
	}
	// Not due yet: nothing happens.
	worker.RunOnce(context.Background())
	if len(loki.requests) != 1 {
		t.Fatalf("requested again before due: %v", loki.requests)
	}
	// Due; Loki has it but not done: checked again later.
	*now = row.NextAttemptAt
	worker.RunOnce(context.Background())
	if row = store.row("proj-a"); row.State != StateRequested || !row.NextAttemptAt.Equal(now.Add(verifyEvery)) {
		t.Fatalf("after received: %+v", row)
	}
	loki.status = StatusProcessed
	*now = row.NextAttemptAt
	worker.RunOnce(context.Background())
	if row = store.row("proj-a"); row.State != StateDone || !row.DoneAt.Equal(*now) {
		t.Fatalf("after processed: %+v", row)
	}
	want := []string{ActionRequested + ":proj-a", ActionDeleted + ":proj-a"}
	if got := audit.actions(); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("audit = %v, want %v", got, want)
	}
	if audit.entries[0].ProjectID != "proj-a" || audit.entries[0].Resource != "project" {
		t.Errorf("audit entry = %+v", audit.entries[0])
	}
}

// Loki down: the row waits and is retried with a growing delay, never dropped.
func TestWorker_RetriesWithBackoffWhileLokiIsDown(t *testing.T) {
	store := newMemStore(pending("proj-a", selA))
	loki := &fakeLoki{requestFn: func(string) error { return errors.New("dial tcp: connection refused") }}
	worker, now := newTestWorker(store, loki, &memAudit{})
	var delays []time.Duration
	for i := 0; i < 9; i++ {
		worker.RunOnce(context.Background())
		row := store.row("proj-a")
		if row.State != StatePending || row.Attempts != i+1 || row.LastError == "" {
			t.Fatalf("attempt %d: %+v", i+1, row)
		}
		delays = append(delays, row.NextAttemptAt.Sub(*now))
		*now = row.NextAttemptAt
	}
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 32 * time.Minute, time.Hour, time.Hour, time.Hour}
	for i := range want {
		if delays[i] != want[i] {
			t.Fatalf("delays = %v, want %v", delays, want)
		}
	}
	// Loki back: the deletion goes through and the error is cleared.
	loki.requestFn = nil
	worker.RunOnce(context.Background())
	if row := store.row("proj-a"); row.State != StateRequested || row.Attempts != 0 || row.LastError != "" {
		t.Fatalf("after recovery: %+v", row)
	}
}

// Loki lost the request (a wiped volume): it is sent again.
func TestWorker_ResendsARequestLokiNoLongerHas(t *testing.T) {
	row := pending("proj-a", selA)
	row.State, row.RequestedAt = StateRequested, workerNow.Add(-time.Hour)
	store := newMemStore(row)
	loki := &fakeLoki{status: StatusMissing}
	worker, _ := newTestWorker(store, loki, &memAudit{})
	worker.RunOnce(context.Background())
	if got := store.row("proj-a"); got.State != StatePending || !got.NextAttemptAt.Equal(workerNow) {
		t.Fatalf("after missing: %+v", got)
	}
	worker.RunOnce(context.Background())
	if got := store.row("proj-a"); got.State != StateRequested || len(loki.requests) != 1 {
		t.Fatalf("not resent: %+v %v", got, loki.requests)
	}
}

func TestWorker_StatusErrorBacksOff(t *testing.T) {
	row := pending("proj-a", selA)
	row.State = StateRequested
	store := newMemStore(row)
	worker, _ := newTestWorker(store, &fakeLoki{statusErr: errors.New("loki answered 503")}, &memAudit{})
	worker.RunOnce(context.Background())
	if got := store.row("proj-a"); got.State != StateRequested || got.Attempts != 1 || !got.NextAttemptAt.Equal(workerNow.Add(time.Minute)) {
		t.Fatalf("after status error: %+v", got)
	}
}

// A row whose selector is not one exact project label is never sent: it is
// refused for good and audited, whatever wrote it.
func TestWorker_RefusesAnUnsafeStoredSelector(t *testing.T) {
	store := newMemStore(pending("proj-a", `{namespace=~".+"}`), pending("proj-b", ""))
	loki := &fakeLoki{}
	audit := &memAudit{}
	worker, _ := newTestWorker(store, loki, audit)
	worker.RunOnce(context.Background())
	if len(loki.requests) != 0 {
		t.Fatalf("sent %v", loki.requests)
	}
	for _, id := range []string{"proj-a", "proj-b"} {
		if got := store.row(id); got.State != StateRefused {
			t.Errorf("%s: %+v", id, got)
		}
	}
	if len(audit.entries) != 2 || audit.entries[0].Action != ActionRefused {
		t.Errorf("audit = %v", audit.actions())
	}
}

// One project's failure does not hold up another's.
func TestWorker_OneFailureDoesNotBlockOthers(t *testing.T) {
	store := newMemStore(pending("proj-a", selA), pending("proj-b", `{project_id="proj-b"}`))
	loki := &fakeLoki{requestFn: func(selector string) error {
		if selector == selA {
			return errors.New("loki answered 500")
		}
		return nil
	}}
	worker, _ := newTestWorker(store, loki, &memAudit{})
	worker.RunOnce(context.Background())
	if store.row("proj-a").State != StatePending || store.row("proj-b").State != StateRequested {
		t.Fatalf("a=%+v b=%+v", store.row("proj-a"), store.row("proj-b"))
	}
}

func TestWorker_StoreErrorsAreSurvived(t *testing.T) {
	store := newMemStore(pending("proj-a", selA))
	store.claimErr = errors.New("db down")
	worker, _ := newTestWorker(store, &fakeLoki{}, &memAudit{})
	if n := worker.RunOnce(context.Background()); n != 0 {
		t.Fatalf("handled %d with the store down", n)
	}
	store.claimErr = nil
	store.saveErr = errors.New("db down")
	if n := worker.RunOnce(context.Background()); n != 1 {
		t.Fatalf("handled %d", n)
	}
	// An audit write that fails does not undo the deletion.
	store.saveErr = nil
	audit := &memAudit{err: errors.New("audit down")}
	worker, _ = newTestWorker(store, &fakeLoki{}, audit)
	worker.RunOnce(context.Background())
	if store.row("proj-a").State != StateRequested {
		t.Fatalf("row = %+v", store.row("proj-a"))
	}
}

func TestWorker_RunStopsWithTheContext(t *testing.T) {
	store := newMemStore()
	worker, _ := newTestWorker(store, &fakeLoki{}, nil)
	worker.every = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		worker.Run(ctx)
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		store.mu.Lock()
		claims := len(store.claimedAt)
		store.mu.Unlock()
		if claims >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestBackoff(t *testing.T) {
	for attempts, want := range map[int]time.Duration{0: time.Minute, 1: time.Minute, 2: 2 * time.Minute, 7: time.Hour, 60: time.Hour} {
		if got := backoff(attempts); got != want {
			t.Errorf("backoff(%d) = %v, want %v", attempts, got, want)
		}
	}
}
