package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// fakeRestoreJobStore is an in-memory RestoreJobStore carrying the same
// conditional-write semantics the real table enforces: a running job may
// only be written by the process that owns it, and a terminal job may not be
// written at all.
type fakeRestoreJobStore struct {
	mu   sync.Mutex
	jobs map[string]domain.RestoreJob
	// clock lets a test age a heartbeat without sleeping.
	clock func() time.Time
}

func newFakeJobs() *fakeRestoreJobStore {
	return &fakeRestoreJobStore{jobs: map[string]domain.RestoreJob{}, clock: time.Now}
}

func (f *fakeRestoreJobStore) now() time.Time {
	if f.clock != nil {
		return f.clock()
	}
	return time.Now()
}

func (f *fakeRestoreJobStore) UpdateRunningRestoreJob(_ context.Context, j *domain.RestoreJob, owner string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	stored, ok := f.jobs[j.ID]
	if !ok || stored.Status != domain.RestoreStatusRunning || stored.Owner != owner {
		return false, nil
	}
	j.Owner = owner
	j.CreatedAt = stored.CreatedAt
	j.HeartbeatAt = f.now().UTC().Format(time.RFC3339)
	j.UpdatedAt = j.HeartbeatAt
	f.jobs[j.ID] = *j
	return true, nil
}

func (f *fakeRestoreJobStore) HeartbeatRestoreJob(_ context.Context, id, owner string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	stored, ok := f.jobs[id]
	if !ok || stored.Status != domain.RestoreStatusRunning || stored.Owner != owner {
		return false, nil
	}
	stored.HeartbeatAt = f.now().UTC().Format(time.RFC3339)
	f.jobs[id] = stored
	return true, nil
}

func (f *fakeRestoreJobStore) FailAbandonedRestoreJobs(_ context.Context, owner string, staleAfter time.Duration, reason string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cutoff := f.now().Add(-staleAfter)
	failed := []string{}
	for id, j := range f.jobs {
		if j.Status != domain.RestoreStatusRunning || j.Owner == owner {
			continue
		}
		if beat, err := time.Parse(time.RFC3339, j.HeartbeatAt); err == nil && beat.After(cutoff) {
			continue
		}
		j.Status = domain.RestoreStatusFailed
		j.FailureReason = reason
		f.jobs[id] = j
		failed = append(failed, id)
	}
	return failed, nil
}

func (f *fakeRestoreJobStore) status(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.jobs[id].Status
}

func (f *fakeRestoreJobStore) UpsertRestoreJob(_ context.Context, j *domain.RestoreJob) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	j.UpdatedAt = f.now().UTC().Format(time.RFC3339)
	j.HeartbeatAt = j.UpdatedAt
	if existing, ok := f.jobs[j.ID]; !ok {
		j.CreatedAt = j.UpdatedAt
		f.jobs[j.ID] = *j
	} else {
		j.CreatedAt = existing.CreatedAt
		f.jobs[j.ID] = *j
	}
	return nil
}

func (f *fakeRestoreJobStore) FindRestoreJob(_ context.Context, projectID, id string) (*domain.RestoreJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[id]
	if !ok || (j.SourceProjectID != projectID && j.NewProjectID != projectID) {
		return nil, nil
	}
	return &j, nil
}

func (f *fakeRestoreJobStore) ListRunningRestoreJobs(_ context.Context) ([]domain.RestoreJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []domain.RestoreJob{}
	for _, j := range f.jobs {
		if j.Status == domain.RestoreStatusRunning {
			out = append(out, j)
		}
	}
	return out, nil
}

