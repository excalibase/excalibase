package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// inPlaceWorld records every call an in-place restore makes, in order, so a
// test can read the sequence back.
type inPlaceWorld struct {
	mu    sync.Mutex
	calls []string

	instances storage.InstanceStore
	released  bool

	holdErr     error
	targetErr   error
	backupErr   error
	replaceErrs []error // one per ReplaceDatabase call, in order
	probeErrs   []error
	// One-shot failures of the project-side steps, by step name.
	failOnce     map[string]error
	targets      []map[string]interface{}
	statusesSeen []string
}

func (w *inPlaceWorld) record(call string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, call)
}

func (w *inPlaceWorld) HoldForInPlaceRestore(_ context.Context, projectID string) (*domain.DatabaseInstance, func(), error) {
	w.record("hold")
	if w.holdErr != nil {
		return nil, nil, w.holdErr
	}
	inst, err := w.instances.FindByProjectID(projectID)
	if err != nil {
		return nil, nil, err
	}
	return inst, func() { w.released = true }, nil
}

func (w *inPlaceWorld) step(name string) error {
	w.record(name)
	w.mu.Lock()
	defer w.mu.Unlock()
	err := w.failOnce[name]
	delete(w.failOnce, name)
	return err
}

func (w *inPlaceWorld) StopReplication(context.Context, *domain.DatabaseInstance) error {
	return w.step("stop-replication")
}

func (w *inPlaceWorld) RestartReplication(context.Context, *domain.DatabaseInstance) error {
	return w.step("restart-replication")
}

func (w *inPlaceWorld) ReapplyCredentials(context.Context, *domain.DatabaseInstance) error {
	return w.step("reapply-credentials")
}

func (w *inPlaceWorld) AnnounceDatabaseReplaced(context.Context, string) { w.record("announce") }

func (w *inPlaceWorld) InPlaceRecoveryTarget(_ context.Context, _ *domain.DatabaseInstance, req domain.RestoreRequest) (map[string]interface{}, error) {
	if req.BackupID != "" {
		w.record("target:" + req.BackupID)
		return map[string]interface{}{"backupID": req.BackupID, "targetImmediate": true}, nil
	}
	w.record("target")
	if w.targetErr != nil {
		return nil, w.targetErr
	}
	return map[string]interface{}{"targetTime": "2026-10-07T12:00:00Z"}, nil
}

func (w *inPlaceWorld) TakeSafetyBackup(context.Context, *domain.DatabaseInstance) (string, error) {
	w.record("safety-backup")
	if w.backupErr != nil {
		return "", w.backupErr
	}
	return "safety-1", nil
}

func (w *inPlaceWorld) ReplaceDatabase(_ context.Context, inst *domain.DatabaseInstance, target map[string]interface{}) error {
	w.record("replace")
	w.mu.Lock()
	w.targets = append(w.targets, target)
	var err error
	if len(w.replaceErrs) > 0 {
		err, w.replaceErrs = w.replaceErrs[0], w.replaceErrs[1:]
	}
	w.mu.Unlock()
	row, _ := w.instances.FindByProjectID(inst.ProjectID)
	w.statusesSeen = append(w.statusesSeen, row.Status)
	return err
}

func (w *inPlaceWorld) Probe(context.Context, string) error {
	w.record("probe")
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.probeErrs) > 0 {
		err := w.probeErrs[0]
		w.probeErrs = w.probeErrs[1:]
		return err
	}
	return nil
}

func (w *inPlaceWorld) ProjectStatusChanged(_, status string) { w.record("observed:" + status) }

func newInPlaceWorld(t *testing.T, project *domain.DatabaseInstance) (*inPlaceWorld, *InPlaceRestore) {
	t.Helper()
	instances, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := instances.Create(project); err != nil {
		t.Fatal(err)
	}
	w := &inPlaceWorld{instances: instances}
	restorer := NewInPlaceRestore(InPlaceRestoreConfig{
		Projects: w, Instances: instances, Probe: w, Observers: []StatusObserver{w},
	})
	return w, restorer
}

func inPlaceProject() *domain.DatabaseInstance {
	return &domain.DatabaseInstance{
		ProjectID: "proj-a", OrgID: "org", Status: string(domain.StatusActive),
		BackupEnabled: boolPtr(true), DeletionProtection: boolPtr(true),
	}
}

func restoreAt() domain.RestoreRequest {
	return domain.RestoreRequest{Mode: domain.RestoreModeInPlace, ConfirmReplace: true, TargetProjectID: "proj-a"}
}

func phasesOf(ctx context.Context) (context.Context, *[]string) {
	phases := []string{}
	var mu sync.Mutex
	return context.WithValue(ctx, restoreProgressKey{}, func(p string) {
		mu.Lock()
		defer mu.Unlock()
		phases = append(phases, p)
	}), &phases
}

