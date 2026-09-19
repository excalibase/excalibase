package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

const observedPauseProject = "pause-1"

// lifecyclePauser records the order of the lifecycle calls a pause makes, so a
// test can assert replication stops before the backup rather than after it.
type lifecyclePauser struct {
	log                []string
	stopReplicationErr error
	pauseErr           error
	resumeErr          error
	// pauseErrOnce fails only the first Pause, so a retry can converge.
	pauseErrOnce error
}

func (p *lifecyclePauser) StopReplication(context.Context, string, string) error {
	p.log = append(p.log, "stop-replication")
	return p.stopReplicationErr
}

func (p *lifecyclePauser) Pause(context.Context, string, string) error {
	p.log = append(p.log, "pause")
	if p.pauseErrOnce != nil {
		err := p.pauseErrOnce
		p.pauseErrOnce = nil
		return err
	}
	return p.pauseErr
}

func (p *lifecyclePauser) Resume(context.Context, string, string) error {
	p.log = append(p.log, "resume")
	return p.resumeErr
}

// scriptedBackups is a pre-pause backup whose observed status is scripted:
// statuses are returned one per poll, the last one repeating.
type scriptedBackups struct {
	configured bool
	triggerErr error
	statuses   []string
	polls      int
	// idless models an adapter that accepts a backup without naming it.
	idless bool
	log    *[]string
}

func (b *scriptedBackups) BackupsConfigured(string) (bool, error) { return b.configured, nil }

func (b *scriptedBackups) TriggerManualBackup(context.Context, string) (map[string]interface{}, error) {
	if b.log != nil {
		*b.log = append(*b.log, "backup")
	}
	if b.triggerErr != nil {
		return nil, b.triggerErr
	}
	if b.idless {
		return map[string]interface{}{"status": "IN_PROGRESS"}, nil
	}
	return map[string]interface{}{"id": "bk-1", "status": "IN_PROGRESS"}, nil
}

func (b *scriptedBackups) BackupStatus(context.Context, string, string) (string, error) {
	if len(b.statuses) == 0 {
		return "IN_PROGRESS", nil
	}
	i := b.polls
	if i >= len(b.statuses) {
		i = len(b.statuses) - 1
	}
	b.polls++
	return b.statuses[i], nil
}

// restartRecorder stands in for the control plane's watcher redeploy.
type restartRecorder struct {
	log *[]string
	err error
}

func (r *restartRecorder) RestartReplication(context.Context, *domain.DatabaseInstance) error {
	*r.log = append(*r.log, "restart-replication")
	return r.err
}

type observedPauseFixture struct {
	svc     *PauseService
	store   *storage.FileSystemStore
	pauser  *lifecyclePauser
	backups *scriptedBackups
}

