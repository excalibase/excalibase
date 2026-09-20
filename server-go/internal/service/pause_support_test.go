package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

func backupSupportService(t *testing.T, storageSource BackupStorageSource) (*BackupService, *storage.FileSystemStore) {
	t.Helper()
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := store.Create(&domain.DatabaseInstance{
		ProjectID: "bk-p", OrgID: "org", Namespace: "org-bk-p",
		DeploymentMode: domain.ModeK8s, Status: "ACTIVE",
		Tier: domain.Standard, BackupEnabled: boolPtr(true),
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	return NewBackupService(store, k8s.NewMockClient(), t.TempDir(), storageSource), store
}

func TestBackupsConfiguredReflectsTheAdaptersObjectStore(t *testing.T) {
	wired, _ := backupSupportService(t, StaticBackupStorage(r2Storage()))
	got, err := wired.BackupsConfigured("bk-p")
	if err != nil || !got {
		t.Errorf("a wired object store must report configured: %v %v", got, err)
	}

	bare, _ := backupSupportService(t, nil)
	got, err = bare.BackupsConfigured("bk-p")
	if err != nil || got {
		t.Errorf("without an object store there is nowhere to write a backup: %v %v", got, err)
	}
}

func TestDockerAdapterReportsWhetherItHasSomewhereToWrite(t *testing.T) {
	bare := NewDockerBackupAdapter(DockerBackupAdapterConfig{})
	if bare.BackupsConfigured() {
		t.Error("an adapter with no uploader and no bucket has nowhere to write")
	}
	wired := NewDockerBackupAdapter(DockerBackupAdapterConfig{Uploader: newFakeS3Uploader(), Bucket: "b"})
	if !wired.BackupsConfigured() {
		t.Error("an adapter with an uploader and a bucket is configured")
	}
}

func TestBackupSupportRefusesAnUnknownProject(t *testing.T) {
	svc, _ := backupSupportService(t, StaticBackupStorage(r2Storage()))

	if _, err := svc.BackupsConfigured("nope"); err == nil {
		t.Error("an unknown project must be reported, not answered")
	}
	if _, err := svc.BackupStatus(context.Background(), "nope", "bk-1"); err == nil {
		t.Error("an unknown project must be reported, not answered")
	}
}

func TestBackupStatusFollowsATriggeredBackup(t *testing.T) {
	svc, _ := backupSupportService(t, StaticBackupStorage(r2Storage()))
	started, err := svc.TriggerManualBackup(context.Background(), "bk-p")
	if err != nil {
		t.Fatalf("TriggerManualBackup: %v", err)
	}
	id, _ := started["id"].(string)

	status, err := svc.BackupStatus(context.Background(), "bk-p", id)
	if err != nil {
		t.Fatalf("BackupStatus: %v", err)
	}
	if status != "IN_PROGRESS" {
		t.Errorf("status: got %q, want IN_PROGRESS", status)
	}
	if _, err := svc.BackupStatus(context.Background(), "bk-p", "never-taken"); err == nil {
		t.Error("a backup id the project does not have must be reported, not guessed")
	}
}

func TestNewPausePollerCarriesTheConfiguredBudget(t *testing.T) {
	if got := NewPausePoller(42 * time.Minute).Timeout; got != 42*time.Minute {
		t.Errorf("timeout: got %v, want 42m", got)
	}
}

func TestPauseWithoutABackupServiceStillObservesTheWorkload(t *testing.T) {
	f := newObservedPause(t)
	f.svc.backups = nil

	if err := f.pause(t); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if got := f.reload(t).Status; got != string(domain.StatusPaused) {
		t.Errorf("status: got %s, want PAUSED", got)
	}
}

func TestPauseReportsBackupStorageItCannotResolve(t *testing.T) {
	f := newObservedPause(t)
	f.svc.backups = &unresolvableBackups{}

	if err := f.pause(t); !errors.Is(err, ErrPauseBackupNotCompleted) {
		t.Fatalf("err: got %v, want ErrPauseBackupNotCompleted", err)
	}
}

func TestPauseReportsABackupStatusItCannotRead(t *testing.T) {
	f := newObservedPause(t)
	f.svc.backups = &unreadableBackups{}

	if err := f.pause(t); !errors.Is(err, ErrPauseBackupNotCompleted) {
		t.Fatalf("err: got %v, want ErrPauseBackupNotCompleted", err)
	}
}

// unresolvableBackups cannot say whether the project has backup storage.
type unresolvableBackups struct{}

func (unresolvableBackups) BackupsConfigured(string) (bool, error) {
	return false, errors.New("vault sealed")
}

func (unresolvableBackups) TriggerManualBackup(context.Context, string) (map[string]interface{}, error) {
	return nil, nil
}

func (unresolvableBackups) LatestBackupID(context.Context, string) (string, error) {
	return "bk-1", nil
}

func (unresolvableBackups) BackupStatus(context.Context, string, string) (string, error) {
	return "", nil
}

// unreadableBackups starts a backup but cannot be asked how it is going.
type unreadableBackups struct{}

func (unreadableBackups) BackupsConfigured(string) (bool, error) { return true, nil }

func (unreadableBackups) TriggerManualBackup(context.Context, string) (map[string]interface{}, error) {
	return map[string]interface{}{"id": "bk-1"}, nil
}

func (unreadableBackups) LatestBackupID(context.Context, string) (string, error) {
	return "bk-1", nil
}

func (unreadableBackups) BackupStatus(context.Context, string, string) (string, error) {
	return "", errors.New("platform db unavailable")
}

func TestResumeRefusesAModeWithNoPauser(t *testing.T) {
	f := newObservedPause(t)
	if err := f.pause(t); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	f.svc.pausers = map[domain.DeploymentMode]provisioner.Pauser{}

	if err := f.svc.Resume(context.Background(), observedPauseProject); !errors.Is(err, ErrPauseUnsupported) {
		t.Fatalf("err: got %v, want ErrPauseUnsupported", err)
	}
}

// A project a teardown has claimed is no longer PAUSED, so a resume has
// nothing to do: it must not start the workload back up under a deletion.
func TestResumeDoesNothingForAProjectBeingDeleted(t *testing.T) {
	f := newObservedPause(t)
	if err := f.pause(t); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if _, err := f.store.BeginDeletion(observedPauseProject, nil); err != nil {
		t.Fatalf("BeginDeletion: %v", err)
	}
	f.pauser.log = nil

	if err := f.svc.Resume(context.Background(), observedPauseProject); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if len(f.pauser.log) != 0 {
		t.Errorf("a deleting project's workload must stay down: %v", f.pauser.log)
	}
	if got := f.reload(t).Status; got != string(domain.StatusDeleting) {
		t.Errorf("status: got %s, want DELETING", got)
	}
}

func TestResumeWithoutAReplicationRestarterStillActivates(t *testing.T) {
	f := newObservedPause(t)
	if err := f.pause(t); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	f.svc.replication = nil

	if err := f.svc.Resume(context.Background(), observedPauseProject); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if got := f.reload(t).Status; got != "ACTIVE" {
		t.Errorf("status: got %s, want ACTIVE", got)
	}
}

func TestResumeFailsWhenTheWorkloadDoesNotComeBack(t *testing.T) {
	f := newObservedPause(t)
	if err := f.pause(t); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	f.pauser.resumeErr = errors.New("cluster never became ready")

	if err := f.svc.Resume(context.Background(), observedPauseProject); !errors.Is(err, ErrResumeNotObserved) {
		t.Fatalf("err: got %v, want ErrResumeNotObserved", err)
	}
	inst := f.reload(t)
	if inst.CurrentStep != resumeStepStartWorkload {
		t.Errorf("step: got %q, want %q", inst.CurrentStep, resumeStepStartWorkload)
	}
}

func TestRestartReplicationNeedsItsDependencies(t *testing.T) {
	h := newRegistrationHarness(t)
	inst := restoredInstance()

	// No PostgreSQL provisioner in the factory.
	if err := h.svc.RestartReplication(context.Background(), inst); !errors.Is(err, ErrReplicationRestartUnavailable) {
		t.Fatalf("err: got %v, want ErrReplicationRestartUnavailable", err)
	}
	if err := h.svc.RestartReplication(context.Background(), nil); !errors.Is(err, ErrProjectRegistrationInvalid) {
		t.Fatalf("nil project: got %v, want ErrProjectRegistrationInvalid", err)
	}
}

func TestRestartReplicationRedeploysTheWatcher(t *testing.T) {
	h := newRegistrationHarness(t)
	h.svc.factory = provisioner.NewFactory(provisioner.NewPostgreSQLProvisioner(h.kube, "/charts/excalibase-watcher"))
	h.svc.SetNatsCredentialMinter(NewNatsCredentialMinter(newFakeNatsCredStore()))
	inst := restoredInstance()
	if err := h.vault.Put("projects/"+inst.ProjectID+"/credentials/"+roleWatcher,
		map[string]string{"password": "watcher-pw"}); err != nil {
		t.Fatalf("seed watcher credentials: %v", err)
	}

	if err := h.svc.RestartReplication(context.Background(), inst); err != nil {
		t.Fatalf("RestartReplication: %v", err)
	}
	if !strings.Contains(strings.Join(h.kube.Calls, ","), "InstallHelmChart:"+inst.Namespace+"/excalibase-watcher") {
		t.Errorf("the watcher chart must be reinstalled, calls=%v", h.kube.Calls)
	}
}

func TestRestartReplicationFailsWithoutWatcherCredentials(t *testing.T) {
	h := newRegistrationHarness(t)
	h.svc.factory = provisioner.NewFactory(provisioner.NewPostgreSQLProvisioner(h.kube, "/charts/excalibase-watcher"))
	h.svc.SetNatsCredentialMinter(NewNatsCredentialMinter(newFakeNatsCredStore()))

	if err := h.svc.RestartReplication(context.Background(), restoredInstance()); err == nil {
		t.Fatal("a watcher cannot be started without its replication role")
	}
}

// A project on a tier without backups (FREE: backupEnabled false, or a row
// that never had it set) has no backup to take. Triggering one anyway files
// a Backup CR that fails asynchronously, long after the pause believed it
// succeeded — so the question has to be asked per project, not only of the
// platform's object store (EXC-363).
func TestBackupsConfiguredIsFalseForAProjectWithBackupsOff(t *testing.T) {
	svc, store := backupSupportService(t, StaticBackupStorage(r2Storage()))

	for name, enabled := range map[string]*bool{
		"never set":      nil,
		"explicitly off": boolPtr(false),
	} {
		t.Run(name, func(t *testing.T) {
			inst, err := store.FindByProjectID("bk-p")
			if err != nil || inst == nil {
				t.Fatalf("load project: %v", err)
			}
			inst.BackupEnabled = enabled
			if err := store.Update(inst); err != nil {
				t.Fatalf("update project: %v", err)
			}

			got, err := svc.BackupsConfigured("bk-p")
			if err != nil {
				t.Fatalf("BackupsConfigured: %v", err)
			}
			if got {
				t.Error("a project with backups off has nothing to back up, however the platform store is wired")
			}
		})
	}
}

func TestBackupsConfiguredIsTrueOnlyWhenBothTheProjectAndTheStoreAgree(t *testing.T) {
	svc, store := backupSupportService(t, StaticBackupStorage(r2Storage()))
	inst, _ := store.FindByProjectID("bk-p")
	inst.BackupEnabled = boolPtr(true)
	if err := store.Update(inst); err != nil {
		t.Fatalf("update project: %v", err)
	}

	got, err := svc.BackupsConfigured("bk-p")
	if err != nil || !got {
		t.Errorf("an enabled project with an object store must report configured: %v %v", got, err)
	}

	bare, bareStore := backupSupportService(t, nil)
	enabled, _ := bareStore.FindByProjectID("bk-p")
	enabled.BackupEnabled = boolPtr(true)
	if err := bareStore.Update(enabled); err != nil {
		t.Fatalf("update project: %v", err)
	}
	got, err = bare.BackupsConfigured("bk-p")
	if err != nil || got {
		t.Errorf("an enabled project with nowhere to write must report unconfigured: %v %v", got, err)
	}
}

// A FREE project pauses without a pre-pause backup rather than filing one
// that will fail after the project is already down.
func TestPauseOfAFreeProjectTakesNoBackup(t *testing.T) {
	f := newObservedPause(t)
	inst := f.reload(t)
	inst.Tier = domain.Free
	inst.BackupEnabled = boolPtr(false)
	if err := f.store.Update(inst); err != nil {
		t.Fatalf("update project: %v", err)
	}
	f.svc.backups = &tierAwareBackups{store: f.store, log: &f.pauser.log}

	if err := f.pause(t); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	for _, call := range f.pauser.log {
		if call == "backup" {
			t.Fatal("a FREE project must not file a pre-pause backup")
		}
	}
	if got := f.reload(t).Status; got != string(domain.StatusPaused) {
		t.Errorf("status: got %s, want PAUSED", got)
	}
}

// tierAwareBackups answers BackupsConfigured from the project row, the way
// BackupService does.
type tierAwareBackups struct {
	store storage.InstanceStore
	log   *[]string
}

func (b *tierAwareBackups) BackupsConfigured(projectID string) (bool, error) {
	inst, err := b.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return false, errors.New("project not found")
	}
	return inst.BackupEnabled != nil && *inst.BackupEnabled, nil
}

func (b *tierAwareBackups) TriggerManualBackup(context.Context, string) (map[string]interface{}, error) {
	*b.log = append(*b.log, "backup")
	return map[string]interface{}{"id": "bk-1"}, nil
}

func (b *tierAwareBackups) LatestBackupID(context.Context, string) (string, error) {
	return "bk-1", nil
}

func (b *tierAwareBackups) BackupStatus(context.Context, string, string) (string, error) {
	return backupStatusCompleted, nil
}

func TestResumeReportsAWorkloadStateItCannotRead(t *testing.T) {
	f := newObservedPause(t)
	f.pauser.pauseErr = errors.New("hibernate refused")
	if err := f.pause(t); err == nil {
		t.Fatal("pause must fail")
	}
	f.pauser.workloadErr = errors.New("apiserver unreachable")

	if err := f.svc.Resume(context.Background(), observedPauseProject); err == nil {
		t.Fatal("a resume that cannot see the workload must not guess")
	}
}

func TestPauseRefusesAProjectBeingDeletedOutright(t *testing.T) {
	f := newObservedPause(t)
	if _, err := f.store.BeginDeletion(observedPauseProject, nil); err != nil {
		t.Fatalf("BeginDeletion: %v", err)
	}

	if err := f.pause(t); !errors.Is(err, storage.ErrProjectDeleting) {
		t.Fatalf("err: got %v, want ErrProjectDeleting", err)
	}
}

func TestPauseLeavesStatesItDoesNotOwn(t *testing.T) {
	f := newObservedPause(t)
	inst := f.reload(t)
	inst.Status = string(domain.StatusResuming)
	if err := f.store.Update(inst); err != nil {
		t.Fatalf("update: %v", err)
	}

	if err := f.pause(t); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if len(f.pauser.log) != 0 {
		t.Errorf("a resuming project is not a pause's to touch: %v", f.pauser.log)
	}
}

func TestBackupSupportRefusesAModeWithNoAdapter(t *testing.T) {
	svc, store := backupSupportService(t, StaticBackupStorage(r2Storage()))
	inst, _ := store.FindByProjectID("bk-p")
	inst.DeploymentMode = domain.DeploymentMode("operator")
	inst.BackupEnabled = boolPtr(true)
	if err := store.Update(inst); err != nil {
		t.Fatalf("update: %v", err)
	}

	if _, err := svc.BackupsConfigured("bk-p"); !errors.Is(err, ErrUnsupportedBackupMode) {
		t.Errorf("BackupsConfigured: got %v, want ErrUnsupportedBackupMode", err)
	}
	if _, err := svc.BackupStatus(context.Background(), "bk-p", "bk-1"); !errors.Is(err, ErrUnsupportedBackupMode) {
		t.Errorf("BackupStatus: got %v, want ErrUnsupportedBackupMode", err)
	}
}

// main gates pause/resume on a RESTORING project centrally (409 through the
// project-access middleware). The service must agree: a project whose
// restore has not been confirmed is neither pausable nor resumable, so an
// internal caller cannot do what the HTTP surface refuses.
func TestLifecycleLeavesANotServableProjectAlone(t *testing.T) {
	for name, status := range map[string]string{
		"restoring": string(domain.StatusRestoring),
		"deleting":  string(domain.StatusDeleting),
	} {
		t.Run(name, func(t *testing.T) {
			f := newObservedPause(t)
			inst := f.reload(t)
			inst.Status = status
			if err := f.store.Update(inst); err != nil {
				t.Fatalf("set %s: %v", status, err)
			}
			f.pauser.log = nil

			pauseErr := f.pause(t)
			if status == string(domain.StatusDeleting) && !errors.Is(pauseErr, storage.ErrProjectDeleting) {
				t.Errorf("pause of a deleting project: got %v, want ErrProjectDeleting", pauseErr)
			}
			if status == string(domain.StatusRestoring) && pauseErr != nil {
				t.Errorf("pause of a restoring project must be a no-op, got %v", pauseErr)
			}
			if err := f.svc.Resume(context.Background(), observedPauseProject); err != nil {
				t.Errorf("resume must be a no-op, got %v", err)
			}
			if len(f.pauser.log) != 0 {
				t.Errorf("a project the platform may not serve must not be touched: %v", f.pauser.log)
			}
		})
	}
}

func TestPausableAndResumableAgreeWithTheServableRule(t *testing.T) {
	for _, status := range []string{
		string(domain.StatusRestoring), string(domain.StatusDeleting), string(domain.StatusBackupsPendingDelete),
	} {
		if pausable(status) {
			t.Errorf("%s must not be pausable: the platform may not serve it", status)
		}
		if !domain.IsNotServable(status) {
			t.Errorf("%s should be covered by IsNotServable", status)
		}
	}
	if !pausable("ACTIVE") || !pausable(string(domain.StatusPausing)) {
		t.Error("ACTIVE and PAUSING are the pausable states")
	}
}

// The retry backoff is cleared once the project settles, so the next time it
// needs pausing it does not inherit a backoff grown by an old failure.
func TestASettledProjectForgetsItsPauseBackoff(t *testing.T) {
	for name, settle := range map[string]func(*observedPauseFixture) error{
		"pause succeeds": func(f *observedPauseFixture) error {
			return f.svc.Pause(context.Background(), observedPauseProject, domain.PauseReasonManual)
		},
		"resume succeeds": func(f *observedPauseFixture) error {
			if err := f.svc.Pause(context.Background(), observedPauseProject, domain.PauseReasonManual); err != nil {
				return err
			}
			return f.svc.Resume(context.Background(), observedPauseProject)
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newObservedPause(t)
			inst := f.reload(t)
			inst.PauseAttempts = 4
			inst.PauseLastAttemptAt = &domain.FlexTime{Time: time.Unix(1, 0)}
			if err := f.store.Update(inst); err != nil {
				t.Fatalf("seed backoff: %v", err)
			}

			if err := settle(f); err != nil {
				t.Fatalf("settle: %v", err)
			}
			got := f.reload(t)
			if got.PauseAttempts != 0 || got.PauseLastAttemptAt != nil {
				t.Errorf("backoff not cleared: attempts=%d last=%v", got.PauseAttempts, got.PauseLastAttemptAt)
			}
		})
	}
}

