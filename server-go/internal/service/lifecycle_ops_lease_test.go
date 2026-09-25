package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

type lifecycleOp struct {
	name string
	op   ProjectOperation
	run  func(*ProvisioningService) error
}

func lifecycleOps() []lifecycleOp {
	ctx := context.Background()
	return []lifecycleOp{
		{"upgrade", OperationUpgrade, func(s *ProvisioningService) error { return s.UpgradeVersion(ctx, testOpsDB, "17") }},
		{"maintenance", OperationMaintenance, func(s *ProvisioningService) error {
			return s.SetMaintenanceWindow(ctx, testOpsDB, domain.MaintenanceWindowConfig{Window: "0 3 * * 0", DurationMinutes: 60})
		}},
	}
}

func setupLeasedOpsTest(t *testing.T) (*ProvisioningService, *storage.FileSystemStore, *k8s.MockClient) {
	t.Helper()
	svc, store, mock := setupOpsTest(t)
	cluster := k8s.BuildPostgreSQLCluster(k8s.PostgreSQLClusterOpts{
		ProjectID: testOpsDB, Namespace: testOpsDBNS,
		Tier: config.TierConfig{Instances: 1, StorageSize: "5Gi", Memory: "512Mi", CPU: "0.5"},
	})
	if err := mock.ApplyCRD(context.Background(), k8s.CNPGClusterGVR, testOpsDBNS, cluster); err != nil {
		t.Fatalf("seed cluster: %v", err)
	}
	return svc, store, mock
}

func TestLifecycleOpsTakeAndReleaseTheProjectLease(t *testing.T) {
	for _, tc := range lifecycleOps() {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _ := setupLeasedOpsTest(t)
			claimer := &countingClaimer{}
			svc.SetOperationClaimer(claimer)
			if err := tc.run(svc); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if claimer.claims != 1 || claimer.releases != 1 {
				t.Fatalf("claims=%d releases=%d, want one of each", claimer.claims, claimer.releases)
			}
			if claimer.heldNames[0] != tc.op {
				t.Errorf("lease held as %q, want %q", claimer.heldNames[0], tc.op)
			}
		})
	}
}

func TestLifecycleOpsRefusedWhileAnotherOperationHoldsTheProject(t *testing.T) {
	for _, tc := range lifecycleOps() {
		t.Run(tc.name, func(t *testing.T) {
			svc, store, mock := setupLeasedOpsTest(t)
			svc.SetOperationClaimer(&heldClaimer{})
			before, _ := mock.GetCRD(context.Background(), k8s.CNPGClusterGVR, testOpsDBNS, testOpsDBPostgres)
			beforeSpec := before.DeepCopy()

			err := tc.run(svc)
			if !errors.Is(err, ErrProjectOperationRunning) || !errors.Is(err, storage.ErrProjectBusy) {
				t.Fatalf("err = %v, want the project-busy refusal", err)
			}
			after, _ := mock.GetCRD(context.Background(), k8s.CNPGClusterGVR, testOpsDBNS, testOpsDBPostgres)
			if !equalUnstructured(beforeSpec.Object, after.Object) {
				t.Error("the cluster was patched while another operation held the project")
			}
			if _, ok := mock.CRDs[testOpsDBNS+"/"+testOpsDB+"-postgres-pooler"]; ok {
				t.Error("a pooler was created while another operation held the project")
			}
			inst, _ := store.FindByProjectID(testOpsDB)
			if inst.Tier != domain.Free || inst.MaintenanceWindow != "" || inst.PoolerEnabled != nil {
				t.Error("the project row was written while another operation held the project")
			}
		})
	}
}

func clusterOps() []lifecycleOp {
	var ops []lifecycleOp
	for _, op := range lifecycleOps() {
		if op.op != OperationMaintenance {
			ops = append(ops, op)
		}
	}
	return ops
}

