package service

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

type poolClock struct{ at time.Time }

func (c *poolClock) now() time.Time { return c.at }

type recordingNotifier struct{ raised, resolved int }

func (n *recordingNotifier) Notify(_ context.Context, change AlertChange) {
	if change.Raised != nil {
		n.raised++
	}
	if change.Resolved != nil {
		n.resolved++
	}
}

func poolMonitor(t *testing.T, inUse *int) (*PoolPressureMonitor, *poolClock, *recordingNotifier, *AlertingService) {
	t.Helper()
	alerts := NewAlertingService(t.TempDir())
	clock := &poolClock{at: time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)}
	monitor := NewPoolPressureMonitor(func() sql.DBStats {
		return sql.DBStats{MaxOpenConnections: 20, InUse: *inUse}
	}, alerts, clock.now)
	notifier := &recordingNotifier{}
	monitor.SetNotifier(notifier)
	return monitor, clock, notifier, alerts
}

func openPoolAlerts(alerts *AlertingService) int {
	count := 0
	for _, alert := range alerts.GetActiveAlerts() {
		if alert.Metric == poolMetric && !alert.Resolved {
			count++
		}
	}
	return count
}

// A pool pinned at its ceiling for five minutes raises one alert; a pool that
// is merely busy for a moment raises nothing.
func TestPoolPressure_RaisesOnlyAfterFiveMinutesAtTheCeiling(t *testing.T) {
	inUse := 20
	monitor, clock, notifier, alerts := poolMonitor(t, &inUse)
	ctx := context.Background()

	monitor.Sample(ctx)
	clock.at = clock.at.Add(4 * time.Minute)
	monitor.Sample(ctx)
	if openPoolAlerts(alerts) != 0 || notifier.raised != 0 {
		t.Fatal("raised before five minutes at the ceiling")
	}
	clock.at = clock.at.Add(61 * time.Second)
	monitor.Sample(ctx)
	monitor.Sample(ctx)
	if openPoolAlerts(alerts) != 1 || notifier.raised != 1 {
		t.Fatalf("open=%d raised=%d, want one alert raised once", openPoolAlerts(alerts), notifier.raised)
	}
}

func TestPoolPressure_ADipBelowTheCeilingRestartsTheClock(t *testing.T) {
	inUse := 20
	monitor, clock, _, alerts := poolMonitor(t, &inUse)
	ctx := context.Background()
	monitor.Sample(ctx)
	clock.at = clock.at.Add(4 * time.Minute)
	inUse = 15
	monitor.Sample(ctx)
	inUse = 20
	clock.at = clock.at.Add(time.Minute)
	monitor.Sample(ctx)
	clock.at = clock.at.Add(4 * time.Minute)
	monitor.Sample(ctx)
	if openPoolAlerts(alerts) != 0 {
		t.Fatal("the five minutes were counted across a dip")
	}
}

func TestPoolPressure_ResolvesWhenThePoolDrops(t *testing.T) {
	inUse := 20
	monitor, clock, notifier, alerts := poolMonitor(t, &inUse)
	ctx := context.Background()
	monitor.Sample(ctx)
	clock.at = clock.at.Add(6 * time.Minute)
	monitor.Sample(ctx)
	inUse = 12
	clock.at = clock.at.Add(30 * time.Second)
	monitor.Sample(ctx)
	if openPoolAlerts(alerts) != 0 || notifier.resolved != 1 {
		t.Fatalf("open=%d resolved=%d, want the alert resolved", openPoolAlerts(alerts), notifier.resolved)
	}
}

func TestPoolPressure_UnboundedPoolNeverAlerts(t *testing.T) {
	alerts := NewAlertingService(t.TempDir())
	clock := &poolClock{at: time.Now()}
	monitor := NewPoolPressureMonitor(func() sql.DBStats { return sql.DBStats{InUse: 99} }, alerts, clock.now)
	monitor.Sample(context.Background())
	clock.at = clock.at.Add(time.Hour)
	monitor.Sample(context.Background())
	if openPoolAlerts(alerts) != 0 {
		t.Fatal("an unbounded pool cannot be exhausted")
	}
}
