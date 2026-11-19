package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type fakeClusterLister struct {
	clusters []*unstructured.Unstructured
	err      error
}

func (f *fakeClusterLister) ListCRDs(context.Context, schema.GroupVersionResource, string) ([]*unstructured.Unstructured, error) {
	return f.clusters, f.err
}

type walNotifier struct{ changes []AlertChange }

func (r *walNotifier) Notify(_ context.Context, change AlertChange) {
	r.changes = append(r.changes, change)
}

func archivingCluster(name, status string, since time.Time, hibernated bool) *unstructured.Unstructured {
	cluster := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": name, "namespace": "ns-" + name},
		"status": map[string]interface{}{"conditions": []interface{}{
			map[string]interface{}{"type": "ContinuousArchiving", "status": status, "lastTransitionTime": since.UTC().Format(time.RFC3339)},
		}},
	}}
	if hibernated {
		cluster.SetAnnotations(map[string]string{"cnpg.io/hibernation": "on"})
	}
	return cluster
}

func newWALMonitor(t *testing.T, lister *fakeClusterLister, clock *time.Time) (*WALArchiveMonitor, *AlertingService, *walNotifier) {
	t.Helper()
	alerts := NewAlertingService(t.TempDir())
	notifier := &walNotifier{}
	monitor := NewWALArchiveMonitor(lister, alerts, 30*time.Minute, func() time.Time { return *clock })
	monitor.SetNotifier(notifier)
	return monitor, alerts, notifier
}

// EXC-784: nothing alerted for ten days while uploads failed. Raise after the threshold, once.
func TestWALArchiveMonitor_RaisesAfterTheThresholdOnceAndResolvesOnRecovery(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	lister := &fakeClusterLister{clusters: []*unstructured.Unstructured{archivingCluster("p1", "False", now.Add(-29*time.Minute), false)}}
	monitor, alerts, notifier := newWALMonitor(t, lister, &now)

	monitor.Sample(context.Background())
	if got := alerts.GetActiveAlerts(); len(got) != 0 {
		t.Fatalf("raised after 29 minutes: %+v", got)
	}

	now = now.Add(2 * time.Minute)
	monitor.Sample(context.Background())
	monitor.Sample(context.Background())
	open := alerts.GetActiveAlerts()
	if len(open) != 1 || open[0].Metric != "wal.archiving:ns-p1/p1" || open[0].ProjectID != "" {
		t.Fatalf("open alerts = %+v", open)
	}
	if !strings.Contains(open[0].Message, "ns-p1") || !strings.Contains(open[0].Message, "31m") {
		t.Fatalf("message = %q", open[0].Message)
	}
	if len(notifier.changes) != 1 || notifier.changes[0].Raised == nil {
		t.Fatalf("notified %+v, want one raise", notifier.changes)
	}

	lister.clusters = []*unstructured.Unstructured{archivingCluster("p1", "True", now, false)}
	monitor.Sample(context.Background())
	if got := alerts.GetActiveAlerts(); len(got) != 0 {
		t.Fatalf("still open after recovery: %+v", got)
	}
}

func TestWALArchiveMonitor_HibernatedClusterNeverAlertsAndClearsItsAlert(t *testing.T) {
	now := time.Now()
	lister := &fakeClusterLister{clusters: []*unstructured.Unstructured{archivingCluster("p1", "False", now.Add(-5*time.Hour), false)}}
	monitor, alerts, _ := newWALMonitor(t, lister, &now)
	monitor.Sample(context.Background())
	if len(alerts.GetActiveAlerts()) != 1 {
		t.Fatal("setup: no alert raised")
	}

	lister.clusters = []*unstructured.Unstructured{archivingCluster("p1", "False", now.Add(-5*time.Hour), true)}
	monitor.Sample(context.Background())
	if got := alerts.GetActiveAlerts(); len(got) != 0 {
		t.Fatalf("a paused project's alert stayed open: %+v", got)
	}
}

func TestWALArchiveMonitor_DeletedClusterClearsItsAlert(t *testing.T) {
	now := time.Now()
	lister := &fakeClusterLister{clusters: []*unstructured.Unstructured{archivingCluster("p1", "False", now.Add(-time.Hour), false)}}
	monitor, alerts, _ := newWALMonitor(t, lister, &now)
	monitor.Sample(context.Background())

	lister.clusters = nil
	monitor.Sample(context.Background())
	if got := alerts.GetActiveAlerts(); len(got) != 0 {
		t.Fatalf("alert for a deleted cluster stayed open: %+v", got)
	}
}

// A failed read says nothing about the clusters: no raise, no resolve.
func TestWALArchiveMonitor_FailedReadChangesNothing(t *testing.T) {
	now := time.Now()
	lister := &fakeClusterLister{clusters: []*unstructured.Unstructured{archivingCluster("p1", "False", now.Add(-time.Hour), false)}}
	monitor, alerts, _ := newWALMonitor(t, lister, &now)
	monitor.Sample(context.Background())

	lister.err = errors.New("apiserver down")
	monitor.Sample(context.Background())
	if len(alerts.GetActiveAlerts()) != 1 {
		t.Fatal("a failed read resolved the alert")
	}
}

// A condition whose time cannot be read is aged from the first sample that saw it.
func TestWALArchiveMonitor_UnreadableTimeIsAgedFromFirstSight(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	cluster := archivingCluster("p1", "False", now, false)
	conditions, _, _ := unstructured.NestedSlice(cluster.Object, "status", "conditions")
	conditions[0].(map[string]interface{})["lastTransitionTime"] = "garbage"
	_ = unstructured.SetNestedSlice(cluster.Object, conditions, "status", "conditions")
	monitor, alerts, _ := newWALMonitor(t, &fakeClusterLister{clusters: []*unstructured.Unstructured{cluster}}, &now)

	monitor.Sample(context.Background())
	if len(alerts.GetActiveAlerts()) != 0 {
		t.Fatal("raised on first sight")
	}
	now = now.Add(31 * time.Minute)
	monitor.Sample(context.Background())
	if len(alerts.GetActiveAlerts()) != 1 {
		t.Fatal("not raised 31 minutes after first sight")
	}
}
