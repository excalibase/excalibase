package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// deletingRow drives a project into DELETING by failing its namespace delete,
// and hands back the snapshot a concurrent flow would have read beforehand.
func deletingRow(t *testing.T) (*ProvisioningService, *storage.FileSystemStore, *domain.DatabaseInstance) {
	t.Helper()
	svc, store, mock := setupDeletionTest(t)
	stale, _ := store.FindByProjectID(testDeletingProj)
	if stale == nil {
		t.Fatal("seed row missing")
	}
	mock.DeleteNamespaceError = errors.New("forbidden")
	if err := svc.Deprovision(context.Background(), testDeletingProj); err == nil {
		t.Fatal(testWantErrNil)
	}
	return svc, store, stale
}

// A flow that read the row before the teardown started must not be able to
// write it back to a usable status, nor wipe how far the teardown got.
func TestStaleUpdateCannotRevertADeletingProject(t *testing.T) {
	_, store, stale := deletingRow(t)

	stale.Status = "ACTIVE"
	stale.DeletionStep = ""
	stale.DeletionError = ""
	if err := store.Update(stale); !errors.Is(err, storage.ErrProjectDeleting) {
		t.Fatalf("Update on a deleting row = %v, want ErrProjectDeleting", err)
	}

	got, _ := store.FindByProjectID(testDeletingProj)
	if got.Status != string(domain.StatusDeleting) {
		t.Errorf("status = %q, want %q", got.Status, domain.StatusDeleting)
	}
	if got.DeletionStep != domain.DeletionStepDeleteResources || got.DeletionError == "" {
		t.Errorf("deletion fields wiped: step=%q error=%q", got.DeletionStep, got.DeletionError)
	}
}

// The interleaving the review described, ordered deterministically: Resume
// reads PAUSED, the DELETE commits, Resume finishes. Resume must report a
// conflict and leave the row deleting.
func TestResumeLosesToAnInFlightDeletion(t *testing.T) {
	svc, store, mock := setupDeletionTest(t)
	inst, _ := store.FindByProjectID(testDeletingProj)
	inst.Status = string(domain.StatusPaused)
	inst.DeploymentMode = domain.ModeK8s
	if err := store.Update(inst); err != nil {
		t.Fatal(err)
	}

	pauseSvc := NewPauseService(PauseServiceConfig{
		Instances: store,
		Pausers: map[domain.DeploymentMode]provisioner.Pauser{
			domain.ModeK8s: &orderedPauser{before: func() {
				mock.DeleteNamespaceError = errors.New("forbidden")
				if err := svc.Deprovision(context.Background(), testDeletingProj); err == nil {
					t.Error(testWantErrNil)
				}
			}},
		},
	})

	err := pauseSvc.Resume(context.Background(), testDeletingProj)
	if !errors.Is(err, storage.ErrProjectDeleting) {
		t.Fatalf("Resume = %v, want ErrProjectDeleting", err)
	}
	got, _ := store.FindByProjectID(testDeletingProj)
	if got == nil || got.Status != string(domain.StatusDeleting) {
		t.Fatalf("status = %v, want DELETING", got)
	}
	if got.DeletionError == "" {
		t.Error("deletion error wiped by the losing resume")
	}
}

// orderedPauser runs `before` inside the resume, between the status write and
// the final one — the window the review's interleaving needs.
type orderedPauser struct{ before func() }

func (p *orderedPauser) WorkloadStopped(context.Context, string, string) (bool, error) {
	return true, nil
}

func (p *orderedPauser) StopReplication(context.Context, string, string) error { return nil }

func (p *orderedPauser) Pause(context.Context, string, string) error { return nil }
func (p *orderedPauser) Resume(context.Context, string, string) error {
	p.before()
	return nil
}

