package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// Pausing stops a project's database. A project without one has nothing to
// stop: the caller is told so, and the row is left exactly as it was.
func TestPausingAProjectWithoutADatabaseIsRefused(t *testing.T) {
	svc, store, pauser, bk := setupPauseTest(t)
	store.Create(&domain.DatabaseInstance{
		ProjectID: "p1", OrgID: "o", Status: "ACTIVE", NoDatabase: true,
		DeploymentMode: domain.ModeK8s, Tier: domain.Free, Namespace: "o-p1",
	})
	if err := svc.Pause(context.Background(), "p1", domain.PauseReasonManual); !errors.Is(err, domain.ErrNoDatabase) {
		t.Fatalf("Pause: got %v, want domain.ErrNoDatabase", err)
	}
	if err := svc.Resume(context.Background(), "p1"); !errors.Is(err, domain.ErrNoDatabase) {
		t.Fatalf("Resume: got %v, want domain.ErrNoDatabase", err)
	}
	if pauser.pauseCalls != 0 || bk.calls != 0 {
		t.Fatal("a project without a database was backed up or stopped")
	}
	if got, _ := store.FindByProjectID("p1"); got.Status != "ACTIVE" {
		t.Fatalf("status %s, want ACTIVE", got.Status)
	}
}

// The idle sweep leaves a project without a database alone, however long it
// has been quiet: there is no database whose idleness costs anything.
func TestIdleSweepSkipsAProjectWithoutADatabase(t *testing.T) {
	f := newIdleFixture(t)
	created := f.clock.Now().Add(-30 * day)
	if err := f.instances.Create(&domain.DatabaseInstance{
		ProjectID: "apps", OrgID: "o", OwnerID: "u1", Tier: domain.Free, Status: "ACTIVE",
		DeploymentMode: domain.ModeK8s, NoDatabase: true, CreatedAt: &domain.FlexTime{Time: created},
	}); err != nil {
		t.Fatal(err)
	}
	report := f.run(t)
	if len(report.Paused) != 0 || len(report.Warned) != 0 {
		t.Fatalf("report %+v: a project without a database was warned or paused", report)
	}
}
