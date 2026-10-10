package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

type fakeWorkloadTeardown struct {
	namespaces []string
	err        error
}

func (f *fakeWorkloadTeardown) TeardownProject(_ context.Context, projectID, namespace string) error {
	if f.err != nil {
		return f.err
	}
	f.namespaces = append(f.namespaces, projectID+"/"+namespace)
	return nil
}

// On a single host nothing removes a project's apps with its database (EXC-575):
// the teardown takes them, while the database they share a network with still stands.
func TestDeprovision_TearsDownTheProjectsAppWorkloadsBeforeItsDatabase(t *testing.T) {
	svc, store, _ := setupDeletionTest(t)
	teardown := &fakeWorkloadTeardown{}
	svc.SetProjectWorkloadTeardown(teardown)
	at := map[string]int{}
	for i, step := range svc.deletionSteps(false) {
		at[step.name] = i
	}
	workloads, ok := at[domain.DeletionStepDeleteAppWorkloads]
	if !ok || workloads > at[domain.DeletionStepDeleteResources] {
		t.Fatalf("app workloads at %d (present %v), resources at %d", workloads, ok, at[domain.DeletionStepDeleteResources])
	}
	inst, _ := store.FindByProjectID(testDeletingProj)
	namespace := inst.Namespace
	if err := svc.Deprovision(context.Background(), testDeletingProj); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
	if len(teardown.namespaces) != 1 || teardown.namespaces[0] != testDeletingProj+"/"+namespace {
		t.Fatalf("torn down %v, want %q", teardown.namespaces, namespace)
	}
}

func TestDeprovision_AppWorkloadTeardownFailureKeepsTheProjectDeleting(t *testing.T) {
	svc, store, _ := setupDeletionTest(t)
	svc.SetProjectWorkloadTeardown(&fakeWorkloadTeardown{err: errors.New("engine down")})
	if err := svc.Deprovision(context.Background(), testDeletingProj); err == nil {
		t.Fatal(testWantErrNil)
	}
	requireDeletingRow(t, store, domain.DeletionStepDeleteAppWorkloads)
}

func TestDeprovision_WithoutASingleHostRuntimeHasNoWorkloadStep(t *testing.T) {
	svc, _, _ := setupDeletionTest(t)
	for _, step := range svc.deletionSteps(false) {
		if step.name == domain.DeletionStepDeleteAppWorkloads {
			t.Fatal("Kubernetes deletes app workloads with the namespace; no step of its own")
		}
	}
}
