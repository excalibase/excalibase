package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// A restore sizes the new cluster from the disk the source recorded, so every
// project records one from the moment it is created (EXC-492).
func TestProvisionRecordsThePlansStartingDisk(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)
	setOrgTier(svc, "org-plan", domain.Standard)
	resp, err := provisionInto(t, svc, "org-plan", "p")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	inst, _ := store.FindByProjectID(resp.ProjectID)
	if inst.StorageSize != "50Gi" {
		t.Errorf("recorded disk = %q, want STANDARD's starting 50Gi", inst.StorageSize)
	}
}

func recordedDisk(t *testing.T, store *storage.FileSystemStore) string {
	t.Helper()
	inst, err := store.FindByProjectID(testOpsDB)
	if err != nil || inst == nil {
		t.Fatalf("read project: %v", err)
	}
	return inst.StorageSize
}

func TestResizeStorageRecordsTheGrownDisk(t *testing.T) {
	svc, store, _ := setupClusterChangeTest(t)
	if err := svc.ResizeStorage(context.Background(), testOpsDB, "4Gi"); err != nil {
		t.Fatalf("ResizeStorage: %v", err)
	}
	if got := recordedDisk(t, store); got != "4Gi" {
		t.Errorf("recorded disk = %q, want 4Gi", got)
	}
}

// The record is written before the cluster is asked, and put back if the
// cluster refuses: a record smaller than the disk would size a restore short.
func TestResizeStorageKeepsTheRecordWhenTheClusterRefuses(t *testing.T) {
	svc, store, mock := setupClusterChangeTest(t)
	mock.UpdateCRDError = errors.New("apiserver unavailable")
	if err := svc.ResizeStorage(context.Background(), testOpsDB, "4Gi"); err == nil {
		t.Fatal("a resize the cluster refused reported success")
	}
	if got := recordedDisk(t, store); got != "2Gi" {
		t.Errorf("recorded disk = %q, want the unchanged 2Gi", got)
	}
}

func TestApplyOrgTierRecordsTheDisk(t *testing.T) {
	svc, store, _ := setupClusterChangeTest(t)
	if err := svc.ApplyOrgTier(context.Background(), testOpsDB, domain.Free); err != nil {
		t.Fatalf("ApplyOrgTier: %v", err)
	}
	if got := recordedDisk(t, store); got != "5Gi" {
		t.Errorf("recorded disk = %q, want FREE's 5Gi", got)
	}
}

func restorePlanFor(t *testing.T, tier domain.TierType, sourceDisk string) (RestorePlan, error) {
	t.Helper()
	svc, _, _ := setupProvisioningTest(t)
	setOrgTier(svc, "org", tier)
	src := sourceInstance()
	src.StorageSize = sourceDisk
	return svc.RestorePlan(context.Background(), src)
}

func TestRestorePlanSizesTheDiskFromTheSourcesRecord(t *testing.T) {
	cases := []struct {
		name, tier, source, want string
	}{
		{"a grown disk is kept", string(domain.Standard), "80Gi", "80Gi"},
		{"never below the plan's start", string(domain.Standard), "5Gi", "50Gi"},
		{"at the plan's maximum", string(domain.Standard), "500Gi", "500Gi"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := restorePlanFor(t, domain.TierType(tc.tier), tc.source)
			if err != nil {
				t.Fatalf("RestorePlan: %v", err)
			}
			if plan.Config.StorageSize != tc.want {
				t.Errorf("disk = %q, want %q", plan.Config.StorageSize, tc.want)
			}
		})
	}
}

// Refused before anything is created: the data would not fit the target plan.
func TestRestorePlanRefusesASourceLargerThanTheTargetPlanAllows(t *testing.T) {
	if _, err := restorePlanFor(t, domain.Free, "50Gi"); !errors.Is(err, ErrRestoreDiskAbovePlan) {
		t.Fatalf("err = %v, want ErrRestoreDiskAbovePlan", err)
	}
}

// No guessing: a source without a recorded disk may have grown.
func TestRestorePlanRefusesASourceWithNoRecordedDisk(t *testing.T) {
	for _, disk := range []string{"", "lots"} {
		if _, err := restorePlanFor(t, domain.Standard, disk); !errors.Is(err, ErrRestoreSourceDiskUnknown) {
			t.Errorf("disk %q: err = %v, want ErrRestoreSourceDiskUnknown", disk, err)
		}
	}
}

// The restored project records the disk its cluster was created with.
func TestRestoredProjectRecordsItsDisk(t *testing.T) {
	plans := enterprisePlan()
	plans.plan.Config = config.TierConfig{Instances: 1, StorageSize: "600Gi", MaxStorageSize: "2Ti", Memory: "16Gi", CPU: "4", StatementTimeout: "60s"}
	restored := newRestoredInstance(t, plans)
	if restored.StorageSize != "600Gi" {
		t.Errorf("restored disk = %q, want 600Gi", restored.StorageSize)
	}
}

func newRestoredInstance(t *testing.T, plans *fakeRestorePlans) *domain.DatabaseInstance {
	t.Helper()
	mock := k8s.NewMockClient()
	reg := &fakeRegistrar{}
	adapter := newRestoreReadyAdapter(t, mock, reg)
	adapter.SetRestorePlanSource(plans)
	if _, err := restoreDst(t, adapter, tenantSource()); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(reg.calls) != 1 {
		t.Fatalf("registered %d projects, want 1", len(reg.calls))
	}
	return reg.calls[0]
}
