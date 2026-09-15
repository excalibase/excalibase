package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

const day = 24 * time.Hour

// fakeIdlePauser records Pause calls and flips the instance to PAUSED the
// way PauseService would, so a second pass sees the new status.
type fakeIdlePauser struct {
	mu        sync.Mutex
	instances storage.InstanceStore
	calls     []string
	reasons   []string
	err       error
}

func (f *fakeIdlePauser) Pause(_ context.Context, projectID, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, projectID)
	f.reasons = append(f.reasons, reason)
	inst, _ := f.instances.FindByProjectID(projectID)
	inst.Status = string(domain.StatusPaused)
	inst.PauseReason = reason
	return f.instances.Save(inst)
}

func (f *fakeIdlePauser) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type fakeIdleNotifier struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (f *fakeIdleNotifier) NotifyIdleWarning(_ context.Context, inst *domain.DatabaseInstance, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, inst.ProjectID)
	return f.err
}

type fakeAuditWriter struct {
	mu      sync.Mutex
	entries []domain.AuditEntry
}

func (f *fakeAuditWriter) LogAudit(_ context.Context, e *domain.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, *e)
	return nil
}

func (f *fakeAuditWriter) actions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.entries))
	for _, e := range f.entries {
		out = append(out, e.Action)
	}
	return out
}

type idleFixture struct {
	scheduler *IdlePauseScheduler
	instances *storage.FileSystemStore
	activity  *fakeActivityStore
	pauser    *fakeIdlePauser
	notifier  *fakeIdleNotifier
	audit     *fakeAuditWriter
	clock     *fakeClock
}

func tierResolverForTest(_ context.Context, tier domain.TierType) (config.TierConfig, error) {
	switch tier {
	case domain.Free:
		return config.TierConfig{AutoPauseAfterDays: 7}, nil
	case domain.Standard:
		return config.TierConfig{AutoPauseAfterDays: 0}, nil
	}
	return config.TierConfig{}, errors.New("unknown tier")
}

func newIdleFixture(t *testing.T) *idleFixture {
	t.Helper()
	instances, _ := storage.NewFileSystemStore(t.TempDir())
	f := &idleFixture{
		instances: instances,
		activity:  &fakeActivityStore{},
		pauser:    &fakeIdlePauser{instances: instances},
		notifier:  &fakeIdleNotifier{},
		audit:     &fakeAuditWriter{},
		clock:     &fakeClock{now: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)},
	}
	f.scheduler = NewIdlePauseScheduler(IdlePauseSchedulerConfig{
		Instances: instances,
		Activity:  f.activity,
		Tiers:     tierResolverForTest,
		Pauser:    f.pauser,
		Notifier:  f.notifier,
		Audit:     f.audit,
		Lock:      &fakeLeaderLock{},
		Now:       f.clock.Now,
	})
	return f
}