func TestInPlaceRestore_ReplacesTheDatabaseAfterASafetyBackup(t *testing.T) {
	w, restorer := newInPlaceWorld(t, inPlaceProject())
	ctx, phases := phasesOf(context.Background())

	if err := restorer.Restore(ctx, w, "proj-a", restoreAt()); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	want := []string{"hold", "target", "safety-backup", "target:safety-1", "observed:RESTORING", "stop-replication",
		"replace", "reapply-credentials", "probe", "restart-replication", "announce", "observed:ACTIVE"}
	if strings.Join(w.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls:\n got %v\nwant %v", w.calls, want)
	}
	if w.statusesSeen[0] != string(domain.StatusRestoring) {
		t.Errorf("the project must not be served while its database is replaced, saw %s", w.statusesSeen[0])
	}
	if w.targets[0]["targetTime"] != "2026-10-07T12:00:00Z" {
		t.Errorf("replaced to %v", w.targets[0])
	}
	row, _ := w.instances.FindByProjectID("proj-a")
	if row.Status != string(domain.StatusActive) || row.FailureReason != "" || row.CurrentStep != "" {
		t.Errorf("after the restore: %+v", row)
	}
	if row.DeletionProtection == nil || !*row.DeletionProtection {
		t.Error("a replace must not drop the project's deletion protection")
	}
	if !w.released {
		t.Error("the project's lease must be released")
	}
	wantPhases := []string{domain.RestoreStepSafetyBackup, domain.RestoreStepReplacing, domain.RestoreStepChecking}
	if strings.Join(*phases, ",") != strings.Join(wantPhases, ",") {
		t.Errorf("phases %v, want %v", *phases, wantPhases)
	}
}

func TestInPlaceRestore_AnUnreachableTargetChangesNothing(t *testing.T) {
	w, restorer := newInPlaceWorld(t, inPlaceProject())
	w.targetErr = ErrRestoreTargetNotArchived

	err := restorer.Restore(context.Background(), w, "proj-a", restoreAt())
	if !errors.Is(err, ErrRestoreTargetNotArchived) {
		t.Fatalf("got %v", err)
	}
	if strings.Join(w.calls, ",") != "hold,target" {
		t.Fatalf("nothing may be touched: %v", w.calls)
	}
	if !w.released {
		t.Error("lease not released")
	}
}

func TestInPlaceRestore_AFailedSafetyBackupChangesNothing(t *testing.T) {
	w, restorer := newInPlaceWorld(t, inPlaceProject())
	w.backupErr = errors.New("backup FAILED")

	err := restorer.Restore(context.Background(), w, "proj-a", restoreAt())
	var public *InPlaceRestoreError
	if !errors.As(err, &public) || !strings.Contains(public.Public, "nothing was changed") {
		t.Fatalf("got %v", err)
	}
	for _, call := range w.calls {
		if call == "replace" || strings.HasPrefix(call, "observed:") {
			t.Fatalf("the database must not be touched without a safety backup: %v", w.calls)
		}
	}
	row, _ := w.instances.FindByProjectID("proj-a")
	if row.Status != string(domain.StatusActive) {
		t.Errorf("status %s", row.Status)
	}
}

func TestInPlaceRestore_AFailedReplaceIsRolledBackToTheSafetyBackup(t *testing.T) {
	w, restorer := newInPlaceWorld(t, inPlaceProject())
	w.replaceErrs = []error{errors.New("recovered cluster never became ready")}
	ctx, phases := phasesOf(context.Background())

	err := restorer.Restore(ctx, w, "proj-a", restoreAt())
	var public *InPlaceRestoreError
	if !errors.As(err, &public) || !strings.Contains(public.Public, "put back as it was") || !strings.Contains(public.Public, "safety-1") {
		t.Fatalf("got %v", err)
	}
	if len(w.targets) != 2 || w.targets[1]["backupID"] != "safety-1" {
		t.Fatalf("the rollback recovers the safety backup: %v", w.targets)
	}
	row, _ := w.instances.FindByProjectID("proj-a")
	if row.Status != string(domain.StatusActive) {
		t.Errorf("a rolled-back project is usable again, got %s", row.Status)
	}
	if !hasEntry(*phases, domain.RestoreStepRollingBack) {
		t.Errorf("phases %v", *phases)
	}
}

func TestInPlaceRestore_AFailedProbeIsRolledBack(t *testing.T) {
	w, restorer := newInPlaceWorld(t, inPlaceProject())
	w.probeErrs = []error{errors.New("connection refused")}

	err := restorer.Restore(context.Background(), w, "proj-a", restoreAt())
	if err == nil || len(w.targets) != 2 {
		t.Fatalf("err=%v targets=%v", err, w.targets)
	}
	row, _ := w.instances.FindByProjectID("proj-a")
	if row.Status != string(domain.StatusActive) {
		t.Errorf("status %s", row.Status)
	}
}