// Counting an attempt must never touch a project the platform may not serve.
func TestRecordPauseAttemptRespectsTheOneWayDoor(t *testing.T) {
	f := newObservedPause(t)
	inst := f.reload(t)
	inst.Status = string(domain.StatusRestoring)
	if err := f.store.Update(inst); err != nil {
		t.Fatalf("set RESTORING: %v", err)
	}

	if _, err := f.store.RecordPauseAttempt(observedPauseProject, time.Now()); !errors.Is(err, storage.ErrProjectNotPausable) {
		t.Fatalf("err: got %v, want ErrProjectNotPausable", err)
	}
	if _, err := f.store.RecordPauseAttempt("missing", time.Now()); !errors.Is(err, storage.ErrProjectNotFound) {
		t.Errorf("missing project: got %v, want ErrProjectNotFound", err)
	}
}

func TestLatestBackupIDNamesTheNewest(t *testing.T) {
	svc, store := backupSupportService(t, StaticBackupStorage(r2Storage()))

	if got, err := svc.LatestBackupID(context.Background(), "bk-p"); err != nil || got != "" {
		t.Errorf("a project with no backups has none: got %q %v", got, err)
	}
	first, err := svc.TriggerManualBackup(context.Background(), "bk-p")
	if err != nil {
		t.Fatalf("TriggerManualBackup: %v", err)
	}
	got, err := svc.LatestBackupID(context.Background(), "bk-p")
	if err != nil || got != first["id"] {
		t.Errorf("latest: got %q %v, want %v", got, err, first["id"])
	}

	if _, err := svc.LatestBackupID(context.Background(), "nope"); err == nil {
		t.Error("an unknown project must be reported, not answered")
	}
	inst, _ := store.FindByProjectID("bk-p")
	inst.DeploymentMode = domain.DeploymentMode("operator")
	if err := store.Update(inst); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := svc.LatestBackupID(context.Background(), "bk-p"); !errors.Is(err, ErrUnsupportedBackupMode) {
		t.Errorf("unsupported mode: got %v", err)
	}
}