// Only one teardown may run at a time: the second DELETE arriving while the
// first is still working is refused, so a retry behind a proxy timeout can
// never run a second teardown over the first.
func TestConcurrentDeletionIsRefusedWhileOneIsRunning(t *testing.T) {
	svc, store, mock := setupDeletionTest(t)
	var second error
	nested := false

	// The nested DELETE is issued from inside the first teardown's resource
	// step, so the two genuinely overlap.
	inner := provisioner.NewPostgreSQLProvisioner(mock, "")
	inner.SetDeletionPoller(fastPoller(2))
	svc.factory = provisioner.NewFactory(&reentrantProvisioner{
		PostgreSQLProvisioner: inner,
		onDeprovision: func() {
			if nested {
				return
			}
			nested = true
			second = svc.Deprovision(context.Background(), testDeletingProj)
		},
	})

	if err := svc.Deprovision(context.Background(), testDeletingProj); err != nil {
		t.Fatalf("first teardown: %v", err)
	}
	if !errors.Is(second, ErrDeletionInProgress) {
		t.Fatalf("second DELETE = %v, want ErrDeletionInProgress", second)
	}
	_ = store
}

// reentrantProvisioner runs a callback in the middle of the teardown so a
// test can issue a second DELETE while the first still holds its claim.
type reentrantProvisioner struct {
	*provisioner.PostgreSQLProvisioner
	onDeprovision func()
}

func (p *reentrantProvisioner) Deprovision(ctx context.Context, namespace, projectID string) error {
	p.onDeprovision()
	return p.PostgreSQLProvisioner.Deprovision(ctx, namespace, projectID)
}

// Pause loses the same way Resume does: a teardown that claimed the project
// between the read and the write owns it, and stopping its workload here
// would race the teardown over resources it is already removing.
func TestPauseLosesToAnInFlightDeletion(t *testing.T) {
	svc, store, mock := setupDeletionTest(t)
	inst, _ := store.FindByProjectID(testDeletingProj)
	inst.DeploymentMode = domain.ModeK8s
	if err := store.Update(inst); err != nil {
		t.Fatal(err)
	}
	mock.DeleteNamespaceError = errors.New("forbidden")
	if err := svc.Deprovision(context.Background(), testDeletingProj); err == nil {
		t.Fatal(testWantErrNil)
	}

	pauseSvc := NewPauseService(PauseServiceConfig{
		Instances: store,
		Pausers:   map[domain.DeploymentMode]provisioner.Pauser{domain.ModeK8s: &orderedPauser{before: func() {}}},
	})
	// The row already reads DELETING, which is not PAUSED — Resume no-ops —
	// so drive Pause, whose first write is refused by the door.
	if err := pauseSvc.Pause(context.Background(), testDeletingProj, "manual"); !errors.Is(err, storage.ErrProjectDeleting) {
		t.Fatalf("Pause = %v, want ErrProjectDeleting", err)
	}
	got, _ := store.FindByProjectID(testDeletingProj)
	if got.Status != string(domain.StatusDeleting) {
		t.Errorf("status = %q, want DELETING", got.Status)
	}
}

// A claim that cannot be taken at all is reported, never assumed granted.
func TestDeprovisionReportsAFailedClaim(t *testing.T) {
	svc, store, _ := setupDeletionTest(t)
	svc.SetDeletionClaimer(failingClaimer{err: errors.New("database unreachable")})

	if err := svc.Deprovision(context.Background(), testDeletingProj); err == nil {
		t.Fatal(testWantErrNil)
	}
	if inst, _ := store.FindByProjectID(testDeletingProj); inst == nil || inst.Status != "ACTIVE" {
		t.Error("nothing may be claimed or torn down when the claim failed")
	}
}

type failingClaimer struct{ err error }

func (c failingClaimer) Claim(context.Context, string) (func(), bool, error) {
	return nil, false, c.err
}

