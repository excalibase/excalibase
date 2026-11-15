package service

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

const (
	pausedScheduleKey = rotNamespace + "/" + rotProject + "-postgres-backup"
	hibernatedBackup  = "cannot backup a hibernated cluster"
)

func (h *rotationHarness) seedSchedule(t *testing.T, status string) {
	t.Helper()
	inst, err := h.store.FindByProjectID(rotProject)
	if err != nil || inst == nil {
		t.Fatalf("project: %v", err)
	}
	inst.Status = status
	if err := h.store.Update(inst); err != nil {
		t.Fatal(err)
	}
	backup, err := k8s.BuildScheduledBackup(rotProject, rotNamespace, "0 2 * * *")
	if err != nil {
		t.Fatal(err)
	}
	h.kube.CRDs[pausedScheduleKey] = backup
}

func (h *rotationHarness) scheduleSuspended() bool {
	suspended, _, _ := unstructured.NestedBool(h.kube.CRDs[pausedScheduleKey].Object, "spec", "suspend")
	return suspended
}

func (h *rotationHarness) seedFailedBackup(name, message string) {
	backup := k8s.BuildManualBackup(rotProject, rotNamespace, name)
	_ = unstructured.SetNestedField(backup.Object, "failed", "status", "phase")
	_ = unstructured.SetNestedField(backup.Object, message, "status", "error")
	h.kube.CRDs[rotNamespace+"/"+name] = backup
}

// EXC-758: projects paused before pause learned to suspend the schedule are
// fixed at startup.
func TestReconcilePausedBackups_SuspendsThePausedProjectsSchedule(t *testing.T) {
	h := newRotationHarness(t)
	h.seedSchedule(t, string(domain.StatusPaused))

	report := h.svc.ReconcilePausedBackups(context.Background())
	if len(report.Failed) != 0 {
		t.Fatalf("failed: %v", report.Failed)
	}
	if !h.scheduleSuspended() {
		t.Fatal("a paused project's nightly backup still fires")
	}
}

func TestReconcilePausedBackups_LeavesARunningProjectsScheduleAlone(t *testing.T) {
	h := newRotationHarness(t)
	h.seedSchedule(t, "ACTIVE")

	h.svc.ReconcilePausedBackups(context.Background())
	if h.scheduleSuspended() {
		t.Fatal("a running project's backups were suspended")
	}
}

func TestReconcilePausedBackups_ClearsOnlyTheHibernatedFailures(t *testing.T) {
	h := newRotationHarness(t)
	h.seedSchedule(t, string(domain.StatusPaused))
	h.seedFailedBackup("night-1", hibernatedBackup)
	h.seedFailedBackup("night-2", hibernatedBackup)
	h.seedFailedBackup("store-down", "bucket unreachable")

	report := h.svc.ReconcilePausedBackups(context.Background())
	if report.Cleared != 2 {
		t.Fatalf("cleared %d, want 2", report.Cleared)
	}
	if h.kube.CRDs[rotNamespace+"/night-1"] != nil || h.kube.CRDs[rotNamespace+"/night-2"] != nil {
		t.Fatal("a hibernated failure was kept")
	}
	if h.kube.CRDs[rotNamespace+"/store-down"] == nil {
		t.Fatal("a failure with another cause was deleted")
	}
}

// A project the platform has no cluster for yet is not an error.
func TestReconcilePausedBackups_NothingToDoOnSingleHost(t *testing.T) {
	h := newRotationHarness(t)
	h.svc.k8sClient = nil
	if report := h.svc.ReconcilePausedBackups(context.Background()); len(report.Failed) != 0 || report.Cleared != 0 {
		t.Fatalf("report = %+v", report)
	}
}
