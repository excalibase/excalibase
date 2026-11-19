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
	"github.com/excalibase/provisioning-poc/internal/storage"
)

const (
	walArchiveMetricPrefix = "wal.archiving:"
	walMonitorMetricPrefix = "wal.monitor:"
	// walMonitorProject names the one failure the monitor raises about itself.
	walMonitorProject = "clusters"
)

// ClusterLister reads custom resources; a Kubernetes client has it.
type ClusterLister interface {
	ListCRDs(ctx context.Context, gvr schema.GroupVersionResource, namespace string) ([]*unstructured.Unstructured, error)
}

// ClusterNamespaces are the namespaces whose database clusters the monitor reads.
type ClusterNamespaces func(ctx context.Context) ([]string, error)

// ProjectNamespaces lists the namespaces of the Kubernetes Postgres projects in
// store. Provisioning may read clusters in a project's namespace and nowhere
// else, so the monitor asks per namespace and never across all of them.
func ProjectNamespaces(store storage.InstanceStore) ClusterNamespaces {
	return func(context.Context) ([]string, error) {
		instances, err := store.FindAll()
		if err != nil {
			return nil, fmt.Errorf("list projects: %w", err)
		}
		seen := map[string]bool{}
		var namespaces []string
		for _, inst := range instances {
			if inst != nil && inst.DeploymentMode != domain.ModeDocker && inst.DBType == domain.PostgreSQL && !inst.NoDatabase &&
				inst.Namespace != "" && !seen[inst.Namespace] {
				seen[inst.Namespace] = true
				namespaces = append(namespaces, inst.Namespace)
			}
		}
		return namespaces, nil
	}
}

// WALArchiveMonitor raises a platform alert when a database cluster has been
// failing to archive its WAL for longer than a threshold (EXC-784). Uploads
// failed for ten days in EXC-773 and the unarchived WAL filled two disks while
// nothing told anyone. It reads CloudNativePG's own ContinuousArchiving
// condition, so it also catches a renewer that hung instead of failing, and it
// runs on the metrics loop beside the pod pressure checks (EXC-747): the
// platform has no Prometheus (EXC-633). The alert is a platform alert with no
// project, mailed to the platform admins. A monitor that cannot read the
// clusters raises its own alert, so it cannot fail silently.
type WALArchiveMonitor struct {
	clusters   ClusterLister
	namespaces ClusterNamespaces
	alerts     *AlertingService
	after      time.Duration
	now        func() time.Time
	notifier   AlertNotifier
	self       *failureAlerts
	// firstSeen ages a condition whose own time cannot be read.
	firstSeen map[string]time.Time
}

func NewWALArchiveMonitor(clusters ClusterLister, namespaces ClusterNamespaces, alerts *AlertingService, after time.Duration, now func() time.Time) *WALArchiveMonitor {
	m := &WALArchiveMonitor{clusters: clusters, namespaces: namespaces, alerts: alerts, after: after, now: now, firstSeen: map[string]time.Time{}}
	m.self = newFailureAlerts(alerts, nil, after, walMonitorMetricPrefix, describeMonitorFailure)
	return m
}

// SetNotifier sends every raised or resolved alert on; without one it is only recorded.
func (m *WALArchiveMonitor) SetNotifier(notifier AlertNotifier) {
	m.notifier = notifier
	m.self.notifier = notifier
}

func describeMonitorFailure(_ string, failing time.Duration, cause error) string {
	return fmt.Sprintf("The WAL archive check cannot read the database clusters (%v) and has not for %s, so a database that cannot archive would go unnoticed. Check provisioning's access to the clusters resource in the project namespaces",
		cause, failing)
}

// Sample checks once. A namespace that cannot be read raises and resolves
// nothing for its clusters, and a read that keeps failing is its own alert.
func (m *WALArchiveMonitor) Sample(ctx context.Context) {
	now := m.now()
	namespaces, err := m.namespaces(ctx)
	if err != nil {
		log.Printf("ERROR: wal archive monitor: list project namespaces: %v", err)
		m.self.track(ctx, now, map[string]error{walMonitorProject: err}, nil)
		return
	}
	var clusters []*unstructured.Unstructured
	unreadable := map[string]bool{}
	var firstFailure error
	for _, namespace := range namespaces {
		listed, err := m.clusters.ListCRDs(ctx, k8s.CNPGClusterGVR, namespace)
		if err != nil {
			log.Printf("ERROR: wal archive monitor: list clusters in %s: %v", namespace, err)
			unreadable[namespace] = true
			if firstFailure == nil {
				firstFailure = fmt.Errorf("namespace %s: %w", namespace, err)
			}
			continue
		}
		clusters = append(clusters, listed...)
	}
	if firstFailure != nil {
		m.self.track(ctx, now, map[string]error{walMonitorProject: firstFailure}, nil)
	} else {
		m.self.track(ctx, now, nil, []string{walMonitorProject})
	}
	failing := map[string]bool{}
	for _, failure := range k8s.ArchivingFailures(clusters) {
		metric := walArchiveMetricPrefix + failure.Namespace + "/" + failure.Cluster
		failing[metric] = true
		since := m.since(metric, failure, now)
		change := m.alerts.Track("", metric, m.isOpen(metric) || now.Sub(since) >= m.after, func() domain.Alert {
			return m.alertFor(failure, now.Sub(since), now)
		})
		m.notify(ctx, change)
	}
	m.resolveRecovered(ctx, failing, unreadable)
}

func (m *WALArchiveMonitor) isOpen(metric string) bool {
	for _, open := range m.alerts.GetActiveAlerts() {
		if open.ProjectID == "" && open.Metric == metric {
			return true
		}
	}
	return false
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
// hibernated, or is gone. The alerts of a namespace that could not be read are
// left as they are.
func (m *WALArchiveMonitor) resolveRecovered(ctx context.Context, failing, unreadable map[string]bool) {
	for metric := range m.firstSeen {
		if !failing[metric] {
			delete(m.firstSeen, metric)
		}
	}
	for _, open := range m.alerts.GetActiveAlerts() {
		if open.ProjectID != "" || !strings.HasPrefix(open.Metric, walArchiveMetricPrefix) || failing[open.Metric] {
			continue
		}
		namespace, _, _ := strings.Cut(strings.TrimPrefix(open.Metric, walArchiveMetricPrefix), "/")
		if !unreadable[namespace] {
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
