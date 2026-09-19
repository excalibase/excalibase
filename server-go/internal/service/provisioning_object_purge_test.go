package service

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// EXC-419 — a deleted project's rows go with its record, and after that
// nothing names the bytes it stored: no bucket, no project, no listing. They
// stay in the object store forever, billed to nobody. The teardown clears the
// project's own prefix before the record that names it is removed.

// fakeObjectPurger records the projects it was asked to clear and can be told
// to fail, which is what a real object store does when it is unreachable.
type fakeObjectPurger struct {
	mu       sync.Mutex
	purged   []string
	deleted  int
	failures int
	err      error
}

func (f *fakeObjectPurger) PurgeProjectObjects(_ context.Context, projectID string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		f.failures++
		return 0, f.err
	}
	f.purged = append(f.purged, projectID)
	return f.deleted, nil
}

func (f *fakeObjectPurger) purgedProjects() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.purged...)
}

func TestDeprovision_PurgesTheProjectsObjects(t *testing.T) {
	svc, store, _ := setupDeletionTest(t)
	purger := &fakeObjectPurger{deleted: 2}
	svc.SetObjectPurger(purger)

	if err := svc.Deprovision(context.Background(), testDeletingProj); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
	if got := purger.purgedProjects(); len(got) != 1 || got[0] != testDeletingProj {
		t.Fatalf("the project's object prefix was not purged: %v", got)
	}
	if inst, _ := store.FindByProjectID(testDeletingProj); inst != nil {
		t.Error("the record should be gone once everything under it is")
	}
}

// The purge runs before the credentials and the record: once the record is
// gone, nothing can name the prefix to clear it.
func TestDeprovision_PurgesObjectsBeforeTheRecord(t *testing.T) {
	svc, _, _ := setupDeletionTest(t)
	svc.SetObjectPurger(&fakeObjectPurger{})
	steps := svc.deletionSteps(false)
	purgeAt, vaultAt, recordAt := -1, -1, -1
	for i, step := range steps {
		switch step.name {
		case domain.DeletionStepDeleteObjects:
			purgeAt = i
		case domain.DeletionStepDeleteVault:
			vaultAt = i
		case domain.DeletionStepDeleteRecord:
			recordAt = i
		}
	}
	if purgeAt < 0 {
		t.Fatal("the object purge must be one of the teardown steps")
	}
	if purgeAt > vaultAt || purgeAt > recordAt {
		t.Errorf("purge at %d must run before vault (%d) and record (%d)", purgeAt, vaultAt, recordAt)
	}
}

// An object store that cannot be cleared leaves the project DELETING, with
// the step recorded, and a retry finishes the job.
func TestDeprovision_ObjectPurgeFailureKeepsTheProjectDeleting(t *testing.T) {
	svc, store, _ := setupDeletionTest(t)
	purger := &fakeObjectPurger{err: errors.New("object store unreachable")}
	svc.SetObjectPurger(purger)

	if err := svc.Deprovision(context.Background(), testDeletingProj); err == nil {
		t.Fatal(testWantErrNil)
	}
	requireDeletingRow(t, store, domain.DeletionStepDeleteObjects)

	purger.err = nil
	if err := svc.Deprovision(context.Background(), testDeletingProj); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got := purger.purgedProjects(); len(got) != 1 {
		t.Errorf("the retry should have completed the purge: %v", got)
	}
	if inst, _ := store.FindByProjectID(testDeletingProj); inst != nil {
		t.Error("record should be gone after the retry")
	}
}

// User files go with the project whatever the caller said about backups:
// keeping backups is a choice about backups, not about the project's data.
func TestDeprovision_PurgesObjectsEvenWhenBackupsAreKept(t *testing.T) {
	svc, _, _ := setupDeletionTest(t)
	purger := &fakeObjectPurger{}
	svc.SetObjectPurger(purger)
	keep := false

	if err := svc.DeprovisionWithOptions(context.Background(), testDeletingProj,
		DeprovisionOptions{DeleteBackups: &keep}); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
	if got := purger.purgedProjects(); len(got) != 1 {
		t.Errorf("user files must be purged regardless of the backup choice: %v", got)
	}
}

// With no object store wired there is nothing to purge, and the teardown says
// so by not carrying the step at all rather than running one that fails.
func TestDeprovision_NoObjectPurgerMeansNoStep(t *testing.T) {
	svc, store, _ := setupDeletionTest(t)

	for _, step := range svc.deletionSteps(false) {
		if step.name == domain.DeletionStepDeleteObjects {
			t.Fatal("no object store is wired: the purge step must not be in the list")
		}
	}
	if err := svc.Deprovision(context.Background(), testDeletingProj); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
	if inst, _ := store.FindByProjectID(testDeletingProj); inst != nil {
		t.Error("record should be gone")
	}
}
