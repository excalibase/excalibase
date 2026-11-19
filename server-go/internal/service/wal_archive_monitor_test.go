package service

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// fakeClusterLister answers per namespace, as the real client does: provisioning
// may read clusters in a project's namespace and nowhere else.
type fakeClusterLister struct {
	byNamespace map[string][]*unstructured.Unstructured
	errs        map[string]error
	asked       []string
}

func (f *fakeClusterLister) ListCRDs(_ context.Context, _ schema.GroupVersionResource, namespace string) ([]*unstructured.Unstructured, error) {
	f.asked = append(f.asked, namespace)
	if err := f.errs[namespace]; err != nil {
		return nil, err
	}
	return f.byNamespace[namespace], nil
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

func clusterListForbidden() error {
	return apierrors.NewForbidden(schema.GroupResource{Group: "postgresql.cnpg.io", Resource: "clusters"}, "", errors.New("cannot list"))
}

func lister(clusters ...*unstructured.Unstructured) *fakeClusterLister {
	f := &fakeClusterLister{byNamespace: map[string][]*unstructured.Unstructured{}, errs: map[string]error{}}
	for _, cluster := range clusters {
		f.byNamespace[cluster.GetNamespace()] = append(f.byNamespace[cluster.GetNamespace()], cluster)
	}
	return f
}

func newWALMonitor(t *testing.T, l *fakeClusterLister, namespaces []string, clock *time.Time) (*WALArchiveMonitor, *AlertingService, *walNotifier) {
	t.Helper()
	alerts := NewAlertingService(t.TempDir())
	notifier := &walNotifier{}
	monitor := NewWALArchiveMonitor(l, func(context.Context) ([]string, error) { return namespaces, nil }, alerts, 30*time.Minute, func() time.Time { return *clock })
	monitor.SetNotifier(notifier)
	return monitor, alerts, notifier
}

func openMetrics(alerts *AlertingService) []string {
	var metrics []string
	for _, open := range alerts.GetActiveAlerts() {
		metrics = append(metrics, open.Metric)
	}
	return metrics
}

// EXC-784: nothing alerted for ten days while uploads failed. Raise after the threshold, once.
func TestWALArchiveMonitor_RaisesAfterTheThresholdOnceAndResolvesOnRecovery(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	l := lister(archivingCluster("p1", "False", now.Add(-29*time.Minute), false))
	monitor, alerts, notifier := newWALMonitor(t, l, []string{"ns-p1"}, &now)

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

	l.byNamespace["ns-p1"] = []*unstructured.Unstructured{archivingCluster("p1", "True", now, false)}
	monitor.Sample(context.Background())
	if got := alerts.GetActiveAlerts(); len(got) != 0 {
		t.Fatalf("still open after recovery: %+v", got)
	}
}

// Mutant: > for >=. A cluster failing for exactly the threshold is already past it.
func TestWALArchiveMonitor_RaisesAtExactlyTheThreshold(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	monitor, alerts, _ := newWALMonitor(t, lister(archivingCluster("p1", "False", now.Add(-30*time.Minute), false)), []string{"ns-p1"}, &now)
	monitor.Sample(context.Background())
	if len(alerts.GetActiveAlerts()) != 1 {
		t.Fatal("not raised at exactly 30 minutes")
	}
}

func TestWALArchiveMonitor_HibernatedClusterNeverAlertsAndClearsItsAlert(t *testing.T) {
	now := time.Now()
	l := lister(archivingCluster("p1", "False", now.Add(-5*time.Hour), false))
	monitor, alerts, _ := newWALMonitor(t, l, []string{"ns-p1"}, &now)
	monitor.Sample(context.Background())
	if len(alerts.GetActiveAlerts()) != 1 {
		t.Fatal("setup: no alert raised")
	}
	l.byNamespace["ns-p1"] = []*unstructured.Unstructured{archivingCluster("p1", "False", now.Add(-5*time.Hour), true)}
	monitor.Sample(context.Background())
	if got := alerts.GetActiveAlerts(); len(got) != 0 {
		t.Fatalf("a paused project's alert stayed open: %+v", got)
	}
}

func TestWALArchiveMonitor_DeletedClusterClearsItsAlert(t *testing.T) {
	now := time.Now()
	l := lister(archivingCluster("p1", "False", now.Add(-time.Hour), false))
	monitor, alerts, _ := newWALMonitor(t, l, []string{"ns-p1"}, &now)
	monitor.Sample(context.Background())
	l.byNamespace["ns-p1"] = nil
	monitor.Sample(context.Background())
	if got := alerts.GetActiveAlerts(); len(got) != 0 {
		t.Fatalf("alert for a deleted cluster stayed open: %+v", got)
	}
}

// The monitor reads only the namespaces projects live in: provisioning has no cluster-wide grant on clusters.
func TestWALArchiveMonitor_ListsEachProjectNamespaceNeverAllNamespaces(t *testing.T) {
	now := time.Now()
	l := lister()
	monitor, _, _ := newWALMonitor(t, l, []string{"ns-a", "ns-b"}, &now)
	monitor.Sample(context.Background())
	if strings.Join(l.asked, ",") != "ns-a,ns-b" {
		t.Fatalf("listed %q, want one list per project namespace and none for \"\"", l.asked)
	}
}

// EXC-784 review: a 403 on the clusters resource made the monitor log and stay silent, which is the
// original failure again. A persistent read failure is itself an alert, and the run is marked failed.
func TestWALArchiveMonitor_ForbiddenReadRaisesItsOwnAlertAfterTheThreshold(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	l := lister()
	l.errs["ns-a"] = clusterListForbidden()
	monitor, alerts, notifier := newWALMonitor(t, l, []string{"ns-a"}, &now)
	var logged bytes.Buffer
	log.SetOutput(&logged)
	defer log.SetOutput(os.Stderr)

	monitor.Sample(context.Background())
	if !strings.Contains(logged.String(), "ERROR: wal archive monitor") || !strings.Contains(logged.String(), "ns-a") {
		t.Fatalf("a forbidden read was not logged as an error: %q", logged.String())
	}
	if got := alerts.GetActiveAlerts(); len(got) != 0 {
		t.Fatalf("raised on the first failed read: %+v", got)
	}

	now = now.Add(31 * time.Minute)
	monitor.Sample(context.Background())
	open := alerts.GetActiveAlerts()
	if len(open) != 1 || open[0].Metric != "wal.monitor:clusters" || !strings.Contains(open[0].Message, "forbidden") {
		t.Fatalf("open alerts = %+v", open)
	}
	if len(notifier.changes) != 1 {
		t.Fatalf("notified %d times, want one raise", len(notifier.changes))
	}

	delete(l.errs, "ns-a")
	monitor.Sample(context.Background())
	if got := alerts.GetActiveAlerts(); len(got) != 0 {
		t.Fatalf("the monitor alert stayed open after reads worked: %+v", got)
	}
}

// One unreadable namespace does not blind the monitor to the rest, and does not close the alerts it owns.
func TestWALArchiveMonitor_AnUnreadableNamespaceKeepsItsAlertsAndSparesTheRest(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	l := lister(archivingCluster("a", "False", now.Add(-time.Hour), false), archivingCluster("b", "False", now.Add(-time.Hour), false))
	monitor, alerts, _ := newWALMonitor(t, l, []string{"ns-a", "ns-b"}, &now)
	monitor.Sample(context.Background())
	if len(alerts.GetActiveAlerts()) != 2 {
		t.Fatal("setup: two alerts expected")
	}

	l.errs["ns-a"] = clusterListForbidden()
	l.byNamespace["ns-b"] = []*unstructured.Unstructured{archivingCluster("b", "True", now, false)}
	monitor.Sample(context.Background())
	got := openMetrics(alerts)
	if len(got) != 1 || got[0] != "wal.archiving:ns-a/a" {
		t.Fatalf("open = %v, want only the unreadable namespace's alert kept", got)
	}
}

func TestWALArchiveMonitor_AFailingProjectListIsAMonitorFailure(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	alerts := NewAlertingService(t.TempDir())
	monitor := NewWALArchiveMonitor(lister(), func(context.Context) ([]string, error) { return nil, errors.New("db down") },
		alerts, 30*time.Minute, func() time.Time { return now })
	monitor.Sample(context.Background())
	now = now.Add(time.Hour)
	monitor.Sample(context.Background())
	if got := openMetrics(alerts); len(got) != 1 || got[0] != "wal.monitor:clusters" {
		t.Fatalf("open = %v", got)
	}
}

// A condition whose time cannot be read is aged from the first sample that saw it.
func TestWALArchiveMonitor_UnreadableTimeIsAgedFromFirstSight(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	cluster := archivingCluster("p1", "False", now, false)
	conditions, _, _ := unstructured.NestedSlice(cluster.Object, "status", "conditions")
	conditions[0].(map[string]interface{})["lastTransitionTime"] = "garbage"
	_ = unstructured.SetNestedSlice(cluster.Object, conditions, "status", "conditions")
	monitor, alerts, _ := newWALMonitor(t, lister(cluster), []string{"ns-p1"}, &now)

	monitor.Sample(context.Background())
	if len(alerts.GetActiveAlerts()) != 0 {
		t.Fatal("raised on first sight")
	}
	now = now.Add(31 * time.Minute)
	monitor.Sample(context.Background())
	if len(alerts.GetActiveAlerts()) != 1 {
		t.Fatal("not raised 31 minutes after first sight")
	}
	// A restart forgets first sight; the alert that is open must stay open.
	restarted := NewWALArchiveMonitor(lister(cluster), func(context.Context) ([]string, error) { return []string{"ns-p1"}, nil },
		alerts, 30*time.Minute, func() time.Time { return now })
	restarted.Sample(context.Background())
	if len(alerts.GetActiveAlerts()) != 1 {
		t.Fatal("a restart resolved an open alert")
	}
}

func TestProjectNamespaces_OnlyKubernetesPostgresProjectsOncePerNamespace(t *testing.T) {
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	add := func(id, namespace string, engine domain.DatabaseType, mode domain.DeploymentMode, noDatabase bool) {
		_ = store.Create(&domain.DatabaseInstance{ProjectID: id, OrgID: "org", Namespace: namespace, DBType: engine, DeploymentMode: mode, NoDatabase: noDatabase})
	}
	add("a", "ns-a", domain.PostgreSQL, domain.ModeK8s, false)
	add("a2", "ns-a", domain.PostgreSQL, domain.ModeK8s, false)
	add("b", "ns-b", domain.PostgreSQL, domain.ModeK8s, false)
	add("host", "ctr", domain.PostgreSQL, domain.ModeDocker, false)
	add("mysql", "ns-m", domain.MySQL, domain.ModeK8s, false)
	add("empty", "ns-e", domain.PostgreSQL, domain.ModeK8s, true)

	got, err := ProjectNamespaces(store)(context.Background())
	sort.Strings(got)
	if err != nil || strings.Join(got, ",") != "ns-a,ns-b" {
		t.Fatalf("namespaces = %v, err %v", got, err)
	}
	if _, err := ProjectNamespaces(failingListStore{store})(context.Background()); err == nil {
		t.Fatal("a failing project list was not reported")
	}
}
