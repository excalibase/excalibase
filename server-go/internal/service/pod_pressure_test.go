package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/k8s"
)

type fakePodSource struct {
	capacity k8s.ClusterCapacity
	pending  []k8s.PendingPod
	err      error
}

func (f *fakePodSource) GetClusterCapacity(context.Context) (k8s.ClusterCapacity, error) {
	return f.capacity, f.err
}
func (f *fakePodSource) PendingPods(context.Context) ([]k8s.PendingPod, error) {
	return f.pending, f.err
}

func nodePods(allocatable, running int64) k8s.ClusterCapacity {
	return k8s.ClusterCapacity{AllocatablePods: allocatable, Pods: running,
		Nodes: []k8s.NodeCapacity{{Name: "n1", AllocatablePods: allocatable, Pods: running}}}
}

func newPressure(t *testing.T, source *fakePodSource, now time.Time) (*PodPressureMonitor, *AlertingService) {
	t.Helper()
	alerts := NewAlertingService(t.TempDir())
	return NewPodPressureMonitor(source, alerts, func() time.Time { return now }), alerts
}

// Raise at 85% or more, resolve only below 80%: churn around the line is one alert, not one per minute.
func TestPodPressure_RaisesAtEightyFiveAndResolvesBelowEighty(t *testing.T) {
	source := &fakePodSource{capacity: nodePods(200, 169)} // 84.5%
	monitor, alerts := newPressure(t, source, time.Now())
	steps := []struct {
		running int64
		open    bool
	}{
		{169, false}, // below 85%: nothing yet
		{170, true},  // exactly 85%: raised
		{169, true},  // back under 85%: still open
		{160, true},  // exactly 80%: still open
		{159, false}, // below 80%: resolved
		{169, false}, // and 84.5% does not raise it again
	}
	for _, step := range steps {
		source.capacity = nodePods(200, step.running)
		monitor.Sample(context.Background())
		got := alerts.GetActiveAlerts()
		if (len(got) == 1) != step.open {
			t.Fatalf("at %d of 200 pods: open alerts = %+v, want open=%v", step.running, got, step.open)
		}
		if step.open && (got[0].Metric != "cluster.pods:n1" || !strings.Contains(got[0].Message, "of 200")) {
			t.Fatalf("alert = %+v", got[0])
		}
	}
}

func TestPodPressure_UnreportedPodLimitNeverAlerts(t *testing.T) {
	monitor, alerts := newPressure(t, &fakePodSource{capacity: nodePods(0, 500)}, time.Now())
	monitor.Sample(context.Background())
	if got := alerts.GetActiveAlerts(); len(got) != 0 {
		t.Fatalf("alerts = %+v", got)
	}
}

func TestPodPressure_PendingOverTenMinutesAlerts(t *testing.T) {
	now := time.Now()
	source := &fakePodSource{capacity: nodePods(200, 10), pending: []k8s.PendingPod{
		{Namespace: "a", Name: "young", Since: now.Add(-9 * time.Minute)},
		{Namespace: "app-x", Name: "old", Since: now.Add(-3 * time.Hour)},
	}}
	monitor, alerts := newPressure(t, source, now)
	monitor.Sample(context.Background())
	got := alerts.GetActiveAlerts()
	if len(got) != 1 || got[0].Metric != "cluster.pending" ||
		!strings.Contains(got[0].Message, "1 pod(s)") || !strings.Contains(got[0].Message, "app-x/old") {
		t.Fatalf("alerts = %+v", got)
	}

	source.pending = source.pending[:1]
	monitor.Sample(context.Background())
	if got := alerts.GetActiveAlerts(); len(got) != 0 {
		t.Fatalf("not resolved: %+v", got)
	}
}

// A read that fails neither raises nor resolves: an unanswered question is not good news.
func TestPodPressure_AFailedReadChangesNothing(t *testing.T) {
	source := &fakePodSource{capacity: nodePods(200, 190)}
	monitor, alerts := newPressure(t, source, time.Now())
	monitor.Sample(context.Background())
	source.err = errors.New("api down")
	monitor.Sample(context.Background())
	if got := alerts.GetActiveAlerts(); len(got) != 1 {
		t.Fatalf("alerts = %+v, want the open one kept", got)
	}
}

type recordingNotifier struct{ raised []string }

func (r *recordingNotifier) Notify(_ context.Context, change AlertChange) {
	if change.Raised != nil {
		r.raised = append(r.raised, change.Raised.Metric)
	}
}

// An alert nobody reads is no alert: a raise is sent to the admins, once, not on every tick.
func TestPodPressure_NotifiesOnRaiseOnly(t *testing.T) {
	now := time.Now()
	source := &fakePodSource{capacity: nodePods(200, 190), pending: []k8s.PendingPod{{Namespace: "a", Name: "old", Since: now.Add(-time.Hour)}}}
	monitor, _ := newPressure(t, source, now)
	notifier := &recordingNotifier{}
	monitor.SetNotifier(notifier)
	monitor.Sample(context.Background())
	monitor.Sample(context.Background())
	if len(notifier.raised) != 2 || notifier.raised[0] != "cluster.pods:n1" || notifier.raised[1] != "cluster.pending" {
		t.Fatalf("notified of %v, want each raise once", notifier.raised)
	}
}

// A node that is cordoned or gone is not in the capacity read; its alert must not stay open for ever.
func TestPodPressure_ResolvesTheAlertOfANodeThatIsGone(t *testing.T) {
	source := &fakePodSource{capacity: nodePods(200, 190)}
	monitor, alerts := newPressure(t, source, time.Now())
	monitor.Sample(context.Background())
	if len(alerts.GetActiveAlerts()) != 1 {
		t.Fatal("setup: the alert was not raised")
	}
	source.capacity = k8s.ClusterCapacity{}
	monitor.Sample(context.Background())
	if got := alerts.GetActiveAlerts(); len(got) != 0 {
		t.Fatalf("alert of a node that is gone stayed open: %+v", got)
	}
}

// The alert names the pod that has waited longest, whichever order the cluster lists them in.
func TestPodPressure_NamesTheOldestOfSeveralStuckPods(t *testing.T) {
	now := time.Now()
	source := &fakePodSource{capacity: nodePods(200, 10), pending: []k8s.PendingPod{
		{Namespace: "a", Name: "newer", Since: now.Add(-time.Hour)},
		{Namespace: "b", Name: "oldest", Since: now.Add(-5 * time.Hour)},
		{Namespace: "c", Name: "middle", Since: now.Add(-2 * time.Hour)},
	}}
	monitor, alerts := newPressure(t, source, now)
	monitor.Sample(context.Background())
	got := alerts.GetActiveAlerts()
	if len(got) != 1 || !strings.Contains(got[0].Message, "b/oldest") || !strings.Contains(got[0].Message, "3 pod(s)") {
		t.Fatalf("alerts = %+v", got)
	}
}