func TestEpisodeBackupReportsALatestItCannotRead(t *testing.T) {
	f := newObservedPause(t)
	inst := f.reload(t)
	inst.Status = string(domain.StatusPausing)
	inst.PauseBackupID = "bk-1"
	inst.PauseBackupAt = &domain.FlexTime{Time: time.Now()}
	if err := f.store.Update(inst); err != nil {
		t.Fatalf("seed episode: %v", err)
	}
	f.svc.backups = &latestlessBackups{}

	if err := f.pause(t); !errors.Is(err, ErrPauseBackupNotCompleted) {
		t.Fatalf("err: got %v, want ErrPauseBackupNotCompleted", err)
	}
}

// latestlessBackups cannot say which backup is newest.
type latestlessBackups struct{}

func (latestlessBackups) BackupsConfigured(string) (bool, error) { return true, nil }

func (latestlessBackups) TriggerManualBackup(context.Context, string) (map[string]interface{}, error) {
	return map[string]interface{}{"id": "bk-2"}, nil
}

func (latestlessBackups) BackupStatus(context.Context, string, string) (string, error) {
	return backupStatusCompleted, nil
}

func (latestlessBackups) LatestBackupID(context.Context, string) (string, error) {
	return "", errors.New("platform db unavailable")
}

// The sweep must report, not swallow, a retry it could not count or run.
func TestTheSweepReportsAResumeRetryItCannotRun(t *testing.T) {
	f := newIdleFixture(t)
	f.project(t, "stuck-resume", domain.Free, 8*day)
	inst, _ := f.instances.FindByProjectID("stuck-resume")
	inst.Status = string(domain.StatusResuming)
	if err := f.instances.Update(inst); err != nil {
		t.Fatalf("stick in RESUMING: %v", err)
	}
	f.resumer.err = errors.New("cluster still not ready")

	report := f.run(t)
	if len(report.Failed) != 1 || report.Failed[0] != "stuck-resume" {
		t.Errorf("a failed resume retry must be reported: %+v", report)
	}
}

func TestTheSweepSkipsAResumeWhenNoResumerIsWired(t *testing.T) {
	f := newIdleFixture(t)
	f.project(t, "stuck-resume", domain.Free, 8*day)
	inst, _ := f.instances.FindByProjectID("stuck-resume")
	inst.Status = string(domain.StatusResuming)
	if err := f.instances.Update(inst); err != nil {
		t.Fatalf("stick in RESUMING: %v", err)
	}
	f.scheduler.resumer = nil

	f.run(t)
	if len(f.resumer.calls) != 0 {
		t.Errorf("without a resumer nothing may be retried: %v", f.resumer.calls)
	}
}