func waitForJobStatus(t *testing.T, jobs *fakeRestoreJobStore, projectID, id, target string) *domain.RestoreJob {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := jobs.FindRestoreJob(context.Background(), projectID, id)
		if got != nil && got.Status == target {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s never reached %s", id, target)
	return nil
}

func TestOrchestrator_RunsAllStepsThenCompletes(t *testing.T) {
	jobs := newFakeJobs()
	orch := NewRestoreOrchestrator(RestoreOrchestratorConfig{Jobs: jobs})
	steps := []string{}
	mu := sync.Mutex{}
	orch.SetSteps([]RestoreStep{
		{Name: "validate", Run: func(_ context.Context, _ *domain.RestoreJob) error {
			mu.Lock()
			steps = append(steps, "validate")
			mu.Unlock()
			return nil
		}},
		{Name: "fetch", Run: func(_ context.Context, _ *domain.RestoreJob) error {
			mu.Lock()
			steps = append(steps, "fetch")
			mu.Unlock()
			return nil
		}},
		{Name: "create-target", Run: func(_ context.Context, _ *domain.RestoreJob) error {
			mu.Lock()
			steps = append(steps, "create-target")
			mu.Unlock()
			return nil
		}},
	})

	job, err := orch.Start(context.Background(), &domain.DatabaseInstance{ProjectID: "src"}, domain.RestoreRequest{
		NewProjectName: "dst", TargetProjectID: "dst",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if job.Status != domain.RestoreStatusRunning {
		t.Errorf("initial status: got %s", job.Status)
	}

	final := waitForJobStatus(t, jobs, "src", job.ID, domain.RestoreStatusCompleted)
	if final.FailureReason != "" {
		t.Errorf("FailureReason set on success: %q", final.FailureReason)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(steps) != 3 || steps[0] != "validate" || steps[1] != "fetch" || steps[2] != "create-target" {
		t.Errorf("step order: %v", steps)
	}
}

func TestOrchestrator_FailsAtStep_StopsAndPersistsReason(t *testing.T) {
	jobs := newFakeJobs()
	orch := NewRestoreOrchestrator(RestoreOrchestratorConfig{Jobs: jobs})
	called := []string{}
	mu := sync.Mutex{}
	orch.SetSteps([]RestoreStep{
		{Name: "ok-1", Run: func(_ context.Context, _ *domain.RestoreJob) error {
			mu.Lock()
			called = append(called, "ok-1")
			mu.Unlock()
			return nil
		}},
		{Name: "fail", Run: func(_ context.Context, _ *domain.RestoreJob) error {
			mu.Lock()
			called = append(called, "fail")
			mu.Unlock()
			return errors.New("disk full")
		}},
		{Name: "ok-2", Run: func(_ context.Context, _ *domain.RestoreJob) error {
			mu.Lock()
			called = append(called, "ok-2")
			mu.Unlock()
			return nil
		}},
	})

	job, _ := orch.Start(context.Background(), &domain.DatabaseInstance{ProjectID: "src"}, domain.RestoreRequest{
		NewProjectName: "dst", TargetProjectID: "dst",
	})
	final := waitForJobStatus(t, jobs, "src", job.ID, domain.RestoreStatusFailed)

	mu.Lock()
	defer mu.Unlock()
	if len(called) != 2 || called[0] != "ok-1" || called[1] != "fail" {
		t.Errorf("ok-2 should not have run: %v", called)
	}
	if final.FailureReason == "" || final.CurrentStep != "fail" {
		t.Errorf("failure metadata: reason=%q step=%q", final.FailureReason, final.CurrentStep)
	}
}

func TestOrchestrator_TargetKindPersisted(t *testing.T) {
	jobs := newFakeJobs()
	orch := NewRestoreOrchestrator(RestoreOrchestratorConfig{Jobs: jobs})
	orch.SetSteps([]RestoreStep{
		{Name: "noop", Run: func(_ context.Context, _ *domain.RestoreJob) error { return nil }},
	})

	cases := []struct {
		name string
		req  domain.RestoreRequest
		kind string
	}{
		{"latest", domain.RestoreRequest{NewProjectName: "p", TargetProjectID: "p"}, "latest"},
		{"xid", domain.RestoreRequest{NewProjectName: "p", TargetProjectID: "p", TargetXID: "12345"}, "xid"},
		{"lsn", domain.RestoreRequest{NewProjectName: "p", TargetProjectID: "p", TargetLSN: "0/1500000"}, "lsn"},
		{"name", domain.RestoreRequest{NewProjectName: "p", TargetProjectID: "p", TargetName: "before-bad-migration"}, "name"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			job, err := orch.Start(context.Background(), &domain.DatabaseInstance{ProjectID: "src"}, c.req)
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			waitForJobStatus(t, jobs, "src", job.ID, domain.RestoreStatusCompleted)
			got, _ := jobs.FindRestoreJob(context.Background(), "src", job.ID)
			if got.TargetKind != c.kind {
				t.Errorf("TargetKind: got %q, want %q", got.TargetKind, c.kind)
			}
		})
	}
}

func TestOrchestrator_RejectsTwoTargets(t *testing.T) {
	jobs := newFakeJobs()
	orch := NewRestoreOrchestrator(RestoreOrchestratorConfig{Jobs: jobs})
	now := time.Now()
	_, err := orch.Start(context.Background(), &domain.DatabaseInstance{ProjectID: "src"}, domain.RestoreRequest{
		NewProjectName: "dst", TargetProjectID: "dst",
		TargetTime: &domain.FlexTime{Time: now},
		TargetXID:  "12345",
	})
	if err == nil {
		t.Error("expected error for two targets")
	}
}

func TestOrchestrator_SweepStale_FailsAbandonedRunning(t *testing.T) {
	clock := &movableClock{t: time.Unix(4_000_000, 0)}
	jobs := newOwnedJobs(clock.now)
	// A job another process was driving, whose heartbeat has long stopped.
	jobs.UpsertRestoreJob(context.Background(), &domain.RestoreJob{
		ID: "old", SourceProjectID: "s", NewProjectID: "d",
		Status: domain.RestoreStatusRunning, Owner: "gone-replica",
	})
	clock.advance(2 * defaultRestoreHeartbeatStale)

	orch := ownedOrchestrator(jobs, "this-replica", clock)
	if err := orch.SweepStale(context.Background()); err != nil {
		t.Fatalf("SweepStale: %v", err)
	}
	got, _ := jobs.FindRestoreJob(context.Background(), "s", "old")
	if got.Status != domain.RestoreStatusFailed {
		t.Errorf("abandoned job status: got %s", got.Status)
	}
	if got.FailureReason == "" {
		t.Errorf("FailureReason should be set on swept job")
	}
}

func TestOrchestrator_Get_ReturnsRunning(t *testing.T) {
	jobs := newFakeJobs()
	// Step that blocks forever — lets us catch the job mid-flight.
	hold := make(chan struct{})
	defer close(hold)
	orch := NewRestoreOrchestrator(RestoreOrchestratorConfig{Jobs: jobs})
	orch.SetSteps([]RestoreStep{
		{Name: "blocker", Run: func(_ context.Context, _ *domain.RestoreJob) error {
			<-hold
			return nil
		}},
	})

	job, _ := orch.Start(context.Background(), &domain.DatabaseInstance{ProjectID: "src"}, domain.RestoreRequest{
		NewProjectName: "dst", TargetProjectID: "dst",
	})
	// Tiny pause so the goroutine sets current_step before we read.
	time.Sleep(50 * time.Millisecond)
	got, _ := orch.Get(context.Background(), "src", job.ID)
	if got.Status != domain.RestoreStatusRunning {
		t.Errorf("status: got %s", got.Status)
	}
	if got.CurrentStep != "blocker" {
		t.Errorf("current step: got %q", got.CurrentStep)
	}
}

// The orchestrator writes the job row that tells the client where its restore
// landed, so it refuses to start one whose target id was never allocated.
func TestOrchestrator_RefusesAnUnallocatedTargetID(t *testing.T) {
	jobs := newFakeJobs()
	orch := NewRestoreOrchestrator(RestoreOrchestratorConfig{Jobs: jobs})

	_, err := orch.Start(context.Background(), &domain.DatabaseInstance{ProjectID: "src"},
		domain.RestoreRequest{NewProjectName: "dst"})
	if !errors.Is(err, ErrTargetProjectIDMissing) {
		t.Fatalf("err: got %v, want ErrTargetProjectIDMissing", err)
	}
	if running, _ := jobs.ListRunningRestoreJobs(context.Background()); len(running) != 0 {
		t.Error("no job row may be written for a restore that cannot start")
	}
}
