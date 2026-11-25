package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// Metric prefixes of the platform alerts an automatic retry raises once it has
// failed too often (EXC-873). One alert per project and prefix.
const (
	StuckRetryMetricPrefix    = "selfheal.stuck:"
	RollbackRetryMetricPrefix = "selfheal.retry:"
)

// DefaultRetryAlertAfter is how many failed automatic attempts hand a project
// to staff.
const DefaultRetryAlertAfter = 5

// RetryEscalator turns an automatic retry that keeps failing into a platform
// alert, mailed to the platform admins like the backup-credential alert. The
// customer is told the platform is retrying; staff are told when it cannot.
// A nil escalator does nothing.
type RetryEscalator struct {
	alerts   *AlertingService
	notifier AlertNotifier
	after    int
	now      func() time.Time
}

// NewRetryEscalator raises an alert once a project has failed after attempts.
// It is nil without an alert service, which every caller treats as off.
func NewRetryEscalator(alerts *AlertingService, notifier AlertNotifier, after int) *RetryEscalator {
	if alerts == nil {
		return nil
	}
	if after <= 0 {
		after = DefaultRetryAlertAfter
	}
	return &RetryEscalator{alerts: alerts, notifier: notifier, after: after, now: time.Now}
}

// Failed records that attempts have failed so far; at the limit it raises the
// alert, once, and keeps it open until Reconcile closes it.
func (e *RetryEscalator) Failed(ctx context.Context, prefix, projectID, what string, attempts int, cause error) {
	if e == nil || attempts < e.after {
		return
	}
	now := e.now()
	change := e.alerts.Track("", prefix+projectID, true, func() domain.Alert {
		return domain.Alert{
			ID: fmt.Sprintf("%s%s-%d", prefix, projectID, now.UnixNano()), Severity: "CRITICAL",
			Message:   fmt.Sprintf("The automatic %s of project %s has failed %d times: %v. It keeps retrying; it needs a person", what, projectID, attempts, cause),
			Timestamp: &domain.FlexTime{Time: now},
		}
	})
	e.notify(ctx, change)
}

// Reconcile closes every open alert under prefix whose project is not in
// failing: it recovered, or it is gone.
func (e *RetryEscalator) Reconcile(ctx context.Context, prefix string, failing map[string]bool) {
	if e == nil {
		return
	}
	for _, open := range e.alerts.GetActiveAlerts() {
		project, ours := strings.CutPrefix(open.Metric, prefix)
		if open.ProjectID != "" || !ours || failing[project] {
			continue
		}
		e.notify(ctx, e.alerts.Track("", open.Metric, false, nil))
	}
}

func (e *RetryEscalator) notify(ctx context.Context, change AlertChange) {
	if e.notifier != nil && (change.Raised != nil || change.Resolved != nil) {
		e.notifier.Notify(ctx, change)
	}
}
