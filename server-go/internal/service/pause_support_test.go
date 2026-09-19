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

func (unresolvableBackups) BackupStatus(context.Context, string, string) (string, error) {
	return "", nil
}

// unreadableBackups starts a backup but cannot be asked how it is going.
type unreadableBackups struct{}

func (unreadableBackups) BackupsConfigured(string) (bool, error) { return true, nil }

func (unreadableBackups) TriggerManualBackup(context.Context, string) (map[string]interface{}, error) {
	return map[string]interface{}{"id": "bk-1"}, nil
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
