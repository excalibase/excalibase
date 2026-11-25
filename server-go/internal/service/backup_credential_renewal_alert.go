package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

const renewalMetricPrefix = "backup.renewal:"

// failureAlerts raises a platform alert per project whose work has failed on
// every pass for after, and resolves it once a pass succeeds or the project is
// no longer visited (EXC-784). The failure time lives in memory: a restart
// starts the clock again.
type failureAlerts struct {
	alerts       *AlertingService
	notifier     AlertNotifier
	after        time.Duration
	prefix       string
	describe     func(project string, failing time.Duration, cause error) string
	failingSince map[string]time.Time
}

func newFailureAlerts(alerts *AlertingService, notifier AlertNotifier, after time.Duration, prefix string, describe func(string, time.Duration, error) string) *failureAlerts {
	return &failureAlerts{alerts: alerts, notifier: notifier, after: after, prefix: prefix, describe: describe, failingSince: map[string]time.Time{}}
}

// track takes one pass: the projects that failed with their cause, and those that worked.
func (f *failureAlerts) track(ctx context.Context, now time.Time, failures map[string]error, healthy []string) {
	for project, cause := range failures {
		since, failing := f.failingSince[project]
		if !failing {
			since = now
			f.failingSince[project] = now
		}
		// An alert that is already open stays open on any failure: after a restart
		// or a leader change the clock above starts again, and that must not
		// close it. Only a success or a project that is gone does.
		breached := f.isOpen(f.prefix+project) || now.Sub(since) >= f.after
		change := f.alerts.Track("", f.prefix+project, breached, func() domain.Alert {
			return domain.Alert{
				ID: fmt.Sprintf("%s%s-%d", f.prefix, project, now.UnixNano()), Severity: "CRITICAL",
				Message: f.describe(project, now.Sub(since).Round(time.Minute), cause), Timestamp: &domain.FlexTime{Time: now},
			}
		})
		f.notify(ctx, change)
	}
	for _, project := range healthy {
		delete(f.failingSince, project)
		f.notify(ctx, f.alerts.Track("", f.prefix+project, false, nil))
	}
	f.resolveGone(ctx, failures, healthy)
}

func (f *failureAlerts) isOpen(metric string) bool {
	for _, open := range f.alerts.GetActiveAlerts() {
		if open.ProjectID == "" && open.Metric == metric {
			return true
		}
	}
	return false
}

// resolveGone closes the alert of a project the pass no longer visits (deleted,
// backups off, paused).
func (f *failureAlerts) resolveGone(ctx context.Context, failures map[string]error, healthy []string) {
	seen := map[string]bool{}
	for project := range failures {
		seen[project] = true
	}
	for _, project := range healthy {
		seen[project] = true
	}
	for project := range f.failingSince {
		if !seen[project] {
			delete(f.failingSince, project)
		}
	}
	for _, open := range f.alerts.GetActiveAlerts() {
		project, ours := strings.CutPrefix(open.Metric, f.prefix)
		if open.ProjectID == "" && ours && !seen[project] {
			f.notify(ctx, f.alerts.Track("", open.Metric, false, nil))
		}
	}
}

func (f *failureAlerts) notify(ctx context.Context, change AlertChange) {
	if f.notifier != nil && (change.Raised != nil || change.Resolved != nil) {
		f.notifier.Notify(ctx, change)
	}
}

func describeRenewalFailure(project string, failing time.Duration, cause error) string {
	return fmt.Sprintf("The backup credential of project %s could not be renewed for %s: %v. Its WAL archiving stops when the current credential expires",
		project, failing, cause)
}