func newObservedPause(t *testing.T) *observedPauseFixture {
	t.Helper()
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	pauser := &lifecyclePauser{}
	backups := &scriptedBackups{configured: true, statuses: []string{"COMPLETED"}, log: &pauser.log}
	svc := NewPauseService(PauseServiceConfig{
		Instances: store,
		Pausers: map[domain.DeploymentMode]provisioner.Pauser{
			domain.ModeK8s: pauser,
		},
		Backups:     backups,
		Replication: &restartRecorder{log: &pauser.log},
		Poller:      provisioner.Poller{Interval: time.Second, Timeout: 5 * time.Second, Now: stepClock(), After: instantAfter},
	})
	if err := store.Create(&domain.DatabaseInstance{
		ProjectID: observedPauseProject, OrgID: "org", Namespace: "org-pause-1",
		DeploymentMode: domain.ModeK8s, Status: "ACTIVE",
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	return &observedPauseFixture{svc: svc, store: store, pauser: pauser, backups: backups}
}

// stepClock advances one second per read, so a bounded wait driven by it
// spends its budget without any real time passing.
func stepClock() func() time.Time {
	now := time.Unix(0, 0)
	return func() time.Time {
		now = now.Add(time.Second)
		return now
	}
}

func instantAfter(time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	ch <- time.Unix(0, 0)
	return ch
}

func (f *observedPauseFixture) pause(t *testing.T) error {
	t.Helper()
	return f.svc.Pause(context.Background(), observedPauseProject, domain.PauseReasonManual)
}

func (f *observedPauseFixture) reload(t *testing.T) *domain.DatabaseInstance {
	t.Helper()
	inst, err := f.store.FindByProjectID(observedPauseProject)
	if err != nil || inst == nil {
		t.Fatalf("reload project: %v", err)
	}
	return inst
}

func TestPauseOrdersReplicationStopBeforeTheBackup(t *testing.T) {
	f := newObservedPause(t)

	if err := f.pause(t); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	want := []string{"stop-replication", "backup", "pause"}
	if strings.Join(f.pauser.log, ",") != strings.Join(want, ",") {
		t.Errorf("order: got %v, want %v", f.pauser.log, want)
	}
	if got := f.reload(t).Status; got != string(domain.StatusPaused) {
		t.Errorf("status: got %s, want PAUSED", got)
	}
}

func TestPauseFailsWhenThePrePauseBackupFails(t *testing.T) {
	f := newObservedPause(t)
	f.backups.statuses = []string{"IN_PROGRESS", "FAILED"}

	err := f.pause(t)
	if !errors.Is(err, ErrPauseBackupNotCompleted) {
		t.Fatalf("err: got %v, want ErrPauseBackupNotCompleted", err)
	}
	inst := f.reload(t)
	if inst.Status != "ACTIVE" {
		t.Errorf("a project that was not backed up must stay usable, got %s", inst.Status)
	}
	for _, call := range f.pauser.log {
		if call == "pause" {
			t.Error("the workload must not be stopped without a completed backup")
		}
	}
}

func TestPauseFailsWhenThePrePauseBackupNeverCompletes(t *testing.T) {
	f := newObservedPause(t)
	f.backups.statuses = []string{"IN_PROGRESS"}

	if err := f.pause(t); !errors.Is(err, ErrPauseBackupNotCompleted) {
		t.Fatalf("err: got %v, want ErrPauseBackupNotCompleted", err)
	}
	if got := f.reload(t).Status; got != "ACTIVE" {
		t.Errorf("status: got %s, want ACTIVE", got)
	}
}

func TestPauseSkipsTheBackupWhenTheProjectHasNowhereToWriteOne(t *testing.T) {
	f := newObservedPause(t)
	f.backups.configured = false

	if err := f.pause(t); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	for _, call := range f.pauser.log {
		if call == "backup" {
			t.Fatal("a project with no backup storage has no backup to wait for")
		}
	}
	if got := f.reload(t).Status; got != string(domain.StatusPaused) {
		t.Errorf("status: got %s, want PAUSED", got)
	}
}

func TestPauseIsNotPausedUntilTheWorkloadIsObservedStopped(t *testing.T) {
	f := newObservedPause(t)
	f.pauser.pauseErr = errors.New("pod pause-1-postgres-1 still Terminating")

	err := f.pause(t)
	if !errors.Is(err, ErrPauseNotObserved) {
		t.Fatalf("err: got %v, want ErrPauseNotObserved", err)
	}
	inst := f.reload(t)
	if inst.Status != string(domain.StatusPausing) {
		t.Errorf("status: got %s, want PAUSING", inst.Status)
	}
	if inst.CurrentStep != pauseStepStopWorkload {
		t.Errorf("step: got %q, want %q", inst.CurrentStep, pauseStepStopWorkload)
	}
	if inst.FailureReason == "" {
		t.Error("a stuck pause must say which step it stuck on")
	}
	if strings.Contains(inst.FailureReason, "Terminating") {
		t.Errorf("the stored reason must not carry internals: %q", inst.FailureReason)
	}
}

func TestPauseRetryConvergesAfterAFailedAttempt(t *testing.T) {
	f := newObservedPause(t)
	f.pauser.pauseErrOnce = errors.New("api server unreachable")

	if err := f.pause(t); !errors.Is(err, ErrPauseNotObserved) {
		t.Fatalf("first attempt: got %v, want ErrPauseNotObserved", err)
	}
	if err := f.pause(t); err != nil {
		t.Fatalf("retry must converge: %v", err)
	}
	inst := f.reload(t)
	if inst.Status != string(domain.StatusPaused) {
		t.Errorf("status: got %s, want PAUSED", inst.Status)
	}
	if inst.FailureReason != "" || inst.CurrentStep != "" {
		t.Errorf("a converged retry must clear the failure: step=%q reason=%q", inst.CurrentStep, inst.FailureReason)
	}
}

func TestPauseStopsWhenTheProjectIsBeingDeleted(t *testing.T) {
	f := newObservedPause(t)
	if _, err := f.store.BeginDeletion(observedPauseProject, nil); err != nil {
		t.Fatalf("BeginDeletion: %v", err)
	}

	if err := f.pause(t); !errors.Is(err, storage.ErrProjectDeleting) {
		t.Fatalf("err: got %v, want ErrProjectDeleting", err)
	}
	if len(f.pauser.log) != 0 {
		t.Errorf("a project a teardown owns must not be touched: %v", f.pauser.log)
	}
}

func TestResumeRestartsReplicationOnlyAfterTheWorkloadIsBack(t *testing.T) {
	f := newObservedPause(t)
	if err := f.pause(t); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	f.pauser.log = nil

	if err := f.svc.Resume(context.Background(), observedPauseProject); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	want := []string{"resume", "restart-replication"}
	if strings.Join(f.pauser.log, ",") != strings.Join(want, ",") {
		t.Errorf("order: got %v, want %v", f.pauser.log, want)
	}
	if got := f.reload(t).Status; got != "ACTIVE" {
		t.Errorf("status: got %s, want ACTIVE", got)
	}
}

func TestResumeStaysResumingWhenReplicationCannotBeRestarted(t *testing.T) {
	f := newObservedPause(t)
	if err := f.pause(t); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	f.svc.replication = &restartRecorder{log: &f.pauser.log, err: errors.New("vault sealed")}

	if err := f.svc.Resume(context.Background(), observedPauseProject); !errors.Is(err, ErrResumeNotObserved) {
		t.Fatalf("err: got %v, want ErrResumeNotObserved", err)
	}
	if got := f.reload(t).Status; got != string(domain.StatusResuming) {
		t.Errorf("status: got %s, want RESUMING", got)
	}
}

func TestPauseClientErrorsCarryNoInternals(t *testing.T) {
	for _, err := range []error{ErrPauseBackupNotCompleted, ErrPauseNotObserved, ErrResumeNotObserved} {
		msg := strings.ToLower(err.Error())
		for _, leak := range []string{"namespace", "cnpg", "vault", "pod/", "helm"} {
			if strings.Contains(msg, leak) {
				t.Errorf("%v leaks %q", err, leak)
			}
		}
	}
}

// The idle-pause scheduler calls the same path as a manual pause. A failed
// idle pause must leave the project retryable rather than wedged in PAUSING
// with nothing able to move it.
func TestIdlePauseThatFailsLeavesTheProjectRetryable(t *testing.T) {
	f := newObservedPause(t)
	f.pauser.pauseErrOnce = errors.New("node under memory pressure")

	err := f.svc.Pause(context.Background(), observedPauseProject, domain.PauseReasonIdle)
	if !errors.Is(err, ErrPauseNotObserved) {
		t.Fatalf("first sweep: got %v, want ErrPauseNotObserved", err)
	}
	if got := f.reload(t).Status; got != string(domain.StatusPausing) {
		t.Fatalf("status after a failed idle pause: got %s, want PAUSING", got)
	}

	if err := f.svc.Pause(context.Background(), observedPauseProject, domain.PauseReasonIdle); err != nil {
		t.Fatalf("the next sweep must converge: %v", err)
	}
	inst := f.reload(t)
	if inst.Status != string(domain.StatusPaused) || inst.PauseReason != domain.PauseReasonIdle {
		t.Errorf("converged row: status=%s reason=%s", inst.Status, inst.PauseReason)
	}
}

func TestPauseReportsABackupItCannotStart(t *testing.T) {
	f := newObservedPause(t)
	f.backups.triggerErr = errors.New("r2 unreachable")

	if err := f.pause(t); !errors.Is(err, ErrPauseBackupNotCompleted) {
		t.Fatalf("err: got %v, want ErrPauseBackupNotCompleted", err)
	}
	if got := f.reload(t).Status; got != "ACTIVE" {
		t.Errorf("status: got %s, want ACTIVE", got)
	}
}

func TestPauseFailsWhenReplicationCannotBeStopped(t *testing.T) {
	f := newObservedPause(t)
	f.pauser.stopReplicationErr = errors.New("helm release locked")

	if err := f.pause(t); !errors.Is(err, ErrPauseNotObserved) {
		t.Fatalf("err: got %v, want ErrPauseNotObserved", err)
	}
	inst := f.reload(t)
	if inst.CurrentStep != pauseStepStopReplication {
		t.Errorf("step: got %q, want %q", inst.CurrentStep, pauseStepStopReplication)
	}
	for _, call := range f.pauser.log {
		if call == "backup" {
			t.Error("the backup must not start while replication is still open")
		}
	}
}

func TestPauseRefusesABackupItCannotFollow(t *testing.T) {
	f := newObservedPause(t)
	f.backups.idless = true

	if err := f.pause(t); !errors.Is(err, ErrPauseBackupNotCompleted) {
		t.Fatalf("err: got %v, want ErrPauseBackupNotCompleted", err)
	}
}
