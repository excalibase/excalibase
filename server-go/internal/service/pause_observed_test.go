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
	// workloadDown is what WorkloadStopped reports: whether the project's
	// database is observed not running.
	workloadDown bool
	// workloadErr models an apiserver that cannot be asked.
	workloadErr error
	// onPause runs at the moment the workload is being stopped — the window
	// where another replica must not be able to act on the project.
	onPause func()
	// onResume runs at the moment the workload is being started.
	onResume func()
}

func (p *lifecyclePauser) WorkloadStopped(context.Context, string, string) (bool, error) {
	return p.workloadDown, p.workloadErr
}

func (p *lifecyclePauser) StopReplication(context.Context, string, string) error {
	p.log = append(p.log, "stop-replication")
	return p.stopReplicationErr
}

func (p *lifecyclePauser) Pause(context.Context, string, string) error {
	p.log = append(p.log, "pause")
	if p.onPause != nil {
		p.onPause()
	}
	if p.pauseErrOnce != nil {
		err := p.pauseErrOnce
		p.pauseErrOnce = nil
		return err
	}
	return p.pauseErr
}

func (p *lifecyclePauser) Resume(context.Context, string, string) error {
	p.log = append(p.log, "resume")
	if p.onResume != nil {
		p.onResume()
	}
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
	// triggers counts the backups actually filed.
	triggers int
	// latestID is what List would report as the project's newest backup.
	latestID string
	log    *[]string
}

func (b *scriptedBackups) BackupsConfigured(string) (bool, error) { return b.configured, nil }

func (b *scriptedBackups) TriggerManualBackup(context.Context, string) (map[string]interface{}, error) {
	if b.log != nil {
		*b.log = append(*b.log, "backup")
	}
	b.triggers++
	if b.triggerErr != nil {
		return nil, b.triggerErr
	}
	if b.idless {
		return map[string]interface{}{"status": "IN_PROGRESS"}, nil
	}
	return map[string]interface{}{"id": "bk-1", "status": "IN_PROGRESS"}, nil
}

