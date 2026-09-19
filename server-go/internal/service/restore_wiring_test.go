package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestBackupServiceFansTheProbeAndTimeoutToEveryAdapter(t *testing.T) {
	mock := k8s.NewMockClient()
	dir := t.TempDir()
	svc := NewBackupService(emptyInstanceStore(t), mock, dir, StaticBackupStorage(r2Storage()))
	dockerAdapter := NewDockerBackupAdapter(DockerBackupAdapterConfig{Bucket: "b"})
	svc.RegisterAdapter(domain.ModeDocker, dockerAdapter)

	svc.SetDatabaseProbe(alwaysAnswers{})
	svc.SetRestoreReadyTimeout(3 * time.Minute)

	k8sAdapter := svc.adapters[domain.ModeK8s].(*K8sBackupAdapter)
	if k8sAdapter.probe == nil || k8sAdapter.poller.Timeout != 3*time.Minute {
		t.Errorf("k8s adapter not wired: probe=%v timeout=%v", k8sAdapter.probe, k8sAdapter.poller.Timeout)
	}
	if dockerAdapter.probe == nil || dockerAdapter.restoreTimeout() != 3*time.Minute {
		t.Errorf("docker adapter not wired: probe=%v timeout=%v", dockerAdapter.probe, dockerAdapter.restoreTimeout())
	}
}

func TestDockerRestoreTimeoutFallsBackToThePackageDefault(t *testing.T) {
	adapter := NewDockerBackupAdapter(DockerBackupAdapterConfig{Bucket: "b"})
	if got := adapter.restoreTimeout(); got != defaultRestoreReadyTimeout {
		t.Errorf("got %v, want the package default", got)
	}
}