// project saves an ACTIVE instance created `age` before the fixture clock.
func (f *idleFixture) project(t *testing.T, id string, tier domain.TierType, age time.Duration) {
	t.Helper()
	created := f.clock.Now().Add(-age)
	if err := f.instances.Save(&domain.DatabaseInstance{
		ProjectID: id, OrgID: "o", OwnerID: "u1", Tier: tier, Status: "ACTIVE",
		DeploymentMode: domain.ModeDocker, CreatedAt: &domain.FlexTime{Time: created},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

func (f *idleFixture) run(t *testing.T) IdlePauseReport {
	t.Helper()
	report, err := f.scheduler.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	return report
}

func TestIdlePause_WarnsOnceAtDaySix(t *testing.T) {
	f := newIdleFixture(t)
	f.project(t, "p1", domain.Free, 6*day+time.Hour)

	report := f.run(t)
	if len(report.Warned) != 1 || report.Warned[0] != "p1" || len(report.Paused) != 0 {
		t.Fatalf("first pass: %+v", report)
	}
	if len(f.notifier.calls) != 1 {
		t.Errorf("notifier calls: %d", len(f.notifier.calls))
	}
	row, ok, _ := f.activity.GetProjectActivity(context.Background(), "p1")
	if !ok || row.IdleWarnedAt == nil || !row.IdleWarnedAt.Equal(f.clock.Now()) {
		t.Errorf("warning must be persisted on the activity row: ok=%v %+v", ok, row)
	}
	if got := f.audit.actions(); len(got) != 1 || got[0] != AuditActionIdleWarning {
		t.Errorf("audit: %v", got)
	}

	f.clock.Advance(time.Hour)
	report = f.run(t)
	if len(report.Warned) != 0 || len(f.notifier.calls) != 1 {
		t.Errorf("second pass must not warn again: %+v notifier=%d", report, len(f.notifier.calls))
	}
}

func TestIdlePause_NoWarningBeforeDaySix(t *testing.T) {
	f := newIdleFixture(t)
	f.project(t, "p1", domain.Free, 5*day+23*time.Hour)
	report := f.run(t)
	if len(report.Warned) != 0 || len(report.Paused) != 0 {
		t.Errorf("nothing should happen before N-1 days: %+v", report)
	}
}

func TestIdlePause_PausesAtDaySeven(t *testing.T) {
	f := newIdleFixture(t)
	f.project(t, "p1", domain.Free, 7*day+time.Minute)

	report := f.run(t)
	if len(report.Paused) != 1 || report.Paused[0] != "p1" {
		t.Fatalf("expected p1 paused: %+v", report)
	}
	if f.pauser.count() != 1 || f.pauser.reasons[0] != domain.PauseReasonIdle {
		t.Errorf("pauser: calls=%v reasons=%v", f.pauser.calls, f.pauser.reasons)
	}
	if got := f.audit.actions(); len(got) != 1 || got[0] != AuditActionIdlePause {
		t.Errorf("audit: %v", got)
	}

	// Idempotent: the project is PAUSED now, a second pass leaves it alone.
	f.clock.Advance(time.Hour)
	f.run(t)
	if f.pauser.count() != 1 {
		t.Errorf("second pass must not re-pause: %d", f.pauser.count())
	}
}

func TestIdlePause_UsesActivityRowOverCreatedAt(t *testing.T) {
	f := newIdleFixture(t)
	f.project(t, "p1", domain.Free, 30*day)
	_ = f.activity.TouchProjectActivity(context.Background(), "p1", "api", f.clock.Now().Add(-2*day))

	report := f.run(t)
	if len(report.Warned) != 0 || len(report.Paused) != 0 {
		t.Errorf("recent activity must keep the project alive: %+v", report)
	}
}

func TestIdlePause_SkipsRecentlyResumed(t *testing.T) {
	f := newIdleFixture(t)
	f.project(t, "p1", domain.Free, 30*day)
	_ = f.activity.TouchProjectActivity(context.Background(), "p1", "api", f.clock.Now().Add(-20*day))
	inst, _ := f.instances.FindByProjectID("p1")
	inst.LastActiveAt = &domain.FlexTime{Time: f.clock.Now().Add(-time.Hour)}
	_ = f.instances.Save(inst)

	report := f.run(t)
	if len(report.Paused) != 0 || len(report.Warned) != 0 {
		t.Errorf("a project resumed less than 24h ago must be left alone: %+v", report)
	}
}

func TestIdlePause_ResumeStartsAFreshWarningCycle(t *testing.T) {
	f := newIdleFixture(t)
	f.project(t, "p1", domain.Free, 30*day)
	warnedAt := f.clock.Now().Add(-10 * day)
	_ = f.activity.MarkIdleWarned(context.Background(), "p1", f.clock.Now().Add(-16*day), warnedAt)
	inst, _ := f.instances.FindByProjectID("p1")
	inst.LastActiveAt = &domain.FlexTime{Time: f.clock.Now().Add(-6*day - time.Hour)}
	_ = f.instances.Save(inst)

	report := f.run(t)
	if len(report.Warned) != 1 {
		t.Errorf("an old warning predating the resume must not suppress the new one: %+v", report)
	}
}

func TestIdlePause_SkipsTiersWithoutAutoPause(t *testing.T) {
	f := newIdleFixture(t)
	f.project(t, "std", domain.Standard, 60*day)
	f.project(t, "unknown", domain.TierType("PLATINUM"), 60*day)

	report := f.run(t)
	if len(report.Warned) != 0 || len(report.Paused) != 0 {
		t.Errorf("tiers with autoPauseAfterDays=0 or unresolvable tiers must be skipped: %+v", report)
	}
}

func TestIdlePause_SkipsNonActiveProjects(t *testing.T) {
	f := newIdleFixture(t)
	f.project(t, "p1", domain.Free, 30*day)
	inst, _ := f.instances.FindByProjectID("p1")
	inst.Status = "PROVISIONING"
	_ = f.instances.Save(inst)

	report := f.run(t)
	if len(report.Paused) != 0 || len(report.Warned) != 0 {
		t.Errorf("only ACTIVE projects are candidates: %+v", report)
	}
}

func TestIdlePause_NotifierFailureStillRecordsWarning(t *testing.T) {
	f := newIdleFixture(t)
	f.notifier.err = errors.New("smtp down")
	f.project(t, "p1", domain.Free, 6*day+time.Hour)

	report := f.run(t)
	if len(report.Warned) != 1 {
		t.Fatalf("warning must be recorded even when email fails: %+v", report)
	}
	if got := f.audit.actions(); len(got) != 1 || got[0] != AuditActionIdleWarning {
		t.Errorf("audit must carry the warning regardless of email: %v", got)
	}
}

func TestIdlePause_PauseFailureIsReportedAndOthersContinue(t *testing.T) {
	f := newIdleFixture(t)
	f.project(t, "p1", domain.Free, 8*day)
	f.project(t, "p2", domain.Free, 6*day+time.Hour)
	f.pauser.err = errors.New("backup failed")

	report := f.run(t)
	if len(report.Failed) != 1 || report.Failed[0] != "p1" {
		t.Errorf("pause failure must be reported: %+v", report)
	}
	if len(report.Warned) != 1 || report.Warned[0] != "p2" {
		t.Errorf("one failure must not stop the sweep: %+v", report)
	}
}

func TestIdlePause_OptionalCollaboratorsMayBeNil(t *testing.T) {
	instances, _ := storage.NewFileSystemStore(t.TempDir())
	clock := &fakeClock{now: time.Now()}
	pauser := &fakeIdlePauser{instances: instances}
	scheduler := NewIdlePauseScheduler(IdlePauseSchedulerConfig{
		Instances: instances, Activity: &fakeActivityStore{}, Tiers: tierResolverForTest,
		Pauser: pauser, Lock: &fakeLeaderLock{}, Now: clock.Now,
	})
	_ = instances.Save(&domain.DatabaseInstance{
		ProjectID: "p1", Tier: domain.Free, Status: "ACTIVE",
		CreatedAt: &domain.FlexTime{Time: clock.Now().Add(-6*day - time.Hour)},
	})
	if _, err := scheduler.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce without notifier/audit: %v", err)
	}
}

func TestIdlePause_TickRespectsLeaderLock(t *testing.T) {
	f := newIdleFixture(t)
	f.project(t, "p1", domain.Free, 8*day)
	f.scheduler.lock = &refusingLock{}
	f.scheduler.interval = 10 * time.Millisecond

	f.scheduler.Start(context.Background())
	time.Sleep(80 * time.Millisecond)
	f.scheduler.Stop()
	if f.pauser.count() != 0 {
		t.Errorf("non-leader must not pause anything: %d", f.pauser.count())
	}
}

func TestIdlePause_TickPausesWhenLeader(t *testing.T) {
	f := newIdleFixture(t)
	f.project(t, "p1", domain.Free, 8*day)
	f.scheduler.interval = 10 * time.Millisecond

	f.scheduler.Start(context.Background())
	t.Cleanup(f.scheduler.Stop)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if f.pauser.count() == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("leader tick never paused the idle project")
}

func TestIdlePause_StartIsIdempotentAndStopIsSafeTwice(t *testing.T) {
	f := newIdleFixture(t)
	f.scheduler.interval = time.Hour
	f.scheduler.Start(context.Background())
	f.scheduler.Start(context.Background())
	f.scheduler.Stop()
	f.scheduler.Stop()
}