func (b *scriptedBackups) LatestBackupID(context.Context, string) (string, error) {
	if b.latestID != "" {
		return b.latestID, nil
	}
	return "bk-1", nil
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

// newPauseServiceOver builds a second PauseService over the same stores and
// the same claimer — a second control-plane replica.
func newPauseServiceOver(f *observedPauseFixture) *PauseService {
	svc := NewPauseService(PauseServiceConfig{
		Instances:   f.store,
		Pausers:     map[domain.DeploymentMode]provisioner.Pauser{domain.ModeK8s: f.pauser},
		Backups:     f.backups,
		Replication: &restartRecorder{log: &f.pauser.log},
		Poller:      provisioner.Poller{Interval: time.Second, Timeout: 5 * time.Second, Now: stepClock(), After: instantAfter},
	})
	svc.claimer = f.svc.claimer
	return svc
}

func (f *observedPauseFixture) pause(t *testing.T) error {
	t.Helper()
	return f.svc.Pause(context.Background(), observedPauseProject, domain.PauseReasonManual)
}

// replicationRunning reports whether the watcher is up at the end of the
// recorded sequence: it starts up, and every stop must be matched by a
// later restart.
func (f *observedPauseFixture) replicationRunning() bool {
	running := true
	for _, call := range f.pauser.log {
		switch call {
		case "stop-replication":
			running = false
		case "restart-replication":
			running = true
		}
	}
	return running
}

func (f *observedPauseFixture) reload(t *testing.T) *domain.DatabaseInstance {
	t.Helper()
	inst, err := f.store.FindByProjectID(observedPauseProject)
	if err != nil || inst == nil {
		t.Fatalf("reload project: %v", err)
	}
	return inst
}

func TestPauseStopsReplicationBeforeTheShutdown(t *testing.T) {
	f := newObservedPause(t)

	if err := f.pause(t); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	want := []string{"backup", "stop-replication", "pause"}
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
}

func TestPauseRefusesABackupItCannotFollow(t *testing.T) {
	f := newObservedPause(t)
	f.backups.idless = true

	if err := f.pause(t); !errors.Is(err, ErrPauseBackupNotCompleted) {
		t.Fatalf("err: got %v, want ErrPauseBackupNotCompleted", err)
	}
}

// EXC-403 review: a pause that gives up must never leave a running project
// with its replication stopped. The watcher's slot would retain WAL with no
// consumer, and realtime would be silently off on a project reported ACTIVE.
func TestAFailedPauseLeavesReplicationRunning(t *testing.T) {
	cases := map[string]func(*observedPauseFixture){
		"backup fails": func(f *observedPauseFixture) {
			f.backups.statuses = []string{"FAILED"}
		},
		"hibernate is refused": func(f *observedPauseFixture) {
			f.pauser.pauseErr = errors.New("cluster rejected the annotation")
		},
	}
	for name, break_ := range cases {
		t.Run(name, func(t *testing.T) {
			f := newObservedPause(t)
			break_(f)

			if err := f.pause(t); err == nil {
				t.Fatal("the pause must fail")
			}
			if !f.replicationRunning() {
				t.Errorf("replication must be back on a project that is still running: %v", f.pauser.log)
			}
		})
	}
}

// If replication cannot be put back, the project must not claim to be a
// plain healthy ACTIVE project — the failure has to stay visible.
func TestAFailedPauseThatCannotRestoreReplicationSaysSo(t *testing.T) {
	f := newObservedPause(t)
	// The backup completes, replication stops, and then the shutdown is
	// refused — so there IS a watcher to put back, and it cannot be.
	f.pauser.pauseErr = errors.New("cluster rejected the annotation")
	f.svc.replication = &restartRecorder{log: &f.pauser.log, err: errors.New("vault sealed")}

	if err := f.pause(t); err == nil {
		t.Fatal("the pause must fail")
	}
	inst := f.reload(t)
	if inst.CurrentStep != pauseStepRestoreReplication {
		t.Errorf("step: got %q, want %q", inst.CurrentStep, pauseStepRestoreReplication)
	}
	if inst.FailureReason == "" {
		t.Error("a project whose replication could not be restarted must say so")
	}
	if inst.Status == "ACTIVE" {
		t.Error("a project running without replication must not read as a plain healthy project")
	}
}

// Taking the backup before the watcher is stopped shrinks the window where
// the project is running without replication to nothing for the commonest
// failure — a backup that does not complete.
func TestPauseBacksUpBeforeStoppingReplication(t *testing.T) {
	f := newObservedPause(t)

	if err := f.pause(t); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	want := []string{"backup", "stop-replication", "pause"}
	if strings.Join(f.pauser.log, ",") != strings.Join(want, ",") {
		t.Errorf("order: got %v, want %v", f.pauser.log, want)
	}
}

// A backup that never starts is the same story: nothing was stopped, so
// there is nothing to put back.
func TestAFailedBackupNeverStopsReplicationAtAll(t *testing.T) {
	f := newObservedPause(t)
	f.backups.triggerErr = errors.New("r2 unreachable")

	if err := f.pause(t); !errors.Is(err, ErrPauseBackupNotCompleted) {
		t.Fatalf("err: got %v, want ErrPauseBackupNotCompleted", err)
	}
	for _, call := range f.pauser.log {
		if call == "stop-replication" || call == "restart-replication" {
			t.Errorf("replication must never have been touched: %v", f.pauser.log)
		}
	}
}

// The error text promises a retry converges. That is only true if a project
// left in an intermediate state can be driven out of it through the same
// call — otherwise a failed resume strands the project forever.
func TestResumeRetryConvergesFromResuming(t *testing.T) {
	f := newObservedPause(t)
	if err := f.pause(t); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	f.pauser.resumeErr = errors.New("cluster never became ready")
	if err := f.svc.Resume(context.Background(), observedPauseProject); !errors.Is(err, ErrResumeNotObserved) {
		t.Fatalf("first attempt: got %v", err)
	}
	if got := f.reload(t).Status; got != string(domain.StatusResuming) {
		t.Fatalf("status: got %s, want RESUMING", got)
	}

	f.pauser.resumeErr = nil
	if err := f.svc.Resume(context.Background(), observedPauseProject); err != nil {
		t.Fatalf("retry must converge: %v", err)
	}
	if got := f.reload(t).Status; got != "ACTIVE" {
		t.Errorf("status: got %s, want ACTIVE", got)
	}
}

// A pause that failed after the database was already stopped leaves PAUSING.
// Resuming such a project has to work — the label says PAUSING but the
// workload is down, and only observation can tell.
func TestResumeAcceptsAPausingProjectWhoseWorkloadIsDown(t *testing.T) {
	f := newObservedPause(t)
	f.pauser.pauseErr = errors.New("recorded after the pods went away")
	if err := f.pause(t); !errors.Is(err, ErrPauseNotObserved) {
		t.Fatalf("pause: got %v", err)
	}
	if got := f.reload(t).Status; got != string(domain.StatusPausing) {
		t.Fatalf("status: got %s, want PAUSING", got)
	}
	f.pauser.pauseErr = nil
	f.pauser.workloadDown = true

	if err := f.svc.Resume(context.Background(), observedPauseProject); err != nil {
		t.Fatalf("a stuck PAUSING project with a stopped database must be resumable: %v", err)
	}
	if got := f.reload(t).Status; got != "ACTIVE" {
		t.Errorf("status: got %s, want ACTIVE", got)
	}
}

// ...but a PAUSING project whose database is still up has nothing to resume:
// starting a running workload is not something to guess at.
func TestResumeLeavesAPausingProjectWhoseWorkloadIsStillUp(t *testing.T) {
	f := newObservedPause(t)
	f.pauser.pauseErr = errors.New("hibernate refused")
	if err := f.pause(t); err == nil {
		t.Fatal("pause must fail")
	}
	f.pauser.log = nil
	f.pauser.workloadDown = false

	if err := f.svc.Resume(context.Background(), observedPauseProject); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	for _, call := range f.pauser.log {
		if call == "resume" {
			t.Error("a running workload must not be started again")
		}
	}
}

func TestPauseAcceptsAProjectLeftInPausing(t *testing.T) {
	f := newObservedPause(t)
	f.pauser.pauseErrOnce = errors.New("transient")
	if err := f.pause(t); !errors.Is(err, ErrPauseNotObserved) {
		t.Fatalf("first attempt: got %v", err)
	}
	if err := f.pause(t); err != nil {
		t.Fatalf("retry from PAUSING must converge: %v", err)
	}
	if got := f.reload(t).Status; got != string(domain.StatusPaused) {
		t.Errorf("status: got %s, want PAUSED", got)
	}
}
