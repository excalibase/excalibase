package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

type fakeSitePurger struct {
	purged []string
	err    error
}

func (f *fakeSitePurger) PurgeProjectSites(_ context.Context, projectID string) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.purged = append(f.purged, projectID)
	return 1, nil
}

func TestDeprovision_RemovesTheProjectsSites(t *testing.T) {
	svc, store, _ := setupDeletionTest(t)
	purger := &fakeSitePurger{}
	svc.SetSitePurger(purger)

	if err := svc.Deprovision(context.Background(), testDeletingProj); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
	if len(purger.purged) != 1 || purger.purged[0] != testDeletingProj {
		t.Fatalf("the project's sites were not removed: %v", purger.purged)
	}
	if inst, _ := store.FindByProjectID(testDeletingProj); inst != nil {
		t.Error("the record should be gone")
	}
}

// A site is public: it stops being served before the rest of the project is torn down.
func TestDeprovision_RemovesSitesBeforeTheDatabase(t *testing.T) {
	svc, _, _ := setupDeletionTest(t)
	svc.SetSitePurger(&fakeSitePurger{})
	at := map[string]int{}
	for i, step := range svc.deletionSteps(false) {
		at[step.name] = i
	}
	sites, ok := at[domain.DeletionStepDeleteSites]
	if !ok {
		t.Fatal("removing the sites must be a teardown step")
	}
	if sites > at[domain.DeletionStepDeleteResources] {
		t.Fatalf("sites at %d, resources at %d", sites, at[domain.DeletionStepDeleteResources])
	}
}

func TestDeprovision_SitePurgeFailureKeepsTheProjectDeleting(t *testing.T) {
	svc, store, _ := setupDeletionTest(t)
	purger := &fakeSitePurger{err: errors.New("cloudflare down")}
	svc.SetSitePurger(purger)

	if err := svc.Deprovision(context.Background(), testDeletingProj); err == nil {
		t.Fatal(testWantErrNil)
	}
	requireDeletingRow(t, store, domain.DeletionStepDeleteSites)
}
