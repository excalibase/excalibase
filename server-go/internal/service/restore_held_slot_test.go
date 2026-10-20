package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// fullOrgCapacity answers as the platform store does once a running copy holds
// the organisation's last slot: no project slot left, disk within the plan.
type fullOrgCapacity struct{ diskErr error }

func (fullOrgCapacity) EnsureOrgCanTakeProject(context.Context, string) error {
	return &OrgProjectLimitError{Limit: 2, Tier: domain.Free}
}

func (c fullOrgCapacity) RequireRestoreDiskFits(context.Context, *domain.DatabaseInstance) error {
	return c.diskErr
}

func heldSlotService(t *testing.T, capacity OrgProjectCapacity) (*BackupService, *fakeAdapter) {
	t.Helper()
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(&domain.DatabaseInstance{ProjectID: "src", OrgID: "org", DeploymentMode: domain.ModeK8s, Status: "ACTIVE"}); err != nil {
		t.Fatal(err)
	}
	adapter := &fakeAdapter{}
	svc := NewBackupServiceWithAdapters(store, map[domain.DeploymentMode]BackupAdapter{domain.ModeK8s: adapter}, "")
	svc.SetOrgProjectCapacity(capacity)
	return svc, adapter
}

// EXC-639: the job took the slot when it was submitted, so the restore it runs
// must not count that slot against itself.
func TestASubmittedCopyIsNotRefusedByItsOwnSlot(t *testing.T) {
	svc, adapter := heldSlotService(t, fullOrgCapacity{})
	if err := svc.RestoreSubmittedCopy(context.Background(), "src", copyRequest()); err != nil {
		t.Fatalf("RestoreSubmittedCopy = %v, want the restore to run", err)
	}
	if adapter.restoreCalls != 1 {
		t.Fatalf("restore calls = %d, want 1", adapter.restoreCalls)
	}
}

func TestASubmittedCopyStillNeedsItsDiskToFit(t *testing.T) {
	svc, adapter := heldSlotService(t, fullOrgCapacity{diskErr: ErrRestoreDiskAbovePlan})
	if err := svc.RestoreSubmittedCopy(context.Background(), "src", copyRequest()); !errors.Is(err, ErrRestoreDiskAbovePlan) {
		t.Fatalf("RestoreSubmittedCopy = %v, want ErrRestoreDiskAbovePlan", err)
	}
	if adapter.restoreCalls != 0 {
		t.Fatal("a refused restore must not reach the adapter")
	}
}

// A restore with no job behind it holds no slot, so it is still counted.
func TestAnUnsubmittedCopyIsStillRefusedAtTheLimit(t *testing.T) {
	svc, adapter := heldSlotService(t, fullOrgCapacity{})
	var limit *OrgProjectLimitError
	if _, err := svc.RestoreFromBackup(context.Background(), "src", copyRequest()); !errors.As(err, &limit) {
		t.Fatalf("RestoreFromBackup = %v, want the project limit", err)
	}
	if adapter.restoreCalls != 0 {
		t.Fatal("a refused restore must not reach the adapter")
	}
}