func TestClusterChangesAreRefusedUnlessTheProjectIsActive(t *testing.T) {
	statuses := []string{string(domain.StatusPaused), string(domain.StatusDeleting), string(domain.StatusRestoring), "FAILED"}
	for _, tc := range clusterOps() {
		for _, status := range statuses {
			t.Run(tc.name+"/"+status, func(t *testing.T) {
				svc, store, mock := setupLeasedOpsTest(t)
				markStatus(t, store, status)
				before, _ := mock.GetCRD(context.Background(), k8s.CNPGClusterGVR, testOpsDBNS, testOpsDBPostgres)
				beforeSpec := before.DeepCopy()

				err := tc.run(svc)
				if !errors.Is(err, ErrProjectNotActive) {
					t.Fatalf("err = %v, want ErrProjectNotActive", err)
				}
				if !strings.Contains(err.Error(), status) {
					t.Errorf("message %q does not name the status %s", err, status)
				}
				after, _ := mock.GetCRD(context.Background(), k8s.CNPGClusterGVR, testOpsDBNS, testOpsDBPostgres)
				if !equalUnstructured(beforeSpec.Object, after.Object) {
					t.Errorf("the cluster of a %s project was changed", status)
				}
			})
		}
	}
}

func TestMaintenanceWindowEditsAreAllowedInAnyStatusTheStoreAccepts(t *testing.T) {
	for _, status := range []string{string(domain.StatusPaused), string(domain.StatusRestoring), "FAILED"} {
		t.Run(status, func(t *testing.T) {
			svc, store, _ := setupLeasedOpsTest(t)
			markStatus(t, store, status)
			cfg := domain.MaintenanceWindowConfig{Window: "0 3 * * 0", DurationMinutes: 60}
			if err := svc.SetMaintenanceWindow(context.Background(), testOpsDB, cfg); err != nil {
				t.Fatalf("SetMaintenanceWindow on %s: %v", status, err)
			}
			inst, _ := store.FindByProjectID(testOpsDB)
			if inst.MaintenanceWindow != cfg.Window || inst.Status != status {
				t.Errorf("window=%q status=%q, want %q kept at %s", inst.MaintenanceWindow, inst.Status, cfg.Window, status)
			}
		})
	}
}

// recordingStore fails the test on an unconditional write: every lifecycle
// operation must write only while the row still holds the status it read.
type recordingStore struct {
	*storage.FileSystemStore
	t        *testing.T
	expected []string
}

func (s *recordingStore) Update(*domain.DatabaseInstance) error {
	s.t.Error("unconditional Update: the write would overwrite whatever moved the project")
	return nil
}

func (s *recordingStore) UpdateIfStatus(inst *domain.DatabaseInstance, expected string) error {
	s.expected = append(s.expected, expected)
	return s.FileSystemStore.UpdateIfStatus(inst, expected)
}

func TestLifecycleOpsWriteTheRowOnlyWhileItHoldsTheStatusTheyRead(t *testing.T) {
	for _, tc := range lifecycleOps() {
		t.Run(tc.name, func(t *testing.T) {
			base, fs, mock := setupLeasedOpsTest(t)
			rec := &recordingStore{FileSystemStore: fs, t: t}
			svc := NewProvisioningService(rec, base.factory, mock)
			if err := tc.run(svc); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			for _, got := range rec.expected {
				if got != "ACTIVE" {
					t.Errorf("conditional write expected %q, want ACTIVE", got)
				}
			}
		})
	}
}

func TestRotateCredentialsWritesTheOwnerRowConditionally(t *testing.T) {
	h := newRotationHarness(t)
	rec := &recordingStore{FileSystemStore: h.store, t: t}
	h.svc.store = rec
	if _, err := h.svc.RotateCredentials(context.Background(), rotProject); err != nil {
		t.Fatalf("RotateCredentials: %v", err)
	}
	if len(rec.expected) != 1 || rec.expected[0] != "ACTIVE" {
		t.Errorf("owner row writes = %v, want one conditional write on ACTIVE", rec.expected)
	}
}

func markStatus(t *testing.T, store *storage.FileSystemStore, status string) {
	t.Helper()
	inst, err := store.FindByProjectID(testOpsDB)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	inst.Status = status
	if err := store.Update(inst); err != nil {
		t.Fatalf("mark %s: %v", status, err)
	}
}

func equalUnstructured(a, b map[string]interface{}) bool {
	return fmtSpec(a) == fmtSpec(b)
}

func fmtSpec(obj map[string]interface{}) string {
	encoded, _ := json.Marshal(obj["spec"])
	return string(encoded)
}
