package service

import (
	"context"
	"errors"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/k8s"
)

func renewalAlertLab(t *testing.T) (*renewalLab, *AlertingService, *walNotifier) {
	t.Helper()
	lab := newRenewalLab(t)
	alerts := NewAlertingService(t.TempDir())
	notifier := &walNotifier{}
	lab.renewer.SetAlerts(alerts, notifier, 30*time.Minute)
	return lab, alerts, notifier
}

// EXC-784: a renewal that keeps failing for a project is an alert, after the threshold and once.
func TestRenewalAlert_RaisesWhenAProjectKeepsFailingAndResolvesWhenItRecovers(t *testing.T) {
	lab, alerts, notifier := renewalAlertLab(t)
	lab.project(t, "broken", "ACTIVE", time.Time{}) // no credential Secret: the read fails
	ctx := context.Background()

	lab.renewer.RenewDue(ctx)
	lab.now = lab.now.Add(20 * time.Minute)
	lab.renewer.RenewDue(ctx)
	if got := alerts.GetActiveAlerts(); len(got) != 0 {
		t.Fatalf("raised after 20 minutes: %+v", got)
	}

	lab.now = lab.now.Add(11 * time.Minute)
	lab.renewer.RenewDue(ctx)
	lab.renewer.RenewDue(ctx)
	open := alerts.GetActiveAlerts()
	if len(open) != 1 || open[0].Metric != "backup.renewal:broken" || open[0].ProjectID != "" {
		t.Fatalf("open alerts = %+v", open)
	}
	if !strings.Contains(open[0].Message, "broken") || !strings.Contains(open[0].Message, "read backup credentials") {
		t.Fatalf("message = %q", open[0].Message)
	}
	if len(notifier.changes) != 1 || notifier.changes[0].Raised == nil {
		t.Fatalf("notified %d times, want one raise", len(notifier.changes))
	}

	lab.project2Secret(t, "broken")
	lab.renewer.RenewDue(ctx)
	if got := alerts.GetActiveAlerts(); len(got) != 0 {
		t.Fatalf("still open after the renewal worked: %+v", got)
	}
}

// A single failed pass that the next pass fixes never alerts.
func TestRenewalAlert_ABriefFailureNeverAlerts(t *testing.T) {
	lab, alerts, _ := renewalAlertLab(t)
	lab.project(t, "blip", "ACTIVE", time.Time{})
	ctx := context.Background()
	lab.renewer.RenewDue(ctx)
	lab.project2Secret(t, "blip")
	lab.now = lab.now.Add(time.Hour)
	lab.renewer.RenewDue(ctx)
	if got := alerts.GetActiveAlerts(); len(got) != 0 {
		t.Fatalf("alerts = %+v", got)
	}
}

// project2Secret gives a project a credential that is due, so the next pass renews it.
func (lab *renewalLab) project2Secret(t *testing.T, id string) {
	t.Helper()
	other := newRenewalLab(t)
	other.project(t, id, "ACTIVE", lab.now.Add(time.Hour))
	lab.mock.Secrets["org-"+id+"/"+k8s.BackupCredentialsSecretName] = other.mock.Secrets["org-"+id+"/"+k8s.BackupCredentialsSecretName]
}

func TestRenewalWithoutAlertsStillRuns(t *testing.T) {
	lab := newRenewalLab(t)
	lab.project(t, "broken", "ACTIVE", time.Time{})
	if report := lab.renewer.RenewDue(context.Background()); len(report.Failed) != 1 {
		t.Fatalf("report = %+v", report)
	}
}

// A pass that cannot list the projects says nothing about any of them: it must not close an open alert.
func TestRenewalAlert_AFailedListingResolvesNothing(t *testing.T) {
	lab, alerts, _ := renewalAlertLab(t)
	lab.project(t, "broken", "ACTIVE", time.Time{})
	ctx := context.Background()
	lab.renewer.RenewDue(ctx)
	lab.now = lab.now.Add(time.Hour)
	lab.renewer.RenewDue(ctx)
	if len(alerts.GetActiveAlerts()) != 1 {
		t.Fatal("setup: no alert raised")
	}
	lab.renewer.cfg.Instances = failingListStore{lab.store}
	lab.renewer.RenewDue(ctx)
	if len(alerts.GetActiveAlerts()) != 1 {
		t.Fatal("a failed listing resolved the alert")
	}
}

// A project that stops archiving (deleted, backups off) is no longer visited; its alert ends.
func TestRenewalAlert_AProjectThatStopsArchivingResolves(t *testing.T) {
	lab, alerts, _ := renewalAlertLab(t)
	lab.project(t, "broken", "ACTIVE", time.Time{})
	ctx := context.Background()
	lab.renewer.RenewDue(ctx)
	lab.now = lab.now.Add(time.Hour)
	lab.renewer.RenewDue(ctx)
	if err := lab.store.Delete("broken"); err != nil {
		t.Fatal(err)
	}
	lab.renewer.RenewDue(ctx)
	if got := alerts.GetActiveAlerts(); len(got) != 0 {
		t.Fatalf("alert of a deleted project stayed open: %+v", got)
	}
}

type failingListStore struct{ storage.InstanceStore }

func (failingListStore) FindAll() ([]*domain.DatabaseInstance, error) {
	return nil, errors.New("db down")
}

// EXC-784 review: a restart or leader change forgets when a failure began. The alert that is open must
// not be resolved by a failure below the threshold on the new tracker.
func TestRenewalAlert_ARestartDoesNotCloseAnOpenAlert(t *testing.T) {
	lab, alerts, _ := renewalAlertLab(t)
	lab.project(t, "broken", "ACTIVE", time.Time{})
	ctx := context.Background()
	lab.renewer.RenewDue(ctx)
	lab.now = lab.now.Add(31 * time.Minute)
	lab.renewer.RenewDue(ctx)
	if len(alerts.GetActiveAlerts()) != 1 {
		t.Fatal("setup: no alert raised")
	}

	restarted := NewBackupCredentialRenewer(lab.renewer.cfg)
	restarted.SetAlerts(alerts, nil, 30*time.Minute)
	restarted.RenewDue(ctx)
	if len(alerts.GetActiveAlerts()) != 1 {
		t.Fatal("a failing project's open alert was resolved after a restart")
	}
}