func TestRestoreRefusesWithoutADatabaseProbe(t *testing.T) {
	t.Run("k8s", func(t *testing.T) {
		mock := k8s.NewMockClient()
		adapter := NewK8sBackupAdapter(mock, t.TempDir(), StaticBackupStorage(r2Storage()))
		adapter.SetInstanceStore(emptyInstanceStore(t))
		adapter.SetProjectRegistrar(&fakeRegistrar{})

		_, err := adapter.Restore(context.Background(), sourceInstance(),
			domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"})
		if !errors.Is(err, ErrDatabaseProbeNotConfigured) {
			t.Fatalf("err: got %v, want ErrDatabaseProbeNotConfigured", err)
		}
		if len(mock.Namespaces) != 0 {
			t.Error("nothing may be created for a restore that could never be verified")
		}
	})

	t.Run("docker", func(t *testing.T) {
		adapter, store, _, _, _ := setupDockerAdapter(t)
		adapter.SetDatabaseProbe(nil)
		adapter.SetInstanceStore(store)
		adapter.SetDockerClient(&fakeDockerClientForAdapter{})
		adapter.SetProjectRegistrar(&fakeRegistrar{store: store})
		src, _ := store.FindByProjectID("dk-1")

		_, err := adapter.Restore(context.Background(), src,
			domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"})
		if !errors.Is(err, ErrDatabaseProbeNotConfigured) {
			t.Fatalf("err: got %v, want ErrDatabaseProbeNotConfigured", err)
		}
	})
}

func TestK8sRestoreStopsWhenTheNamespaceCannotBeCreated(t *testing.T) {
	f := newObservedRestore(t)
	f.mock.NamespaceError = errors.New("quota exceeded")

	if _, err := f.restore(t); !errors.Is(err, ErrRestoreNotObserved) {
		t.Fatalf("err: got %v, want ErrRestoreNotObserved", err)
	}
	if len(f.mock.CRDs) != 0 {
		t.Error("no cluster may be applied into a namespace that does not exist")
	}
}

func TestK8sRestoreKeepsWaitingWhenTheClusterCannotBeRead(t *testing.T) {
	f := newObservedRestore(t)
	// The Cluster disappears from the apiserver's view between polls, then
	// comes back reconciled. A read failure is not proof of failure.
	f.clock.tick = func(round int) {
		switch round {
		case 1:
			delete(f.mock.CRDs, restoreTargetNS+"/"+restoreTargetCluster)
		case 2:
			f.mock.CRDs[restoreTargetNS+"/"+restoreTargetCluster] = restoreClusterObject()
			f.reconcile()
		}
	}

	if _, err := f.restore(t); err != nil {
		t.Fatalf("Restore: %v", err)
	}
}

func TestK8sRestoreKeepsWaitingWhenPodReadinessCannotBeRead(t *testing.T) {
	f := newObservedRestore(t)
	f.mock.PodReadyError = errors.New("apiserver unreachable")
	f.clock.tick = func(round int) {
		if round == 1 {
			f.reconcile()
		}
		if round == 3 {
			f.mock.PodReadyError = nil
		}
	}

	if _, err := f.restore(t); err != nil {
		t.Fatalf("Restore: %v", err)
	}
}

func TestDockerRestoreCompensatesWhenTheContainerNeverRuns(t *testing.T) {
	for name, failOn := range map[string]string{"start": "start", "health": "health"} {
		t.Run(name, func(t *testing.T) {
			h := newDockerRestoreHarness(t)
			docker := &fakeDockerClientForAdapter{failOn: failOn}
			h.adapter.SetDockerClient(docker)
			h.adapter.SetProjectRegistrar(&fakeRegistrar{store: h.instances})

			_, err := h.adapter.Restore(context.Background(), h.source,
				domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"})
			if !errors.Is(err, ErrRestoreNotObserved) {
				t.Fatalf("err: got %v, want ErrRestoreNotObserved", err)
			}
			if saved, _ := h.instances.FindByProjectID("dst"); saved != nil {
				t.Error("no project may survive a container that never served")
			}
		})
	}
}

func TestDockerRestoreGivesUpWhenRecoveryNeverFinishes(t *testing.T) {
	h := newDockerRestoreHarness(t)
	h.adapter.SetDockerClient(&fakeDockerClientForAdapter{execCode: 1})
	h.adapter.SetProjectRegistrar(&fakeRegistrar{store: h.instances})
	h.adapter.SetRestoreReadyTimeout(time.Nanosecond)

	_, err := h.adapter.Restore(context.Background(), h.source,
		domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"})
	if !errors.Is(err, ErrRestoreNotObserved) {
		t.Fatalf("err: got %v, want ErrRestoreNotObserved", err)
	}
}

func TestOrchestratorSweepLogsAJobItCannotMark(t *testing.T) {
	orch := NewRestoreOrchestrator(RestoreOrchestratorConfig{Jobs: unwritableJobStore{}})
	if err := orch.SweepStale(context.Background()); err != nil {
		t.Fatalf("a job that cannot be marked must not abort the sweep: %v", err)
	}
}

// unwritableJobStore lists an abandoned job but refuses to update it.
type unwritableJobStore struct{}

func (unwritableJobStore) UpsertRestoreJob(context.Context, *domain.RestoreJob) error {
	return errors.New("platform db unavailable")
}

func (unwritableJobStore) FindRestoreJob(context.Context, string, string) (*domain.RestoreJob, error) {
	return nil, ErrRestoreJobNotFound
}

func (unwritableJobStore) ListRunningRestoreJobs(context.Context) ([]domain.RestoreJob, error) {
	return []domain.RestoreJob{{ID: "j1", Status: domain.RestoreStatusRunning}}, nil
}

func TestRegisterProjectHoldsAnUnverifiedRestoreInRestoring(t *testing.T) {
	h := newRegistrationHarness(t)

	inst := restoredInstance()
	if err := h.svc.RegisterProject(context.Background(), inst, RegistrationOptions{
		ResetRolePasswords: true, Unverified: true,
	}); err != nil {
		t.Fatalf("RegisterProject: %v", err)
	}

	saved, err := h.store.FindByProjectID(testRegProject)
	if err != nil || saved == nil {
		t.Fatalf("project not registered: %v", err)
	}
	if saved.Status != string(domain.StatusRestoring) || saved.CurrentStage != domain.StatusRestoring {
		t.Errorf("an unverified restore must not look usable: status=%s stage=%s", saved.Status, saved.CurrentStage)
	}
}

// restoreClusterObject rebuilds the CRD the restore applied, for tests that
// take it away and put it back.
func restoreClusterObject() *unstructured.Unstructured {
	return k8s.BuildRestoreCluster(k8s.RestoreClusterOpts{
		SourceProjectID: "src",
		NewProjectID:    "dst",
		Namespace:       restoreTargetNS,
		Store:           k8s.ObjectStoreOpts{Bucket: "b"},
	})
}
