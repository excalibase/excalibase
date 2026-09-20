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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestBackupServiceFansTheProbeAndTimeoutToEveryAdapter(t *testing.T) {
	mock := k8s.NewMockClient()
	dir := t.TempDir()
	svc := NewBackupService(emptyInstanceStore(t), mock, dir, StaticBackupStorage(r2Storage()))
	dockerAdapter := NewDockerBackupAdapter(DockerBackupAdapterConfig{Bucket: "b"})
	svc.RegisterAdapter(domain.ModeDocker, dockerAdapter)

	if err := svc.SetDatabaseProbe(alwaysAnswers{}); err != nil {
		t.Fatalf("SetDatabaseProbe: %v", err)
	}
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

// The periodic sweeper only acts while this replica is the leader — one
// replica failing other replicas' jobs is enough.
func TestSweeperOnlyRunsForTheLeader(t *testing.T) {
	jobs := newFakeJobs()
	clock := &movableClock{t: time.Unix(3_000_000, 0)}
	jobs.clock = clock.now
	ctx := context.Background()
	if err := jobs.UpsertRestoreJob(ctx, &domain.RestoreJob{
		ID: "orphan", SourceProjectID: "s", NewProjectID: "d",
		Status: domain.RestoreStatusRunning, Owner: "dead",
	}); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	clock.advance(2 * defaultRestoreHeartbeatStale)
	orch := ownedOrchestrator(jobs, "me", clock)

	orch.sweepIfLeader(ctx, followerLeadership{})
	if jobs.status("orphan") != domain.RestoreStatusRunning {
		t.Error("a follower must not sweep")
	}

	orch.sweepIfLeader(ctx, leaderLeadership{})
	if jobs.status("orphan") != domain.RestoreStatusFailed {
		t.Error("the leader must sweep")
	}
}

func TestSweeperStandsDownWhenLeadershipCannotBeChecked(t *testing.T) {
	jobs := newFakeJobs()
	orch := ownedOrchestrator(jobs, "me", &movableClock{t: time.Unix(3_000_000, 0)})
	// No panic, no sweep: an unknown leadership state is not a licence to
	// fail other replicas' jobs.
	orch.sweepIfLeader(context.Background(), brokenLeadership{})
}

type followerLeadership struct{}

func (followerLeadership) IsLeader(context.Context) (bool, error) { return false, nil }

type leaderLeadership struct{}

func (leaderLeadership) IsLeader(context.Context) (bool, error) { return true, nil }

type brokenLeadership struct{}

func (brokenLeadership) IsLeader(context.Context) (bool, error) {
	return false, errors.New("advisory lock unavailable")
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

func TestSetDatabaseProbeRefusesAnUnverifiableWiring(t *testing.T) {
	store := emptyInstanceStore(t)
	svc := NewBackupService(store, k8s.NewMockClient(), t.TempDir(), StaticBackupStorage(r2Storage()))

	if err := svc.SetDatabaseProbe(nil); !errors.Is(err, ErrDatabaseProbeNotConfigured) {
		t.Errorf("nil probe: got %v, want ErrDatabaseProbeNotConfigured", err)
	}

	bare := NewBackupServiceWithAdapters(store, map[domain.DeploymentMode]BackupAdapter{}, t.TempDir())
	if err := bare.SetDatabaseProbe(alwaysAnswers{}); !errors.Is(err, ErrDatabaseProbeNotConfigured) {
		t.Errorf("no adapters: got %v, want ErrDatabaseProbeNotConfigured", err)
	}

	legacy := NewBackupServiceWithAdapters(store, map[domain.DeploymentMode]BackupAdapter{
		domain.DeploymentMode("legacy"): &fakeAdapter{},
	}, t.TempDir())
	err := legacy.SetDatabaseProbe(alwaysAnswers{})
	if !errors.Is(err, ErrDatabaseProbeNotConfigured) {
		t.Fatalf("adapter without a probe setter: got %v", err)
	}
	if !strings.Contains(err.Error(), "legacy") {
		t.Errorf("the error must name the adapter, got %v", err)
	}
}

func TestRegisterVerifiedProjectRefusesWithoutAProbe(t *testing.T) {
	pc := provisioner.NewProvisionContext(nil, nil)
	err := registerVerifiedProject(context.Background(), pc, &fakeRegistrar{}, emptyInstanceStore(t), nil,
		&domain.DatabaseInstance{ProjectID: "p"}, RegistrationOptions{})
	if !errors.Is(err, ErrDatabaseProbeNotConfigured) {
		t.Fatalf("err: got %v, want ErrDatabaseProbeNotConfigured", err)
	}
}

// A project that vanishes between registration and the activating write is a
// real error, not a deletion — it must be reported, not silently swallowed.
func TestRestoreReportsAnActivationWriteItCannotMake(t *testing.T) {
	store := emptyInstanceStore(t)
	pc := provisioner.NewProvisionContext(nil, nil)
	reg := &vanishingRegistrar{store: store}
	inst := &domain.DatabaseInstance{ProjectID: "gone", OrgID: "org"}

	err := registerVerifiedProject(context.Background(), pc, reg, store, alwaysAnswers{}, inst, RegistrationOptions{})
	if err == nil || errors.Is(err, ErrRestoreTargetDeleting) {
		t.Fatalf("err: got %v, want the activation failure reported", err)
	}
}

// vanishingRegistrar registers the project and then removes the row, so the
// caller's activating update has nothing to write.
type vanishingRegistrar struct{ store storage.InstanceStore }

func (r *vanishingRegistrar) RegisterProject(_ context.Context, inst *domain.DatabaseInstance, opts RegistrationOptions) error {
	markProjectRestoring(inst)
	if err := r.store.Create(inst); err != nil {
		return err
	}
	return r.store.Delete(inst.ProjectID)
}
