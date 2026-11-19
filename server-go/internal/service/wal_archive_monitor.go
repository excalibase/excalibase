package service

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

const walArchiveMetricPrefix = "wal.archiving:"

// ClusterLister reads custom resources; a Kubernetes client has it.
type ClusterLister interface {
	ListCRDs(ctx context.Context, gvr schema.GroupVersionResource, namespace string) ([]*unstructured.Unstructured, error)
}

// WALArchiveMonitor raises a platform alert when a database cluster has been
// failing to archive its WAL for longer than a threshold (EXC-784). Uploads
// failed for ten days in EXC-773 and the unarchived WAL filled two disks while
// nothing told anyone. It reads CloudNativePG's own ContinuousArchiving
// condition, so it also catches a renewer that hung instead of failing, and it
// runs on the metrics loop beside the pod pressure checks (EXC-747): the
// platform has no Prometheus (EXC-633). The alert is a platform alert with no
// project, mailed to the platform admins.
type WALArchiveMonitor struct {
	clusters ClusterLister
	alerts   *AlertingService
	after    time.Duration
	now      func() time.Time
	notifier AlertNotifier
	// firstSeen ages a condition whose own time cannot be read.
	firstSeen map[string]time.Time
}

func NewWALArchiveMonitor(clusters ClusterLister, alerts *AlertingService, after time.Duration, now func() time.Time) *WALArchiveMonitor {
	return &WALArchiveMonitor{clusters: clusters, alerts: alerts, after: after, now: now, firstSeen: map[string]time.Time{}}
}

// SetNotifier sends every raised or resolved alert on; without one it is only recorded.
func (m *WALArchiveMonitor) SetNotifier(notifier AlertNotifier) { m.notifier = notifier }

// Sample checks once. A read that fails raises and resolves nothing.
func (m *WALArchiveMonitor) Sample(ctx context.Context) {
	clusters, err := m.clusters.ListCRDs(ctx, k8s.CNPGClusterGVR, "")
	if err != nil {
		log.Printf("ERROR: wal archive monitor: list clusters: %v", err)
		return
	}
	now := m.now()
	failing := map[string]bool{}
	for _, failure := range k8s.ArchivingFailures(clusters) {
		metric := walArchiveMetricPrefix + failure.Namespace + "/" + failure.Cluster
		failing[metric] = true
		since := m.since(metric, failure, now)
		change := m.alerts.Track("", metric, now.Sub(since) >= m.after, func() domain.Alert {
			return m.alertFor(failure, now.Sub(since), now)
		})
		m.notify(ctx, change)
	}
	m.resolveRecovered(ctx, failing)
}

func (m *WALArchiveMonitor) since(metric string, failure k8s.ArchivingFailure, now time.Time) time.Time {
	if !failure.Since.IsZero() {
		return failure.Since
	}
	if seen, ok := m.firstSeen[metric]; ok {
		return seen
	}
	m.firstSeen[metric] = now
	return now
}

// resolveRecovered closes the alert of a cluster that archives again, was
// hibernated, or is gone.
func (m *WALArchiveMonitor) resolveRecovered(ctx context.Context, failing map[string]bool) {
	for metric := range m.firstSeen {
		if !failing[metric] {
			delete(m.firstSeen, metric)
		}
	}
	for _, open := range m.alerts.GetActiveAlerts() {
		if open.ProjectID == "" && strings.HasPrefix(open.Metric, walArchiveMetricPrefix) && !failing[open.Metric] {
			m.notify(ctx, m.alerts.Track("", open.Metric, false, nil))
		}
	}
}

func (m *WALArchiveMonitor) alertFor(failure k8s.ArchivingFailure, failing time.Duration, now time.Time) domain.Alert {
	return domain.Alert{
		ID:       fmt.Sprintf("wal.archiving-%s-%s-%d", failure.Namespace, failure.Cluster, now.UnixNano()),
		Severity: "CRITICAL",
		Message: fmt.Sprintf("Database %s in namespace %s has failed to archive WAL for %s; the unarchived WAL fills its disk and point-in-time recovery stops at the last upload. Check its backup credential and the object store",
			failure.Cluster, failure.Namespace, failing.Round(time.Minute)),
		Timestamp: &domain.FlexTime{Time: now},
	}
}

func (m *WALArchiveMonitor) notify(ctx context.Context, change AlertChange) {
	if m.notifier != nil && (change.Raised != nil || change.Resolved != nil) {
		m.notifier.Notify(ctx, change)
	}
}