// A confirmed purge with no purger wired stops the teardown at the backup
// step instead of removing the record and stranding the objects.
func TestDeprovisionStopsWhenAConfirmedPurgeHasNoPurger(t *testing.T) {
	svc, store, _ := setupDeletionTest(t)
	if _, err := store.BeginDeletion(testDeletingProj, DeleteBackupsOption(true)); err != nil {
		t.Fatal(err)
	}

	if err := svc.Deprovision(context.Background(), testDeletingProj); !errors.Is(err, ErrBackupPurgeNotConfigured) {
		t.Fatalf("err = %v, want ErrBackupPurgeNotConfigured", err)
	}
	// The project's resources are gone; the purge it was promised is not,
	// which is exactly the marker POST /backups/purge retries from.
	got, _ := store.FindByProjectID(testDeletingProj)
	if got == nil || got.Status != string(domain.StatusBackupsPendingDelete) {
		t.Fatalf("row = %v, want BACKUPS_PENDING_DELETE", got)
	}
	if got.DeletionStep != domain.DeletionStepDeleteBackups || got.DeletionError == "" {
		t.Errorf("step/error = %q/%q", got.DeletionStep, got.DeletionError)
	}
}

// The retained-backup prefix is the real object layout, never a guess: the
// k8s layout is derivable, while docker's depends on the key prefix the
// purger was wired with, so without one no prefix is reported.
func TestRetainedBackupPrefixFollowsTheDeploymentLayout(t *testing.T) {
	svc, store, _ := setupDeletionTest(t)
	inst, _ := store.FindByProjectID(testDeletingProj)

	inst.DeploymentMode = domain.ModeK8s
	if prefix, ok := svc.RetainedBackupPrefix(inst); !ok || !strings.Contains(prefix, testDeletingProj) {
		t.Errorf("k8s prefix = %q, %v; want the project's prefix", prefix, ok)
	}

	inst.DeploymentMode = domain.ModeDocker
	if prefix, ok := svc.RetainedBackupPrefix(inst); ok {
		t.Errorf("docker without a purger reported %q; the key prefix is unknown", prefix)
	}
}

// The other ordering: the teardown claims the project between Resume's read
// and its first write. Resume must stop there — past that point it would
// start a workload back up inside a namespace being removed.
func TestResumeStopsWhenTheClaimLandsBeforeItsFirstWrite(t *testing.T) {
	svc, store, mock := setupDeletionTest(t)
	inst, _ := store.FindByProjectID(testDeletingProj)
	inst.Status = string(domain.StatusPaused)
	inst.DeploymentMode = domain.ModeK8s
	if err := store.Update(inst); err != nil {
		t.Fatal(err)
	}

	resumed := false
	hooked := &hookStore{InstanceStore: store, beforeUpdate: func() {
		mock.DeleteNamespaceError = errors.New("forbidden")
		if err := svc.Deprovision(context.Background(), testDeletingProj); err == nil {
			t.Error(testWantErrNil)
		}
	}}
	pauseSvc := NewPauseService(PauseServiceConfig{
		Instances: hooked,
		Pausers: map[domain.DeploymentMode]provisioner.Pauser{
			domain.ModeK8s: &orderedPauser{before: func() { resumed = true }},
		},
	})

	if err := pauseSvc.Resume(context.Background(), testDeletingProj); !errors.Is(err, storage.ErrProjectDeleting) {
		t.Fatalf("Resume = %v, want ErrProjectDeleting", err)
	}
	if resumed {
		t.Error("the workload must not be started for a project a teardown owns")
	}
}

// hookStore runs a callback before the first Update, so a test can land a
// competing write in the window a caller holds a stale read.
type hookStore struct {
	storage.InstanceStore
	beforeUpdate func()
	fired        bool
}

func (s *hookStore) Update(inst *domain.DatabaseInstance) error {
	if !s.fired && s.beforeUpdate != nil {
		s.fired = true
		s.beforeUpdate()
	}
	return s.InstanceStore.Update(inst)
}
