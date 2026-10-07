package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// withFailingRecords is the restorer over a store whose status writes fail.
func withFailingRecords(t *testing.T, w *inPlaceWorld) *InPlaceRestore {
	t.Helper()
	return NewInPlaceRestore(InPlaceRestoreConfig{
		Projects:  w,
		Instances: &failingStore{InstanceStore: w.instances, updateErr: errors.New("disk full")},
		Probe:     w,
	})
}

func TestInPlaceRestore_AStatusThatCannotBeRecordedStopsBeforeTheSwap(t *testing.T) {
	w, _ := newInPlaceWorld(t, inPlaceProject())
	err := withFailingRecords(t, w).Restore(context.Background(), w, "proj-a", restoreAt())
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("got %v", err)
	}
	if hasEntry(w.calls, "replace") {
		t.Errorf("the database is not replaced unrecorded: %v", w.calls)
	}
}

func TestInPlaceRestore_ASafetyBackupThatCannotBeResolvedChangesNothing(t *testing.T) {
	w, restorer := newInPlaceWorld(t, inPlaceProject())
	w.safetyTargetErr = errors.New("backup not listed")
	err := restorer.Restore(context.Background(), w, "proj-a", restoreAt())
	var public *InPlaceRestoreError
	if !errors.As(err, &public) || !strings.Contains(public.Public, "cannot be restored") {
		t.Fatalf("got %v", err)
	}
	if hasEntry(w.calls, "replace") {
		t.Errorf("calls %v", w.calls)
	}
}

func TestInPlaceRestore_AProjectGoneMidRestoreIsAnError(t *testing.T) {
	_, restorer := newInPlaceWorld(t, inPlaceProject())
	if err := restorer.setStatus("missing", domain.StatusActive, "", ""); err == nil {
		t.Fatal("want an error")
	}
}

// A rollback whose outcome cannot be written still answers with its reason.
func TestInPlaceRestore_RollbacksAnswerEvenWhenTheirStatusCannotBeWritten(t *testing.T) {
	cases := map[string]struct {
		project  func() *domain.DatabaseInstance
		failures []error
		want     string
	}{
		"rolled back":     {inPlaceProject, []error{errors.New("first")}, "disk full"},
		"not rolled back": {inPlaceProject, []error{errors.New("first"), errors.New("second")}, "could not be put back"},
		"nothing to roll back to": {func() *domain.DatabaseInstance {
			p := inPlaceProject()
			p.Status, p.CurrentStep = string(domain.StatusRestoring), domain.RestoreStepRestoreStopped
			return p
		}, []error{errors.New("first")}, "restore again from Backups"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			w, _ := newInPlaceWorld(t, c.project())
			w.replaceErrs = c.failures
			restorer := withFailingRecords(t, w)
			inst, _ := w.instances.FindByProjectID("proj-a")
			var safety *safetyBackup
			if inst.Status == string(domain.StatusActive) {
				safety = &safetyBackup{id: "safety-1", target: map[string]interface{}{"backupID": "safety-1"}}
				w.replaceErrs = c.failures[1:]
			}
			err := restorer.rollBack(context.Background(), w, inst, safety, c.failures[0])
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v", err)
			}
		})
	}
}
