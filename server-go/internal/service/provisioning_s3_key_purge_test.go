package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

type fakeS3KeyPurger struct {
	purged []string
	err    error
}

func (f *fakeS3KeyPurger) PurgeProject(_ context.Context, projectID string) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.purged = append(f.purged, projectID)
	return 1, nil
}

func TestDeprovision_RevokesTheProjectsS3Keys(t *testing.T) {
	svc, store, _ := setupDeletionTest(t)
	purger := &fakeS3KeyPurger{}
	svc.SetS3KeyPurger(purger)
	if err := svc.Deprovision(context.Background(), testDeletingProj); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
	if len(purger.purged) != 1 || purger.purged[0] != testDeletingProj {
		t.Fatalf("the project's S3 keys were not revoked: %v", purger.purged)
	}
	if inst, _ := store.FindByProjectID(testDeletingProj); inst != nil {
		t.Error("the record should be gone")
	}
}

// An S3 key reaches the project's files from outside: it stops working right
// after the public port, before anything else is torn down.
func TestDeprovision_RevokesS3KeysFirst(t *testing.T) {
	svc, _, _ := setupDeletionTest(t)
	svc.SetS3KeyPurger(&fakeS3KeyPurger{})
	svc.SetObjectPurger(nil)
	steps := svc.deletionSteps(false)
	if len(steps) == 0 || steps[0].name != domain.DeletionStepRevokeS3Keys {
		names := []string{}
		for _, step := range steps {
			names = append(names, step.name)
		}
		t.Fatalf("steps = %v, want %s first", names, domain.DeletionStepRevokeS3Keys)
	}
}

func TestDeprovision_S3KeyPurgeFailureKeepsTheProjectDeleting(t *testing.T) {
	svc, store, _ := setupDeletionTest(t)
	svc.SetS3KeyPurger(&fakeS3KeyPurger{err: errors.New("platform db down")})
	if err := svc.Deprovision(context.Background(), testDeletingProj); err == nil {
		t.Fatal(testWantErrNil)
	}
	requireDeletingRow(t, store, domain.DeletionStepRevokeS3Keys)
}
