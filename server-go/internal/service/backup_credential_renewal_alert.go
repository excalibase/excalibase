package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

const renewalMetricPrefix = "backup.renewal:"

// trackAlerts raises the alert of a project whose renewal has failed since
// alertAfter ago and resolves the one of a project that renewed, or had nothing
// to renew, this pass. A pass that could not even list the projects changes
// nothing.
func (r *BackupCredentialRenewer) trackAlerts(ctx context.Context, report RenewalReport) {
	if r.alerts == nil || !report.listed {
		return
	}
	now := r.cfg.Now()
	for project, cause := range report.failures {
		since, failing := r.failingSince[project]
		if !failing {
			since = now
			r.failingSince[project] = now
		}
		change := r.alerts.Track("", renewalMetricPrefix+project, now.Sub(since) >= r.alertAfter, func() domain.Alert {
			return domain.Alert{
				ID: fmt.Sprintf("backup.renewal-%s-%d", project, now.UnixNano()), Severity: "CRITICAL",
				Message: fmt.Sprintf("The backup credential of project %s could not be renewed for %s: %v. Its WAL archiving stops when the current credential expires",
					project, now.Sub(since).Round(time.Minute), cause),
				Timestamp: &domain.FlexTime{Time: now},
			}
		})
		r.notify(ctx, change)
	}
	for _, project := range report.healthy {
		delete(r.failingSince, project)
		r.notify(ctx, r.alerts.Track("", renewalMetricPrefix+project, false, nil))
	}
	r.resolveGone(ctx, report)
}

// resolveGone closes the alert of a project that no longer archives (deleted,
// backups off), which the pass no longer visits.
func (r *BackupCredentialRenewer) resolveGone(ctx context.Context, report RenewalReport) {
	seen := map[string]bool{}
	for project := range report.failures {
		seen[project] = true
	}
	for _, project := range report.healthy {
		seen[project] = true
	}
	for project := range r.failingSince {
		if !seen[project] {
			delete(r.failingSince, project)
		}
	}
	for _, open := range r.alerts.GetActiveAlerts() {
		project, ours := strings.CutPrefix(open.Metric, renewalMetricPrefix)
		if open.ProjectID == "" && ours && !seen[project] {
			r.notify(ctx, r.alerts.Track("", open.Metric, false, nil))
		}
	}
}

func (r *BackupCredentialRenewer) notify(ctx context.Context, change AlertChange) {
	if r.notifier != nil && (change.Raised != nil || change.Resolved != nil) {
		r.notifier.Notify(ctx, change)
	}
}
