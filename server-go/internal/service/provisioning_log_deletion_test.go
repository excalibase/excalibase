package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/logdelete"
)

// EXC-678: a deleted project's stored log lines go with it. The teardown
// queues one Loki deletion for exactly the project's own lines.

type fakeLogDeletionQueue struct {
	mu     sync.Mutex
	queued []logdelete.Deletion
	err    error
}

func (f *fakeLogDeletionQueue) EnqueueProjectLogDeletion(_ context.Context, d logdelete.Deletion) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.queued = append(f.queued, d)
	return nil
}

type fakeAudit struct {
	mu      sync.Mutex
	entries []domain.AuditEntry
}

func (f *fakeAudit) LogAudit(_ context.Context, entry *domain.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, *entry)
	return nil
}

var logDeletionNow = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func TestDeprovision_QueuesTheProjectsLogDeletion(t *testing.T) {
	svc, store, _ := setupDeletionTest(t)
	created := logDeletionNow.Add(-48 * time.Hour)
	inst, _ := store.FindByProjectID(testDeletingProj)
	inst.CreatedAt = &domain.FlexTime{Time: created}
	if err := store.Update(inst); err != nil {
		t.Fatal(err)
	}
	queue, audit := &fakeLogDeletionQueue{}, &fakeAudit{}
	svc.SetLogDeletion(queue, audit, 7*24*time.Hour)
	svc.SetDeletionClock(func() time.Time { return logDeletionNow })

	if err := svc.Deprovision(context.Background(), testDeletingProj); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
	if len(queue.queued) != 1 {
		t.Fatalf("queued = %+v", queue.queued)
	}
	got := queue.queued[0]
	if got.ProjectID != testDeletingProj || got.Selector != `{namespace="`+testDeletingNS+`"}` ||
		!got.Start.Equal(created.Add(-24*time.Hour)) || !got.End.Equal(logDeletionNow.Add(5*time.Minute)) ||
		!got.NextAttemptAt.Equal(logDeletionNow) {
		t.Errorf("queued = %+v", got)
	}
	if len(audit.entries) != 1 || audit.entries[0].Action != logdelete.ActionQueued || audit.entries[0].ResourceID != testDeletingProj {
		t.Errorf("audit = %+v", audit.entries)
	}
}

// Queued once the database resources are gone (nothing writes new lines) and
// before anything that can stop the teardown short of the record.
func TestDeprovision_QueuesLogDeletionAfterResourcesBeforeBackupsAndRecord(t *testing.T) {
	svc, _, _ := setupDeletionTest(t)
	svc.SetLogDeletion(&fakeLogDeletionQueue{}, nil, 0)
	at := map[string]int{}
	for i, step := range svc.deletionSteps(true) {
		at[step.name] = i
	}
	queue, ok := at[domain.DeletionStepQueueLogDeletion]
	if !ok {
		t.Fatal("the log deletion must be a teardown step")
	}
	if queue < at[domain.DeletionStepDeleteResources] || queue > at[domain.DeletionStepDeleteBackups] || queue > at[domain.DeletionStepDeleteRecord] {
		t.Errorf("steps = %v", at)
	}
}

func TestDeprovision_NoLogDeletionStepWhenOff(t *testing.T) {
	svc, _, _ := setupDeletionTest(t)
	for _, step := range svc.deletionSteps(false) {
		if step.name == domain.DeletionStepQueueLogDeletion {
			t.Fatal("log deletion queued with the feature off")
		}
	}
}

// The queue being down fails the teardown at its step; the same DELETE resumes it.
func TestDeprovision_LogDeletionEnqueuerDownStopsTheTeardown(t *testing.T) {
	svc, store, _ := setupDeletionTest(t)
	queue := &fakeLogDeletionQueue{err: errors.New("platform db down")}
	svc.SetLogDeletion(queue, nil, 0)
	if err := svc.Deprovision(context.Background(), testDeletingProj); err == nil {
		t.Fatal(testWantErrNil)
	}
	requireDeletingRow(t, store, domain.DeletionStepQueueLogDeletion)
	queue.err = nil
	if err := svc.Deprovision(context.Background(), testDeletingProj); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if len(queue.queued) != 1 {
		t.Fatalf("queued = %+v", queue.queued)
	}
}

// No safe selector (a project that never got a namespace): the logs are not
// deleted, the skip is audited, and the project still goes.
func TestDeprovision_LogDeletionSkippedWithoutASafeSelector(t *testing.T) {
	svc, store, _ := setupDeletionTest(t)
	inst, _ := store.FindByProjectID(testDeletingProj)
	// The platform namespace: never a project's.
	inst.Namespace = "excalibase"
	queue, audit := &fakeLogDeletionQueue{}, &fakeAudit{}
	svc.SetLogDeletion(queue, audit, 0)
	if err := svc.queueLogDeletion(context.Background(), inst); err != nil {
		t.Fatalf("step: %v", err)
	}
	if len(queue.queued) != 0 {
		t.Fatalf("queued with an unsafe selector: %+v", queue.queued)
	}
	if len(audit.entries) != 1 || audit.entries[0].Action != logdelete.ActionSkipped {
		t.Fatalf("audit = %+v", audit.entries)
	}
	if err := svc.queueLogDeletion(context.Background(), nil); err != nil {
		t.Errorf("nil project: %v", err)
	}
}
