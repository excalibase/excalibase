package k8s

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	suspendNS      = "org1-sus"
	suspendProject = "sus"
	hibernatedErr  = "cannot backup a hibernated cluster"
)

func seedScheduledBackup(t *testing.T, mock *MockClient) {
	t.Helper()
	backup, err := BuildScheduledBackup(suspendProject, suspendNS, "0 2 * * *")
	if err != nil {
		t.Fatal(err)
	}
	mock.CRDs[suspendNS+"/"+backup.GetName()] = backup
}

func scheduledBackupSuspended(t *testing.T, mock *MockClient) bool {
	t.Helper()
	stored := mock.CRDs[suspendNS+"/"+suspendProject+"-postgres-backup"]
	if stored == nil {
		t.Fatal("scheduled backup missing")
	}
	suspended, _, _ := unstructured.NestedBool(stored.Object, "spec", "suspend")
	return suspended
}

func TestSetScheduledBackupSuspended_FlipsTheProjectsSchedule(t *testing.T) {
	mock := NewMockClient()
	seedScheduledBackup(t, mock)

	if err := SetScheduledBackupSuspended(context.Background(), mock, suspendNS, suspendProject, true); err != nil {
		t.Fatal(err)
	}
	if !scheduledBackupSuspended(t, mock) {
		t.Fatal("the schedule still fires")
	}
	if err := SetScheduledBackupSuspended(context.Background(), mock, suspendNS, suspendProject, false); err != nil {
		t.Fatal(err)
	}
	if scheduledBackupSuspended(t, mock) {
		t.Fatal("the schedule stayed suspended")
	}
}

func TestSetScheduledBackupSuspended_NoScheduleIsNothingToDo(t *testing.T) {
	mock := NewMockClient()
	if err := SetScheduledBackupSuspended(context.Background(), mock, suspendNS, suspendProject, true); err != nil {
		t.Fatalf("a project without a schedule: %v", err)
	}
}

func TestSetScheduledBackupSuspended_AlreadyInTheWantedStateWritesNothing(t *testing.T) {
	mock := NewMockClient()
	seedScheduledBackup(t, mock)
	if err := SetScheduledBackupSuspended(context.Background(), mock, suspendNS, suspendProject, false); err != nil {
		t.Fatal(err)
	}
	for _, call := range mock.Calls {
		if call == "UpdateCRD:"+suspendNS+"/"+suspendProject+"-postgres-backup" {
			t.Fatal("rewrote a schedule that already was in the wanted state")
		}
	}
}

func seedBackup(mock *MockClient, name, phase, message string) {
	backup := BuildManualBackup(suspendProject, suspendNS, name)
	if phase != "" {
		_ = unstructured.SetNestedField(backup.Object, phase, "status", "phase")
		_ = unstructured.SetNestedField(backup.Object, message, "status", "error")
	}
	mock.CRDs[suspendNS+"/"+name] = backup
}

// Only a Backup that failed because the cluster was hibernated holds nothing
// worth keeping; every other Backup, failed or not, stays.
func TestDeleteHibernatedBackupFailures_RemovesOnlyThose(t *testing.T) {
	mock := NewMockClient()
	seedBackup(mock, "night-1", "failed", hibernatedErr)
	seedBackup(mock, "night-2", "failed", hibernatedErr)
	seedBackup(mock, "broken-store", "failed", "bucket unreachable")
	seedBackup(mock, "good", "completed", "")
	seedBackup(mock, "running", "", "")

	removed, err := DeleteHibernatedBackupFailures(context.Background(), mock, suspendNS)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("removed %d, want 2", removed)
	}
	for _, kept := range []string{"broken-store", "good", "running"} {
		if mock.CRDs[suspendNS+"/"+kept] == nil {
			t.Errorf("%s was deleted", kept)
		}
	}
	for _, gone := range []string{"night-1", "night-2"} {
		if mock.CRDs[suspendNS+"/"+gone] != nil {
			t.Errorf("%s was kept", gone)
		}
	}
}
