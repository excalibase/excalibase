package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storagebudget"
)

const gib = int64(1) << 30

// budgetOf is a budget of percent of capacity over the mock's recorded allocation.
func budgetOf(mock *k8s.MockClient, capacity int64, percent int) *storagebudget.Budget {
	return storagebudget.New(storagebudget.NewClusterSource(storagebudget.FixedCapacity(capacity), mock), percent)
}

// The seeded cluster holds 2Gi; a budget of 8Gi with 7Gi allocated has room for
// a 1Gi grow and not for a 3Gi one.
func TestResizeStorageIsHeldToThePlatformBudget(t *testing.T) {
	svc, store, mock := setupClusterChangeTest(t)
	mock.Storage = k8s.StorageAllocation{TenantBytes: 7 * gib}
	svc.SetStorageBudget(budgetOf(mock, 10*gib, 80))

	before := opsCluster(t, mock)
	err := svc.ResizeStorage(context.Background(), testOpsDB, "5Gi")
	if !errors.Is(err, storagebudget.ErrExceeded) || !strings.Contains(err.Error(), "3Gi") {
		t.Fatalf("err = %v, want the budget refusal naming the 3Gi growth", err)
	}
	if !equalUnstructured(before.Object, opsCluster(t, mock).Object) {
		t.Error("a refused grow changed the cluster")
	}
	if inst, _ := store.FindByProjectID(testOpsDB); inst.StorageSize != "2Gi" {
		t.Errorf("recorded disk = %s, want 2Gi kept", inst.StorageSize)
	}
	if err := svc.ResizeStorage(context.Background(), testOpsDB, "3Gi"); err != nil {
		t.Fatalf("a 1Gi grow within the budget: %v", err)
	}
}

func TestApplyOrgTierIsHeldToThePlatformBudget(t *testing.T) {
	svc, _, mock := setupClusterChangeTest(t)
	// FREE starts at 5Gi: the 2Gi disk grows by 3Gi.
	mock.Storage = k8s.StorageAllocation{TenantBytes: 7 * gib}
	svc.SetStorageBudget(budgetOf(mock, 10*gib, 80))
	before := opsCluster(t, mock)
	if err := svc.ApplyOrgTier(context.Background(), testOpsDB, domain.Free); !errors.Is(err, storagebudget.ErrExceeded) {
		t.Fatalf("err = %v, want the budget refusal", err)
	}
	if !equalUnstructured(before.Object, opsCluster(t, mock).Object) {
		t.Error("a refused tier change changed the cluster")
	}
}

// A new database asks for its instances times the plan's disk, and is refused
// before anything is created when that would pass the budget.
func TestAdmitDatabaseIsHeldToThePlatformBudget(t *testing.T) {
	svc, _, mock := setupClusterChangeTest(t)
	mock.Storage = k8s.StorageAllocation{TenantBytes: 76 * gib}
	svc.SetStorageBudget(budgetOf(mock, 100*gib, 80))
	err := svc.RequireStorageForDatabase(context.Background(), 1, "5Gi")
	if !errors.Is(err, storagebudget.ErrExceeded) || !strings.Contains(err.Error(), "1 x 5Gi") {
		t.Fatalf("err = %v, want the budget refusal for 1 x 5Gi", err)
	}
	if err := svc.RequireStorageForDatabase(context.Background(), 1, "4Gi"); err != nil {
		t.Fatalf("4Gi fits: %v", err)
	}
}

func TestRequireRestoreDiskFitsIsHeldToThePlatformBudget(t *testing.T) {
	svc, store, mock := setupClusterChangeTest(t)
	source, _ := store.FindByProjectID(testOpsDB)
	mock.Storage = k8s.StorageAllocation{TenantBytes: 78 * gib}
	svc.SetStorageBudget(budgetOf(mock, 100*gib, 80))
	if err := svc.RequireRestoreDiskFits(context.Background(), source); !errors.Is(err, storagebudget.ErrExceeded) {
		t.Fatalf("err = %v, want the budget refusal", err)
	}
}
