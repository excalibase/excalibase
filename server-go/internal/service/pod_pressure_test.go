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

func TestPodPressure_AlertsAboveEightyFivePercentAndResolvesBelow(t *testing.T) {
	now := time.Now()
	source := &fakePodSource{capacity: nodePods(200, 170)} // exactly 85%: not above
	monitor, alerts := newPressure(t, source, now)
	monitor.Sample(context.Background())
	if got := alerts.GetActiveAlerts(); len(got) != 0 {
		t.Fatalf("85%% alerted: %+v", got)
	}

	source.capacity = nodePods(200, 180)
	monitor.Sample(context.Background())
	monitor.Sample(context.Background()) // still one alert while it stays high
	got := alerts.GetActiveAlerts()
	if len(got) != 1 || got[0].Metric != "cluster.pods:n1" || !strings.Contains(got[0].Message, "180 of 200") {
		t.Fatalf("alerts = %+v", got)
	}

	source.capacity = nodePods(200, 100)
	monitor.Sample(context.Background())
	if got := alerts.GetActiveAlerts(); len(got) != 0 {
		t.Fatalf("not resolved: %+v", got)
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