func TestInPlaceRestore_AFailedRollbackLeavesTheProjectStoppedWithTheWayBack(t *testing.T) {
	w, restorer := newInPlaceWorld(t, inPlaceProject())
	w.replaceErrs = []error{errors.New("first"), errors.New("second")}

	err := restorer.Restore(context.Background(), w, "proj-a", restoreAt())
	var public *InPlaceRestoreError
	if !errors.As(err, &public) || !strings.Contains(public.Public, "safety-1") {
		t.Fatalf("got %v", err)
	}
	row, _ := w.instances.FindByProjectID("proj-a")
	if row.Status != string(domain.StatusRestoring) {
		t.Fatalf("a project with no confirmed database must not be served, got %s", row.Status)
	}
	if row.CurrentStep != domain.RestoreStepRestoreStopped || !strings.Contains(row.FailureReason, "safety-1") {
		t.Errorf("the project must say how to get its data back: %+v", row)
	}
}

func TestInPlaceRestore_RefusesAProjectWithoutBackups(t *testing.T) {
	project := inPlaceProject()
	project.BackupEnabled = boolPtr(false)
	w, restorer := newInPlaceWorld(t, project)

	err := restorer.Restore(context.Background(), w, "proj-a", restoreAt())
	if !errors.Is(err, ErrInPlaceNoBackups) {
		t.Fatalf("got %v", err)
	}
}

// A restore that was interrupted left the project RESTORING. Restoring again
// works from the project's backups; there is no running database to back up.
func TestInPlaceRestore_AgainAfterAnInterruptionSkipsTheSafetyBackup(t *testing.T) {
	project := inPlaceProject()
	project.Status = string(domain.StatusRestoring)
	project.CurrentStep = domain.RestoreStepRestoreStopped
	project.FailureReason = "interrupted"
	w, restorer := newInPlaceWorld(t, project)

	if err := restorer.Restore(context.Background(), w, "proj-a", restoreAt()); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if hasEntry(w.calls, "safety-backup") {
		t.Errorf("no running database to back up: %v", w.calls)
	}
	row, _ := w.instances.FindByProjectID("proj-a")
	if row.Status != string(domain.StatusActive) || row.FailureReason != "" {
		t.Errorf("after the restore: %+v", row)
	}
}

func TestInPlaceRestore_AgainAfterAnInterruptionThatFailsStaysStopped(t *testing.T) {
	project := inPlaceProject()
	project.Status = string(domain.StatusRestoring)
	project.CurrentStep = domain.RestoreStepRestoreStopped
	w, restorer := newInPlaceWorld(t, project)
	w.replaceErrs = []error{errors.New("boom")}

	if err := restorer.Restore(context.Background(), w, "proj-a", restoreAt()); err == nil {
		t.Fatal("want an error")
	}
	if len(w.targets) != 1 {
		t.Errorf("no safety backup to roll back to: %v", w.targets)
	}
	row, _ := w.instances.FindByProjectID("proj-a")
	if row.Status != string(domain.StatusRestoring) || row.CurrentStep != domain.RestoreStepRestoreStopped {
		t.Errorf("after the failure: %+v", row)
	}
}

func TestInPlaceRestore_ABusyProjectIsRefused(t *testing.T) {
	w, restorer := newInPlaceWorld(t, inPlaceProject())
	w.holdErr = ErrProjectOperationRunning

	if err := restorer.Restore(context.Background(), w, "proj-a", restoreAt()); !errors.Is(err, ErrProjectOperationRunning) {
		t.Fatalf("got %v", err)
	}
}

func hasEntry(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// A failure after the swap is rolled back like a failed swap; a watcher that
// cannot be stopped does not stop the restore.
func TestInPlaceRestore_AFailedCheckAfterTheSwapIsRolledBack(t *testing.T) {
	for _, failing := range []string{"reapply-credentials", "restart-replication"} {
		t.Run(failing, func(t *testing.T) {
			w, restorer := newInPlaceWorld(t, inPlaceProject())
			w.failOnce = map[string]error{failing: errors.New("boom"), "stop-replication": errors.New("not running")}

			err := restorer.Restore(context.Background(), w, "proj-a", restoreAt())
			var public *InPlaceRestoreError
			if !errors.As(err, &public) || !strings.Contains(public.Public, "put back as it was") {
				t.Fatalf("got %v", err)
			}
			if len(w.targets) != 2 {
				t.Errorf("targets %v", w.targets)
			}
		})
	}
}
