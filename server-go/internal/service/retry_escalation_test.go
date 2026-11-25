package service

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

type mailCountingNotifier struct {
	mu      sync.Mutex
	raised  int
	cleared int
}

func (n *mailCountingNotifier) Notify(_ context.Context, change AlertChange) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if change.Raised != nil {
		n.raised++
	}
	if change.Resolved != nil {
		n.cleared++
	}
}

func openPlatformAlerts(t *testing.T, alerts *AlertingService) map[string]bool {
	t.Helper()
	open, err := alerts.OpenPlatformMetrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return open
}

func TestRetryEscalator_StaysQuietBelowTheLimit(t *testing.T) {
	alerts := NewAlertingService(t.TempDir())
	notifier := &mailCountingNotifier{}
	escalator := NewRetryEscalator(alerts, notifier, 3)

	escalator.Failed(context.Background(), StuckRetryMetricPrefix, "p1", "pause", 2, errors.New("boom"))

	if open := openPlatformAlerts(t, alerts); len(open) != 0 || notifier.raised != 0 {
		t.Fatalf("alert raised after 2 of 3 attempts: %v, mails %d", open, notifier.raised)
	}
}

func TestRetryEscalator_RaisesOnceAtTheLimitAndNamesTheProject(t *testing.T) {
	alerts := NewAlertingService(t.TempDir())
	notifier := &mailCountingNotifier{}
	escalator := NewRetryEscalator(alerts, notifier, 3)

	for attempts := 3; attempts <= 6; attempts++ {
		escalator.Failed(context.Background(), StuckRetryMetricPrefix, "p1", "pause", attempts, errors.New("boom"))
	}

	if open := openPlatformAlerts(t, alerts); !open[StuckRetryMetricPrefix+"p1"] || len(open) != 1 {
		t.Fatalf("open alerts = %v", open)
	}
	if notifier.raised != 1 {
		t.Fatalf("staff told %d times, want once", notifier.raised)
	}
	active := alerts.GetActiveAlerts()
	if len(active) != 1 || active[0].Severity != "CRITICAL" || active[0].ProjectID != "" {
		t.Fatalf("alert = %+v, want one CRITICAL platform alert", active)
	}
}

func TestRetryEscalator_ReconcileClosesOnlyWhatStoppedFailing(t *testing.T) {
	alerts := NewAlertingService(t.TempDir())
	notifier := &mailCountingNotifier{}
	escalator := NewRetryEscalator(alerts, notifier, 1)
	escalator.Failed(context.Background(), StuckRetryMetricPrefix, "p1", "pause", 1, errors.New("x"))
	escalator.Failed(context.Background(), StuckRetryMetricPrefix, "p2", "resume", 1, errors.New("x"))
	alerts.Track("", "wal.archiving:ns/p9", true, func() domain.Alert { return domain.Alert{ID: "w", Severity: "CRITICAL"} })

	escalator.Reconcile(context.Background(), StuckRetryMetricPrefix, map[string]bool{"p2": true})

	open := openPlatformAlerts(t, alerts)
	if open[StuckRetryMetricPrefix+"p1"] || !open[StuckRetryMetricPrefix+"p2"] || !open["wal.archiving:ns/p9"] {
		t.Fatalf("open after reconcile = %v", open)
	}
	if notifier.cleared != 1 {
		t.Fatalf("resolved mails = %d, want 1", notifier.cleared)
	}
}

func TestRetryEscalator_NilIsOff(t *testing.T) {
	var escalator *RetryEscalator
	escalator.Failed(context.Background(), StuckRetryMetricPrefix, "p1", "pause", 99, errors.New("x"))
	escalator.Reconcile(context.Background(), StuckRetryMetricPrefix, nil)
	if NewRetryEscalator(nil, nil, 3) != nil {
		t.Fatal("no alert service must give no escalator")
	}
}
