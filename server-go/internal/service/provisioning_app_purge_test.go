package service

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

type fakeAppPurger struct {
	mu     sync.Mutex
	purged []string
	err    error
}

func (f *fakeAppPurger) PurgeProjectApps(projectID string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return 0, f.err
	}
	f.purged = append(f.purged, projectID)
	return 1, nil
}

func (f *fakeAppPurger) purgedProjects() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.purged...)
}

func TestDeprovision_RemovesTheProjectsApps(t *testing.T) {
	svc, store, _ := setupDeletionTest(t)
	purger := &fakeAppPurger{}
	svc.SetAppPurger(purger)

	if err := svc.Deprovision(context.Background(), testDeletingProj); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
	if got := purger.purgedProjects(); len(got) != 1 || got[0] != testDeletingProj {
		t.Fatalf("the project's apps were not removed: %v", got)
	}
	if inst, _ := store.FindByProjectID(testDeletingProj); inst != nil {
		t.Error("the record should be gone")
	}
}

// The app rows go once the namespace their workloads ran in is gone, and
// before the record: afterwards nothing could name them.
func TestDeprovision_RemovesAppsAfterTheWorkloadsAndBeforeTheRecord(t *testing.T) {
	svc, _, _ := setupDeletionTest(t)
	svc.SetAppPurger(&fakeAppPurger{})
	at := map[string]int{}
	for i, step := range svc.deletionSteps(false) {
		at[step.name] = i
	}
	apps, ok := at[domain.DeletionStepDeleteApps]
	if !ok {
		t.Fatal("removing the apps must be a teardown step")
	}
	if apps < at[domain.DeletionStepDeleteResources] || apps > at[domain.DeletionStepDeleteRecord] {
		t.Fatalf("apps at %d, resources at %d, record at %d", apps,
			at[domain.DeletionStepDeleteResources], at[domain.DeletionStepDeleteRecord])
	}
}

func TestDeprovision_AppPurgeFailureKeepsTheProjectDeleting(t *testing.T) {
	svc, store, _ := setupDeletionTest(t)
	purger := &fakeAppPurger{err: errors.New("platform db down")}
	svc.SetAppPurger(purger)

	if err := svc.Deprovision(context.Background(), testDeletingProj); err == nil {
		t.Fatal(testWantErrNil)
	}
	requireDeletingRow(t, store, domain.DeletionStepDeleteApps)

	purger.err = nil
	if err := svc.Deprovision(context.Background(), testDeletingProj); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if inst, _ := store.FindByProjectID(testDeletingProj); inst != nil {
		t.Error("record should be gone after the retry")
	}
}
